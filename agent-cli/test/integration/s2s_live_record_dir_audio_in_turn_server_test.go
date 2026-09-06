package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"strconv"
	"sync"
	"time"
)

type cliLiveRecordDirServer struct {
	mu                 sync.Mutex
	timeline           []string
	outbound           []cliLiveOutbound
	responses          chan int
	events             chan []byte
	closed             chan struct{}
	closeOnce          sync.Once
	dialOnce           sync.Once
	dialCount          int
	nextTurn           int
	providerError      bool
	readErr            error
	closeAfterTurn     int
	closeAfterResponse bool
	responseText       string
}

type cliLiveOutbound struct {
	typeName string
	audio    []byte
	payload  []byte
}

func newCLILiveRecordDirServer(providerError bool) *cliLiveRecordDirServer {
	return &cliLiveRecordDirServer{
		responses:     make(chan int, 8),
		events:        make(chan []byte, 64),
		closed:        make(chan struct{}),
		providerError: providerError,
	}
}

func newCLILiveRecordDirReadErrorServer(readErr error) *cliLiveRecordDirServer {
	server := newCLILiveRecordDirServer(false)
	server.readErr = readErr
	return server
}

func newCLILiveRecordDirCloseAfterTurnServer(turn int) *cliLiveRecordDirServer {
	server := newCLILiveRecordDirServer(false)
	server.closeAfterTurn = turn
	return server
}

func newCLILiveRecordDirPromptServer(responseText string) *cliLiveRecordDirServer {
	server := newCLILiveRecordDirCloseAfterTurnServer(1)
	server.closeAfterResponse = true
	server.responseText = responseText
	return server
}

func (s *cliLiveRecordDirServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.mu.Lock()
	s.dialCount++
	s.mu.Unlock()
	s.dialOnce.Do(func() { go s.serve() })
	return &cliLiveRecordDirConn{server: s}, nil
}

func (s *cliLiveRecordDirServer) serve() {
	s.sendEvent(`{"type":"session.created","session":{"id":"sess_cli_live","model":"gpt-realtime"}}`)
	if s.providerError {
		s.sendEvent(`{"type":"error","error":{"type":"authentication_error","code":"invalid_api_key","message":"invalid API key"}}`)
		return
	}
	s.sendEvent(`{"type":"session.updated","session":{"id":"sess_cli_live","model":"gpt-realtime"}}`)

	for {
		select {
		case turn := <-s.responses:
			responseID := "resp_" + strconv.Itoa(turn)
			transcriptText := s.responseText
			if transcriptText == "" {
				transcriptText = "response turn " + strconv.Itoa(turn)
			}
			audio := base64.StdEncoding.EncodeToString([]byte{byte(turn), 0, byte(turn + 10), 0})
			s.sendEvent(`{"type":"response.created","response":{"id":"` + responseID + `"}}`)
			closeAfterTurn := s.closeAfterTurn > 0 && turn == s.closeAfterTurn
			if closeAfterTurn && !s.closeAfterResponse {
				// Put the provider terminal event ahead of this response's
				// terminal boundary. The session runner must drain the queued
				// response but must not mistake the close for completion of any
				// still-undispatched scheduled input.
				s.sendEvent(`{"type":"session.closed","session_id":"sess_cli_live","reason":"scheduled_fixture_complete"}`)
			}
			s.sendEvent(`{"type":"response.output_audio_transcript.done","transcript":"` + transcriptText + `"}`)
			s.sendEvent(`{"type":"response.output_audio.delta","delta":"` + audio + `","format":"pcm16"}`)
			s.sendEvent(`{"type":"response.output_audio.done"}`)
			s.sendEvent(`{"type":"response.done","response":{"id":"` + responseID + `","status":"completed"}}`)
			if closeAfterTurn {
				if s.closeAfterResponse {
					s.sendEvent(`{"type":"session.closed","session_id":"sess_cli_live","reason":"scheduled_fixture_complete"}`)
				}
				return
			}
		case <-s.closed:
			return
		}
	}
}

func (s *cliLiveRecordDirServer) sendEvent(payload string) {
	select {
	case s.events <- []byte(payload):
	case <-s.closed:
	}
}

func (s *cliLiveRecordDirServer) recordTimeline(event string) {
	s.mu.Lock()
	s.timeline = append(s.timeline, event)
	s.mu.Unlock()
}

func (s *cliLiveRecordDirServer) snapshots() ([]string, []cliLiveOutbound, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	timeline := append([]string(nil), s.timeline...)
	outbound := make([]cliLiveOutbound, len(s.outbound))
	for index, event := range s.outbound {
		outbound[index] = cliLiveOutbound{typeName: event.typeName, audio: append([]byte(nil), event.audio...), payload: append([]byte(nil), event.payload...)}
	}
	return timeline, outbound, s.dialCount
}

type cliLiveRecordDirConn struct {
	server *cliLiveRecordDirServer
}

func (c *cliLiveRecordDirConn) ReadMessage() (int, []byte, error) {
	if c.server.readErr != nil {
		return 0, nil, c.server.readErr
	}
	select {
	case payload := <-c.server.events:
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err == nil {
			c.server.recordTimeline("in:" + envelope.Type)
		}
		return 1, payload, nil
	case <-c.server.closed:
		return 0, nil, errors.New("hermetic live connection closed")
	}
}

func (c *cliLiveRecordDirConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}

	var audio []byte
	if envelope.Audio != "" {
		var err error
		audio, err = base64.StdEncoding.DecodeString(envelope.Audio)
		if err != nil {
			return err
		}
	}

	c.server.mu.Lock()
	c.server.timeline = append(c.server.timeline, "out:"+envelope.Type)
	c.server.outbound = append(c.server.outbound, cliLiveOutbound{typeName: envelope.Type, audio: audio, payload: append([]byte(nil), payload...)})
	if envelope.Type == "response.create" {
		c.server.nextTurn++
		turn := c.server.nextTurn
		c.server.mu.Unlock()
		select {
		case c.server.responses <- turn:
		case <-c.server.closed:
		}
		return nil
	}
	c.server.mu.Unlock()
	return nil
}

func (c *cliLiveRecordDirConn) Close() error {
	c.server.closeOnce.Do(func() { close(c.server.closed) })
	return nil
}

// cliLiveScheduledBoundaryServer is a live-shaped OpenAI transport for the
// production CLI scheduled-audio path. It can withhold the initial
// session.updated acknowledgement, and models the provider-side auto-commit
// that occurs when Server VAD owns a speech boundary before the client sends
// its explicit boundary.
type cliLiveScheduledBoundaryServer struct {
	mu sync.Mutex

	timeline       []string
	outbound       []cliLiveOutbound
	providerErrors []string
	responses      chan int
	events         chan []byte
	closed         chan struct{}
	closeOnce      sync.Once
	dialOnce       sync.Once
	dialCount      int
	nextTurn       int

	sessionCreated        chan struct{}
	sessionUpdatedRelease chan struct{}
	sessionUpdateObserved chan struct{}
	sessionCreatedOnce    sync.Once
	releaseOnce           sync.Once
	firstResponseCancel   chan struct{}
	firstCancelOnce       sync.Once

	serverVADEnabled    bool
	bargeIn             bool
	turnObserved        bool
	turnHasAudio        bool
	turnHasSpeech       bool
	turnAutoCommitted   bool
	clientCommitted     bool
	providerAutoCommits int
	sessionUpdates      []json.RawMessage
}

func newCLILiveScheduledBoundaryServer(delaySessionUpdated bool) *cliLiveScheduledBoundaryServer {
	server := &cliLiveScheduledBoundaryServer{
		responses:             make(chan int, 8),
		events:                make(chan []byte, 64),
		closed:                make(chan struct{}),
		sessionCreated:        make(chan struct{}),
		sessionUpdateObserved: make(chan struct{}, 16),
		serverVADEnabled:      true,
	}
	if delaySessionUpdated {
		server.sessionUpdatedRelease = make(chan struct{})
	}
	return server
}

func (s *cliLiveScheduledBoundaryServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.mu.Lock()
	s.dialCount++
	s.mu.Unlock()
	s.dialOnce.Do(func() { go s.serve() })
	return &cliLiveScheduledBoundaryConn{server: s}, nil
}

func (s *cliLiveScheduledBoundaryServer) serve() {
	s.sendEvent(`{"type":"session.created","session":{"id":"sess_cli_boundary","model":"gpt-realtime"}}`)
	closeChannelOnce(s.sessionCreated, &s.sessionCreatedOnce)
	if s.sessionUpdatedRelease != nil {
		select {
		case <-s.sessionUpdatedRelease:
		case <-s.closed:
			return
		}
	}
	s.sendEvent(`{"type":"session.updated","session":{"id":"sess_cli_boundary","model":"gpt-realtime"}}`)

	for {
		select {
		case turn := <-s.responses:
			responseID := "resp_boundary_" + strconv.Itoa(turn)
			transcriptText := "response turn " + strconv.Itoa(turn)
			audio := base64.StdEncoding.EncodeToString([]byte{byte(turn), 0, byte(turn + 20), 0})
			s.sendEvent(`{"type":"response.created","response":{"id":"` + responseID + `"}}`)
			if s.bargeIn && turn == 1 {
				// Hold the first response open after its observable start. The
				// scheduled second input must reach ModelRunner, which owns the
				// cancellation, before this fixture publishes terminality.
				s.sendEvent(`{"type":"response.output_audio.delta","response_id":"` + responseID + `","delta":"` + audio + `","format":"pcm16"}`)
				select {
				case <-s.firstResponseCancel:
					// This delta is deliberately stale and must be suppressed by
					// the customer-facing stream after the accepted cancellation.
					s.sendEvent(`{"type":"response.output_audio.delta","response_id":"` + responseID + `","delta":"Y2FuY2VsLXN0YWxl","format":"pcm16"}`)
					s.sendEvent(`{"type":"response.done","response":{"id":"` + responseID + `","status":"cancelled"}}`)
				case <-s.closed:
					return
				}
				continue
			}
			s.sendEvent(`{"type":"response.output_audio_transcript.done","transcript":"` + transcriptText + `"}`)
			s.sendEvent(`{"type":"response.output_audio.delta","delta":"` + audio + `","format":"pcm16"}`)
			s.sendEvent(`{"type":"response.output_audio.done"}`)
			s.sendEvent(`{"type":"response.done","response":{"id":"` + responseID + `","status":"completed"}}`)
		case <-s.closed:
			return
		}
	}
}

func (s *cliLiveScheduledBoundaryServer) sendEvent(payload string) {
	select {
	case s.events <- []byte(payload):
	case <-s.closed:
	}
}

func (s *cliLiveScheduledBoundaryServer) shutdown() {
	s.closeOnce.Do(func() { close(s.closed) })
}

func (s *cliLiveScheduledBoundaryServer) releaseSessionUpdated() {
	if s == nil || s.sessionUpdatedRelease == nil {
		return
	}
	s.releaseOnce.Do(func() { close(s.sessionUpdatedRelease) })
}

func (s *cliLiveScheduledBoundaryServer) snapshots() ([]string, []cliLiveOutbound, []string, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	timeline := append([]string(nil), s.timeline...)
	outbound := make([]cliLiveOutbound, len(s.outbound))
	for index, event := range s.outbound {
		outbound[index] = cliLiveOutbound{typeName: event.typeName, audio: append([]byte(nil), event.audio...), payload: append([]byte(nil), event.payload...)}
	}
	return timeline, outbound, append([]string(nil), s.providerErrors...), s.dialCount, s.serverVADEnabled
}

func (s *cliLiveScheduledBoundaryServer) sessionUpdateSnapshot() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessionUpdates) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), s.sessionUpdates[0]...)
}

func (s *cliLiveScheduledBoundaryServer) sessionUpdatesSnapshot() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	updates := make([]json.RawMessage, len(s.sessionUpdates))
	for index, update := range s.sessionUpdates {
		updates[index] = append(json.RawMessage(nil), update...)
	}
	return updates
}

func (s *cliLiveScheduledBoundaryServer) waitForSessionUpdates(timeout time.Duration, ready func([]json.RawMessage) bool) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if ready(s.sessionUpdatesSnapshot()) {
			return true
		}
		select {
		case <-s.sessionUpdateObserved:
		case <-deadline.C:
			return false
		case <-s.closed:
			return false
		}
	}
}

func (s *cliLiveScheduledBoundaryServer) providerAutoCommitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providerAutoCommits
}

type cliLiveScheduledBoundaryConn struct {
	server *cliLiveScheduledBoundaryServer
}

func (c *cliLiveScheduledBoundaryConn) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-c.server.events:
		var envelope struct {
			Type  string `json:"type"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &envelope); err == nil {
			c.server.mu.Lock()
			c.server.timeline = append(c.server.timeline, "in:"+envelope.Type)
			if envelope.Type == "error" && envelope.Error.Code != "" {
				c.server.providerErrors = append(c.server.providerErrors, envelope.Error.Code)
			}
			c.server.mu.Unlock()
		}
		return 1, payload, nil
	case <-c.server.closed:
		return 0, nil, errors.New("hermetic scheduled-boundary connection closed")
	}
}

func (c *cliLiveScheduledBoundaryConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type    string          `json:"type"`
		Audio   string          `json:"audio"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}

	var audio []byte
	if envelope.Audio != "" {
		var err error
		audio, err = base64.StdEncoding.DecodeString(envelope.Audio)
		if err != nil {
			return err
		}
	}

	var observations []string
	var responseTurn int
	var commitEmpty bool
	var responseAccepted bool
	c.server.mu.Lock()
	c.server.timeline = append(c.server.timeline, "out:"+envelope.Type)
	c.server.outbound = append(c.server.outbound, cliLiveOutbound{typeName: envelope.Type, audio: audio, payload: append([]byte(nil), payload...)})
	switch envelope.Type {
	case "session.update":
		c.server.sessionUpdates = append(c.server.sessionUpdates, append(json.RawMessage(nil), envelope.Session...))
		if enabled, present := scheduledServerVADSetting(envelope.Session); present {
			c.server.serverVADEnabled = enabled
		}
		select {
		case c.server.sessionUpdateObserved <- struct{}{}:
		default:
		}
	case "response.cancel":
		if c.server.bargeIn && c.server.firstResponseCancel != nil {
			c.server.firstCancelOnce.Do(func() { close(c.server.firstResponseCancel) })
		}
	case "input_audio_buffer.append":
		c.server.turnHasAudio = true
		if !c.server.turnObserved {
			c.server.turnObserved = true
			if hasNonZeroPCM(audio) {
				c.server.turnHasSpeech = true
				if c.server.serverVADEnabled {
					observations = append(observations, `{"type":"input_audio_buffer.speech_started"}`)
				}
			}
		} else if hasNonZeroPCM(audio) {
			c.server.turnHasSpeech = true
		}
		if c.server.serverVADEnabled && c.server.turnHasSpeech && !c.server.turnAutoCommitted {
			// A finite test append represents the provider having observed the
			// speech stop for that input. Server VAD commits and clears its
			// buffer before the later client boundary arrives, even when
			// create_response is false.
			observations = append(observations, `{"type":"input_audio_buffer.speech_stopped"}`)
			c.server.turnAutoCommitted = true
			c.server.turnHasAudio = false
			c.server.providerAutoCommits++
		}
	case "input_audio_buffer.commit":
		if c.server.turnAutoCommitted || !c.server.turnHasAudio {
			// The client commit is redundant after provider VAD has already
			// committed and cleared the input buffer.
			commitEmpty = true
		} else {
			c.server.turnHasAudio = false
			c.server.clientCommitted = true
		}
	case "response.create":
		c.server.nextTurn++
		responseTurn = c.server.nextTurn
		responseAccepted = c.server.clientCommitted && !c.server.serverVADEnabled
		c.server.turnObserved = false
		c.server.turnHasAudio = false
		c.server.turnHasSpeech = false
		c.server.turnAutoCommitted = false
		c.server.clientCommitted = false
	}
	c.server.mu.Unlock()

	for _, observation := range observations {
		c.server.sendEvent(observation)
	}
	if commitEmpty {
		c.server.sendEvent(`{"type":"error","error":{"type":"invalid_request_error","code":"input_audio_buffer_commit_empty","message":"input audio buffer is empty after provider VAD committed it"}}`)
		return nil
	}
	if responseTurn > 0 && responseAccepted {
		select {
		case c.server.responses <- responseTurn:
		case <-c.server.closed:
		}
	}
	return nil
}

func (c *cliLiveScheduledBoundaryConn) Close() error {
	c.server.shutdown()
	return nil
}

func closeChannelOnce(channel chan struct{}, once *sync.Once) {
	if channel == nil || once == nil {
		return
	}
	once.Do(func() { close(channel) })
}

func scheduledServerVADSetting(session json.RawMessage) (bool, bool) {
	var update struct {
		Audio struct {
			Input struct {
				TurnDetection json.RawMessage `json:"turn_detection"`
			} `json:"input"`
		} `json:"audio"`
		TurnDetection json.RawMessage `json:"turn_detection"`
	}
	if err := json.Unmarshal(session, &update); err != nil {
		return true, false
	}
	detection := update.Audio.Input.TurnDetection
	if len(detection) == 0 {
		detection = update.TurnDetection
	}
	if len(detection) == 0 {
		return true, false
	}
	return !bytes.Equal(bytes.TrimSpace(detection), []byte("null")), true
}

func hasNonZeroPCM(audio []byte) bool {
	for _, sample := range audio {
		if sample != 0 {
			return true
		}
	}
	return false
}
