package openai

import (
	"errors"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/transporttest"
)

func TestOpenAIRealtimeSharedTransportS11Conformance(t *testing.T) {
	dialErr := &openAITransportOperationError{operation: "dial"}
	readErr := &openAITransportOperationError{operation: "read"}
	writeErr := &openAITransportOperationError{operation: "write"}
	closeErr := &openAITransportOperationError{operation: "close"}
	h := transporttest.ConformanceHarness{
		Endpoint: "wss://openai-s11.invalid/v1/realtime",
		Headers:  map[string]string{"Authorization": "Bearer s11", "X-Trace": "openai-s11"},
		Inbound: []transporttest.Message{
			{Type: 1, Payload: []byte{0, 1, 2}},
			{Type: 2, Payload: []byte("openai-inbound-second")},
		},
		Outbound: []transporttest.Message{
			{Type: 3, Payload: []byte{9, 0, 8}},
			{Type: 4, Payload: []byte("openai-outbound-second")},
		},
	}
	h.NewValid = func() (transport.Dialer, transporttest.Observer) {
		return configuredOpenAITransportDialer(newOpenAITransportFixture(h.Inbound, nil, nil, nil, nil))
	}
	h.DialFailure = openAITransportFailure("dial", dialErr)
	h.ReadFailure = openAITransportFailure("read", readErr)
	h.WriteFailure = openAITransportFailure("write", writeErr)
	h.CloseFailure = openAITransportFailure("close", closeErr)
	transporttest.RunS11(t, h)
}

type openAITransportOperationError struct {
	operation string
}

func (e *openAITransportOperationError) Error() string {
	return "openai " + e.operation + " operation failed"
}

type openAITransportObserver struct {
	dials  []transporttest.DialCall
	writes []transporttest.Message
	closes int
}

func (o *openAITransportObserver) DialCalls() []transporttest.DialCall {
	return append([]transporttest.DialCall(nil), o.dials...)
}

func (o *openAITransportObserver) WrittenMessages() []transporttest.Message {
	return append([]transporttest.Message(nil), o.writes...)
}

func (o *openAITransportObserver) CloseCount() int { return o.closes }

type openAITransportDialer struct {
	observer           *openAITransportObserver
	inbound            []transporttest.Message
	dialErr, readErr   error
	writeErr, closeErr error
}

func newOpenAITransportFixture(inbound []transporttest.Message, dialErr, readErr, writeErr, closeErr error) (transport.Dialer, transporttest.Observer) {
	observer := &openAITransportObserver{}
	return &openAITransportDialer{
		observer: observer,
		inbound:  cloneOpenAITransportMessages(inbound),
		dialErr:  dialErr,
		readErr:  readErr,
		writeErr: writeErr,
		closeErr: closeErr,
	}, observer
}

func configuredOpenAITransportDialer(dialer transport.Dialer, observer transporttest.Observer) (transport.Dialer, transporttest.Observer) {
	provider := New(WithWebSocketDialer(dialer))
	if provider.realtimeDialer != dialer {
		panic("OpenAI WithWebSocketDialer did not retain the shared dialer")
	}
	return provider.realtimeDialer, observer
}

func openAITransportFailure(operation string, want error) transporttest.FailureCase {
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
		panic("unknown OpenAI transport failure operation: " + operation)
	}
	return transporttest.FailureCase{
		New: func() transport.Dialer {
			dialer, observer := newOpenAITransportFixture(nil, dialErr, readErr, writeErr, closeErr)
			configured, _ := configuredOpenAITransportDialer(dialer, observer)
			return configured
		},
		WantErr: want,
		MatchErr: func(err error) bool {
			var typed *openAITransportOperationError
			return errors.As(err, &typed) && typed.operation == operation
		},
	}
}

func (d *openAITransportDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d.dialErr != nil {
		return nil, fmt.Errorf("openai transport dial: %w", d.dialErr)
	}
	d.observer.dials = append(d.observer.dials, transporttest.DialCall{
		Endpoint: endpoint,
		Headers:  cloneOpenAITransportHeaders(headers),
	})
	return &openAITransportConn{
		observer: d.observer,
		inbound:  cloneOpenAITransportMessages(d.inbound),
		readErr:  d.readErr,
		writeErr: d.writeErr,
		closeErr: d.closeErr,
	}, nil
}

type openAITransportConn struct {
	observer          *openAITransportObserver
	inbound           []transporttest.Message
	readErr, writeErr error
	closeErr          error
}

func (c *openAITransportConn) ReadMessage() (int, []byte, error) {
	if c.readErr != nil {
		return 0, nil, fmt.Errorf("openai transport read: %w", c.readErr)
	}
	message := c.inbound[0]
	c.inbound = c.inbound[1:]
	return message.Type, append([]byte(nil), message.Payload...), nil
}

func (c *openAITransportConn) WriteMessage(messageType int, payload []byte) error {
	if c.writeErr != nil {
		return fmt.Errorf("openai transport write: %w", c.writeErr)
	}
	c.observer.writes = append(c.observer.writes, transporttest.Message{
		Type:    messageType,
		Payload: append([]byte(nil), payload...),
	})
	return nil
}

func (c *openAITransportConn) Close() error {
	c.observer.closes++
	if c.closeErr != nil {
		return fmt.Errorf("openai transport close: %w", c.closeErr)
	}
	return nil
}

func cloneOpenAITransportMessages(messages []transporttest.Message) []transporttest.Message {
	cloned := make([]transporttest.Message, len(messages))
	for i, message := range messages {
		cloned[i] = transporttest.Message{Type: message.Type, Payload: append([]byte(nil), message.Payload...)}
	}
	return cloned
}

func cloneOpenAITransportHeaders(headers map[string]string) map[string]string {
	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}
	return cloned
}
