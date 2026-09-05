package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/parity"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

const parityScenarioFile = "s2s_v1_text_in_audio_out.scenario.json"
const paritySessionFixture = "s2s_v1_text_in_audio_out.session.json"

const (
	parityEndpoint       = "memory://s2s-parity"
	parityClockTick      = time.Millisecond
	paritySignalingWait  = 250 * time.Millisecond
	parityScenarioBudget = time.Second
)

var (
	_ transport.Dialer = (*rtcReplayDialer)(nil)
	_ rtc.Dialer       = (*rtcReplayDialer)(nil)
)

type parityTransport string

const (
	transportWebSocket parityTransport = "websocket"
	transportRTC       parityTransport = "webrtc"
)

type committedScenario struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	Steps        []committedStep     `json:"steps"`
	Expectations []committedExpected `json:"expectations"`
}

type committedStep struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type committedExpected struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
	Value string `json:"value"`
}

type transportRun struct {
	Projection parity.Projection
	Client     parity.Projection
	Agent      parity.Projection
}

type scenarioCapture struct {
	client []transcript.Record
	agent  []transcript.Record
}

func TestCommittedScenarioHasWSRTCProjectionParity(t *testing.T) {
	scenario := loadParityScenario(t)
	capture := loadParityCapture(t)
	scenarioBefore := scenario

	ws := runParityScenario(t, scenario, capture, transportWebSocket)
	rtcRun := runParityScenario(t, scenario, capture, transportRTC)

	assertTransportProjectionParity(t, scenarioName(scenario), "WebSocket", "WebRTC", ws.Projection, rtcRun.Projection)
	if !reflect.DeepEqual(ws.Client, ws.Agent) {
		t.Fatalf("scenario %q WebSocket client/agent projections diverged:\n%s", scenarioName(scenario), parity.FormatReport(parity.Compare(ws.Client, ws.Agent)))
	}
	if !reflect.DeepEqual(rtcRun.Client, rtcRun.Agent) {
		t.Fatalf("scenario %q WebRTC client/agent projections diverged:\n%s", scenarioName(scenario), parity.FormatReport(parity.Compare(rtcRun.Client, rtcRun.Agent)))
	}
	if !reflect.DeepEqual(scenario, scenarioBefore) {
		t.Fatal("transport runs mutated the committed scenario definition")
	}

	// A second pass through each transport must retain exactly the same logical
	// evidence. The deterministic clock is recreated per run, so host timing
	// cannot enter either projection.
	wsAgain := runParityScenario(t, scenario, capture, transportWebSocket)
	rtcAgain := runParityScenario(t, scenario, capture, transportRTC)
	if !reflect.DeepEqual(ws.Projection, wsAgain.Projection) {
		t.Fatal("repeated WebSocket scenario run changed its projection")
	}
	if !reflect.DeepEqual(rtcRun.Projection, rtcAgain.Projection) {
		t.Fatal("repeated WebRTC scenario run changed its projection")
	}

	if len(ws.Projection.Audio.Frames) == 0 || len(ws.Projection.Transcripts) == 0 || ws.Projection.Terminal == nil {
		t.Fatalf("scenario projection lacks retained speech evidence: %+v", ws.Projection)
	}
}

func assertTransportProjectionParity(t *testing.T, scenario, leftTransport, rightTransport string, left, right parity.Projection) {
	t.Helper()
	if failure := transportProjectionFailure(scenario, leftTransport, rightTransport, left, right); failure != "" {
		t.Fatal(failure)
	}
	t.Logf("scenario %q: %s and %s projections agree", scenario, leftTransport, rightTransport)
}

func transportProjectionFailure(scenario, leftTransport, rightTransport string, left, right parity.Projection) string {
	differences := parity.Compare(left, right)
	if len(differences) == 0 {
		return ""
	}
	return fmt.Sprintf("scenario %q %s and %s projections diverged:\n%s", scenario, leftTransport, rightTransport, parity.FormatReport(differences))
}

func TestTransportProjectionDivergenceNamesEveryFactCategory(t *testing.T) {
	expected := parity.Projection{
		Turns: []parity.TurnBoundary{{Order: 1, Tick: 1, Kind: "turn.start", Boundary: "start", ID: "turn-1", Role: "user", Payload: []byte("turn")}},
		Audio: parity.AudioSummary{
			FrameCount: 1,
			TotalBytes: 2,
			Frames:     []parity.AudioFrame{{Tick: 2, Bytes: []byte{1, 2}, Payload: []byte("audio")}},
		},
		Transcripts:   []parity.TranscriptFact{{Order: 1, Tick: 3, Text: "hello", Payload: []byte("hello")}},
		ToolCalls:     []parity.ToolCallFact{{Order: 1, ID: "tool-1", Name: "lookup", Arguments: []byte(`{"q":"x"}`), Result: []byte(`{"ok":true}`), CallTick: 4, ResultTick: 5, CallPayload: []byte("call"), ResultPayload: []byte("result")}},
		Interruptions: []parity.InterruptionFact{{Order: 1, Tick: 6, Reason: "barge-in", Provenance: "client", Payload: []byte("interrupt")}},
		Terminal:      &parity.TerminalOutcome{Tick: 7, Reason: "provider_close", Provenance: "provider", Payload: []byte("terminal")},
	}
	actual := parity.Projection{
		Turns: []parity.TurnBoundary{{Order: 1, Tick: 1, Kind: "turn.end", Boundary: "start", ID: "turn-1", Role: "user", Payload: []byte("turn")}},
		Audio: parity.AudioSummary{
			FrameCount: 2,
			TotalBytes: 2,
			Frames:     []parity.AudioFrame{{Tick: 2, Bytes: []byte{1, 2}, Payload: []byte("audio")}},
		},
		Transcripts:   []parity.TranscriptFact{{Order: 1, Tick: 3, Text: "goodbye", Payload: []byte("hello")}},
		ToolCalls:     []parity.ToolCallFact{{Order: 1, ID: "tool-1", Name: "search", Arguments: []byte(`{"q":"x"}`), Result: []byte(`{"ok":true}`), CallTick: 4, ResultTick: 5, CallPayload: []byte("call"), ResultPayload: []byte("result")}},
		Interruptions: []parity.InterruptionFact{{Order: 1, Tick: 6, Reason: "user_cancel", Provenance: "client", Payload: []byte("interrupt")}},
		Terminal:      &parity.TerminalOutcome{Tick: 7, Reason: "client_close", Provenance: "provider", Payload: []byte("terminal")},
	}

	failure := transportProjectionFailure("negative-path", "WebSocket", "WebRTC", expected, actual)
	if failure == "" {
		t.Fatal("diverging projections unexpectedly produced no failure")
	}
	if !strings.Contains(failure, `scenario "negative-path" WebSocket and WebRTC projections diverged:`) {
		t.Fatalf("failure omitted scenario and transport names: %s", failure)
	}
	if !strings.Contains(failure, "Parity comparison: 6 differences") {
		t.Fatalf("failure did not retain one difference per fact category: %s", failure)
	}
	for _, category := range []string{"turns", "audio", "transcripts", "toolCalls", "interruptions", "terminal"} {
		if !strings.Contains(failure, category) {
			t.Errorf("failure omitted %s fact category: %s", category, failure)
		}
	}
}

func loadParityScenario(t *testing.T) committedScenario {
	t.Helper()
	data, err := os.ReadFile(parityScenarioPath(t))
	if err != nil {
		t.Fatalf("read committed parity scenario: %v", err)
	}
	var scenario committedScenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatalf("decode committed parity scenario: %v", err)
	}
	if scenario.ID == "" || scenario.Name == "" || len(scenario.Steps) == 0 || len(scenario.Expectations) == 0 {
		t.Fatalf("committed parity scenario is incomplete: %+v", scenario)
	}
	return scenario
}

func loadParityCapture(t *testing.T) gatewaytesting.SessionCapture {
	t.Helper()
	capture, err := gatewaytesting.LoadSessionCapture(gatewaytesting.SharedSessionFixturePath(paritySessionFixture))
	if err != nil {
		t.Fatalf("load committed parity session fixture: %v", err)
	}
	return capture
}

func parityScenarioPath(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve parity scenario path: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(currentFile), "../../../../go-agent-loop/pkg/probe/testdata/scenarios", parityScenarioFile)
}

func scenarioName(scenario committedScenario) string {
	if scenario.Name != "" {
		return scenario.Name
	}
	return scenario.ID
}

func runParityScenario(t *testing.T, scenario committedScenario, capture gatewaytesting.SessionCapture, kind parityTransport) transportRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), parityScenarioBudget)
	defer cancel()

	var dialer transport.Dialer
	switch kind {
	case transportWebSocket:
		fixturePath := gatewaytesting.SharedSessionFixturePath(paritySessionFixture)
		var err error
		dialer, err = gatewaytesting.NewReplayWebSocketDialer(fixturePath)
		if err != nil {
			t.Fatalf("open WebSocket replay dialer: %v", err)
		}
	case transportRTC:
		dialer = &rtcReplayDialer{capture: capture}
	default:
		t.Fatalf("unsupported parity transport %q", kind)
	}

	conn, err := dialer.Dial(parityEndpoint, map[string]string{"X-Parity-Scenario": scenarioName(scenario)})
	if err != nil {
		t.Fatalf("dial %s transport: %v", kind, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	logicalClock := clock.NewDeterministic(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), parityClockTick)
	observed := driveScenario(ctx, t, scenario, capture, conn, logicalClock)
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("close %s transport: %v", kind, closeErr)
	}

	if replayDialer, ok := dialer.(*gatewaytesting.ReplayWebSocketDialer); ok {
		if replayErr := replayDialer.Err(); replayErr != nil {
			t.Fatalf("%s replay diverged: %v", kind, replayErr)
		}
		select {
		case <-replayDialer.Done():
		default:
			t.Fatalf("%s replay did not reach the end of the fixture", kind)
		}
	}
	if replayDialer, ok := dialer.(*rtcReplayDialer); ok {
		if replayErr := replayDialer.Err(); replayErr != nil {
			t.Fatalf("%s replay diverged: %v", kind, replayErr)
		}
	}

	clientProjection, err := parity.NormalizeClient(observed.client)
	if err != nil {
		t.Fatalf("normalize %s client transcript: %v", kind, err)
	}
	agentProjection, err := parity.NormalizeAgent(string(kind)+" agent", observed.agent)
	if err != nil {
		t.Fatalf("normalize %s agent transcript: %v", kind, err)
	}
	return transportRun{Projection: clientProjection, Client: clientProjection, Agent: agentProjection}
}

func driveScenario(ctx context.Context, t *testing.T, scenario committedScenario, capture gatewaytesting.SessionCapture, conn transport.Conn, logicalClock *clock.Deterministic) scenarioCapture {
	t.Helper()
	if len(scenario.Steps) != 2 || scenario.Steps[0].Type != "send_text" || scenario.Steps[1].Type != "close" {
		t.Fatalf("parity test expects the committed text-then-close scenario, got %+v", scenario.Steps)
	}
	if len(capture.Records) == 0 {
		t.Fatal("parity fixture has no records")
	}

	observed := scenarioCapture{
		client: make([]transcript.Record, 0, len(capture.Records)),
		agent:  make([]transcript.Record, 0, len(capture.Records)),
	}

	request := conversationItemCreate(scenario.Steps[0].Text)
	expected := capture.Records[0]
	if expected.Direction != gatewaytesting.DirectionClientToServer {
		t.Fatalf("first fixture record direction = %q, want client_to_server", expected.Direction)
	}
	if err := conn.WriteMessage(1, request); err != nil {
		t.Fatalf("send scenario text over transport: %v", err)
	}
	if !jsonEqual(eventPayload(expected), request) {
		t.Fatalf("scenario text did not produce the fixture's first outbound event")
	}
	appendSemanticObservation(t, &observed, logicalClock, expected, request)

	fixtureIndex := 1
	for fixtureIndex < len(capture.Records) {
		if ctx.Err() != nil {
			t.Fatalf("scenario context expired at fixture record %d: %v", fixtureIndex+1, ctx.Err())
		}
		expected = capture.Records[fixtureIndex]
		if expected.Direction != gatewaytesting.DirectionServerToClient {
			t.Fatalf("fixture record %d is %q; transport replay advanced out of order", expected.Sequence, expected.Direction)
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read fixture record %d from transport: %v", expected.Sequence, err)
		}
		if !jsonEqual(eventPayload(expected), payload) {
			t.Fatalf("fixture record %d payload changed across transport", expected.Sequence)
		}
		appendSemanticObservation(t, &observed, logicalClock, expected, payload)
		fixtureIndex++
		if expected.Type == "session.closed" {
			break
		}
	}
	if fixtureIndex != len(capture.Records) {
		t.Fatalf("scenario stopped after fixture record %d of %d", fixtureIndex, len(capture.Records))
	}
	return observed
}

func appendSemanticObservation(t *testing.T, observed *scenarioCapture, logicalClock *clock.Deterministic, event gatewaytesting.CapturedSessionEvent, payload []byte) {
	t.Helper()
	semantic, stream, ok, err := semanticParityEvent(event, payload)
	if err != nil {
		t.Fatalf("translate fixture record %d into parity evidence: %v", event.Sequence, err)
	}
	if !ok {
		return
	}
	if event.TimestampMs < 0 {
		t.Fatalf("fixture record %d has negative timestamp %d", event.Sequence, event.TimestampMs)
	}
	tick := uint64(event.TimestampMs)
	logicalClock.AdvanceTo(tick)
	timestamp := logicalClock.Now()
	clientDirection, agentDirection := transcript.DirectionIn, transcript.DirectionOut
	if event.Direction == gatewaytesting.DirectionClientToServer {
		clientDirection, agentDirection = transcript.DirectionOut, transcript.DirectionIn
	}
	observed.client = append(observed.client, transcript.NewRecord(tick, timestamp, transcript.PeerClient, clientDirection, stream, semantic))
	observed.agent = append(observed.agent, transcript.NewRecord(tick, timestamp, transcript.PeerAgent, agentDirection, stream, semantic))
}

func semanticParityEvent(event gatewaytesting.CapturedSessionEvent, payload []byte) ([]byte, transcript.Stream, bool, error) {
	switch event.Type {
	case "conversation.item.create":
		return []byte(`{"kind":"turn.start","id":"turn-1","role":"user"}`), transcript.StreamWS, true, nil
	case "response.audio.delta":
		var value struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(payload, &value); err != nil || value.Delta == "" {
			if err == nil {
				err = errors.New("audio delta is empty")
			}
			return nil, "", false, err
		}
		semantic, err := json.Marshal(struct {
			Kind  string `json:"kind"`
			Bytes string `json:"bytes"`
		}{Kind: "audio.frame", Bytes: value.Delta})
		return semantic, transcript.StreamRTCAudio, true, err
	case "response.audio_transcript.delta":
		var value struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(payload, &value); err != nil || value.Delta == "" {
			if err == nil {
				err = errors.New("audio transcript delta is empty")
			}
			return nil, "", false, err
		}
		semantic, err := json.Marshal(struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}{Kind: "transcript", Text: value.Delta})
		return semantic, transcript.StreamWS, true, err
	case "response.done":
		return []byte(`{"kind":"turn.end","id":"turn-1","role":"assistant"}`), transcript.StreamWS, true, nil
	case "session.closed":
		var value struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(payload, &value); err != nil || value.Reason == "" {
			if err == nil {
				err = errors.New("session close reason is empty")
			}
			return nil, "", false, err
		}
		semantic, err := json.Marshal(struct {
			Kind       string `json:"kind"`
			Reason     string `json:"reason"`
			Provenance string `json:"provenance"`
		}{Kind: "terminal", Reason: value.Reason, Provenance: "provider"})
		return semantic, transcript.StreamWS, true, err
	default:
		return nil, "", false, nil
	}
}

func conversationItemCreate(text string) []byte {
	return mustJSON(struct {
		Type string `json:"type"`
		Item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}{
		Type: "conversation.item.create",
		Item: struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}{
			Type: "message",
			Role: "user",
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{Type: "input_text", Text: text}},
		},
	})
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func eventPayload(event gatewaytesting.CapturedSessionEvent) []byte {
	if len(event.Payload) > 0 {
		return event.Payload
	}
	return event.Data
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return bytes.Equal(left, right)
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return bytes.Equal(left, right)
	}
	leftJSON, err := json.Marshal(leftValue)
	if err != nil {
		return bytes.Equal(left, right)
	}
	rightJSON, err := json.Marshal(rightValue)
	if err != nil {
		return bytes.Equal(left, right)
	}
	return bytes.Equal(leftJSON, rightJSON)
}

type rtcReplayDialer struct {
	capture gatewaytesting.SessionCapture
	mu      sync.Mutex
	conn    *rtcDataConn
}

func (d *rtcReplayDialer) Dial(endpoint string, _ map[string]string) (transport.Conn, error) {
	if endpoint != parityEndpoint {
		return nil, fmt.Errorf("unexpected RTC endpoint %q", endpoint)
	}
	conn, err := newRTCDataConn(cloneCapturedEvents(d.capture.Records))
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.conn = conn
	d.mu.Unlock()
	return conn, nil
}

func (d *rtcReplayDialer) Err() error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Err()
}

func cloneCapturedEvents(events []gatewaytesting.CapturedSessionEvent) []gatewaytesting.CapturedSessionEvent {
	cloned := make([]gatewaytesting.CapturedSessionEvent, len(events))
	for i, event := range events {
		cloned[i] = event
		cloned[i].Payload = append([]byte(nil), event.Payload...)
		cloned[i].Data = append([]byte(nil), event.Data...)
	}
	return cloned
}

const parityDataChannelLabel = "s2s-parity"

type rtcReplayState struct {
	mu     sync.Mutex
	events []gatewaytesting.CapturedSessionEvent
	index  int
	err    error
	failed chan struct{}
	closed chan struct{}

	failOnce  sync.Once
	closeOnce sync.Once
}

func newRTCReplayState(events []gatewaytesting.CapturedSessionEvent) *rtcReplayState {
	return &rtcReplayState{
		events: events,
		failed: make(chan struct{}),
		closed: make(chan struct{}),
	}
}

func (s *rtcReplayState) fail(err error) {
	if err == nil {
		return
	}
	s.failOnce.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.failed)
	})
}

func (s *rtcReplayState) failIfOpen(err error) {
	if !s.isClosed() {
		s.fail(err)
	}
}

func (s *rtcReplayState) markClosed() {
	s.closeOnce.Do(func() { close(s.closed) })
}

func (s *rtcReplayState) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *rtcReplayState) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *rtcReplayState) closeError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.index != len(s.events) {
		return fmt.Errorf("RTC replay incomplete at record %d of %d", s.index, len(s.events))
	}
	return nil
}

func (s *rtcReplayState) receive(dc *webrtc.DataChannel, message webrtc.DataChannelMessage) {
	if !message.IsString {
		s.failIfOpen(errors.New("RTC replay received a binary message; want a text message"))
		return
	}

	s.mu.Lock()
	if s.err != nil || s.isClosed() {
		s.mu.Unlock()
		return
	}
	if s.index >= len(s.events) {
		s.mu.Unlock()
		s.fail(errors.New("RTC replay received an unexpected outbound event after the fixture completed"))
		return
	}
	expected := s.events[s.index]
	if expected.Direction != gatewaytesting.DirectionClientToServer {
		s.mu.Unlock()
		s.fail(fmt.Errorf("RTC replay expected %s event %s at sequence %d before outbound traffic", expected.Direction, expected.Type, expected.Sequence))
		return
	}
	if !jsonEqual(eventPayload(expected), message.Data) {
		s.mu.Unlock()
		s.fail(fmt.Errorf("RTC replay outbound payload for %s at sequence %d did not match the fixture", expected.Type, expected.Sequence))
		return
	}
	s.index++
	responses := make([][]byte, 0)
	for s.index < len(s.events) && s.events[s.index].Direction == gatewaytesting.DirectionServerToClient {
		responses = append(responses, append([]byte(nil), eventPayload(s.events[s.index])...))
		s.index++
	}
	s.mu.Unlock()

	for _, payload := range responses {
		if err := dc.SendText(string(payload)); err != nil {
			s.failIfOpen(fmt.Errorf("RTC replay send server event: %w", err))
			return
		}
	}
}

type rtcDataMessage struct {
	messageType int
	payload     []byte
}

type rtcDataConn struct {
	clientDataChannel *webrtc.DataChannel
	clientPeer        *webrtc.PeerConnection
	serverPeer        *webrtc.PeerConnection
	state             *rtcReplayState
	inbound           chan rtcDataMessage

	mu        sync.Mutex
	closed    bool
	closeErr  error
	closeOnce sync.Once
}

func newRTCDataConn(events []gatewaytesting.CapturedSessionEvent) (_ *rtcDataConn, err error) {
	if len(events) == 0 {
		return nil, errors.New("RTC replay fixture has no records")
	}
	state := newRTCReplayState(events)
	offerer, answerer, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: paritySignalingWait})
	if err != nil {
		return nil, fmt.Errorf("create RTC loopback signaling pair: %w", err)
	}
	defer offerer.Close()
	defer answerer.Close()

	api := parityRTCAPI()
	clientPeer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("create RTC client peer: %w", err)
	}
	serverPeer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		_ = clientPeer.Close()
		return nil, fmt.Errorf("create RTC server peer: %w", err)
	}
	setupComplete := false
	defer func() {
		if !setupComplete {
			state.markClosed()
			_ = clientPeer.Close()
			_ = serverPeer.Close()
		}
	}()

	inbound := make(chan rtcDataMessage, len(events))
	clientOpen := make(chan struct{})
	serverSeen := make(chan struct{})
	serverOpen := make(chan struct{})
	var clientOpenOnce, serverSeenOnce, serverOpenOnce sync.Once

	clientPeer.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		if connectionState == webrtc.PeerConnectionStateFailed || connectionState == webrtc.PeerConnectionStateClosed {
			if !state.isClosed() {
				state.fail(fmt.Errorf("RTC client peer reached %s", connectionState))
			}
		}
	})
	serverPeer.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		if connectionState == webrtc.PeerConnectionStateFailed || connectionState == webrtc.PeerConnectionStateClosed {
			if !state.isClosed() {
				state.fail(fmt.Errorf("RTC server peer reached %s", connectionState))
			}
		}
	})
	serverPeer.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		if dataChannel.Label() != parityDataChannelLabel {
			state.fail(fmt.Errorf("RTC server received unexpected data channel %q", dataChannel.Label()))
			return
		}
		serverSeenOnce.Do(func() { close(serverSeen) })
		dataChannel.OnOpen(func() { serverOpenOnce.Do(func() { close(serverOpen) }) })
		dataChannel.OnMessage(func(message webrtc.DataChannelMessage) { state.receive(dataChannel, message) })
		dataChannel.OnError(func(dataChannelErr error) {
			state.failIfOpen(fmt.Errorf("RTC server data channel: %w", dataChannelErr))
		})
		dataChannel.OnClose(func() {
			if !state.isClosed() {
				state.fail(errors.New("RTC server data channel closed before replay completed"))
			}
		})
	})

	clientDataChannel, err := clientPeer.CreateDataChannel(parityDataChannelLabel, nil)
	if err != nil {
		return nil, fmt.Errorf("create RTC data channel: %w", err)
	}
	clientDataChannel.OnOpen(func() { clientOpenOnce.Do(func() { close(clientOpen) }) })
	clientDataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		messageType := 2
		if message.IsString {
			messageType = 1
		}
		select {
		case inbound <- rtcDataMessage{messageType: messageType, payload: append([]byte(nil), message.Data...)}:
		case <-state.failed:
		case <-state.closed:
		}
	})
	clientDataChannel.OnError(func(dataChannelErr error) {
		state.failIfOpen(fmt.Errorf("RTC client data channel: %w", dataChannelErr))
	})
	clientDataChannel.OnClose(func() {
		if !state.isClosed() {
			state.fail(errors.New("RTC client data channel closed before replay completed"))
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), parityRTCConnectWait)
	defer cancel()
	if err := negotiateRTCDataChannel(ctx, offerer, answerer, clientPeer, serverPeer); err != nil {
		return nil, err
	}
	for _, ready := range []struct {
		name string
		ch   <-chan struct{}
	}{{"server data channel", serverSeen}, {"server data channel open", serverOpen}, {"client data channel open", clientOpen}} {
		if err := waitRTCReady(ctx, state, ready.ch, ready.name); err != nil {
			return nil, err
		}
	}

	setupComplete = true
	return &rtcDataConn{
		clientDataChannel: clientDataChannel,
		clientPeer:        clientPeer,
		serverPeer:        serverPeer,
		state:             state,
		inbound:           inbound,
	}, nil
}

const parityRTCConnectWait = 2 * time.Second

func parityRTCAPI() *webrtc.API {
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	return webrtc.NewAPI(webrtc.WithSettingEngine(settings))
}

func negotiateRTCDataChannel(ctx context.Context, offerer, answerer *rtc.LoopbackEndpoint, clientPeer, serverPeer *webrtc.PeerConnection) error {
	offer, err := clientPeer.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create RTC offer: %w", err)
	}
	offer, err = setLocalAndGather(ctx, clientPeer, offer)
	if err != nil {
		return fmt.Errorf("gather RTC offer: %w", err)
	}
	if err := offerer.SendOffer(ctx, rtc.SessionDescription{Type: offer.Type.String(), SDP: offer.SDP}); err != nil {
		return fmt.Errorf("send RTC offer: %w", err)
	}
	receivedOffer, err := answerer.ReceiveOffer(ctx)
	if err != nil {
		return fmt.Errorf("receive RTC offer: %w", err)
	}
	if err := serverPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: receivedOffer.SDP}); err != nil {
		return fmt.Errorf("set RTC server remote offer: %w", err)
	}

	answer, err := serverPeer.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create RTC answer: %w", err)
	}
	answer, err = setLocalAndGather(ctx, serverPeer, answer)
	if err != nil {
		return fmt.Errorf("gather RTC answer: %w", err)
	}
	if err := answerer.SendAnswer(ctx, rtc.SessionDescription{Type: answer.Type.String(), SDP: answer.SDP}); err != nil {
		return fmt.Errorf("send RTC answer: %w", err)
	}
	receivedAnswer, err := offerer.ReceiveAnswer(ctx)
	if err != nil {
		return fmt.Errorf("receive RTC answer: %w", err)
	}
	if err := clientPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: receivedAnswer.SDP}); err != nil {
		return fmt.Errorf("set RTC client remote answer: %w", err)
	}
	return nil
}

func setLocalAndGather(ctx context.Context, peer *webrtc.PeerConnection, description webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if err := peer.SetLocalDescription(description); err != nil {
		return webrtc.SessionDescription{}, err
	}
	select {
	case <-webrtc.GatheringCompletePromise(peer):
	case <-ctx.Done():
		return webrtc.SessionDescription{}, ctx.Err()
	}
	local := peer.LocalDescription()
	if local == nil {
		return webrtc.SessionDescription{}, errors.New("RTC peer has no local description after gathering")
	}
	return *local, nil
}

func waitRTCReady(ctx context.Context, state *rtcReplayState, ready <-chan struct{}, name string) error {
	select {
	case <-ready:
		return nil
	case <-state.failed:
		return fmt.Errorf("wait for %s: %w", name, state.failure())
	case <-ctx.Done():
		return fmt.Errorf("wait for %s: %w", name, ctx.Err())
	}
}

func (c *rtcDataConn) Err() error {
	return c.state.failure()
}

func (c *rtcDataConn) ReadMessage() (int, []byte, error) {
	select {
	case message := <-c.inbound:
		return message.messageType, message.payload, nil
	case <-c.state.failed:
		return 0, nil, fmt.Errorf("RTC data channel replay: %w", c.state.failure())
	case <-c.state.closed:
		return 0, nil, io.EOF
	}
}

func (c *rtcDataConn) WriteMessage(messageType int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	if messageType != 1 && messageType != 2 {
		return fmt.Errorf("RTC data channel does not support message type %d", messageType)
	}
	var err error
	if messageType == 1 {
		err = c.clientDataChannel.SendText(string(payload))
	} else {
		err = c.clientDataChannel.Send(payload)
	}
	if err != nil {
		wrapped := fmt.Errorf("RTC data channel write: %w", err)
		c.state.fail(wrapped)
		return wrapped
	}
	return nil
}

func (c *rtcDataConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.state.markClosed()

		errs := []error{c.state.closeError()}
		if err := c.clientDataChannel.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close RTC data channel: %w", err))
		}
		if err := c.clientPeer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close RTC client peer: %w", err))
		}
		if err := c.serverPeer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close RTC server peer: %w", err))
		}
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}

var _ transport.Conn = (*rtcDataConn)(nil)
