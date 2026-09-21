package agentruntime

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// observeSessionWire records completed WebSocket operations separately from
// upstream buffer admission. Provider receipts remain distinct remote events.
func observeSessionWire(inner transport.Dialer, opts SessionRunOptions) transport.Dialer {
	if inner == nil || opts.RuntimeObserver == nil {
		return inner
	}
	runtime := newSessionRuntimeObservationRecorder(opts.RuntimeObserver, opts.Clock)
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
		if err != nil {
			runtime.observe("provider_wire_"+SessionRuntimeObservationKind(event.Direction), nil, 0, false, err)
			return
		}
		runtime.observe("provider_wire_"+SessionRuntimeObservationKind(event.Direction), payload, 0, event.Err == nil, event.Err)
	}}
}

func rtcMediaFromSession(session messages.Session) (audio.MediaEndpoints, bool) {
	if owner, ok := session.(audio.MediaSession); ok {
		return owner.RTCMedia(), true
	}
	if owner, ok := session.(interface {
		RTCMedia() (audio.MediaEndpoints, bool)
	}); ok {
		return owner.RTCMedia()
	}
	return audio.MediaEndpoints{}, false
}
