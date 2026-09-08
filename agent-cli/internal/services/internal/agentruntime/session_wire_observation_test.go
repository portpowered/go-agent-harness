package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type wireTraceObserver struct{ events []SessionRuntimeObservation }

func (o *wireTraceObserver) ObserveSessionRuntime(event SessionRuntimeObservation) {
	o.events = append(o.events, event)
}

type wireTraceConn struct {
	transport.Conn
	failure error
}

func (c wireTraceConn) WriteMessage(int, []byte) error { return c.failure }
func (c wireTraceConn) ReadMessage() (int, []byte, error) {
	return 1, []byte(`{"type":"response.done"}`), nil
}

type wireTraceDialer struct{ conn transport.Conn }

func (d wireTraceDialer) Dial(string, map[string]string) (transport.Conn, error) { return d.conn, nil }

func TestSessionWireObservationSeparatesCompletedTransportOperations(t *testing.T) {
	source := clock.NewDeterministic(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), time.Millisecond)
	observer := &wireTraceObserver{}
	failure := io.ErrClosedPipe
	dialer := observeSessionWire(wireTraceDialer{wireTraceConn{failure: failure}}, SessionRunOptions{ModelCatalog: testModelCatalog(), Clock: source, RuntimeObserver: observer})
	conn, err := dialer.Dial("unused", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"type":"response.create"}`)
	if err := conn.WriteMessage(1, request); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if len(observer.events) != 2 {
		t.Fatalf("observations=%d", len(observer.events))
	}
	sent, received := observer.events[0], observer.events[1]
	if sent.Kind != "provider_wire_send" || sent.Clean || sent.Error == "" || received.Kind != "provider_wire_receive" || !received.Clean {
		t.Fatalf("observations=%+v", observer.events)
	}
	var envelope struct {
		MessageType int             `json:"message_type"`
		Payload     json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(sent.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.MessageType != 1 || !bytes.Equal(envelope.Payload, request) {
		t.Fatalf("wire payload=%s", sent.Payload)
	}
}
