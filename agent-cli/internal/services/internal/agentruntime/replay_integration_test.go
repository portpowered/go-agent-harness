package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	internalreplay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/replay"
	publicreplay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type replayObservationConn struct {
	reads  [][]byte
	closed bool
}

func (c *replayObservationConn) ReadMessage() (int, []byte, error) {
	if len(c.reads) == 0 {
		return 0, nil, io.EOF
	}
	payload := c.reads[0]
	c.reads = c.reads[1:]
	return 1, payload, nil
}

func (*replayObservationConn) WriteMessage(int, []byte) error { return nil }
func (c *replayObservationConn) Close() error                 { c.closed = true; return nil }

type replayObservationDialer struct{ conn transport.Conn }

func (d replayObservationDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

func TestReplayServiceConsumesActualWireObservationEnvelope(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "trace")
	scheduler := clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond)
	trace, err := recording.NewTrace(directory, scheduler)
	if err != nil {
		t.Fatal(err)
	}
	conn := &replayObservationConn{reads: [][]byte{
		[]byte(`{"type":"session.created"}`),
		[]byte(`{"type":"response.done"}`),
	}}
	dialer := observeSessionWire(replayObservationDialer{conn: conn}, SessionRunOptions{ModelCatalog: testModelCatalog(), Clock: scheduler, RuntimeObserver: TraceRuntimeObserver{Trace: trace}})
	transportConn, err := dialer.Dial("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	update := []byte(`{"type":"session.update","session":{"model":"gpt-test"}}`)
	if err := transportConn.WriteMessage(1, update); err != nil {
		t.Fatal(err)
	}
	if _, _, err := transportConn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := transportConn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}

	prepared, err := internalreplay.New(internalreplay.Dependencies{ClockFactory: func(origin time.Time) *clock.Deterministic {
		return clock.NewDeterministic(origin, time.Millisecond)
	}}).Prepare(context.Background(), publicreplay.Request{BundlePath: directory, Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Capture.Records) != 3 {
		t.Fatalf("capture records=%d", len(prepared.Capture.Records))
	}
	if prepared.Capture.Records[0].Direction != "client_to_server" || prepared.Capture.Records[0].Type != "session.update" {
		t.Fatalf("first replay record=%+v", prepared.Capture.Records[0])
	}
	if prepared.Capture.Records[0].Type == "message_type" || !bytesEqual(prepared.Capture.Records[0].Payload, update) {
		t.Fatalf("wire envelope was not unwrapped: %s", prepared.Capture.Records[0].Payload)
	}
	if err := prepared.Close(); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("close before wire consumption=%v", err)
	}
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// TestReplayServiceRunsTheRealCoreLoop uses the same OpenAI session adapter
// used by production session planning. The source side is a local scripted
// provider, and the replay side receives only the prepared in-memory dialer;
// no provider endpoint, device, or executable tool is available.
func TestReplayServiceRunsRealCoreLoopAgainstRecordedWire(t *testing.T) {
	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheduler := clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond)
	trace, err := recording.NewTrace(directory, scheduler)
	if err != nil {
		t.Fatal(err)
	}
	sourceConn := newScriptedReplaySourceConn([][]byte{
		[]byte(`{"type":"session.created","session":{"id":"sess-1","model":"gpt-test"}}`),
		[]byte(`{"type":"response.created","response":{"id":"resp-1"}}`),
		[]byte(`{"type":"response.output_item.added","response_id":"resp-1","item":{"type":"function_call","call_id":"call-1","name":"lookup"}}`),
		[]byte(`{"type":"response.function_call_arguments.delta","response_id":"resp-1","call_id":"call-1","delta":"{\"q\":"}`),
		[]byte(`{"type":"response.function_call_arguments.done","response_id":"resp-1","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"value\"}"}`),
		[]byte(`{"type":"response.done","response":{"id":"resp-1","status":"completed"}}`),
		[]byte(`{"type":"response.created","response":{"id":"resp-2"}}`),
		[]byte(`{"type":"response.output_text.delta","response_id":"resp-2","delta":"offline answer"}`),
		[]byte(`{"type":"response.output_text.done","response_id":"resp-2"}`),
		[]byte(`{"type":"response.done","response":{"id":"resp-2","status":"completed"}}`),
	})
	dialer := observeSessionWire(scriptedReplaySourceDialer{conn: sourceConn}, SessionRunOptions{ModelCatalog: testModelCatalog(),
		Clock: scheduler, RuntimeObserver: scriptedTraceObserver{trace: trace, conn: sourceConn},
	})
	provider := oaiprovider.New(oaiprovider.WithAPIKey("offline"), oaiprovider.WithModel("gpt-test"), oaiprovider.WithRealtimeBaseURL("ws://offline.invalid/realtime"), oaiprovider.WithWebSocketDialer(dialer))
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Receive().ReadContext(ctx); err != nil {
		t.Fatal("session.created: ", err)
	}
	completeSender, ok := session.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	if !ok || !completeSender.SendMessage(ctx, messages.NewTextMessage(messages.RoleUser, "hello")) {
		t.Fatal("source prompt was not accepted")
	}
	for {
		message, err := session.Receive().ReadContext(ctx)
		if err != nil {
			t.Fatal("source response: ", err)
		}
		if message.Type == messages.StreamTypeMessageEnd {
			break
		}
	}
	completeSender, ok = session.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	if !ok || !completeSender.SendMessage(ctx, messages.Message{Role: messages.RoleTool, ToolCallID: "call-1", ContentParts: []messages.ContentPart{messages.TextPart{Text: "answer"}}}) {
		t.Fatal("source tool result was not accepted")
	}
	for {
		message, err := session.Receive().ReadContext(ctx)
		if err != nil {
			sourceConn.mu.Lock()
			writes := append([]string(nil), sourceConn.writes...)
			sourceConn.mu.Unlock()
			t.Fatalf("source continuation: %v (writes=%v)", err, writes)
		}
		if message.Type == messages.StreamTypeMessageEnd {
			break
		}
	}
	callPayload, err := json.Marshal(messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"q":"value"}`})
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_call", Payload: callPayload, Clean: true})
	resultPayload, err := json.Marshal(struct {
		CallID   string                    `json:"call_id"`
		Name     string                    `json:"name"`
		Response messages.ToolCallResponse `json:"response"`
		Failed   bool                      `json:"failed"`
	}{"call-1", "lookup", messages.ToolCallResponse{ToolCallID: "call-1", Name: "lookup", Content: "answer"}, false})
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_result", Payload: resultPayload, Clean: true})
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}

	service := internalreplay.New(internalreplay.Dependencies{
		ClockFactory: func(origin time.Time) *clock.Deterministic { return clock.NewDeterministic(origin, time.Millisecond) },
		Runtime:      internalreplay.NewOpenAIRuntimeFactory(),
	})
	var output bytes.Buffer
	replayCtx, replayCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer replayCancel()
	if _, err := service.Run(replayCtx, &output, publicreplay.Request{BundlePath: directory, Model: "gpt-test"}); err != nil {
		t.Fatal("offline core replay: ", err)
	}
	if output.String() != "offline answer" {
		t.Fatalf("core output=%q, want provider text", output.String())
	}
}

type scriptedReplaySourceDialer struct{ conn transport.Conn }

func (d scriptedReplaySourceDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

type scriptedTraceObserver struct {
	trace *recording.Trace
	conn  *scriptedReplaySourceConn
}

func (o scriptedTraceObserver) ObserveSessionRuntime(event SessionRuntimeObservation) {
	(TraceRuntimeObserver{Trace: o.trace}).ObserveSessionRuntime(event)
	if event.Kind != "provider_wire_send" {
		return
	}
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(event.Payload, &envelope) != nil {
		return
	}
	var wire struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(envelope.Payload, &wire) != nil {
		return
	}
	switch {
	case wire.Type == "response.create":
		o.conn.mu.Lock()
		toolResult := o.conn.toolResult
		o.conn.mu.Unlock()
		if toolResult {
			o.conn.toolOnce.Do(func() { close(o.conn.toolGate) })
		} else {
			o.conn.initialOnce.Do(func() { close(o.conn.initialGate) })
		}
	case wire.Type == "conversation.item.create" && wire.Item.Type == "function_call_output":
		o.conn.mu.Lock()
		o.conn.toolResult = true
		o.conn.mu.Unlock()
	}
}

type scriptedReplaySourceConn struct {
	mu          sync.Mutex
	reads       [][]byte
	initialGate chan struct{}
	toolGate    chan struct{}
	initialOnce sync.Once
	toolOnce    sync.Once
	toolResult  bool
	writes      []string
}

func newScriptedReplaySourceConn(reads [][]byte) *scriptedReplaySourceConn {
	return &scriptedReplaySourceConn{reads: reads, initialGate: make(chan struct{}), toolGate: make(chan struct{})}
}

func (c *scriptedReplaySourceConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Hold the first provider response until the recorded user turn has been
	// written. The read loop can otherwise prefetch response.created before the
	// caller's SendMessage, producing a trace with the wrong causal order.
	if len(c.reads) == 9 {
		select {
		case <-c.initialGate:
		default:
			c.mu.Unlock()
			<-c.initialGate
			c.mu.Lock()
		}
	}
	// Stop the scripted provider after the first response.done, before it
	// exposes the continuation. This makes the recorded tool result the causal
	// gate for the second response instead of relying on an outbound write
	// count, which varies with provider admission batching.
	if len(c.reads) == 4 {
		select {
		case <-c.toolGate:
		default:
			c.mu.Unlock()
			<-c.toolGate
			c.mu.Lock()
		}
	}
	if len(c.reads) == 0 {
		return 0, nil, io.EOF
	}
	payload := c.reads[0]
	c.reads = c.reads[1:]
	return 1, payload, nil
}

func (c *scriptedReplaySourceConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	_ = json.Unmarshal(payload, &event)
	c.writes = append(c.writes, event.Type+":"+event.Item.Type)
	return nil
}

func (c *scriptedReplaySourceConn) Close() error {
	c.initialOnce.Do(func() { close(c.initialGate) })
	c.toolOnce.Do(func() { close(c.toolGate) })
	return nil
}
