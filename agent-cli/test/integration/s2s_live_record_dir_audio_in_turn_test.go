package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// cliLiveRecordDirServer is a provider-shaped transport used at the shipped
// Cobra command boundary. It emits session.created once, answers each
// response.create with one terminal response, and intentionally never emits
// session.closed: the live scheduled-input lifecycle must close locally after
// the final response.done.
type cliLiveRecordDirServer struct {
	mu            sync.Mutex
	timeline      []string
	outbound      []cliLiveOutbound
	responses     chan int
	events        chan []byte
	closed        chan struct{}
	closeOnce     sync.Once
	dialOnce      sync.Once
	dialCount     int
	nextTurn      int
	providerError bool
	readErr       error
}

type cliLiveOutbound struct {
	typeName string
	audio    []byte
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
			transcriptText := "response turn " + strconv.Itoa(turn)
			audio := base64.StdEncoding.EncodeToString([]byte{byte(turn), 0, byte(turn + 10), 0})
			s.sendEvent(`{"type":"response.created","response":{"id":"` + responseID + `"}}`)
			s.sendEvent(`{"type":"response.output_audio_transcript.done","transcript":"` + transcriptText + `"}`)
			s.sendEvent(`{"type":"response.output_audio.delta","delta":"` + audio + `","format":"pcm16"}`)
			s.sendEvent(`{"type":"response.output_audio.done"}`)
			s.sendEvent(`{"type":"response.done","response":{"id":"` + responseID + `","status":"completed"}}`)
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
		outbound[index] = cliLiveOutbound{typeName: event.typeName, audio: append([]byte(nil), event.audio...)}
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
	c.server.outbound = append(c.server.outbound, cliLiveOutbound{typeName: envelope.Type, audio: audio})
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

	serverVADEnabled    bool
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
		outbound[index] = cliLiveOutbound{typeName: event.typeName, audio: append([]byte(nil), event.audio...)}
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
	c.server.outbound = append(c.server.outbound, cliLiveOutbound{typeName: envelope.Type, audio: audio})
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

func scheduledServerVADEnabled(session json.RawMessage) bool {
	enabled, _ := scheduledServerVADSetting(session)
	return enabled
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

func newCLIScheduledBoundaryAgent(t *testing.T, server transport.Dialer) *cli.AgentCLI {
	t.Helper()
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI scheduled session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	return agentCLI
}

func newCLIGroundedScheduledBoundaryAgent(t *testing.T, server transport.Dialer) *cli.AgentCLI {
	t.Helper()
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("create grounded hermetic OpenAI scheduled session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortSessionInferencer, sessionInferencer),
	)
	if err != nil {
		t.Fatalf("initialize grounded production CLI: %v", err)
	}
	return agentCLI
}

func newCLIServerVADBoundaryAgent(t *testing.T, server transport.Dialer) *cli.AgentCLI {
	t.Helper()
	createResponse := false
	provider := oaiprovider.New(
		oaiprovider.WithAPIKey("test-key"),
		oaiprovider.WithModel("gpt-realtime"),
		oaiprovider.WithRealtimeBaseURL("wss://hermetic.openai.test/v1/realtime"),
		oaiprovider.WithWebSocketDialer(server),
	)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(provider))
	if err != nil {
		t.Fatalf("create hermetic OpenAI server-VAD session gateway: %v", err)
	}
	sessionInferencer := inference.NewSessionGatewayInferencer(
		sessionGateway,
		inference.WithSessionRequest(inference.SessionRequest{Config: models.SessionConfig{
			Model: "gpt-realtime",
			TurnDetection: &models.TurnDetectionConfig{
				Type:           "server_vad",
				CreateResponse: &createResponse,
			},
		}}),
	)
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize server-VAD CLI: %v", err)
	}
	return agentCLI
}

func scheduledBoundaryArgs(configDir, recordDir string, audioPaths ...string) []string {
	args := []string{
		"--config-dir", configDir,
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--max-duration", "10s",
	}
	for _, path := range audioPaths {
		args = append(args, "--audio-in-turn", path)
	}
	return args
}

// TestSessionCommand_LiveScheduledAudioWithoutPromptSendsGroundingAndTools
// exercises the exact no-prompt command form that previously skipped the
// instruction composition boundary. The production CLI graph owns the default
// registry; only the OpenAI session transport is replaced by the hermetic
// provider-shaped connection.
func TestSessionCommand_LiveScheduledAudioWithoutPromptSendsGroundingAndTools(t *testing.T) {
	server := newCLILiveScheduledBoundaryServer(true)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIGroundedScheduledBoundaryAgent(t, server)
	configDir := t.TempDir()
	recordDir := filepath.Join(t.TempDir(), "grounded-recording")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs(scheduledBoundaryArgs(
		configDir,
		recordDir,
		locateCLIFixture(t, "multiturn_turn1.wav"),
	))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- rootCmd.ExecuteContext(ctx) }()

	select {
	case <-server.sessionCreated:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the no-prompt live SESSION.OPEN fixture")
	}

	timeline, _, _, _, _ := server.snapshots()
	for _, event := range []string{"out:input_audio_buffer.append", "out:input_audio_buffer.commit", "out:response.create"} {
		if containsTimeline(timeline, event) {
			t.Fatalf("no-prompt scheduled audio crossed provider before configuration: %v", timeline)
		}
	}

	groundedReady := server.waitForSessionUpdates(2*time.Second, func(updates []json.RawMessage) bool {
		for _, raw := range updates {
			var update struct {
				Instructions string `json:"instructions"`
				Tools        []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			if json.Unmarshal(raw, &update) == nil && strings.Contains(update.Instructions, "Tool-grounding requirements:") && len(update.Tools) > 0 {
				return true
			}
		}
		return false
	})
	if !groundedReady {
		timeline, _, _, _, _ := server.snapshots()
		updates := server.sessionUpdatesSnapshot()
		t.Fatalf("no-prompt scheduled route did not send a combined grounding/tool update; timeline=%v updates=%v", timeline, updates)
	}

	timeline, _, _, _, _ = server.snapshots()
	updates := server.sessionUpdatesSnapshot()
	groundedUpdateIndex := -1
	for index, raw := range updates {
		var update struct {
			Instructions string `json:"instructions"`
			Tools        []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(raw, &update); err != nil {
			t.Fatalf("decode no-prompt scheduled session.update %d: %v", index, err)
		}
		if !strings.Contains(update.Instructions, "Tool-grounding requirements:") {
			continue
		}
		if groundedUpdateIndex >= 0 {
			t.Fatalf("no-prompt scheduled route sent grounding more than once: updates=%v", updates)
		}
		groundedUpdateIndex = index
		if strings.Count(update.Instructions, "Tool-grounding requirements:") != 1 {
			t.Fatalf("no-prompt grounding policy count = %d, want 1; instructions=%q", strings.Count(update.Instructions, "Tool-grounding requirements:"), update.Instructions)
		}
		if strings.Contains(update.Instructions, "No tools are currently registered") {
			t.Fatalf("no-prompt grounding instructions contradict advertised tools: %q", update.Instructions)
		}
		advertised := make(map[string]bool, len(update.Tools))
		for _, tool := range update.Tools {
			advertised[tool.Name] = true
		}
		for _, name := range []string{"read_file", "exec"} {
			if !advertised[name] {
				t.Fatalf("no-prompt scheduled session.update omitted %q: %#v", name, update.Tools)
			}
		}
	}
	if groundedUpdateIndex < 0 {
		t.Fatalf("no-prompt scheduled route sent no instruction-bearing tool update: %v", updates)
	}
	groundedWireIndex := indexOfTimeline(timeline, "out:session.update", groundedUpdateIndex)
	if groundedWireIndex < 0 {
		t.Fatalf("no-prompt grounding update is absent from outbound timeline: %v", timeline)
	}
	for _, event := range []string{"out:input_audio_buffer.append", "out:input_audio_buffer.commit", "out:response.create"} {
		if eventIndex := indexOfTimeline(timeline, event, 0); eventIndex >= 0 && groundedWireIndex >= eventIndex {
			t.Fatalf("grounding/tool session.update index=%d does not precede %s index=%d: %v", groundedWireIndex, event, eventIndex, timeline)
		}
	}

	server.releaseSessionUpdated()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("no-prompt production CLI session error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no-prompt production CLI session did not complete")
	}

	timeline, _, providerErrors, _, serverVADEnabled := server.snapshots()
	if len(providerErrors) != 0 {
		t.Fatalf("no-prompt provider errors = %v; timeline=%v", providerErrors, timeline)
	}
	if serverVADEnabled {
		t.Fatalf("no-prompt scheduled session left server VAD enabled: %v", timeline)
	}
	firstAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 0)
	firstCommit := indexOfTimeline(timeline, "out:input_audio_buffer.commit", 0)
	firstResponse := indexOfTimeline(timeline, "out:response.create", 0)
	if firstAppend < 0 || firstCommit < 0 || firstResponse < 0 || !(groundedWireIndex < firstAppend && groundedWireIndex < firstCommit && groundedWireIndex < firstResponse) {
		t.Fatalf("no-prompt configuration did not precede the first spoken turn: update=%d append=%d commit=%d response=%d timeline=%v", groundedWireIndex, firstAppend, firstCommit, firstResponse, timeline)
	}
}

func assertScheduledBoundaryOrder(t *testing.T, timeline []string, turns int) {
	t.Helper()
	for turn := 0; turn < turns; turn++ {
		appendIndex := indexOfTimeline(timeline, "out:input_audio_buffer.append", turn)
		commitIndex := indexOfTimeline(timeline, "out:input_audio_buffer.commit", turn)
		responseIndex := indexOfTimeline(timeline, "out:response.create", turn)
		doneIndex := indexOfTimeline(timeline, "in:response.done", turn)
		if appendIndex < 0 || commitIndex < 0 || responseIndex < 0 || doneIndex < 0 {
			t.Fatalf("scheduled turn %d is missing its boundary from %v", turn+1, timeline)
		}
		if !(appendIndex < commitIndex && commitIndex < responseIndex && responseIndex < doneIndex) {
			t.Fatalf("scheduled turn %d lifecycle order = %v, want append < commit < response.create < response.done", turn+1, timeline)
		}
	}
}

func equalDuration24kSilenceFixture(t *testing.T, speechPath string) string {
	t.Helper()
	wavBytes, err := os.ReadFile(speechPath)
	if err != nil {
		t.Fatalf("read 24 kHz speech fixture: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("decode 24 kHz speech fixture: %v", err)
	}
	if rate != wavio.Rate24kHz || len(samples) == 0 {
		t.Fatalf("speech fixture format = rate %d, samples %d; want non-empty 24 kHz PCM16 mono", rate, len(samples))
	}
	for index := range samples {
		samples[index] = 0
	}

	var silence bytes.Buffer
	if err := wavio.Write(&silence, wavio.Rate24kHz, samples); err != nil {
		t.Fatalf("encode equal-duration 24 kHz silence fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "exact-silence-24k.wav")
	if err := os.WriteFile(path, silence.Bytes(), 0o600); err != nil {
		t.Fatalf("write equal-duration 24 kHz silence fixture: %v", err)
	}
	return path
}

// TestSessionCommand_LiveScheduledAudioDoesNotCrossDelayedSessionUpdated
// drives the shipped command surface against a transport that withholds the
// initial configuration acknowledgement. The provider boundary must remain
// free of scheduled input until the observable session.updated event arrives.
func TestSessionCommand_LiveScheduledAudioDoesNotCrossDelayedSessionUpdated(t *testing.T) {
	server := newCLILiveScheduledBoundaryServer(true)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIScheduledBoundaryAgent(t, server)
	recordDir := filepath.Join(t.TempDir(), "delayed-ack-recording")
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs(scheduledBoundaryArgs(
		t.TempDir(),
		recordDir,
		locateCLIFixture(t, "multiturn_turn1.wav"),
	))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- rootCmd.ExecuteContext(ctx) }()

	select {
	case <-server.sessionCreated:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the live SESSION.OPEN fixture")
	}
	timeline, _, _, _, _ := server.snapshots()
	for _, event := range []string{"out:input_audio_buffer.append", "out:input_audio_buffer.commit", "out:response.create"} {
		if containsTimeline(timeline, event) {
			t.Fatalf("scheduled boundary crossed provider before session.updated: %v", timeline)
		}
	}

	server.releaseSessionUpdated()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("delayed-ack production CLI session error: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delayed-ack production CLI session did not complete")
	}

	timeline, outbound, providerErrors, dialCount, serverVADEnabled := server.snapshots()
	if dialCount != 1 {
		t.Fatalf("delayed-ack provider dial count = %d, want 1; timeline=%v", dialCount, timeline)
	}
	if serverVADEnabled {
		t.Fatalf("scheduled session update left server VAD enabled: %v", timeline)
	}
	if len(providerErrors) != 0 {
		t.Fatalf("delayed-ack provider errors = %v; timeline=%v", providerErrors, timeline)
	}
	updatedIndex := indexOfTimeline(timeline, "in:session.updated", 0)
	appendIndex := indexOfTimeline(timeline, "out:input_audio_buffer.append", 0)
	if updatedIndex < 0 || appendIndex < 0 || updatedIndex >= appendIndex {
		t.Fatalf("delayed-ack session.updated did not precede the first append: %v", timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 1 || countTimeline(timeline, "out:input_audio_buffer.commit") != 1 || countTimeline(timeline, "out:response.create") != 1 {
		t.Fatalf("delayed-ack scheduled boundary duplicated or missing: %v", timeline)
	}
	assertScheduledBoundaryOrder(t, timeline, 1)
	appendAudio := audioPayloadsFromOutbound(outbound)
	if len(appendAudio) != 1 || len(appendAudio[0]) == 0 || !hasNonZeroPCM(appendAudio[0]) {
		t.Fatalf("delayed-ack first scheduled append = %v; want one non-empty PCM payload", audioLengths(appendAudio))
	}
	for _, output := range []string{stdout.String(), stderr.String()} {
		if strings.Contains(output, "input_audio_buffer_commit_empty") || strings.Contains(output, "conversation_already_has_active_response") {
			t.Fatalf("delayed-ack CLI output contains a provider collision: %q", output)
		}
	}
	assertCLILiveRecordingBundle(t, recordDir, 1)
}

// TestSessionCommand_LiveScheduledAudioSpeechThenExactSilence keeps a real
// 24 kHz speech fixture and an equal-duration all-zero 24 kHz fixture in one
// persistent production-CLI session. The transport would auto-commit a speech
// stop while Server VAD is enabled, but explicit null keeps both turns under
// the client's one-commit/one-response boundary.
func TestSessionCommand_LiveScheduledAudioSpeechThenExactSilence(t *testing.T) {
	speechPath := locateCorpusWAV(t, "truncated_24k")
	silencePath := equalDuration24kSilenceFixture(t, speechPath)
	server := newCLILiveScheduledBoundaryServer(false)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIScheduledBoundaryAgent(t, server)
	recordDir := filepath.Join(t.TempDir(), "speech-silence-recording")
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs(scheduledBoundaryArgs(t.TempDir(), recordDir, speechPath, silencePath))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := rootCmd.ExecuteContext(ctx)
	if err != nil {
		timeline, outbound, providerErrors, _, _ := server.snapshots()
		t.Fatalf("speech-then-silence production CLI session error: %v\nprovider errors=%v\ntimeline=%v\nappend lengths=%v\nstdout:\n%s\nstderr:\n%s", err, providerErrors, timeline, audioLengthsFromOutbound(outbound), stdout.String(), stderr.String())
	}

	timeline, outbound, providerErrors, dialCount, serverVADEnabled := server.snapshots()
	if dialCount != 1 {
		t.Fatalf("speech-then-silence provider dial count = %d, want 1; timeline=%v", dialCount, timeline)
	}
	if serverVADEnabled {
		t.Fatalf("scheduled session update left server VAD enabled: %v", timeline)
	}
	if got := server.providerAutoCommitCount(); got != 0 {
		t.Fatalf("client-owned scheduled run unexpectedly auto-committed %d turn(s): %v", got, timeline)
	}
	if len(providerErrors) != 0 {
		t.Fatalf("speech-then-silence provider errors = %v; timeline=%v", providerErrors, timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 2 || countTimeline(timeline, "out:input_audio_buffer.commit") != 2 || countTimeline(timeline, "out:response.create") != 2 || countTimeline(timeline, "in:response.done") != 2 {
		t.Fatalf("speech-then-silence scheduled lifecycle = %v, want two complete client-owned turns", timeline)
	}
	assertScheduledBoundaryOrder(t, timeline, 2)
	firstResponseDone := indexOfTimeline(timeline, "in:response.done", 0)
	secondAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 1)
	if firstResponseDone < 0 || secondAppend <= firstResponseDone {
		t.Fatalf("silence turn crossed the active first response: %v", timeline)
	}
	if countTimeline(timeline, "in:input_audio_buffer.speech_started") != 0 || countTimeline(timeline, "in:input_audio_buffer.speech_stopped") != 0 {
		t.Fatalf("client-owned scheduled run unexpectedly emitted VAD observations = %v", timeline)
	}

	appendAudio := audioPayloadsFromOutbound(outbound)
	if len(appendAudio) != 2 || len(appendAudio[0]) == 0 || len(appendAudio[1]) == 0 {
		t.Fatalf("speech-then-silence append payloads = %v, want two non-empty scheduled payloads", audioLengths(appendAudio))
	}
	if len(appendAudio[0]) != len(appendAudio[1]) {
		t.Fatalf("speech and exact-silence payload lengths = %v, want equal duration after normalization", audioLengths(appendAudio))
	}
	if !hasNonZeroPCM(appendAudio[0]) || hasNonZeroPCM(appendAudio[1]) {
		t.Fatalf("scheduled payloads do not preserve speech then exact silence: non-zero=%t,%t", hasNonZeroPCM(appendAudio[0]), hasNonZeroPCM(appendAudio[1]))
	}
	for _, output := range []string{strings.Join(timeline, "\n"), strings.Join(providerErrors, "\n"), stdout.String(), stderr.String()} {
		if strings.Contains(output, "input_audio_buffer_commit_empty") || strings.Contains(output, "conversation_already_has_active_response") {
			t.Fatalf("speech-then-silence run contains a provider collision: %q", output)
		}
	}
	assertCLILiveRecordingBundle(t, recordDir, 2)
}

// TestSessionCommand_LiveScheduledAudioServerVADCreateResponseFalseNegativeControl
// drives the old server_vad plus create_response:false wire configuration
// through the same production CLI boundary. The provider double auto-commits
// and clears a speech buffer at speech stop, so the later client commit must
// be rejected as input_audio_buffer_commit_empty.
func TestSessionCommand_LiveScheduledAudioServerVADCreateResponseFalseNegativeControl(t *testing.T) {
	speechPath := locateCorpusWAV(t, "truncated_24k")
	silencePath := equalDuration24kSilenceFixture(t, speechPath)
	server := newCLILiveScheduledBoundaryServer(false)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIServerVADBoundaryAgent(t, server)
	recordDir := filepath.Join(t.TempDir(), "server-vad-recording")
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs(scheduledBoundaryArgs(t.TempDir(), recordDir, speechPath, silencePath))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := rootCmd.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("former server-VAD scheduled configuration unexpectedly completed")
	}

	timeline, _, providerErrors, _, serverVADEnabled := server.snapshots()
	if !serverVADEnabled {
		t.Fatalf("negative control did not leave server VAD enabled: %v", timeline)
	}
	if server.providerAutoCommitCount() != 1 {
		t.Fatalf("provider auto-commit count = %d, want one speech-stop auto-commit; timeline=%v", server.providerAutoCommitCount(), timeline)
	}
	if len(providerErrors) != 1 || providerErrors[0] != "input_audio_buffer_commit_empty" {
		t.Fatalf("negative-control provider errors = %v, want one input_audio_buffer_commit_empty; timeline=%v", providerErrors, timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 1 || countTimeline(timeline, "out:input_audio_buffer.commit") != 1 {
		t.Fatalf("negative-control client boundary = %v, want one append and one rejected commit", timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") >= 2 || countTimeline(timeline, "in:response.done") != 0 {
		t.Fatalf("negative-control continued after the provider collision: %v", timeline)
	}
	if countTimeline(timeline, "in:input_audio_buffer.speech_started") != 1 || countTimeline(timeline, "in:input_audio_buffer.speech_stopped") != 1 {
		t.Fatalf("negative-control VAD observations = %v, want one speech start and stop: %v", timeline, timeline)
	}
	assertScheduledServerVADCreateResponseFalse(t, server.sessionUpdateSnapshot())
	if !strings.Contains(stdout.String()+stderr.String(), "input audio buffer is empty") {
		t.Fatalf("negative-control CLI output did not preserve provider error: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func assertScheduledServerVADCreateResponseFalse(t *testing.T, session json.RawMessage) {
	t.Helper()
	var update struct {
		Audio struct {
			Input struct {
				TurnDetection json.RawMessage `json:"turn_detection"`
			} `json:"input"`
		} `json:"audio"`
	}
	if err := json.Unmarshal(session, &update); err != nil {
		t.Fatalf("decode negative-control session.update: %v", err)
	}
	if len(update.Audio.Input.TurnDetection) == 0 {
		t.Fatal("negative-control session.update omitted audio.input.turn_detection")
	}
	var detection struct {
		Type           string `json:"type"`
		CreateResponse *bool  `json:"create_response"`
	}
	if err := json.Unmarshal(update.Audio.Input.TurnDetection, &detection); err != nil {
		t.Fatalf("decode negative-control turn detection: %v", err)
	}
	if detection.Type != "server_vad" || detection.CreateResponse == nil || *detection.CreateResponse {
		t.Fatalf("negative-control turn detection = %+v, want server_vad/create_response:false", detection)
	}
}

func audioPayloadsFromOutbound(outbound []cliLiveOutbound) [][]byte {
	audio := make([][]byte, 0, len(outbound))
	for _, event := range outbound {
		if event.typeName == "input_audio_buffer.append" {
			audio = append(audio, append([]byte(nil), event.audio...))
		}
	}
	return audio
}

type cliLiveRecordingEntry struct {
	TurnIndex int `json:"turn_index"`
	Input     struct {
		AudioBytes    uint64   `json:"audio_bytes"`
		Committed     bool     `json:"committed"`
		AudioSegments []string `json:"audio_segments"`
	} `json:"input"`
	Response struct {
		Text          string   `json:"text"`
		Complete      bool     `json:"complete"`
		AudioBytes    uint64   `json:"audio_bytes"`
		AudioSegments []string `json:"audio_segments"`
	} `json:"response"`
}

func TestSessionCommand_LiveRecordDirAudioInTurnUsesLiveLifecycle(t *testing.T) {
	server := newCLILiveRecordDirServer(false)
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	recordDir := filepath.Join(t.TempDir(), "recording")
	firstAudio := locateCLIFixture(t, "multiturn_turn1.wav")
	secondAudio := locateCLIFixture(t, "multiturn_turn2.wav")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--max-duration", "5s",
		"--audio-in-turn", firstAudio,
		"--audio-in-turn", secondAudio,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		timeline, outbound, _ := server.snapshots()
		t.Fatalf("execute live-shaped command: %v; timeline=%v outbound=%v", err, timeline, audioLengthsFromOutbound(outbound))
	}

	timeline, outbound, dialCount := server.snapshots()
	if dialCount != 1 {
		t.Fatalf("live provider dial count = %d, want 1; timeline=%v", dialCount, timeline)
	}
	if containsTimeline(timeline, "in:session.closed") {
		t.Fatal("hermetic provider unexpectedly supplied a captured session.closed event")
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 2 || countTimeline(timeline, "out:input_audio_buffer.commit") != 2 || countTimeline(timeline, "out:response.create") != 2 {
		t.Fatalf("live outbound lifecycle = %v, want two appends, commits, and response.create events", timeline)
	}
	firstAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 0)
	firstResponseDone := indexOfTimeline(timeline, "in:response.done", 0)
	secondAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 1)
	if firstAppend < 0 || firstResponseDone < 0 || secondAppend <= firstResponseDone {
		t.Fatalf("turn-zero or response-gated dispatch order is wrong: %v", timeline)
	}

	appendAudio := make([][]byte, 0, 2)
	for _, event := range outbound {
		if event.typeName == "input_audio_buffer.append" {
			appendAudio = append(appendAudio, event.audio)
		}
	}
	if len(appendAudio) != 2 || len(appendAudio[0]) == 0 || len(appendAudio[1]) == 0 {
		t.Fatalf("provider observed scheduled audio payloads = %d with lengths %v, want two non-empty payloads", len(appendAudio), audioLengths(appendAudio))
	}

	assertCLILiveRecordingBundle(t, recordDir, 2)
}

func TestSessionCommand_LiveRecordDirAudioInTurnProviderErrorWinsOverRecordingValidation(t *testing.T) {
	server := newCLILiveRecordDirServer(true)
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	recordDir := filepath.Join(t.TempDir(), "failed-recording")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("expected provider authentication error")
	}
	if !strings.Contains(err.Error(), "invalid API key") && !strings.Contains(err.Error(), "invalid_api_key") {
		timeline, outbound, _ := server.snapshots()
		t.Fatalf("provider authentication error was not preserved: %v; timeline=%v outbound=%v", err, timeline, audioLengthsFromOutbound(outbound))
	}
	if errors.Is(err, transcript.ErrInvalidRecording) || strings.Contains(err.Error(), "at least one segment is required") {
		t.Fatalf("recording validation masked provider authentication error: %v", err)
	}
}

func TestSessionCommand_LiveRecordDirAudioInTurnUnexpectedProviderCloseWinsOverIncompleteSchedule(t *testing.T) {
	server := newCLILiveRecordDirReadErrorServer(errors.New("websocket: close 1008 (policy violation): Incorrect API key provided: invalid-test-key"))
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", filepath.Join(t.TempDir(), "failed-recording"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "invalid-test-key",
		"--system-prompt", "none",
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("expected provider close error")
	}
	if !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("unexpected provider close error: %v", err)
	}
	if strings.Contains(err.Error(), "scheduled audio session ended before all turns completed") || strings.Contains(err.Error(), "at least one segment is required") {
		t.Fatalf("provider close was masked by a secondary recording error: %v", err)
	}
}

func assertCLILiveRecordingBundle(t *testing.T, destination string, turns int) {
	t.Helper()
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read finalized recording manifest: %v", err)
	}
	var manifest struct {
		Artifacts []struct {
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode recording manifest: %v", err)
	}

	inputArtifacts, outputArtifacts := 0, 0
	for _, artifact := range manifest.Artifacts {
		switch {
		case strings.HasPrefix(artifact.Path, "audio/in-"):
			inputArtifacts++
		case strings.HasPrefix(artifact.Path, "audio/out-"):
			outputArtifacts++
		}
	}
	if inputArtifacts != turns || outputArtifacts != turns {
		t.Fatalf("manifest audio artifacts = input:%d output:%d, want %d each", inputArtifacts, outputArtifacts, turns)
	}
	for index := 0; index < turns; index++ {
		for _, prefix := range []string{"audio/in-", "audio/out-"} {
			path := filepath.Join(destination, prefix+threeDigits(index)+".pcm")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read finalized audio artifact %q: %v", path, err)
			}
			if len(data) == 0 {
				t.Fatalf("finalized audio artifact %q is empty", path)
			}
		}
	}

	logFile, err := os.Open(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("open finalized session log: %v", err)
	}
	defer logFile.Close()
	entries := make([]cliLiveRecordingEntry, 0, turns)
	scanner := bufio.NewScanner(logFile)
	for scanner.Scan() {
		var entry cliLiveRecordingEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode session log entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read finalized session log: %v", err)
	}
	if len(entries) != turns {
		t.Fatalf("session log entries = %d, want %d", len(entries), turns)
	}
	for index, entry := range entries {
		if entry.TurnIndex != index+1 || !entry.Input.Committed || entry.Input.AudioBytes == 0 || !entry.Response.Complete || entry.Response.AudioBytes == 0 {
			t.Fatalf("session log entry %d does not prove a committed input and completed audio response: %#v", index+1, entry)
		}
		wantText := "response turn " + strconv.Itoa(index+1)
		if entry.Response.Text != wantText {
			t.Fatalf("session log response %d text = %q, want %q", index+1, entry.Response.Text, wantText)
		}
	}
}

func threeDigits(index int) string {
	value := strconv.Itoa(index)
	return strings.Repeat("0", 3-len(value)) + value
}

func containsTimeline(timeline []string, want string) bool {
	return indexOfTimeline(timeline, want, 0) >= 0
}

func countTimeline(timeline []string, want string) int {
	count := 0
	for _, event := range timeline {
		if event == want {
			count++
		}
	}
	return count
}

func indexOfTimeline(timeline []string, want string, occurrence int) int {
	seen := 0
	for index, event := range timeline {
		if event != want {
			continue
		}
		if seen == occurrence {
			return index
		}
		seen++
	}
	return -1
}

func audioLengths(audio [][]byte) []int {
	lengths := make([]int, len(audio))
	for index, data := range audio {
		lengths[index] = len(data)
	}
	return lengths
}

func audioLengthsFromOutbound(outbound []cliLiveOutbound) []int {
	lengths := make([]int, 0, len(outbound))
	for _, event := range outbound {
		if event.typeName == "input_audio_buffer.append" {
			lengths = append(lengths, len(event.audio))
		}
	}
	return lengths
}
