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
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/parity"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
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
	_ transport.Conn   = (*fixtureConn)(nil)
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
}

func (d *rtcReplayDialer) Dial(endpoint string, _ map[string]string) (transport.Conn, error) {
	if endpoint != parityEndpoint {
		return nil, fmt.Errorf("unexpected RTC endpoint %q", endpoint)
	}
	if err := completeLoopbackSignaling(); err != nil {
		return nil, err
	}
	events := append([]gatewaytesting.CapturedSessionEvent(nil), d.capture.Records...)
	return &fixtureConn{events: events}, nil
}

func completeLoopbackSignaling() error {
	offerer, answerer, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: paritySignalingWait})
	if err != nil {
		return fmt.Errorf("create RTC loopback signaling pair: %w", err)
	}
	defer offerer.Close()
	defer answerer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), paritySignalingWait)
	defer cancel()
	offer := rtc.SessionDescription{Type: "offer", SDP: "v=0\r\no=- parity-offer 1 IN IP4 127.0.0.1\r\ns=parity\r\nt=0 0"}
	answer := rtc.SessionDescription{Type: "answer", SDP: "v=0\r\no=- parity-answer 1 IN IP4 127.0.0.1\r\ns=parity\r\nt=0 0"}
	if err := offerer.SendOffer(ctx, offer); err != nil {
		return fmt.Errorf("send RTC offer: %w", err)
	}
	if got, err := answerer.ReceiveOffer(ctx); err != nil || got != offer {
		return fmt.Errorf("receive RTC offer: got %#v, err %v", got, err)
	}
	if err := offerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "offer-candidate"}); err != nil {
		return fmt.Errorf("send RTC offer candidate: %w", err)
	}
	if err := offerer.CompleteCandidateGathering(ctx); err != nil {
		return fmt.Errorf("complete RTC offer gathering: %w", err)
	}
	if candidate, err := answerer.ReceiveCandidate(ctx); err != nil || candidate.Candidate != "offer-candidate" {
		return fmt.Errorf("receive RTC offer candidate: got %#v, err %v", candidate, err)
	}
	if _, err := answerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		return fmt.Errorf("finish RTC offer candidate receive: %w", err)
	}
	if err := answerer.SendAnswer(ctx, answer); err != nil {
		return fmt.Errorf("send RTC answer: %w", err)
	}
	if got, err := offerer.ReceiveAnswer(ctx); err != nil || got != answer {
		return fmt.Errorf("receive RTC answer: got %#v, err %v", got, err)
	}
	if err := answerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "answer-candidate"}); err != nil {
		return fmt.Errorf("send RTC answer candidate: %w", err)
	}
	if err := answerer.CompleteCandidateGathering(ctx); err != nil {
		return fmt.Errorf("complete RTC answer gathering: %w", err)
	}
	if candidate, err := offerer.ReceiveCandidate(ctx); err != nil || candidate.Candidate != "answer-candidate" {
		return fmt.Errorf("receive RTC answer candidate: got %#v, err %v", candidate, err)
	}
	if _, err := offerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		return fmt.Errorf("finish RTC answer candidate receive: %w", err)
	}
	if err := offerer.WaitCandidateGathering(ctx); err != nil {
		return fmt.Errorf("wait for RTC offer gathering: %w", err)
	}
	if err := answerer.WaitCandidateGathering(ctx); err != nil {
		return fmt.Errorf("wait for RTC answer gathering: %w", err)
	}
	return nil
}

type fixtureConn struct {
	events []gatewaytesting.CapturedSessionEvent
	index  int
	closed bool
}

func (c *fixtureConn) ReadMessage() (int, []byte, error) {
	if c.closed {
		return 0, nil, io.EOF
	}
	if c.index >= len(c.events) {
		return 0, nil, io.EOF
	}
	event := c.events[c.index]
	if event.Direction != gatewaytesting.DirectionServerToClient {
		return 0, nil, fmt.Errorf("fixture expects outbound event %s at sequence %d", event.Type, event.Sequence)
	}
	c.index++
	return 1, append([]byte(nil), eventPayload(event)...), nil
}

func (c *fixtureConn) WriteMessage(_ int, payload []byte) error {
	if c.closed {
		return io.ErrClosedPipe
	}
	if c.index >= len(c.events) {
		return fmt.Errorf("unexpected outbound event after fixture completed")
	}
	event := c.events[c.index]
	if event.Direction != gatewaytesting.DirectionClientToServer {
		return fmt.Errorf("fixture expects inbound event %s at sequence %d", event.Type, event.Sequence)
	}
	if !jsonEqual(eventPayload(event), payload) {
		return fmt.Errorf("outbound event %s at sequence %d does not match fixture", event.Type, event.Sequence)
	}
	c.index++
	return nil
}

func (c *fixtureConn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.index != len(c.events) {
		return fmt.Errorf("fixture connection closed at record %d of %d", c.index, len(c.events))
	}
	return nil
}
