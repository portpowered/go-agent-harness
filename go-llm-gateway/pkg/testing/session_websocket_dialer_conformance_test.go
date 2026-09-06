package testing

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/transporttest"
)

// TestRecordingWebSocketDialerSharedTransportS11Conformance proves
// RecordingWebSocketDialer satisfies the shared pkg/transport behavioral
// contract through the transporttest.RunS11 suite: dial forwarding without
// header mutation, caller-owned connections, ordered typed reads, byte-exact
// writes, single close observation, and identity-preserving operation errors.
func TestRecordingWebSocketDialerSharedTransportS11Conformance(t *testing.T) {
	dialErr := &dialerTransportOperationError{operation: "dial"}
	readErr := &dialerTransportOperationError{operation: "read"}
	writeErr := &dialerTransportOperationError{operation: "write"}
	closeErr := &dialerTransportOperationError{operation: "close"}
	h := transporttest.ConformanceHarness{
		Endpoint: "wss://record-s11.invalid/v1/realtime",
		Headers:  map[string]string{"Authorization": "Bearer s11", "X-Trace": "record-s11"},
		Inbound: []transporttest.Message{
			{Type: 1, Payload: []byte(`{"type":"session.created","session_id":"sess-s11"}`)},
			{Type: 1, Payload: []byte(`{"type":"response.text_delta","delta":"hi"}`)},
		},
		Outbound: []transporttest.Message{
			{Type: 1, Payload: []byte(`{"type":"session.update","session":{"model":"record-s11"}}`)},
			{Type: 1, Payload: []byte(`{"type":"conversation.item.create"}`)},
		},
	}
	h.NewValid = func() (transport.Dialer, transporttest.Observer) {
		inner, observer := newDialerTransportFixture(h.Inbound, nil, nil, nil, nil)
		return NewRecordingWebSocketDialer(inner, "record-s11", "record-model"), observer
	}
	h.DialFailure = dialerTransportFailure("dial", dialErr)
	h.ReadFailure = dialerTransportFailure("read", readErr)
	h.WriteFailure = dialerTransportFailure("write", writeErr)
	h.CloseFailure = dialerTransportFailure("close", closeErr)
	transporttest.RunS11(t, h)
}

// TestReplayWebSocketDialerSharedTransportContract adapts the shared S11
// semantics to replay. RunS11's dial-forwarding and operation-failure fixtures
// have no honest replay equivalent (replay owns no inner dialer, Dial never
// fails, and Close always returns nil), so each applicable contract point is
// proven directly against the observable replay behavior.
func TestReplayWebSocketDialerSharedTransportContract(t *testing.T) {
	t.Run("dial ignores endpoint and headers without network access", func(t *testing.T) {
		path := writeReplayConformanceCapture(t, []CapturedSessionEvent{
			websocketCapture(DirectionServerToClient, 1, `{"type":"session.created","session_id":"sess-1"}`),
		})
		dialer, err := NewReplayWebSocketDialer(path)
		if err != nil {
			t.Fatalf("NewReplayWebSocketDialer: %v", err)
		}
		headers := map[string]string{"Authorization": "Bearer replay"}
		conn, err := dialer.Dial("wss://127.0.0.1:1/unreachable", headers)
		if err != nil {
			t.Fatalf("Dial must never open a connection or fail: %v", err)
		}
		if conn == nil {
			t.Fatal("Dial returned a nil connection with nil error")
		}
		if len(headers) != 1 || headers["Authorization"] != "Bearer replay" {
			t.Fatalf("Dial mutated caller-owned headers: %#v", headers)
		}
	})

	t.Run("inbound replays in order with preserved types and payloads", func(t *testing.T) {
		outbound := []byte(`{"type":"input_audio_buffer.commit"}`)
		for run := range 2 {
			path := writeReplayConformanceCapture(t, []CapturedSessionEvent{
				websocketCapture(DirectionServerToClient, 1, `{"type":"session.created","session_id":"sess-1"}`),
				websocketCapture(DirectionServerToClient, 2, `{"type":"response.text_delta","delta":"hello"}`),
				websocketCapture(DirectionClientToServer, 3, string(outbound)),
				websocketCapture(DirectionServerToClient, 4, `{"type":"response.done"}`),
			})
			capture, err := LoadSessionCapture(path)
			if err != nil {
				t.Fatalf("run %d: LoadSessionCapture: %v", run, err)
			}
			var inbound [][]byte
			for _, evt := range capture.Records {
				if evt.Direction == DirectionServerToClient {
					inbound = append(inbound, []byte(evt.Payload))
				}
			}
			if len(inbound) != 3 {
				t.Fatalf("run %d: fixture scripted %d inbound messages, want 3", run, len(inbound))
			}
			dialer, err := NewReplayWebSocketDialer(path)
			if err != nil {
				t.Fatalf("run %d: NewReplayWebSocketDialer: %v", run, err)
			}
			conn, err := dialer.Dial("", nil)
			if err != nil {
				t.Fatalf("run %d: Dial: %v", run, err)
			}
			for i := range 2 {
				gotType, gotPayload, err := conn.ReadMessage()
				if err != nil {
					t.Fatalf("run %d: ReadMessage[%d]: %v", run, i, err)
				}
				if gotType != 1 {
					t.Fatalf("run %d: ReadMessage[%d] type = %d, want 1 (text)", run, i, gotType)
				}
				if !bytes.Equal(gotPayload, inbound[i]) {
					t.Fatalf("run %d: ReadMessage[%d] = %s, want capture bytes %s", run, i, gotPayload, inbound[i])
				}
			}
			if err := conn.WriteMessage(1, append([]byte(nil), outbound...)); err != nil {
				t.Fatalf("run %d: expected outbound rejected: %v", run, err)
			}
			gotType, gotPayload, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("run %d: ReadMessage after outbound: %v", run, err)
			}
			if gotType != 1 || !bytes.Equal(gotPayload, inbound[2]) {
				t.Fatalf("run %d: post-outbound read = (%d, %s), want (1, %s)", run, gotType, gotPayload, inbound[2])
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("run %d: Close: %v", run, err)
			}
			if err := dialer.Err(); err != nil {
				t.Fatalf("run %d: fully consumed replay diverged: %v", run, err)
			}
		}
	})

	t.Run("divergent outbound yields typed error through Err and errors.As", func(t *testing.T) {
		path := writeReplayConformanceCapture(t, []CapturedSessionEvent{
			websocketCapture(DirectionClientToServer, 1, `{"type":"session.update","session":{"model":"m"}}`),
		})
		dialer, err := NewReplayWebSocketDialer(path)
		if err != nil {
			t.Fatalf("NewReplayWebSocketDialer: %v", err)
		}
		conn, err := dialer.Dial("wss://ignored.invalid", nil)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}

		err = conn.WriteMessage(1, []byte(`{"type":"session.update","session":{"model":"wrong"}}`))
		if err == nil {
			t.Fatal("expected divergence error for unexpected outbound payload")
		}
		if !errors.Is(err, gateway.ErrReplayMismatch) {
			t.Fatalf("divergence error should match ErrReplayMismatch, got %v", err)
		}
		var mismatch *gateway.ReplayMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("divergence error should expose typed details via errors.As, got %v", err)
		}
		errViaErr := dialer.Err()
		if !errors.Is(errViaErr, gateway.ErrReplayMismatch) {
			t.Fatalf("Err() should preserve the divergence classification, got %v", errViaErr)
		}
		var mismatchViaErr *gateway.ReplayMismatchError
		if !errors.As(errViaErr, &mismatchViaErr) {
			t.Fatalf("Err() should stay reachable via errors.As, got %v", errViaErr)
		}

		_, _, readErr := conn.ReadMessage()
		if !errors.Is(readErr, gateway.ErrReplayMismatch) || !errors.Is(readErr, err) {
			t.Fatalf("reads after divergence should return the same typed error, got %v", readErr)
		}
		select {
		case <-dialer.Done():
		case <-time.After(sessionTestSafetyTimeout):
			t.Fatalf("Done did not close after divergence within %s", sessionTestSafetyTimeout)
		}
	})

	t.Run("close with pending expected outbound reports incompleteness once", func(t *testing.T) {
		path := writeReplayConformanceCapture(t, []CapturedSessionEvent{
			websocketCapture(DirectionServerToClient, 1, `{"type":"session.created","session_id":"sess-1"}`),
			websocketCapture(DirectionClientToServer, 2, `{"type":"conversation.item.create"}`),
		})
		dialer, err := NewReplayWebSocketDialer(path)
		if err != nil {
			t.Fatalf("NewReplayWebSocketDialer: %v", err)
		}
		conn, err := dialer.Dial("", nil)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}

		if err := conn.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		err = dialer.Err()
		if !errors.Is(err, gateway.ErrReplayIncomplete) {
			t.Fatalf("close with pending expected outbound should report incompleteness, got %v", err)
		}
		if errors.Is(err, gateway.ErrReplayMismatch) {
			t.Fatal("incompleteness should not match the mismatch class")
		}
		var incomplete *gateway.ReplayIncompleteError
		if !errors.As(err, &incomplete) {
			t.Fatalf("incompleteness should expose typed details via errors.As, got %v", err)
		}

		if err := conn.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		select {
		case <-dialer.Done():
		default:
			t.Fatal("Done should be closed after connection close")
		}
	})

	t.Run("Done closes exactly once across divergence and close", func(t *testing.T) {
		path := writeReplayConformanceCapture(t, []CapturedSessionEvent{
			websocketCapture(DirectionClientToServer, 1, `{"type":"session.update"}`),
		})
		dialer, err := NewReplayWebSocketDialer(path)
		if err != nil {
			t.Fatalf("NewReplayWebSocketDialer: %v", err)
		}
		conn, err := dialer.Dial("", nil)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		if err := conn.WriteMessage(1, []byte(`{"type":"unexpected"}`)); err == nil {
			t.Fatal("expected divergence error")
		}
		select {
		case <-dialer.Done():
		case <-time.After(sessionTestSafetyTimeout):
			t.Fatalf("Done did not close after divergence within %s", sessionTestSafetyTimeout)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("Close after divergence: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("repeat Close: %v", err)
		}
		if err := dialer.Err(); !errors.Is(err, gateway.ErrReplayMismatch) {
			t.Fatalf("close after divergence must not replace the recorded divergence error, got %v", err)
		}
	})
}

// dialerTransportOperationError is a typed per-operation failure used to prove
// error identity through the recording dialer under errors.Is/errors.As.
type dialerTransportOperationError struct {
	operation string
}

func (e *dialerTransportOperationError) Error() string {
	return "recording transport " + e.operation + " operation failed"
}

type dialerTransportObserver struct {
	dials  []transporttest.DialCall
	writes []transporttest.Message
	closes int
}

func (o *dialerTransportObserver) DialCalls() []transporttest.DialCall {
	return append([]transporttest.DialCall(nil), o.dials...)
}

func (o *dialerTransportObserver) WrittenMessages() []transporttest.Message {
	return append([]transporttest.Message(nil), o.writes...)
}

func (o *dialerTransportObserver) CloseCount() int { return o.closes }

type dialerTransportDialer struct {
	observer           *dialerTransportObserver
	inbound            []transporttest.Message
	dialErr, readErr   error
	writeErr, closeErr error
}

func newDialerTransportFixture(inbound []transporttest.Message, dialErr, readErr, writeErr, closeErr error) (transport.Dialer, transporttest.Observer) {
	observer := &dialerTransportObserver{}
	return &dialerTransportDialer{
		observer: observer,
		inbound:  cloneDialerTransportMessages(inbound),
		dialErr:  dialErr,
		readErr:  readErr,
		writeErr: writeErr,
		closeErr: closeErr,
	}, observer
}

func dialerTransportFailure(operation string, want error) transporttest.FailureCase {
	var dialErr, readErr, writeErr, closeErr error
	switch operation {
	case "dial":
		dialErr = want
	case "read":
		readErr = want
	case "write":
		writeErr = want
	case "close":
		closeErr = want
	default:
		panic("unknown recording transport failure operation: " + operation)
	}
	return transporttest.FailureCase{
		New: func() transport.Dialer {
			inner, _ := newDialerTransportFixture(nil, dialErr, readErr, writeErr, closeErr)
			return NewRecordingWebSocketDialer(inner, "record-s11", "record-model")
		},
		WantErr: want,
		MatchErr: func(err error) bool {
			var typed *dialerTransportOperationError
			return errors.As(err, &typed) && typed.operation == operation
		},
	}
}

func (d *dialerTransportDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d.dialErr != nil {
		return nil, d.dialErr
	}
	d.observer.dials = append(d.observer.dials, transporttest.DialCall{
		Endpoint: endpoint,
		Headers:  cloneDialerTransportHeaders(headers),
	})
	return &dialerTransportConn{
		observer: d.observer,
		inbound:  cloneDialerTransportMessages(d.inbound),
		readErr:  d.readErr,
		writeErr: d.writeErr,
		closeErr: d.closeErr,
	}, nil
}

type dialerTransportConn struct {
	observer           *dialerTransportObserver
	inbound            []transporttest.Message
	readErr            error
	writeErr, closeErr error
}

var _ transport.Conn = (*dialerTransportConn)(nil)

func (c *dialerTransportConn) ReadMessage() (int, []byte, error) {
	if c.readErr != nil {
		return 0, nil, c.readErr
	}
	if len(c.inbound) == 0 {
		return 0, nil, errDialerTransportExhausted
	}
	next := c.inbound[0]
	c.inbound = c.inbound[1:]
	return next.Type, next.Payload, nil
}

func (c *dialerTransportConn) WriteMessage(messageType int, payload []byte) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	c.observer.writes = append(c.observer.writes, transporttest.Message{
		Type:    messageType,
		Payload: append([]byte(nil), payload...),
	})
	return nil
}

func (c *dialerTransportConn) Close() error {
	if c.closeErr != nil {
		return c.closeErr
	}
	c.observer.closes++
	return nil
}

func cloneDialerTransportMessages(messages []transporttest.Message) []transporttest.Message {
	clones := make([]transporttest.Message, len(messages))
	for i, message := range messages {
		clones[i] = transporttest.Message{Type: message.Type, Payload: append([]byte(nil), message.Payload...)}
	}
	return clones
}

func cloneDialerTransportHeaders(headers map[string]string) map[string]string {
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		clone[key] = value
	}
	return clone
}

func writeReplayConformanceCapture(t *testing.T, records []CapturedSessionEvent) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conformance-session.json")
	writeWebSocketCapture(t, path, records)
	return path
}

var errDialerTransportExhausted = errors.New("dialer transport fixture exhausted")

type observedReplayClock struct {
	*clock.Deterministic
	created chan struct{}
}

func (c *observedReplayClock) NewTimer(delay time.Duration) clock.Timer {
	timer := c.Deterministic.NewTimer(delay)
	c.created <- struct{}{}
	return timer
}

func TestReplayCadenceUsesInjectedTimerDomain(t *testing.T) {
	source := &observedReplayClock{Deterministic: clock.NewDeterministic(time.Unix(123, 0), time.Millisecond), created: make(chan struct{}, 1)}
	first := websocketCapture(DirectionServerToClient, 1, `{"type":"session.created"}`)
	second := websocketCapture(DirectionServerToClient, 2, `{"type":"response.done"}`)
	first.TimestampMs, second.TimestampMs = 0, 25
	path := writeReplayConformanceCapture(t, []CapturedSessionEvent{first, second})
	dialer, err := NewReplayWebSocketDialer(path, WithRecordedSessionTiming(), WithReplayClock(source))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("unused", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, _, readErr := conn.ReadMessage(); result <- readErr }()
	select {
	case <-source.created:
	case <-time.After(time.Second):
		t.Fatal("replay did not register an injected timer")
	}
	source.AdvanceBy(24 * time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("replay released before captured offset: %v", err)
	default:
	}
	source.AdvanceBy(time.Millisecond)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("replay ignored injected clock advance")
	}
}
