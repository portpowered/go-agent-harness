package live

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type runtimeTrace struct {
	observer      sessiontrace.RuntimeObserver
	clock         session.LiveClock
	tick          func() uint64
	sequence      atomic.Uint64
	inputMu       sync.Mutex
	input         []byte
	inputOverflow bool
	commits       int
	turns         atomic.Int64
}

func newRuntimeObservations(observer sessiontrace.RuntimeObserver, clock session.LiveClock, tick func() uint64) *runtimeTrace {
	if observer == nil {
		return nil
	}
	return &runtimeTrace{observer: observer, clock: clock, tick: tick}
}

func (r *runtimeTrace) observe(kind sessiontrace.SessionRuntimeObservationKind, payload []byte, turns, commit int, response messages.StreamMessage, clean bool, runErr error) {
	if r == nil || r.observer == nil {
		return
	}
	tick := r.sequence.Add(1)
	if r.tick != nil {
		tick = r.tick()
	}
	var timestamp = r.now()
	r.observer.ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{
		Kind: kind, Tick: tick, Timestamp: timestamp, Payload: append([]byte(nil), payload...),
		TurnsCompleted: turns, InputCommit: commit, ResponseID: response.ResponseID,
		ResponsePurpose: response.ResponsePurpose, StreamID: response.ActorStreamID,
		LoopPassID: response.LoopPassID, Clean: clean, Error: runtimeTraceError(runErr),
	})
}

func (r *runtimeTrace) now() time.Time {
	if r.clock != nil {
		return r.clock()
	}
	return time.Time{}
}

func (r *runtimeTrace) audioOutput(msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if ok && value != nil {
		r.observe(sessiontrace.SessionRuntimeObservationAudioOutput, value.Content, 0, 0, msg, false, nil)
	}
}

func (r *runtimeTrace) capturedAudio(frame sharedaudio.PCMFrame) {
	if r == nil {
		return
	}
	payload := codec.EncodePCM16(frame.Samples)
	r.inputMu.Lock()
	if !r.inputOverflow && len(payload) <= codec.MaxPayloadBytes-len(r.input) {
		r.input = append(r.input, payload...)
	} else {
		r.inputOverflow = true
	}
	r.inputMu.Unlock()
	r.observe(sessiontrace.SessionRuntimeObservationAudioInput, payload, 0, 0, messages.StreamMessage{ActorStreamID: frame.StreamID}, false, nil)
}

func (r *runtimeTrace) inputCommit(providerCreated bool) {
	if r == nil {
		return
	}
	r.inputMu.Lock()
	payload := append([]byte(nil), r.input...)
	r.input = nil
	if r.inputOverflow {
		payload = nil
		r.inputOverflow = false
	}
	commit := 0
	if !providerCreated {
		r.commits++
		commit = r.commits
	}
	r.inputMu.Unlock()
	r.observe(sessiontrace.SessionRuntimeObservationInputCommit, payload, 0, commit, messages.StreamMessage{}, true, nil)
}

func (r *runtimeTrace) responseCreate(msg messages.StreamMessage) {
	r.observe(sessiontrace.SessionRuntimeObservationResponseCreate, nil, 0, 0, msg, true, nil)
}

func (r *runtimeTrace) turnCompleted(msg messages.StreamMessage, interrupted bool) {
	if r == nil || msg.Type != messages.StreamTypeMessageEnd || msg.Role == messages.RoleTool || interrupted {
		return
	}
	turns := int(r.turns.Add(1))
	r.observe(sessiontrace.SessionRuntimeObservationTurnCompleted, nil, turns, 0, msg, true, nil)
}

func (r *runtimeTrace) terminal(turns int, err error) {
	if r == nil {
		return
	}
	if turns == 0 {
		turns = int(r.turns.Load())
	}
	r.observe(sessiontrace.SessionRuntimeObservationTerminal, nil, turns, 0, messages.StreamMessage{}, err == nil, err)
}

func runtimeTraceError(err error) string {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	return err.Error()
}
