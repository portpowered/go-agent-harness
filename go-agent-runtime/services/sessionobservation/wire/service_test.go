package wire

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type observer struct {
	events []sessionobservation.SessionRuntimeObservation
}

func (o *observer) ObserveSessionRuntime(event sessionobservation.SessionRuntimeObservation) {
	o.events = append(o.events, event)
}

func TestNewServiceCreatesIndependentInstances(t *testing.T) {
	firstObserver, secondObserver := &observer{}, &observer{}
	firstClock := platformclock.NewDeterministic(time.Unix(1700000000, 0), time.Second)
	secondClock := platformclock.NewDeterministic(time.Unix(1700000000, 0), time.Second)
	first := NewService(firstObserver, firstClock)
	second := NewService(secondObserver, secondClock)

	first.ProviderAudioSent([]byte("first"))
	second.ProviderAudioSent([]byte("second"))
	first.InputCommit()
	second.InputCommit()
	first.TerminalWithAccounting(1, nil, nil)
	second.TerminalWithAccounting(2, nil, nil)

	if len(firstObserver.events) != 2 || len(secondObserver.events) != 2 {
		t.Fatalf("instance event counts = %d/%d, want 2/2", len(firstObserver.events), len(secondObserver.events))
	}
	if string(firstObserver.events[0].Payload) != "first" || firstObserver.events[0].InputCommit != 1 || firstObserver.events[1].TurnsCompleted != 1 {
		t.Fatalf("first instance leaked or lost state: %#v", firstObserver.events)
	}
	if string(secondObserver.events[0].Payload) != "second" || secondObserver.events[0].InputCommit != 1 || secondObserver.events[1].TurnsCompleted != 2 {
		t.Fatalf("second instance leaked or lost state: %#v", secondObserver.events)
	}
}
