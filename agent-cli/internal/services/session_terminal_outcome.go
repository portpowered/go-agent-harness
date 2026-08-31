package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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

// ErrSessionTerminalAlreadyPublished is returned when a caller tries to reuse
// the terminal side-effect boundary. A terminal report is an owned resource,
// not an idempotent print helper: after it is consumed, a second owner is a
// programming error and cannot emit another block.
var ErrSessionTerminalAlreadyPublished = errors.New("session terminal already published")

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

type sessionTerminalReporter struct {
	mu       sync.Mutex
	outcome  sessionTerminalOutcome
	consumed bool
}

type sessionTerminalReporterContextKey struct{}

func withSessionTerminalReporter(ctx context.Context, reporter *sessionTerminalReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionTerminalReporterContextKey{}, reporter)
}

func sessionTerminalReporterFromContext(ctx context.Context) *sessionTerminalReporter {
	if ctx == nil {
		return nil
	}
	reporter, _ := ctx.Value(sessionTerminalReporterContextKey{}).(*sessionTerminalReporter)
	return reporter
}

func newSessionTerminalReporter() *sessionTerminalReporter {
	return &sessionTerminalReporter{
		outcome: sessionTerminalOutcome{
			artifactState: sessionTerminalArtifactNotApplicable,
			completion:    sessionTerminalCompletionPending,
		},
	}
}

func (r *sessionTerminalReporter) markRunStarted() {
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
func (r *sessionTerminalReporter) observeStreamMessage(msg messages.StreamMessage, leadingNewline bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observeStreamMessageLocked(msg, leadingNewline)
}

func (r *sessionTerminalReporter) observeStreamMessageLocked(msg messages.StreamMessage, leadingNewline bool) {
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
		return value != nil && value.Content != ""
	case *messages.ReasoningDeltaValue:
		return value != nil && value.Content != ""
	case *messages.AudioDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.ImageDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.VideoDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.FileDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.EmbeddingDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.ToolCallDeltaValue:
		return value != nil && value.PartialJSON != ""
	case *messages.ToolCallEndValue:
		return value != nil
	case *messages.RefusalValue:
		return value != nil && value.Message != ""
	case *messages.TranscriptDeltaValue:
		return value != nil && value.Text != ""
	default:
		return false
	}
}

func (r *sessionTerminalReporter) observeSessionCloseLocked(msg messages.StreamMessage, leadingNewline bool) {
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
	default:
		rememberObservedTerminalCandidate(&r.outcome.observedTerminal, candidate)
	}
}

func (r *sessionTerminalReporter) observeErrorLocked(value *messages.ErrorValue, leadingNewline bool) {
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
func (r *sessionTerminalReporter) markDurationExpiryWithOutput(outputState messages.TerminalOutputState) {
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

func markSessionDurationExpiry(reporter *sessionTerminalReporter, planned bool, outputState messages.TerminalOutputState) {
	if planned {
		reporter.markDurationExpiryWithOutput(outputState)
	}
}

func (r *sessionTerminalReporter) markReplayComplete() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.outcome.replayComplete = true
	r.outcome.completion = sessionTerminalCompletionComplete
	r.mu.Unlock()
}

func (r *sessionTerminalReporter) recordArtifactFinalization(requested bool, err error) {
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

func (r *sessionTerminalReporter) publish(out io.Writer, runErr error) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.consumed {
		r.mu.Unlock()
		return ErrSessionTerminalAlreadyPublished
	}
	r.consumed = true
	if !r.outcome.runStarted {
		r.mu.Unlock()
		return nil
	}
	if sessionErrorHasIndependentFailure(runErr) {
		r.outcome.fatalError = errors.Join(r.outcome.fatalError, runErr)
	}
	candidate, replayComplete := r.reconcileLocked(runErr)
	r.mu.Unlock()
	if candidate == nil {
		return nil
	}
	if out == nil {
		out = io.Discard
	}
	return writePublishedSessionTerminal(out, candidate, replayComplete)
}

func (r *sessionTerminalReporter) reconcileLocked(runErr error) (*sessionTerminalCandidate, bool) {
	o := &r.outcome
	if o.fatalError != nil {
		candidate := o.failure
		if candidate == nil {
			candidate = &sessionTerminalCandidate{
				value: messages.NewSessionCloseValueWithTerminal(
					"",
					string(messages.TerminalReasonTerminalFailure),
					string(messages.TerminalReasonTerminalFailure),
					messages.TerminalReasonTerminalFailure,
					messages.TerminalProvenanceSession,
					r.outcome.outputStateOrNone(),
				),
				leadingNewline: true,
			}
		}
		candidate.value = normalizeSessionTerminalValue(candidate.value, messages.TerminalReasonTerminalFailure, o.outputStateOrNone())
		o.cause = candidate.value.TerminalReason
		o.completion = sessionTerminalCompletionIncomplete
		return candidate, false
	}
	if o.replayComplete {
		candidate := &sessionTerminalCandidate{
			value: messages.NewSessionCloseValueWithTerminal(
				"",
				"",
				SessionReplayCompleteClassification,
				messages.TerminalReasonReplayComplete,
				messages.TerminalProvenanceReplay,
				messages.TerminalOutputComplete,
			),
			leadingNewline: true,
		}
		o.cause = messages.TerminalReasonReplayComplete
		o.outputState = messages.TerminalOutputComplete
		o.completion = sessionTerminalCompletionComplete
		return candidate, true
	}
	// A planned duration expiry is controller evidence, so it wins over a
	// provider close or cancellation observed during the mandatory shutdown
	// drain. Fatal errors and verified replay completion remain stronger above;
	// natural provider-close completion still uses observedTerminal below when
	// no duration expiry was planned.
	if o.durationTerminal != nil || o.durationExpired {
		candidate := o.durationTerminal
		if candidate == nil {
			candidate = &sessionTerminalCandidate{
				value: messages.NewSessionCloseValueWithTerminal(
					"",
					string(SessionMaxDurationReason),
					string(SessionMaxDurationReason),
					SessionMaxDurationReason,
					messages.TerminalProvenanceLoop,
					o.outputStateOrNone(),
				),
				leadingNewline: true,
			}
		}
		candidate.value = normalizeSessionTerminalValue(candidate.value, SessionMaxDurationReason, o.outputStateOrNone())
		o.cause = SessionMaxDurationReason
		o.completion = sessionTerminalCompletionIncomplete
		return candidate, false
	}
	if o.cancellation != nil {
		candidate := o.cancellation
		candidate.value = normalizeSessionTerminalValue(candidate.value, messages.TerminalReasonCancellation, o.outputStateOrNone())
		o.cause = messages.TerminalReasonCancellation
		o.completion = sessionTerminalCompletionIncomplete
		return candidate, false
	}
	if o.observedTerminal != nil {
		candidate := o.observedTerminal
		candidate.value = normalizeSessionTerminalValue(candidate.value, terminalReasonOrDefault(candidate.value, messages.TerminalReasonSessionClose), o.outputStateOrNone())
		o.cause = candidate.value.TerminalReason
		if isSuccessfulTerminalReason(o.cause) {
			o.completion = sessionTerminalCompletionComplete
		} else {
			o.completion = sessionTerminalCompletionIncomplete
		}
		return candidate, false
	}
	if sessionErrorIsCancellation(runErr) {
		candidate := &sessionTerminalCandidate{
			value: messages.NewSessionCloseValueWithTerminal(
				"",
				"",
				string(messages.TerminalReasonCancellation),
				messages.TerminalReasonCancellation,
				messages.TerminalProvenanceSession,
				o.outputStateOrNone(),
			),
			leadingNewline: true,
		}
		o.cause = messages.TerminalReasonCancellation
		o.completion = sessionTerminalCompletionIncomplete
		return candidate, false
	}
	// A started session that supplied no more specific evidence still receives
	// one normalized session-close record. This keeps an empty but successful
	// lifecycle observable without pretending it completed a response.
	candidate := &sessionTerminalCandidate{
		value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(messages.TerminalReasonSessionClose),
			string(messages.TerminalReasonSessionClose),
			messages.TerminalReasonSessionClose,
			messages.TerminalProvenanceSession,
			o.outputStateOrNone(),
		),
		leadingNewline: true,
	}
	o.cause = messages.TerminalReasonSessionClose
	o.completion = sessionTerminalCompletionIncomplete
	return candidate, false
}

func (o *sessionTerminalOutcome) outputStateOrNone() messages.TerminalOutputState {
	if o == nil || o.outputState == "" {
		return messages.TerminalOutputNone
	}
	return o.outputState
}

func terminalReasonOrDefault(value *messages.SessionCloseValue, fallback messages.TerminalReason) messages.TerminalReason {
	if value == nil || value.TerminalReason == "" {
		return fallback
	}
	return value.TerminalReason
}

func isSuccessfulTerminalReason(reason messages.TerminalReason) bool {
	return reason == messages.TerminalReasonProviderAuthoredCompletion ||
		reason == messages.TerminalReasonLoopSynthesizedCompletion ||
		reason == messages.TerminalReasonReplayComplete
}

func normalizeSessionTerminalValue(value *messages.SessionCloseValue, fallback messages.TerminalReason, outputState messages.TerminalOutputState) *messages.SessionCloseValue {
	if value == nil {
		value = &messages.SessionCloseValue{}
	}
	value = cloneSessionCloseValue(value)
	if value.TerminalReason == "" {
		value.TerminalReason = fallback
	}
	if value.Classification == "" {
		if value.TerminalReason == messages.TerminalReasonProviderClose {
			value.Classification = "transport"
		} else {
			value.Classification = string(value.TerminalReason)
		}
	}
	if value.TerminalProvenance == "" {
		value.TerminalProvenance = messages.TerminalProvenanceSession
	}
	if value.OutputState == "" {
		value.OutputState = outputState
	}
	if value.OutputState == "" {
		value.OutputState = messages.TerminalOutputNone
	}
	return value
}

func writePublishedSessionTerminal(out io.Writer, candidate *sessionTerminalCandidate, replayComplete bool) error {
	if candidate == nil || candidate.value == nil {
		return nil
	}
	value := candidate.value
	if value.Reason == "" && candidate.leadingNewline {
		if _, err := io.WriteString(out, "\n"); err != nil {
			return err
		}
		if err := writeSessionReplayClose(out, value, false); err != nil {
			return err
		}
	} else if err := writeSessionReplayClose(out, value, candidate.leadingNewline); err != nil {
		return err
	}
	if replayComplete {
		_, err := fmt.Fprintln(out, "[session replay complete]")
		return err
	}
	return nil
}

func sessionErrorHasIndependentFailure(err error) bool {
	if err == nil {
		return false
	}
	var leaves []error
	collectSessionErrorLeaves(err, &leaves)
	for _, leaf := range leaves {
		if leaf == nil || leaf == context.Canceled || leaf == context.DeadlineExceeded || errors.Is(leaf, errSessionMaxDurationExpired) {
			continue
		}
		return true
	}
	return false
}

func sessionErrorIsCancellation(err error) bool {
	if err == nil || sessionErrorHasIndependentFailure(err) {
		return false
	}
	var leaves []error
	collectSessionErrorLeaves(err, &leaves)
	for _, leaf := range leaves {
		if leaf == context.Canceled || leaf == context.DeadlineExceeded {
			return true
		}
	}
	return false
}

func collectSessionErrorLeaves(err error, leaves *[]error) {
	if err == nil {
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			collectSessionErrorLeaves(child, leaves)
		}
		return
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		collectSessionErrorLeaves(unwrapped, leaves)
		return
	}
	*leaves = append(*leaves, err)
}
