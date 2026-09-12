package consumer

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type observer struct {
	mu     sync.Mutex
	events []sessionobservation.SessionRuntimeObservation
}

func (o *observer) ObserveSessionRuntime(event sessionobservation.SessionRuntimeObservation) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.mu.Unlock()
}

func (o *observer) snapshot() []sessionobservation.SessionRuntimeObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]sessionobservation.SessionRuntimeObservation(nil), o.events...)
}

func TestCommitPayloadIsCopiedBeforeCommit(t *testing.T) {
	o := &observer{}
	runtime := wire.NewService(o, platformclock.NewDeterministic(time.Unix(1700000000, 0), time.Millisecond))
	payload := []byte("first-utterance")
	runtime.ProviderAudioSent(payload)
	payload[0] = 'X'
	runtime.InputCommit()
	events := o.snapshot()
	if len(events) != 1 || events[0].Kind != sessionobservation.SessionRuntimeObservationInputCommit || string(events[0].Payload) != "first-utterance" || events[0].InputCommit != 1 {
		t.Fatalf("commit observation = %#v, want copied first payload and ordinal 1", events)
	}
}

func TestTerminalPublishesExactlyOnce(t *testing.T) {
	o := &observer{}
	runtime := wire.NewService(o, platformclock.Real{})
	runtime.TerminalWithAccounting(1, nil, nil)
	runtime.TerminalWithAccounting(2, errors.New("late"), nil)
	events := o.snapshot()
	if len(events) != 1 || events[0].Kind != sessionobservation.SessionRuntimeObservationTerminal || events[0].TurnsCompleted != 1 || !events[0].Clean {
		t.Fatalf("terminal observations = %#v, want one clean first terminal", events)
	}
}

func TestRejectedPlaybackIsNotClean(t *testing.T) {
	o := &observer{}
	runtime := wire.NewService(o, platformclock.Real{})
	runtime.AudioPlaybackReceipt(sessionobservation.PlaybackReceipt{CommandID: 41, Epoch: 8, AudioEndMS: 19, Applied: false, Err: errors.New("rejected")})
	events := o.snapshot()
	if len(events) != 1 || events[0].Kind != sessionobservation.SessionRuntimeObservationAudioPlaybackReceipt || events[0].Clean {
		t.Fatalf("playback observation = %#v, want rejected receipt to be non-clean", events)
	}
}

func TestTwoPublicInstancesDoNotShareOrdinalsOrBuffers(t *testing.T) {
	firstObserver, secondObserver := &observer{}, &observer{}
	first := wire.NewService(firstObserver, platformclock.Real{})
	second := wire.NewService(secondObserver, platformclock.Real{})
	first.ProviderAudioSent([]byte("first"))
	second.ProviderAudioSent([]byte("second"))
	first.InputCommit()
	second.InputCommit()
	first.TerminalWithAccounting(1, nil, nil)
	second.TerminalWithAccounting(2, nil, nil)
	firstEvents, secondEvents := firstObserver.snapshot(), secondObserver.snapshot()
	if len(firstEvents) != 2 || len(secondEvents) != 2 || firstEvents[0].InputCommit != 1 || secondEvents[0].InputCommit != 1 || string(firstEvents[0].Payload) != "first" || string(secondEvents[0].Payload) != "second" {
		t.Fatalf("isolated instance observations = %#v / %#v", firstEvents, secondEvents)
	}
}
