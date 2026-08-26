package services

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

// SessionRuntimeObservationKind identifies an observable boundary in one
// agent session command. The observations are emitted from the runtime after
// the command has accepted the corresponding event, not from a test
// coordinator or a provider transcript parser.
type SessionRuntimeObservationKind string

const (
	SessionRuntimeObservationAudioOutput   SessionRuntimeObservationKind = "audio_output"
	SessionRuntimeObservationAudioInput    SessionRuntimeObservationKind = "audio_input"
	SessionRuntimeObservationTurnCompleted SessionRuntimeObservationKind = "turn_completed"
	SessionRuntimeObservationTerminal      SessionRuntimeObservationKind = "terminal"
)

// SessionRuntimeObservation is one clock-stamped observation from a session
// command. Payload is present only for audio observations and is copied before
// delivery so an observer owns its bytes. Terminal observations contain the
// command's actual clean/error result and completed-turn count.
type SessionRuntimeObservation struct {
	Kind           SessionRuntimeObservationKind
	Tick           uint64
	Timestamp      time.Time
	Payload        []byte
	TurnsCompleted int
	Clean          bool
	Error          string
}

// SessionRuntimeObserver receives runtime observations. It is optional and
// observational: a nil observer preserves the normal session behavior.
type SessionRuntimeObserver interface {
	ObserveSessionRuntime(SessionRuntimeObservation)
}

// sessionRuntimeObservationRecorder is the runtime-owned adapter between the
// session lifecycle and an optional observer. Its clock is supplied by CLI
// composition, so deterministic callers observe the same source that was
// injected into the generated command graph.
type sessionRuntimeObservationRecorder struct {
	observer SessionRuntimeObserver
	clock    platformclock.Source
	sequence atomic.Uint64

	terminalOnce sync.Once
}

func newSessionRuntimeObservationRecorder(observer SessionRuntimeObserver, source platformclock.Source) *sessionRuntimeObservationRecorder {
	if observer == nil {
		return nil
	}
	return &sessionRuntimeObservationRecorder{
		observer: observer,
		clock:    platformclock.Ensure(source),
	}
}

func (r *sessionRuntimeObservationRecorder) observe(kind SessionRuntimeObservationKind, payload []byte, turns int, clean bool, runErr error) {
	if r == nil || r.observer == nil {
		return
	}
	tick, timestamp := r.snapshot()
	r.observer.ObserveSessionRuntime(SessionRuntimeObservation{
		Kind:           kind,
		Tick:           tick,
		Timestamp:      timestamp,
		Payload:        append([]byte(nil), payload...),
		TurnsCompleted: turns,
		Clean:          clean,
		Error:          sessionRuntimeObservationError(runErr),
	})
}

func (r *sessionRuntimeObservationRecorder) audioOutput(payload []byte) {
	r.observe(SessionRuntimeObservationAudioOutput, payload, 0, false, nil)
}

func (r *sessionRuntimeObservationRecorder) audioInput(payload []byte) {
	r.observe(SessionRuntimeObservationAudioInput, payload, 0, false, nil)
}

func (r *sessionRuntimeObservationRecorder) turnCompleted(turns int) {
	r.observe(SessionRuntimeObservationTurnCompleted, nil, turns, true, nil)
}

func (r *sessionRuntimeObservationRecorder) terminal(turns int, runErr error) {
	if r == nil || r.observer == nil {
		return
	}
	r.terminalOnce.Do(func() {
		r.observe(SessionRuntimeObservationTerminal, nil, turns, runErr == nil, runErr)
	})
}

func (r *sessionRuntimeObservationRecorder) snapshot() (uint64, time.Time) {
	tick := r.sequence.Add(1)
	if source, ok := r.clock.(interface{ Tick() uint64 }); ok {
		tick = source.Tick()
	}
	return tick, r.clock.Now()
}

func sessionRuntimeObservationError(err error) string {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	return err.Error()
}
