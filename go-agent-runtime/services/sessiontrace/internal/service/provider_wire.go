package service

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func NewProviderWireDialer(inner transport.Dialer, observer sessiontrace.RuntimeObserver, source clock.Source) sessiontrace.ProviderDialer {
	if inner == nil || observer == nil {
		return inner
	}
	runtime := NewRuntimeRecorder(observer, source)
	return transport.ObservingDialer{Inner: inner, Observe: func(event transport.MessageObservation) {
		var textPayload json.RawMessage
		var binaryPayload []byte
		if json.Valid(event.Payload) {
			textPayload = event.Payload
		} else {
			binaryPayload = event.Payload
		}
		payload, err := json.Marshal(struct {
			MessageType   int             `json:"message_type"`
			Payload       json.RawMessage `json:"payload,omitempty"`
			BinaryPayload []byte          `json:"binary_payload,omitempty"`
		}{event.MessageType, textPayload, binaryPayload})
		kind := sessiontrace.SessionRuntimeObservationKind("provider_wire_" + event.Direction)
		if err != nil {
			runtime.Observe(kind, nil, 0, false, err)
			return
		}
		runtime.Observe(kind, payload, 0, event.Err == nil, event.Err)
	}}
}
