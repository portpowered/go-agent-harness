package agentruntime

import (
	"bytes"
	"strings"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

const (
	traceProviderWireSend    = "provider_wire_send"
	traceProviderWireReceive = "provider_wire_receive"
)

type TraceRuntimeObserver struct {
	Trace      *recording.Trace
	Redactions []string
}

func (o TraceRuntimeObserver) ObserveSessionRuntime(event SessionRuntimeObservation) {
	redact := event.Kind == sessionToolEventTypeCall || event.Kind == sessionToolEventTypeResult || event.Kind == traceProviderWireSend || event.Kind == traceProviderWireReceive
	if len(o.Redactions) > 0 {
		if redact {
			event.Payload = append([]byte(nil), event.Payload...)
		}
		for _, secret := range o.Redactions {
			if secret == "" {
				continue
			}
			event.Error = strings.ReplaceAll(event.Error, secret, "[REDACTED]")
			if redact {
				event.Payload = bytes.ReplaceAll(event.Payload, []byte(secret), []byte("[REDACTED]"))
			}
		}
	}
	o.Trace.ObserveRuntime(recording.RuntimeEvent{Kind: string(event.Kind), Tick: event.Tick, InputCommit: event.InputCommit, ResponseID: event.ResponseID, ResponsePurpose: string(event.ResponsePurpose), StreamID: event.StreamID, LoopPassID: event.LoopPassID, Epoch: event.Epoch, TurnsCompleted: event.TurnsCompleted, Clean: event.Clean, Error: event.Error, Payload: event.Payload})
}

type sessionRuntimeObserverFanout []SessionRuntimeObserver

func (f sessionRuntimeObserverFanout) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	for _, observer := range f {
		if observer != nil {
			observer.ObserveSessionRuntime(observation)
		}
	}
}
func CombineSessionRuntimeObservers(observers ...SessionRuntimeObserver) SessionRuntimeObserver {
	for _, observer := range observers {
		if observer != nil {
			return sessionRuntimeObserverFanout(observers)
		}
	}
	return nil
}
func (TraceRuntimeObserver) ObserveProviderBoundaries() bool { return true }
func (f sessionRuntimeObserverFanout) ObserveProviderBoundaries() bool {
	for _, observer := range f {
		if observer, ok := observer.(interface{ ObserveProviderBoundaries() bool }); ok && observer.ObserveProviderBoundaries() {
			return true
		}
	}
	return false
}
func (TraceRuntimeObserver) RetainCommitPayload() bool { return false }
func (f sessionRuntimeObserverFanout) RetainCommitPayload() bool {
	for _, observer := range f {
		if observer != nil {
			if preference, ok := observer.(interface{ RetainCommitPayload() bool }); !ok || preference.RetainCommitPayload() {
				return true
			}
		}
	}
	return false
}
