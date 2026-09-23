package service

import (
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

const (
	SessionMaxDurationReason            = sessionterminal.MaxDurationReason
	SessionReplayCompleteClassification = sessionterminal.ReplayCompleteClassification
)

// sessionTerminalCompletionState records whether the session has a verified
// completed response. It is deliberately separate from the terminal cause:
// max-duration can be a successful bounded run while replay completion is a
// stronger, capture-derived completion claim.
type sessionTerminalCompletionState uint8

const (
	sessionTerminalCompletionPending sessionTerminalCompletionState = iota
	sessionTerminalCompletionComplete
	sessionTerminalCompletionIncomplete
)

// sessionTerminalArtifactState is the artifact evidence available to the
// final outcome reconciler. Unknown is used only before a duration sidecar has
// reached its finalization boundary; ordinary sessions start at not-applicable.
type sessionTerminalArtifactState uint8

const (
	sessionTerminalArtifactNotApplicable sessionTerminalArtifactState = iota
	sessionTerminalArtifactValid
	sessionTerminalArtifactInvalid
)

type sessionTerminalCandidate struct {
	value          *messages.SessionCloseValue
	leadingNewline bool
}

// sessionTerminalOutcome is the explicit, unannounced state accumulated while
// the session is running. The renderer and lifecycle helpers only add
// evidence; publish is the sole customer-facing side-effect boundary.
type sessionTerminalOutcome struct {
	cause         messages.TerminalReason
	completion    sessionTerminalCompletionState
	outputState   messages.TerminalOutputState
	artifactState sessionTerminalArtifactState
	fatalError    error

	observedTerminal *sessionTerminalCandidate
	durationTerminal *sessionTerminalCandidate
	cancellation     *sessionTerminalCandidate
	failure          *sessionTerminalCandidate
	durationExpired  bool
	replayComplete   bool
	runStarted       bool
}

type terminalReporter struct {
	mu       sync.Mutex
	outcome  sessionTerminalOutcome
	consumed bool
}

func newTerminalReporter() *terminalReporter {
	return &terminalReporter{
		outcome: sessionTerminalOutcome{
			artifactState: sessionTerminalArtifactNotApplicable,
			completion:    sessionTerminalCompletionPending,
		},
	}
}

func (r *terminalReporter) markRunStarted() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.outcome.runStarted = true
	r.mu.Unlock()
}

// observeStreamMessage records output and terminal candidates without writing
// the terminal value. leadingNewline is supplied by the stateful renderer at
// the point where it would previously have printed a close.
func (r *terminalReporter) observeStreamMessage(msg messages.StreamMessage, leadingNewline bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observeStreamMessageLocked(msg, leadingNewline)
}
func (r *terminalReporter) observeStreamMessageLocked(msg messages.StreamMessage, leadingNewline bool) {
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		r.outcome.outputState = messages.TerminalOutputNone
		// The response-complete bit is represented by the candidate's output
		// state below; a new response starts with no delivered output.
	case messages.StreamTypeTextDelta,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeImageDelta,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeFileDelta,
		messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeRefusal:
		if streamMessageHasOutput(msg) {
			r.outcome.outputState = messages.TerminalOutputPartial
		}
	case messages.StreamTypeTranscriptDelta:
		if msg.Role != messages.RoleUser && streamMessageHasOutput(msg) {
			r.outcome.outputState = messages.TerminalOutputPartial
		}
	case messages.StreamTypeTextStart,
		messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeImageStart,
		messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart,
		messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart,
		messages.StreamTypeFileEnd,
		messages.StreamTypeEmbeddingStart,
		messages.StreamTypeEmbeddingEnd,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeVADSpeechStarted,
		messages.StreamTypeVADSpeechStopped,
		messages.StreamTypeTranscriptStart,
		messages.StreamTypeTranscriptEnd,
		messages.StreamTypeInputItemAdded,
		messages.StreamTypePong,
		messages.StreamTypeSessionOpen,
		messages.StreamTypeSessionCreated,
		messages.StreamTypeSessionUpdated,
		messages.StreamTypeSessionUpdate,
		messages.StreamTypeResponseCancel,
		messages.StreamTypeResponseCreate,
		messages.StreamTypeLoopEnd,
		messages.StreamTypeUsageInfo,
		messages.StreamTypeSystemFullMessage:
	case messages.StreamTypeMessageEnd:
		if r.outcome.outputState == messages.TerminalOutputPartial {
			r.outcome.outputState = messages.TerminalOutputComplete
		}
	case messages.StreamTypeSessionClose:
		r.observeSessionCloseLocked(msg, leadingNewline)
	case messages.StreamTypeError:
		if value, ok := msg.Value.(*messages.ErrorValue); ok && value != nil && !value.IsNonTerminal() {
			r.observeErrorLocked(value, leadingNewline)
		}
	}
}

func streamMessageHasOutput(msg messages.StreamMessage) bool {
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		return nonEmptyText(value, func(v *messages.TextDeltaValue) string { return v.Content })
	case *messages.ReasoningDeltaValue:
		return nonEmptyText(value, func(v *messages.ReasoningDeltaValue) string { return v.Content })
	case *messages.AudioDeltaValue:
		return nonEmptyBytes(value, func(v *messages.AudioDeltaValue) []byte { return v.Content })
	case *messages.ImageDeltaValue:
		return nonEmptyBytes(value, func(v *messages.ImageDeltaValue) []byte { return v.Content })
	case *messages.VideoDeltaValue:
		return nonEmptyBytes(value, func(v *messages.VideoDeltaValue) []byte { return v.Content })
	case *messages.FileDeltaValue:
		return nonEmptyBytes(value, func(v *messages.FileDeltaValue) []byte { return v.Content })
	case *messages.EmbeddingDeltaValue:
		return nonEmptyBytes(value, func(v *messages.EmbeddingDeltaValue) []byte { return v.Content })
	case *messages.ToolCallDeltaValue:
		return nonEmptyText(value, func(v *messages.ToolCallDeltaValue) string { return v.PartialJSON })
	case *messages.ToolCallEndValue:
		return value != nil
	case *messages.RefusalValue:
		return nonEmptyText(value, func(v *messages.RefusalValue) string { return v.Message })
	case *messages.TranscriptDeltaValue:
		return nonEmptyText(value, func(v *messages.TranscriptDeltaValue) string { return v.Text })
	default:
		return false
	}
}

func nonEmptyText[T any](value *T, text func(*T) string) bool {
	return value != nil && text(value) != ""
}

func nonEmptyBytes[T any](value *T, content func(*T) []byte) bool {
	return value != nil && len(content(value)) > 0
}

func (r *terminalReporter) observeSessionCloseLocked(msg messages.StreamMessage, leadingNewline bool) {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok || value == nil {
		return
	}
	candidate := &sessionTerminalCandidate{value: cloneSessionCloseValue(value), leadingNewline: leadingNewline}
	switch value.TerminalReason {
	case messages.TerminalReasonReplayComplete:
		r.outcome.replayComplete = true
		r.outcome.completion = sessionTerminalCompletionComplete
	case messages.TerminalReasonCancellation:
		rememberTerminalCandidate(&r.outcome.cancellation, candidate)
	case SessionMaxDurationReason:
		rememberTerminalCandidate(&r.outcome.durationTerminal, candidate)
		r.outcome.durationExpired = true
	case messages.TerminalReasonTerminalFailure,
		messages.TerminalReasonReplayDivergence,
		messages.TerminalReasonReplayIncomplete:
		rememberTerminalCandidate(&r.outcome.failure, candidate)
	case messages.TerminalReasonProviderAuthoredCompletion,
		messages.TerminalReasonLoopSynthesizedCompletion,
		messages.TerminalReasonSessionClose,
		messages.TerminalReasonPartialOutput,
		messages.TerminalReasonProviderClose:
		rememberObservedTerminalCandidate(&r.outcome.observedTerminal, candidate)
	default:
		rememberObservedTerminalCandidate(&r.outcome.observedTerminal, candidate)
	}
}

func (r *terminalReporter) observeErrorLocked(value *messages.ErrorValue, leadingNewline bool) {
	reason := value.TerminalReason
	if reason == "" {
		reason = messages.TerminalReasonTerminalFailure
	}
	classification := value.Classification
	if classification == "" {
		classification = string(reason)
	}
	provenance := value.TerminalProvenance
	if provenance == "" {
		provenance = messages.TerminalProvenanceSession
	}
	outputState := value.OutputState
	if outputState == "" {
		outputState = r.outcome.outputState
	}
	if outputState == "" {
		outputState = messages.TerminalOutputNone
	}
	candidate := &sessionTerminalCandidate{
		value: messages.NewSessionCloseValueWithTerminal(
			"",
			"",
			classification,
			reason,
			provenance,
			outputState,
		),
		leadingNewline: leadingNewline,
	}
	if reason == messages.TerminalReasonCancellation {
		rememberTerminalCandidate(&r.outcome.cancellation, candidate)
		return
	}
	rememberTerminalCandidate(&r.outcome.failure, candidate)
}

// markDurationExpiry records the controller's planned reason but does not
// manufacture a candidate until the duration loop has tried to deliver its
// synthetic close through the renderer. This preserves the renderer's exact
// newline boundary when a partial transcript is still open.
func (r *terminalReporter) markDurationExpiryWithOutput(outputState messages.TerminalOutputState) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.outcome.durationExpired = true
	if outputState != "" {
		r.outcome.outputState = outputState
	}
	r.mu.Unlock()
}

func markSessionDurationExpiry(reporter *terminalReporter, planned bool, outputState messages.TerminalOutputState) {
	if planned {
		reporter.markDurationExpiryWithOutput(outputState)
	}
}

func (r *terminalReporter) markReplayComplete() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.outcome.replayComplete = true
	r.outcome.completion = sessionTerminalCompletionComplete
	r.mu.Unlock()
}

func (r *terminalReporter) recordArtifactFinalization(requested bool, err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !requested {
		r.outcome.artifactState = sessionTerminalArtifactNotApplicable
		return
	}
	if err != nil {
		r.outcome.artifactState = sessionTerminalArtifactInvalid
		r.outcome.fatalError = errors.Join(r.outcome.fatalError, err)
		return
	}
	r.outcome.artifactState = sessionTerminalArtifactValid
}

func rememberTerminalCandidate(slot **sessionTerminalCandidate, candidate *sessionTerminalCandidate) {
	if *slot == nil {
		*slot = candidate
	}
}

func rememberObservedTerminalCandidate(slot **sessionTerminalCandidate, candidate *sessionTerminalCandidate) {
	if *slot == nil || (terminalCandidateIsSpecific(candidate) && !terminalCandidateIsSpecific(*slot)) {
		*slot = candidate
	}
}

func terminalCandidateIsSpecific(candidate *sessionTerminalCandidate) bool {
	if candidate == nil || candidate.value == nil {
		return false
	}
	return candidate.value.TerminalReason != "" && candidate.value.TerminalReason != messages.TerminalReasonSessionClose
}

func cloneSessionCloseValue(value *messages.SessionCloseValue) *messages.SessionCloseValue {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func (r *terminalReporter) publish(out io.Writer, runErr error) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.consumed {
		r.mu.Unlock()
		return sessionterminal.ErrAlreadyPublished
	}
	r.consumed = true
	if !r.outcome.runStarted {
		r.mu.Unlock()
		return nil
	}
	candidate, replayComplete := r.reconcileLocked(runErr)
	fatalError := r.outcome.fatalError
	r.mu.Unlock()
	if candidate == nil {
		return nil
	}
	if out == nil {
		out = io.Discard
	}
	return errors.Join(fatalError, writePublishedSessionTerminal(out, candidate, replayComplete))
}

func (r *terminalReporter) reconcileLocked(runErr error) (*sessionTerminalCandidate, bool) {
	o := &r.outcome
	if o.fatalError != nil || sessionErrorHasIndependentFailure(runErr) {
		return r.reconcileFailure(o)
	}
	if o.replayComplete {
		return r.reconcileReplayComplete(o)
	}
	if o.cancellation != nil {
		return r.reconcileCancellation(o)
	}
	if o.observedTerminal != nil {
		return r.reconcileObserved(o)
	}
	if o.durationTerminal != nil || o.durationExpired {
		return r.reconcileDuration(o)
	}
	if sessionErrorIsCancellation(runErr) {
		return r.reconcileRunCancellation(o)
	}
	// A started session that supplied no more specific evidence still receives
	// one normalized session-close record. This keeps an empty but successful
	// lifecycle observable without pretending it completed a response.
	return r.reconcileDefault(o)
}
