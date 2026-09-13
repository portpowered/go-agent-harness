package agentruntime

import runtimecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type SessionRuntimeObservationKind = runtimecontract.SessionRuntimeObservationKind
type SessionTokenUsageSemantics = runtimecontract.SessionTokenUsageSemantics
type SessionFinalAccounting = runtimecontract.SessionFinalAccounting
type SessionRuntimeFinalAccounting = runtimecontract.SessionRuntimeFinalAccounting
type SessionRuntimeObservation = runtimecontract.SessionRuntimeObservation
type SessionRuntimeObserver = runtimecontract.SessionRuntimeObserver

const SessionRuntimeObservationAudioOutput = runtimecontract.SessionRuntimeObservationAudioOutput
const SessionRuntimeObservationAudioInput = runtimecontract.SessionRuntimeObservationAudioInput
const SessionRuntimeObservationAudioPlaybackReceipt = runtimecontract.SessionRuntimeObservationAudioPlaybackReceipt
const SessionRuntimeObservationAudioRenderTapUnavailable = runtimecontract.SessionRuntimeObservationAudioRenderTapUnavailable
const SessionRuntimeObservationInputCommit = runtimecontract.SessionRuntimeObservationInputCommit
const SessionRuntimeObservationResponseCreate = runtimecontract.SessionRuntimeObservationResponseCreate
const SessionRuntimeObservationTurnCompleted = runtimecontract.SessionRuntimeObservationTurnCompleted
const SessionRuntimeObservationTerminal = runtimecontract.SessionRuntimeObservationTerminal
const SessionTokenUsageIncremental = runtimecontract.SessionTokenUsageIncremental

// sessionRuntimeObservationRecorder is the runtime-owned adapter between the
// session lifecycle and an optional observer. Its clock is supplied by CLI
// composition, so deterministic callers observe the same source that was
// injected into the generated command graph.
type sessionRuntimeObservationRecorder struct {
	observer                  SessionRuntimeObserver
	clock                     platformclock.Source
	sequence                  atomic.Uint64
	providerBoundaryObserving bool
	retainCommitPayload       bool

	terminalOnce sync.Once
	inputMu      sync.Mutex
	inputPayload []byte
	inputCommits int
}

func newSessionRuntimeObservationRecorder(observer SessionRuntimeObserver, source platformclock.Source) *sessionRuntimeObservationRecorder {
	if observer == nil {
		return nil
	}
	retainPayload := true
	if observer, ok := observer.(interface{ RetainCommitPayload() bool }); ok {
		retainPayload = observer.RetainCommitPayload()
	}
	providerBoundaries := false
	if observer, ok := observer.(interface{ ObserveProviderBoundaries() bool }); ok {
		providerBoundaries = observer.ObserveProviderBoundaries()
	}
	return &sessionRuntimeObservationRecorder{
		observer: observer, clock: platformclock.Ensure(source), providerBoundaryObserving: providerBoundaries, retainCommitPayload: retainPayload,
	}
}

// These adapters only translate the two public observation types. The service
// retains ownership of ordering, copying, redaction, and policy composition.
type traceRuntimeObserverAdapter struct {
	observer SessionRuntimeObserver
	provider func() bool
	retain   func() bool
}

func (a traceRuntimeObserverAdapter) ObserveSessionRuntime(o sessiontrace.SessionRuntimeObservation) {
	if a.observer != nil {
		a.observer.ObserveSessionRuntime(fromSessionTraceObservation(o))
	}
}
func (a traceRuntimeObserverAdapter) ObserveProviderBoundaries() bool {
	return a.provider != nil && a.provider()
}
func (a traceRuntimeObserverAdapter) RetainCommitPayload() bool { return a.retain == nil || a.retain() }

func adaptSessionTraceObserver(observer SessionRuntimeObserver) sessiontrace.RuntimeObserver {
	if observer == nil {
		return nil
	}
	return traceRuntimeObserverAdapter{observer: observer,
		provider: func() bool {
			p, ok := observer.(interface{ ObserveProviderBoundaries() bool })
			return ok && p.ObserveProviderBoundaries()
		},
		retain: func() bool {
			p, ok := observer.(interface{ RetainCommitPayload() bool })
			return !ok || p.RetainCommitPayload()
		}}
}

type agentRuntimeObserverAdapter struct{ observer sessiontrace.RuntimeObserver }

func (a agentRuntimeObserverAdapter) ObserveSessionRuntime(o SessionRuntimeObservation) {
	if a.observer != nil {
		a.observer.ObserveSessionRuntime(toSessionTraceObservation(o))
	}
}
func (a agentRuntimeObserverAdapter) ObserveProviderBoundaries() bool {
	p, ok := a.observer.(sessiontrace.ProviderBoundaryObserver)
	return ok && p.ObserveProviderBoundaries()
}
func (a agentRuntimeObserverAdapter) RetainCommitPayload() bool {
	p, ok := a.observer.(sessiontrace.CommitPayloadObserver)
	return ok && p.RetainCommitPayload()
}
func adaptAgentRuntimeObserver(observer sessiontrace.RuntimeObserver) SessionRuntimeObserver {
	if observer == nil {
		return nil
	}
	return agentRuntimeObserverAdapter{observer: observer}
}

func toSessionTraceObservation(o SessionRuntimeObservation) sessiontrace.SessionRuntimeObservation {
	return sessiontrace.SessionRuntimeObservation{Kind: sessiontrace.SessionRuntimeObservationKind(o.Kind), Tick: o.Tick, Timestamp: o.Timestamp, Payload: o.Payload, TurnsCompleted: o.TurnsCompleted, InputCommit: o.InputCommit, ResponseID: o.ResponseID, ResponsePurpose: o.ResponsePurpose, StreamID: o.StreamID, LoopPassID: o.LoopPassID, Epoch: o.Epoch, Clean: o.Clean, Error: o.Error, FinalAccounting: toSessionTraceAccounting(o.FinalAccounting)}
}
func fromSessionTraceObservation(o sessiontrace.SessionRuntimeObservation) SessionRuntimeObservation {
	return SessionRuntimeObservation{Kind: SessionRuntimeObservationKind(o.Kind), Tick: o.Tick, Timestamp: o.Timestamp, Payload: o.Payload, TurnsCompleted: o.TurnsCompleted, InputCommit: o.InputCommit, ResponseID: o.ResponseID, ResponsePurpose: o.ResponsePurpose, StreamID: o.StreamID, LoopPassID: o.LoopPassID, Epoch: o.Epoch, Clean: o.Clean, Error: o.Error, FinalAccounting: fromSessionTraceAccounting(o.FinalAccounting)}
}
func toSessionTraceAccounting(a *SessionFinalAccounting) *sessiontrace.SessionFinalAccounting {
	if a == nil {
		return nil
	}
	return &sessiontrace.SessionFinalAccounting{PromptTokens: a.PromptTokens, CompletionTokens: a.CompletionTokens, TotalTokens: a.TotalTokens, ReasoningTokens: a.ReasoningTokens, UsageSemantics: sessiontrace.SessionTokenUsageSemantics(a.UsageSemantics), Metrics: a.Metrics}
}
func fromSessionTraceAccounting(a *sessiontrace.SessionFinalAccounting) *SessionFinalAccounting {
	if a == nil {
		return nil
	}
	return &SessionFinalAccounting{PromptTokens: a.PromptTokens, CompletionTokens: a.CompletionTokens, TotalTokens: a.TotalTokens, ReasoningTokens: a.ReasoningTokens, UsageSemantics: SessionTokenUsageSemantics(a.UsageSemantics), Metrics: a.Metrics}
}

// enableProviderBoundaryObservations opts a runtime recorder into inbound
// provider commit/response boundaries. Ordinary session runtime observers keep
// their historical client-owned observation surface; room latency evidence is
// the caller that needs server-VAD boundaries as well.
func (r *sessionRuntimeObservationRecorder) enableProviderBoundaryObservations() {
	if r != nil {
		r.providerBoundaryObserving = true
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
	r.observeWithMetadataAndIdentity(kind, payload, turns, inputCommit, clean, runErr, responseID, responsePurpose, "", 0, 0)
}

func (r *sessionRuntimeObservationRecorder) observeWithMetadataAndIdentity(kind SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, responseID string, responsePurpose messages.ResponsePurpose, streamID string, loopPassID int, epoch uint64) {
	r.observeFinalWithMetadataAndIdentity(kind, payload, turns, inputCommit, clean, runErr, nil, responseID, responsePurpose, streamID, loopPassID, epoch)
}

func (r *sessionRuntimeObservationRecorder) observeFinalWithMetadata(kind SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, finalAccounting *SessionFinalAccounting, responseID string, responsePurpose messages.ResponsePurpose) {
	r.observeFinalWithMetadataAndIdentity(kind, payload, turns, inputCommit, clean, runErr, finalAccounting, responseID, responsePurpose, "", 0, 0)
}

func (r *sessionRuntimeObservationRecorder) observeFinalWithMetadataAndIdentity(kind SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, finalAccounting *SessionFinalAccounting, responseID string, responsePurpose messages.ResponsePurpose, streamID string, loopPassID int, epoch uint64) {
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
		StreamID:        streamID,
		LoopPassID:      loopPassID,
		Epoch:           epoch,
		Clean:           clean,
		Error:           sessionRuntimeObservationError(runErr),
		FinalAccounting: cloneSessionFinalAccounting(finalAccounting),
	})
}

// audioOutputMessage records provider output with the identity already
// attached to its stream message. Empty fields intentionally remain empty for
// providers that do not expose a response or stream identity.
func (r *sessionRuntimeObservationRecorder) audioOutputMessage(payload []byte, msg messages.StreamMessage) {
	if r == nil || r.observer == nil {
		return
	}
	// LoopPassID identifies the agent-loop pass. It must not be promoted to a
	// playback epoch: epochs belong to the device playback worker and are only
	// available on its applied/rejected receipt boundary.
	r.observeWithMetadataAndIdentity(SessionRuntimeObservationAudioOutput, payload, 0, 0, false, nil, msg.ResponseID, msg.ResponsePurpose, msg.ActorStreamID, msg.LoopPassID, 0)
}

// audioPlaybackReceipt records the result of an admitted playback control at
// the worker boundary. This keeps the trace honest about the distinction
// between a loop command accepted into memory and a command applied by the
// device worker.
func (r *sessionRuntimeObservationRecorder) audioPlaybackReceipt(receipt audio.PlaybackReceipt) {
	if r == nil {
		return
	}
	errText := ""
	if receipt.Err != nil {
		errText = receipt.Err.Error()
	}
	payload, _ := json.Marshal(struct {
		CommandID  uint64 `json:"command_id"`
		Epoch      uint64 `json:"epoch,omitempty"`
		Applied    bool   `json:"applied"`
		AudioEndMS int    `json:"audio_end_ms,omitempty"`
		Error      string `json:"error,omitempty"`
	}{
		CommandID: receipt.CommandID, Epoch: receipt.Epoch, Applied: receipt.Applied,
		AudioEndMS: receipt.Interruption.AudioEndMS, Error: errText,
	})
	r.observe(SessionRuntimeObservationAudioPlaybackReceipt, payload, 0, false, receipt.Err)
}

// audioInput observes admission to the agent's input buffer. Provider sends
// may lag admission, so this event cannot define the contents of a commit.
func (r *sessionRuntimeObservationRecorder) audioInput(payload []byte) {
	if r == nil {
		return
	}
	r.observe(SessionRuntimeObservationAudioInput, payload, 0, false, nil)
}

// providerAudioSent accumulates only audio accepted by the session transport.
// The model runner sends audio and MESSAGE.END in FIFO order, so a later
// admission cannot contaminate the preceding turn's commit evidence.
func (r *sessionRuntimeObservationRecorder) providerAudioSent(payload []byte) {
	if r == nil {
		return
	}
	r.inputMu.Lock()
	if r.retainCommitPayload {
		r.inputPayload = append(r.inputPayload, payload...)
	}
	r.inputMu.Unlock()
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

// providerInputCommit records a provider-originated commit boundary, such as
// the INPUT_ITEM.ADDED event emitted after server-side VAD. The provider owns
// the commit, so there is no client ordinal to report, but the accumulated
// audio remains available to the runtime observer for evidence consumers.
func (r *sessionRuntimeObservationRecorder) providerInputCommit() {
	if r == nil {
		return
	}
	r.inputMu.Lock()
	payload := append([]byte(nil), r.inputPayload...)
	r.inputPayload = nil
	r.inputMu.Unlock()
	r.observeWithInputCommit(SessionRuntimeObservationInputCommit, payload, 0, 0, true, nil)
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

// Tool evidence is emitted at executor entry/result, independently of provider
// request arrival. This preserves the actual timeout and execution interval.
func (r *sessionRuntimeObservationRecorder) observeToolCall(call messages.ToolCall) {
	if r == nil {
		return
	}
	payload, err := json.Marshal(call)
	r.observe("tool_call", payload, 0, err == nil, err)
}
func (r *sessionRuntimeObservationRecorder) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if r == nil {
		return
	}
	payload, err := json.Marshal(struct {
		CallID   string                    `json:"call_id"`
		Name     string                    `json:"name"`
		Response messages.ToolCallResponse `json:"response"`
		Failed   bool                      `json:"failed"`
	}{call.ID, call.Name, response, failed})
	r.observe("tool_result", payload, 0, !failed && err == nil, err)
}
