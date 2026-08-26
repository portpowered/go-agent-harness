package providers

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwlogging "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// Buffer names used in default session drop records.
const (
	DropBufferSendQueue    = "send_queue"
	DropBufferReceiveQueue = "receive_queue"
)

// gatewayDropSink adapts the gateway-owned logging seam to the canonical
// messages-package drop observer so both surfaces emit identical structured
// records without importing the loop's logging package.
type gatewayDropSink struct {
	inner gwlogging.Logger
}

func (s gatewayDropSink) Warn(msg string, fields ...messages.DropLogField) {
	if len(fields) == 0 {
		s.inner.Warn(msg)
		return
	}
	converted := make([]gwlogging.Field, len(fields))
	for i, field := range fields {
		converted[i] = gwlogging.Field{Key: field.Key, Value: field.Value}
	}
	s.inner.Warn(msg, converted...)
}

// AttachSessionDropLoggers wires the default structured drop observers onto a
// realtime session's input (client-to-provider send queue) and output
// (provider-to-client receive buffer) paths. Each buffer-full drop emits
// exactly one Warn record carrying direction, buffer name, cumulative count,
// and message kind; zero drops emit nothing.
func AttachSessionDropLoggers(logger gwlogging.Logger, sendQueue *messages.TypedBuffer[models.SessionEvent], recvBuf *messages.TypedBuffer[messages.StreamMessage]) {
	if logger == nil {
		return
	}
	sink := gatewayDropSink{inner: logger}
	messages.AttachDefaultDropObserver(sink, messages.DropDirectionInput, DropBufferSendQueue, sendQueue,
		func(event models.SessionEvent) string { return string(event.Type) })
	messages.AttachDefaultDropObserver(sink, messages.DropDirectionOutput, DropBufferReceiveQueue, recvBuf,
		func(msg messages.StreamMessage) string { return string(msg.Type) })
}
