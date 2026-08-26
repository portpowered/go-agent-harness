package fault

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestWrapConnMidStreamCloseIsTypedAndDeterministic(t *testing.T) {
	inner := newFaultTestConn(
		[]faultTestFrame{{Type: 1, Payload: []byte("first")}, {Type: 2, Payload: []byte("second")}, {Type: 1, Payload: []byte("never delivered")}},
	)
	conn, err := WrapConn(inner, WithMidStreamCloseAfter(2))
	if err != nil {
		t.Fatalf("WrapConn: %v", err)
	}

	for index, want := range []faultTestFrame{
		{Type: 1, Payload: []byte("first")},
		{Type: 2, Payload: []byte("second")},
	} {
		gotType, gotPayload, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("ReadMessage[%d]: %v", index, readErr)
		}
		if gotType != want.Type || string(gotPayload) != string(want.Payload) {
			t.Fatalf("ReadMessage[%d] = (%d, %q), want (%d, %q)", index, gotType, gotPayload, want.Type, want.Payload)
		}
	}

	_, _, readErr := conn.ReadMessage()
	var closeErr *MidStreamCloseError
	if !errors.As(readErr, &closeErr) {
		t.Fatalf("fault read error = %v, want *MidStreamCloseError", readErr)
	}
	if !errors.Is(readErr, ErrMidStreamClose) || !errors.Is(readErr, io.EOF) {
		t.Fatalf("fault read error = %v, want injected and EOF identities", readErr)
	}
	if closeErr.AfterFrames != 2 || closeErr.ObservedFrames != 2 {
		t.Fatalf("fault metadata = %#v, want after/observed 2", closeErr)
	}
	if got := conn.ReadFrames(); got != 2 {
		t.Fatalf("ReadFrames = %d, want 2", got)
	}
	if got := inner.CloseCount(); got != 1 {
		t.Fatalf("inner close count = %d, want 1", got)
	}

	// The fault remains stable on repeated operations and Close is idempotent;
	// neither behavior can wedge a session trying to tear down after the read.
	_, _, repeatedErr := conn.ReadMessage()
	if !errors.Is(repeatedErr, ErrMidStreamClose) {
		t.Fatalf("repeated read error = %v, want injected close", repeatedErr)
	}
	if writeErr := conn.WriteMessage(1, []byte("after close")); !errors.Is(writeErr, ErrMidStreamClose) {
		t.Fatalf("post-fault write error = %v, want injected close", writeErr)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("first Close: %v", closeErr)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("second Close: %v", closeErr)
	}
	if got := inner.CloseCount(); got != 1 {
		t.Fatalf("inner close count after repeated Close = %d, want 1", got)
	}
}

func TestWrapDialerAppliesMidStreamCloseToEveryConnection(t *testing.T) {
	inner := &faultTestDialer{conn: newFaultTestConn([]faultTestFrame{{Type: 1, Payload: []byte("frame")}})}
	dialer, err := WrapDialer(inner, WithMidStreamClose(0))
	if err != nil {
		t.Fatalf("WrapDialer: %v", err)
	}

	conn, err := dialer.Dial("fault://scenario", map[string]string{"X-Test": "fault"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, _, err := conn.ReadMessage(); !errors.Is(err, ErrMidStreamClose) {
		t.Fatalf("zero-frame read error = %v, want injected close", err)
	}
	if inner.endpoint != "fault://scenario" || inner.headers["X-Test"] != "fault" {
		t.Fatalf("dial forwarding = endpoint %q headers %#v", inner.endpoint, inner.headers)
	}
	if got := inner.conn.CloseCount(); got != 1 {
		t.Fatalf("inner close count = %d, want 1", got)
	}
}

func TestMidStreamCloseChangesGrokSessionOutcomeFromCleanCompletion(t *testing.T) {
	clean := runFaultScenario(t)
	faulted := runFaultScenario(t, WithMidStreamCloseAfter(3))

	if clean.err != nil {
		t.Fatalf("clean scenario error: %v", clean.err)
	}
	if !clean.sawMessageEnd {
		t.Fatalf("clean scenario did not reach MESSAGE.END: %#v", clean.messages)
	}
	if clean.sawError {
		t.Fatalf("clean scenario emitted ERROR: %#v", clean.messages)
	}

	if faulted.err != nil {
		t.Fatalf("faulted scenario harness error: %v", faulted.err)
	}
	if !faulted.sawError {
		t.Fatalf("faulted scenario did not emit typed ERROR: %#v", faulted.messages)
	}
	if faulted.sawMessageEnd {
		t.Fatalf("faulted scenario reached clean MESSAGE.END: %#v", faulted.messages)
	}
	if !errors.Is(faulted.errorValue.Err, ErrMidStreamClose) {
		t.Fatalf("faulted stream error = %v, want injected close identity", faulted.errorValue.Err)
	}
	var closeErr *MidStreamCloseError
	if !errors.As(faulted.errorValue.Err, &closeErr) {
		t.Fatalf("faulted stream error = %v, want typed close error", faulted.errorValue.Err)
	}
	if closeErr.AfterFrames != 3 || closeErr.ObservedFrames != 3 {
		t.Fatalf("faulted close metadata = %#v, want after/observed 3", closeErr)
	}
	if faulted.errorValue.Classification != "transport" ||
		faulted.errorValue.TerminalReason != messages.TerminalReasonTerminalFailure ||
		faulted.errorValue.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("faulted terminal value = %#v, want typed transport failure", faulted.errorValue)
	}
}

type faultScenarioResult struct {
	messages      []messages.StreamMessage
	sawMessageEnd bool
	sawError      bool
	errorValue    *messages.ErrorValue
	err           error
}

func runFaultScenario(t *testing.T, options ...Option) faultScenarioResult {
	t.Helper()
	inner := &faultTestDialer{conn: newFaultTestConn(faultScenarioFrames())}
	var dialer transport.Dialer = inner
	if len(options) > 0 {
		wrapped, err := WrapDialer(inner, options...)
		if err != nil {
			t.Fatalf("WrapDialer: %v", err)
		}
		dialer = wrapped
	}
	provider := grok.New(
		grok.WithAPIKey("fault-test-key"),
		grok.WithBaseURL("wss://fault.test/v1/realtime"),
		grok.WithWebSocketDialer(dialer),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := provider.ConnectSession(ctx, structSessionConfig())
	if err != nil {
		return faultScenarioResult{err: err}
	}
	defer func() { _ = session.Close() }()

	result := faultScenarioResult{}
	for {
		msg, ok := session.Receive().ReadBlockingContext(ctx)
		if !ok {
			result.err = ctx.Err()
			return result
		}
		result.messages = append(result.messages, msg)
		switch msg.Type {
		case messages.StreamTypeMessageEnd:
			result.sawMessageEnd = true
			return result
		case messages.StreamTypeError:
			result.sawError = true
			value, ok := msg.Value.(*messages.ErrorValue)
			if !ok || value == nil {
				result.err = errors.New("session ERROR carried a non-ErrorValue")
				return result
			}
			result.errorValue = value
			return result
		}
	}
}

func structSessionConfig() models.SessionConfig {
	return models.SessionConfig{Model: "grok-fault-injection"}
}

func faultScenarioFrames() []faultTestFrame {
	return []faultTestFrame{
		{Type: 1, Payload: []byte(`{"type":"session.created","session_id":"fault-session","model":"grok-fault-injection"}`)},
		{Type: 1, Payload: []byte(`{"type":"response.created"}`)},
		{Type: 1, Payload: []byte(`{"type":"response.text.delta","delta":"same deterministic answer"}`)},
		{Type: 1, Payload: []byte(`{"type":"response.done"}`)},
	}
}

type faultTestFrame struct {
	Type    int
	Payload []byte
}

type faultTestConn struct {
	mu        sync.Mutex
	frames    []faultTestFrame
	readIdx   int
	closed    bool
	closeCh   chan struct{}
	closeOnce sync.Once
	closeN    int
}

func newFaultTestConn(frames []faultTestFrame) *faultTestConn {
	owned := make([]faultTestFrame, len(frames))
	for i, frame := range frames {
		owned[i] = faultTestFrame{Type: frame.Type, Payload: append([]byte(nil), frame.Payload...)}
	}
	return &faultTestConn{frames: owned, closeCh: make(chan struct{})}
}

func (c *faultTestConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, nil, io.EOF
	}
	if c.readIdx < len(c.frames) {
		frame := c.frames[c.readIdx]
		c.readIdx++
		c.mu.Unlock()
		return frame.Type, append([]byte(nil), frame.Payload...), nil
	}
	c.mu.Unlock()

	<-c.closeCh
	return 0, nil, io.EOF
}

func (c *faultTestConn) WriteMessage(_ int, _ []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	return nil
}

func (c *faultTestConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.closeN++
		c.mu.Unlock()
		close(c.closeCh)
	})
	return nil
}

func (c *faultTestConn) CloseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeN
}

type faultTestDialer struct {
	conn     *faultTestConn
	endpoint string
	headers  map[string]string
}

var _ transport.Dialer = (*faultTestDialer)(nil)

func (d *faultTestDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	d.endpoint = endpoint
	d.headers = make(map[string]string, len(headers))
	for key, value := range headers {
		d.headers[key] = value
	}
	return d.conn, nil
}
