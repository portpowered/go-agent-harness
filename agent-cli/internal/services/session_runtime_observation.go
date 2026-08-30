package services

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

// SessionRuntimeObservationKind identifies an observable boundary in one
// agent session command. The observations are emitted from the runtime after
// the command has accepted the corresponding event, not from a test
// coordinator or a provider transcript parser.
type SessionRuntimeObservationKind string

const (
	SessionRuntimeObservationAudioOutput    SessionRuntimeObservationKind = "audio_output"
	SessionRuntimeObservationAudioInput     SessionRuntimeObservationKind = "audio_input"
	SessionRuntimeObservationInputCommit    SessionRuntimeObservationKind = "input_commit"
	SessionRuntimeObservationResponseCreate SessionRuntimeObservationKind = "response_create"
	SessionRuntimeObservationTurnCompleted  SessionRuntimeObservationKind = "turn_completed"
	SessionRuntimeObservationTerminal       SessionRuntimeObservationKind = "terminal"
)

// SessionTokenUsageSemantics identifies how provider usage values are consumed
// by the session accounting seam.
type SessionTokenUsageSemantics string

const (
	// SessionTokenUsageIncremental means every non-negative MESSAGE.END usage
	// value is the contribution for that completed turn and is added exactly
	// once to the session total. Providers that expose cumulative readings must
	// normalize them before they reach the session stream.
	SessionTokenUsageIncremental SessionTokenUsageSemantics = "incremental"
)

// SessionFinalAccounting is the production-owned terminal accounting result
// for one session. Token fields are session-cumulative totals, not the usage
// from only the last turn. Metrics is a complete deep-copied snapshot with all
// supported direction/modality series, including untouched zero series.
type SessionFinalAccounting struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
	ReasoningTokens  uint64
	UsageSemantics   SessionTokenUsageSemantics
	Metrics          metrics.Snapshot
}

// SessionRuntimeFinalAccounting is a descriptive alias for callers that want
// the runtime boundary named explicitly.
type SessionRuntimeFinalAccounting = SessionFinalAccounting

// SessionRuntimeObservation is one clock-stamped observation from a session
// command. Payload is present only for audio observations and is copied before
// delivery so an observer owns its bytes. Terminal observations contain the
// command's actual clean/error result, completed-turn count, and the
// production-owned final accounting value.
type SessionRuntimeObservation struct {
	Kind           SessionRuntimeObservationKind
	Tick           uint64
	Timestamp      time.Time
	Payload        []byte
	TurnsCompleted int
	// InputCommit is the one-based ordinal of a client-to-server MESSAGE.END
	// accepted by the session. It is populated only for InputCommit observations.
	InputCommit int
	// ResponseID and ResponsePurpose are populated on ResponseCreate
	// observations when the provider/session seam has those identities.
	ResponseID      string
	ResponsePurpose messages.ResponsePurpose
	Clean           bool
	Error           string
	// FinalAccounting is populated only on the terminal observation. It is
	// copied before delivery and can be retained by the observer safely.
	FinalAccounting *SessionFinalAccounting
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
	inputMu      sync.Mutex
	inputPayload []byte
	inputCommits int
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
	r.observeWithMetadata(kind, payload, turns, 0, clean, runErr, "", "")
}

func (r *sessionRuntimeObservationRecorder) observeWithInputCommit(kind SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error) {
	r.observeFinalWithMetadata(kind, payload, turns, inputCommit, clean, runErr, nil, "", "")
}

func (r *sessionRuntimeObservationRecorder) observeFinal(kind SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, finalAccounting *SessionFinalAccounting) {
	r.observeFinalWithMetadata(kind, payload, turns, inputCommit, clean, runErr, finalAccounting, "", "")
}

func (r *sessionRuntimeObservationRecorder) observeWithMetadata(kind SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, responseID string, responsePurpose messages.ResponsePurpose) {
	r.observeFinalWithMetadata(kind, payload, turns, inputCommit, clean, runErr, nil, responseID, responsePurpose)
}

func (r *sessionRuntimeObservationRecorder) observeFinalWithMetadata(kind SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, finalAccounting *SessionFinalAccounting, responseID string, responsePurpose messages.ResponsePurpose) {
	if r == nil || r.observer == nil {
		return
	}
	tick, timestamp := r.snapshot()
	r.observer.ObserveSessionRuntime(SessionRuntimeObservation{
		Kind:            kind,
		Tick:            tick,
		Timestamp:       timestamp,
		Payload:         append([]byte(nil), payload...),
		TurnsCompleted:  turns,
		InputCommit:     inputCommit,
		ResponseID:      responseID,
		ResponsePurpose: responsePurpose,
		Clean:           clean,
		Error:           sessionRuntimeObservationError(runErr),
		FinalAccounting: cloneSessionFinalAccounting(finalAccounting),
	})
}

func (r *sessionRuntimeObservationRecorder) audioOutput(payload []byte) {
	r.observe(SessionRuntimeObservationAudioOutput, payload, 0, false, nil)
}

func (r *sessionRuntimeObservationRecorder) audioInput(payload []byte) {
	if r == nil {
		return
	}
	r.inputMu.Lock()
	r.inputPayload = append(r.inputPayload, payload...)
	r.inputMu.Unlock()
	r.observe(SessionRuntimeObservationAudioInput, payload, 0, false, nil)
}

// inputCommit records the exact raw PCM accumulated since the previous
// client-to-server MESSAGE.END. It is called by the observed session wrapper
// only after the underlying Session.Send succeeds, so replay validation has
// accepted the actual outbound commit before this evidence is emitted.
func (r *sessionRuntimeObservationRecorder) inputCommit() {
	if r == nil {
		return
	}
	r.inputMu.Lock()
	r.inputCommits++
	commit := r.inputCommits
	payload := append([]byte(nil), r.inputPayload...)
	r.inputPayload = nil
	r.inputMu.Unlock()
	r.observeWithInputCommit(SessionRuntimeObservationInputCommit, payload, 0, commit, true, nil)
}

// responseCreate records the response request accepted by the session. A
// MESSAGE.END is translated by provider sessions into commit plus response
// creation; the observed session calls this immediately after inputCommit so
// both boundaries remain explicit even when they share one deterministic tick.
func (r *sessionRuntimeObservationRecorder) responseCreate(msg messages.StreamMessage) {
	if r == nil {
		return
	}
	r.observeWithMetadata(SessionRuntimeObservationResponseCreate, nil, 0, 0, true, nil, msg.ResponseID, msg.ResponsePurpose)
}

func (r *sessionRuntimeObservationRecorder) turnCompleted(turns int) {
	r.observe(SessionRuntimeObservationTurnCompleted, nil, turns, true, nil)
}

func (r *sessionRuntimeObservationRecorder) terminalWithAccounting(turns int, runErr error, finalAccounting *SessionFinalAccounting) {
	if r == nil || r.observer == nil {
		return
	}
	r.terminalOnce.Do(func() {
		r.observeFinal(SessionRuntimeObservationTerminal, nil, turns, 0, runErr == nil, runErr, finalAccounting)
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

func cloneSessionFinalAccounting(accounting *SessionFinalAccounting) *SessionFinalAccounting {
	if accounting == nil {
		return nil
	}
	clone := *accounting
	clone.Metrics.HistogramBounds = append([]int64(nil), accounting.Metrics.HistogramBounds...)
	clone.Metrics.Series = make([]metrics.SeriesSnapshot, len(accounting.Metrics.Series))
	for index, series := range accounting.Metrics.Series {
		clone.Metrics.Series[index] = series
		clone.Metrics.Series[index].Histogram.Bounds = append([]int64(nil), series.Histogram.Bounds...)
		clone.Metrics.Series[index].Histogram.BucketCounts = append([]uint64(nil), series.Histogram.BucketCounts...)
	}
	return &clone
}
