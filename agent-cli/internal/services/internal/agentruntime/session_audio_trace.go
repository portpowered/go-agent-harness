package agentruntime

import (
	"bytes"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	"strings"
)

// TraceRuntimeObserver translates service observations into the shared recorder.
type TraceRuntimeObserver struct {
	Trace      *recording.Trace
	Redactions []string
}

func (o TraceRuntimeObserver) ObserveSessionRuntime(event SessionRuntimeObservation) {
	if len(o.Redactions) > 0 {
		if runtimePayloadNeedsRedaction(event.Kind) {
			event.Payload = append([]byte(nil), event.Payload...)
		}
		for _, secret := range o.Redactions {
			if secret == "" {
				continue
			}
			event.Error = strings.ReplaceAll(event.Error, secret, "[REDACTED]")
			if runtimePayloadNeedsRedaction(event.Kind) {
				event.Payload = bytes.ReplaceAll(event.Payload, []byte(secret), []byte("[REDACTED]"))
			}
		}
	}

	o.Trace.ObserveRuntime(recording.RuntimeEvent{Kind: string(event.Kind), Tick: event.Tick, InputCommit: event.InputCommit, ResponseID: event.ResponseID, ResponsePurpose: string(event.ResponsePurpose), StreamID: event.StreamID, LoopPassID: event.LoopPassID, Epoch: event.Epoch, TurnsCompleted: event.TurnsCompleted, Clean: event.Clean, Error: event.Error, Payload: event.Payload})
}

// Wire observations contain provider JSON and can include credentials or
// bearer material. Treat them like tool payloads for redaction. Audio payloads
// intentionally remain byte-identical so trace replay retains PCM evidence.
func runtimePayloadNeedsRedaction(kind SessionRuntimeObservationKind) bool {
	switch kind {
	case "tool_call", "tool_result", "provider_wire_send", "provider_wire_receive":
		return true
	default:
		return false
	}
}

type sessionRuntimeObserverFanout []SessionRuntimeObserver

func (f sessionRuntimeObserverFanout) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	for _, observer := range f {
		if observer != nil {
			observer.ObserveSessionRuntime(observation)
		}
	}
}

// CombineSessionRuntimeObservers preserves all non-nil observers in order.
func CombineSessionRuntimeObservers(observers ...SessionRuntimeObserver) SessionRuntimeObserver {
	filtered := make(sessionRuntimeObserverFanout, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			filtered = append(filtered, observer)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// Provider commits release the accumulated utterance and record server VAD
// boundaries even when no explicit client commit is sent.
func (TraceRuntimeObserver) ObserveProviderBoundaries() bool { return true }
func (f sessionRuntimeObserverFanout) ObserveProviderBoundaries() bool {
	for _, observer := range f {
		if observer, ok := observer.(interface{ ObserveProviderBoundaries() bool }); ok && observer.ObserveProviderBoundaries() {
			return true
		}
	}
	return false
}

// The audio trace already persists input chunks. Re-copying whole utterances
// at VAD commit would grow memory during silence or very long utterances.
func (TraceRuntimeObserver) RetainCommitPayload() bool { return false }
func (f sessionRuntimeObserverFanout) RetainCommitPayload() bool {
	for _, observer := range f {
		if observer == nil {
			continue
		}
		preference, ok := observer.(interface{ RetainCommitPayload() bool })
		if !ok || preference.RetainCommitPayload() {
			return true
		}
	}
	return false
}
