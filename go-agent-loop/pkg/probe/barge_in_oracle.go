package probe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// BargeInEventKind is the provider-neutral event vocabulary used by the
// identity-aware interruption oracle. Transport adapters translate provider
// events into these events before recording them.
type BargeInEventKind string

const (
	BargeInEventInputAppend      BargeInEventKind = "input.append"
	BargeInEventInputCommit      BargeInEventKind = "input.commit"
	BargeInEventUserTurn         BargeInEventKind = "user.turn"
	BargeInEventResponseCreated  BargeInEventKind = "response.created"
	BargeInEventResponseOutput   BargeInEventKind = "response.output"
	BargeInEventResponseCancel   BargeInEventKind = "response.cancel"
	BargeInEventResponseTerminal BargeInEventKind = "response.terminal"
	BargeInEventToolCall         BargeInEventKind = "tool.call"
	BargeInEventToolResult       BargeInEventKind = "tool.result"
	BargeInEventContinuation     BargeInEventKind = "continuation"
	BargeInEventSessionTerminal  BargeInEventKind = "session.terminal"
)

// BargeInDisposition is deliberately shared by the normalized event types so
// adapters do not have to invent provider-specific terminal names.
type BargeInDisposition string

const (
	BargeInDispositionCompleted BargeInDisposition = "completed"
	BargeInDispositionCancelled BargeInDisposition = "cancelled"
	BargeInDispositionFailed    BargeInDisposition = "failed"
	BargeInDispositionRejected  BargeInDisposition = "rejected"
	BargeInDispositionDelivered BargeInDisposition = "delivered"
	BargeInDispositionClean     BargeInDisposition = "clean"
)

// BargeInEvent contains identity and ordering evidence, never a provider
// payload. Bytes and NonEmpty are enough to prove that an input or output was
// non-empty without copying customer audio, transcripts, or credentials into
// diagnostics.
type BargeInEvent struct {
	Sequence int
	Kind     BargeInEventKind

	InputID       string
	TurnID        string
	AppendGroupID string
	ResponseID    string
	ToolCallID    string
	Disposition   BargeInDisposition
	Reason        string
	Bytes         int
	NonEmpty      bool
	Clean         bool
}

// BargeInInputExpectation identifies one logical customer utterance. Multiple
// physical input.append events may share the same InputID and AppendGroupID;
// the oracle still requires one commit and one user-turn representation.
type BargeInInputExpectation struct {
	ID     string
	TurnID string
}

// BargeInResponseExpectation describes the expected owner and terminal
// outcome of one response. RequireCancel and ForbidCancel make completion-vs-
// interruption precedence explicit instead of inferring it from counts.
// ForbidOutput makes a deliberately held first-output boundary observable.
type BargeInResponseExpectation struct {
	ID                  string
	InputID             string
	TurnID              string
	Disposition         BargeInDisposition
	RequireCancel       bool
	ForbidCancel        bool
	RequireOutput       bool
	ForbidOutput        bool
	RequireContinuation bool
}

// BargeInToolExpectation identifies the response-owned tool call and its one
// explicit result disposition. ForbidResultAfterCancel is opt-in because some
// providers legitimately let a tool call finish after its spoken response was
// interrupted; a contract that requires cancellation to win for that tool can
// reject a later delivered result explicitly.
type BargeInToolExpectation struct {
	ID                      string
	ResponseID              string
	TurnID                  string
	Disposition             BargeInDisposition
	ForbidResultAfterCancel bool
}

// BargeInContract supplies stable identities expected by a proof. Supplying
// identities is important: a capture with a dropped replacement cannot pass
// by merely having internally consistent aggregate counts.
type BargeInContract struct {
	Inputs    []BargeInInputExpectation
	Responses []BargeInResponseExpectation
	Tools     []BargeInToolExpectation

	RequireSessionTerminal bool
}

type bargeInInputState struct {
	id           string
	turnID       string
	appendGroups map[string]struct{}
	appendBytes  int
	nonEmpty     bool
	firstAppend  int
	commit       int
	userTurn     int
}

type bargeInResponseState struct {
	id                  string
	inputID             string
	turnID              string
	created             int
	terminal            int
	disposition         BargeInDisposition
	cancel              int
	cancelInputID       string
	outputCount         int
	firstOutput         int
	outputAfterCancel   int
	outputAfterTerminal int
	continuation        int
}

type bargeInToolState struct {
	id          string
	responseID  string
	turnID      string
	call        int
	result      int
	disposition BargeInDisposition
}

type bargeInSessionState struct {
	sequence    int
	disposition BargeInDisposition
	clean       bool
}

// BargeInEventSummary is the safe sequence representation included in
// timeout and validation diagnostics. It intentionally excludes Bytes, raw
// reasons, and all provider payload data.
type BargeInEventSummary struct {
	Sequence    int
	Kind        BargeInEventKind
	InputID     string
	TurnID      string
	ResponseID  string
	ToolCallID  string
	Disposition BargeInDisposition
}

func (s BargeInEventSummary) String() string {
	parts := []string{fmt.Sprintf("%d:%s", s.Sequence, s.Kind)}
	if s.InputID != "" {
		parts = append(parts, "input="+s.InputID)
	}
	if s.TurnID != "" {
		parts = append(parts, "turn="+s.TurnID)
	}
	if s.ResponseID != "" {
		parts = append(parts, "response="+s.ResponseID)
	}
	if s.ToolCallID != "" {
		parts = append(parts, "tool="+s.ToolCallID)
	}
	if s.Disposition != "" {
		parts = append(parts, "disposition="+string(s.Disposition))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// BargeInViolation is one focused contract failure. Sequence is zero for a
// contract-level failure that has no corresponding event.
type BargeInViolation struct {
	Sequence int
	Boundary string
	Detail   string
}

func (v BargeInViolation) String() string {
	if v.Sequence > 0 {
		return fmt.Sprintf("%s at sequence %d: %s", v.Boundary, v.Sequence, v.Detail)
	}
	return fmt.Sprintf("%s: %s", v.Boundary, v.Detail)
}

// BargeInValidationReport is a safe, deterministic view of one oracle run.
type BargeInValidationReport struct {
	Valid      bool
	Violations []BargeInViolation
	Observed   []BargeInEventSummary
	Unresolved []string
}

// BargeInValidationError preserves structured failures while making the
// expected boundary, safe observed sequence, and unresolved identities
// visible in ordinary test output.
type BargeInValidationError struct {
	Report BargeInValidationReport
}

func (e *BargeInValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	violations := make([]string, 0, len(e.Report.Violations))
	for _, violation := range e.Report.Violations {
		violations = append(violations, violation.String())
	}
	observed := make([]string, 0, len(e.Report.Observed))
	for _, event := range e.Report.Observed {
		observed = append(observed, event.String())
	}
	return fmt.Sprintf("barge-in oracle validation failed: violations=[%s]; observed=[%s]; unresolved=%v",
		strings.Join(violations, "; "), strings.Join(observed, ","), e.Report.Unresolved)
}

// BargeInLedger correlates normalized events by identity and sequence. It is
// safe for an observer goroutine and a test coordinator to use concurrently.
type BargeInLedger struct {
	mu sync.Mutex

	events     []BargeInEventSummary
	inputs     map[string]*bargeInInputState
	responses  map[string]*bargeInResponseState
	tools      map[string]*bargeInToolState
	session    *bargeInSessionState
	violations []BargeInViolation
	lastSeq    int
}

// NewBargeInLedger returns an empty identity-aware ledger.
func NewBargeInLedger() *BargeInLedger {
	return &BargeInLedger{
		inputs:    make(map[string]*bargeInInputState),
		responses: make(map[string]*bargeInResponseState),
		tools:     make(map[string]*bargeInToolState),
	}
}

// Observe records one normalized event. Invalid events are retained as safe
// summaries so a later validation error can explain the observed boundary;
// callers should call Validate after the stream has drained.
func (l *BargeInLedger) Observe(event BargeInEvent) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observeLocked(event)
}

func (l *BargeInLedger) observeLocked(event BargeInEvent) {
	l.events = append(l.events, BargeInEventSummary{
		Sequence: event.Sequence, Kind: event.Kind, InputID: event.InputID,
		TurnID: event.TurnID, ResponseID: event.ResponseID,
		ToolCallID: event.ToolCallID, Disposition: event.Disposition,
	})
	if event.Sequence <= 0 {
		l.violate(event.Sequence, string(event.Kind), "event sequence must be positive")
	} else if event.Sequence <= l.lastSeq {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("event sequence %d is not after %d", event.Sequence, l.lastSeq))
	} else {
		l.lastSeq = event.Sequence
	}
	if l.session != nil {
		l.violate(event.Sequence, string(event.Kind), "event occurred after session terminal observation")
		return
	}

	switch event.Kind {
	case BargeInEventInputAppend:
		l.observeInputAppend(event)
	case BargeInEventInputCommit:
		l.observeInputCommit(event)
	case BargeInEventUserTurn:
		l.observeUserTurn(event)
	case BargeInEventResponseCreated:
		l.observeResponseCreated(event)
	case BargeInEventResponseOutput:
		l.observeResponseOutput(event)
	case BargeInEventResponseCancel:
		l.observeResponseCancel(event)
	case BargeInEventResponseTerminal:
		l.observeResponseTerminal(event)
	case BargeInEventToolCall:
		l.observeToolCall(event)
	case BargeInEventToolResult:
		l.observeToolResult(event)
	case BargeInEventContinuation:
		l.observeContinuation(event)
	case BargeInEventSessionTerminal:
		l.observeSessionTerminal(event)
	default:
		l.violate(event.Sequence, "event", fmt.Sprintf("unknown event kind %q", event.Kind))
	}
}

func (l *BargeInLedger) observeInputAppend(event BargeInEvent) {
	if event.InputID == "" {
		l.violate(event.Sequence, string(event.Kind), "input identity is required")
		return
	}
	if event.Bytes < 0 {
		l.violate(event.Sequence, string(event.Kind), "input byte count must not be negative")
	}
	state := l.inputs[event.InputID]
	if state == nil {
		state = &bargeInInputState{id: event.InputID, turnID: event.TurnID, appendGroups: make(map[string]struct{})}
		l.inputs[event.InputID] = state
	} else if event.TurnID != "" && state.turnID != "" && event.TurnID != state.turnID {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("input %q changed turn identity", event.InputID))
	} else if state.turnID == "" {
		state.turnID = event.TurnID
	}
	if event.TurnID == "" {
		l.violate(event.Sequence, string(event.Kind), "turn identity is required")
	}
	if state.commit > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("input %q appended after commit", event.InputID))
	}
	groupID := event.AppendGroupID
	if groupID == "" {
		groupID = event.InputID
	}
	state.appendGroups[groupID] = struct{}{}
	state.appendBytes += event.Bytes
	state.nonEmpty = state.nonEmpty || event.NonEmpty || event.Bytes > 0
	if state.firstAppend == 0 {
		state.firstAppend = event.Sequence
	}
}

func (l *BargeInLedger) observeInputCommit(event BargeInEvent) {
	state := l.inputs[event.InputID]
	if event.InputID == "" || state == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("commit references unknown input %q", event.InputID))
		return
	}
	if state.commit > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("input %q was committed more than once", event.InputID))
		return
	}
	if event.TurnID == "" || event.TurnID != state.turnID {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("commit for input %q has wrong turn identity", event.InputID))
	}
	if !state.nonEmpty {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("input %q committed without non-empty audio", event.InputID))
	}
	state.commit = event.Sequence
}

func (l *BargeInLedger) observeUserTurn(event BargeInEvent) {
	state := l.inputs[event.InputID]
	if event.InputID == "" || state == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("user turn references unknown input %q", event.InputID))
		return
	}
	if state.commit == 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("user turn for input %q precedes commit", event.InputID))
	}
	if state.userTurn > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("input %q has duplicate user-turn representation", event.InputID))
		return
	}
	if event.TurnID == "" || event.TurnID != state.turnID {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("user turn for input %q has wrong turn identity", event.InputID))
	}
	state.userTurn = event.Sequence
}

func (l *BargeInLedger) observeResponseCreated(event BargeInEvent) {
	if event.ResponseID == "" {
		l.violate(event.Sequence, string(event.Kind), "response identity is required")
		return
	}
	if _, exists := l.responses[event.ResponseID]; exists {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q was created more than once", event.ResponseID))
		return
	}
	state := &bargeInResponseState{id: event.ResponseID, inputID: event.InputID, turnID: event.TurnID, created: event.Sequence}
	l.responses[event.ResponseID] = state
	input := l.inputs[event.InputID]
	if event.InputID == "" || input == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q references unknown input %q", event.ResponseID, event.InputID))
		return
	}
	if input.commit == 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q was created before input %q committed", event.ResponseID, event.InputID))
	}
	if event.TurnID == "" || event.TurnID != input.turnID {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q has wrong owner turn", event.ResponseID))
	}
}

func (l *BargeInLedger) observeResponseOutput(event BargeInEvent) {
	state := l.responses[event.ResponseID]
	if event.ResponseID == "" || state == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("output references unknown response %q", event.ResponseID))
		return
	}
	if event.NonEmpty || event.Bytes > 0 {
		state.outputCount++
		if state.firstOutput == 0 {
			state.firstOutput = event.Sequence
		}
	}
	if state.cancel > 0 {
		state.outputAfterCancel++
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q emitted stale output after cancellation", event.ResponseID))
	}
	if state.terminal > 0 {
		state.outputAfterTerminal++
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q emitted stale output after terminality", event.ResponseID))
	}
	if event.Bytes < 0 {
		l.violate(event.Sequence, string(event.Kind), "output byte count must not be negative")
	}
}

func (l *BargeInLedger) observeResponseCancel(event BargeInEvent) {
	state := l.responses[event.ResponseID]
	if event.ResponseID == "" || state == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("cancellation references unknown response %q", event.ResponseID))
		return
	}
	if event.InputID == "" {
		l.violate(event.Sequence, string(event.Kind), "cancellation must identify interrupting input")
	}
	if state.terminal > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q was cancelled after terminality", event.ResponseID))
		return
	}
	if state.cancel > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q received duplicate cancellation", event.ResponseID))
		return
	}
	state.cancel = event.Sequence
	state.cancelInputID = event.InputID
}

func (l *BargeInLedger) observeResponseTerminal(event BargeInEvent) {
	state := l.responses[event.ResponseID]
	if event.ResponseID == "" || state == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("terminal references unknown response %q", event.ResponseID))
		return
	}
	if state.terminal > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q received duplicate terminal disposition", event.ResponseID))
		return
	}
	if !validResponseDisposition(event.Disposition) {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q has undocumented terminal disposition", event.ResponseID))
	}
	if event.Disposition == BargeInDispositionFailed && strings.TrimSpace(event.Reason) == "" {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("failed response %q has no reason", event.ResponseID))
	}
	if state.cancel > 0 && event.Disposition != BargeInDispositionCancelled {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("cancelled response %q ended as %q", event.ResponseID, event.Disposition))
	}
	if state.cancel == 0 && event.Disposition == BargeInDispositionCancelled {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q was marked cancelled without a cancellation event", event.ResponseID))
	}
	state.terminal = event.Sequence
	state.disposition = event.Disposition
}

func (l *BargeInLedger) observeToolCall(event BargeInEvent) {
	if event.ToolCallID == "" {
		l.violate(event.Sequence, string(event.Kind), "tool-call identity is required")
		return
	}
	if _, exists := l.tools[event.ToolCallID]; exists {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool call %q was issued more than once", event.ToolCallID))
		return
	}
	state := &bargeInToolState{id: event.ToolCallID, responseID: event.ResponseID, turnID: event.TurnID, call: event.Sequence}
	l.tools[event.ToolCallID] = state
	response := l.responses[event.ResponseID]
	if event.ResponseID == "" || response == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool call %q references unknown response %q", event.ToolCallID, event.ResponseID))
		return
	}
	if response.terminal > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool call %q was issued after response terminality", event.ToolCallID))
	}
	if event.TurnID == "" || event.TurnID != response.turnID {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool call %q has wrong owner turn", event.ToolCallID))
	}
}

func (l *BargeInLedger) observeToolResult(event BargeInEvent) {
	state := l.tools[event.ToolCallID]
	if event.ToolCallID == "" || state == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool result references unknown call %q", event.ToolCallID))
		return
	}
	if state.result > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool call %q received duplicate result disposition", event.ToolCallID))
		return
	}
	if event.ResponseID != "" && event.ResponseID != state.responseID {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool result for %q has wrong response identity", event.ToolCallID))
	}
	if event.TurnID == "" || event.TurnID != state.turnID {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool result for %q has wrong turn identity", event.ToolCallID))
	}
	if !validToolDisposition(event.Disposition) {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool call %q has undocumented result disposition", event.ToolCallID))
	}
	if event.Disposition != BargeInDispositionDelivered && strings.TrimSpace(event.Reason) == "" {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("tool result for %q has no rejection or cancellation reason", event.ToolCallID))
	}
	state.result = event.Sequence
	state.disposition = event.Disposition
}

func (l *BargeInLedger) observeContinuation(event BargeInEvent) {
	state := l.responses[event.ResponseID]
	input := l.inputs[event.InputID]
	if event.ResponseID == "" || state == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("continuation references unknown response %q", event.ResponseID))
		return
	}
	if event.InputID == "" || input == nil {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("continuation references unknown input %q", event.InputID))
		return
	}
	if state.inputID != event.InputID || state.turnID != input.turnID {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("continuation response %q is attributed to the wrong input", event.ResponseID))
	}
	if state.continuation > 0 {
		l.violate(event.Sequence, string(event.Kind), fmt.Sprintf("response %q has duplicate continuation identity", event.ResponseID))
		return
	}
	state.continuation = event.Sequence
}

func (l *BargeInLedger) observeSessionTerminal(event BargeInEvent) {
	if l.session != nil {
		l.violate(event.Sequence, string(event.Kind), "duplicate session terminal observation")
		return
	}
	if !validSessionDisposition(event.Disposition) {
		l.violate(event.Sequence, string(event.Kind), "undocumented session terminal disposition")
	}
	if event.Disposition == BargeInDispositionClean && !event.Clean {
		// Clean is an explicit terminal assertion, kept separate from payload
		// fields so customer data never enters proof records.
		l.violate(event.Sequence, string(event.Kind), "clean terminal observation did not assert clean success")
	}
	if event.Clean && event.Disposition != BargeInDispositionClean {
		l.violate(event.Sequence, string(event.Kind), "terminal observation asserted clean success with a non-clean disposition")
	}
	l.session = &bargeInSessionState{sequence: event.Sequence, disposition: event.Disposition, clean: event.Clean}
}

func (l *BargeInLedger) violate(sequence int, boundary, detail string) {
	l.violations = append(l.violations, BargeInViolation{Sequence: sequence, Boundary: boundary, Detail: detail})
}

// Check evaluates the ledger without mutating it. Repeated checks therefore
// produce the same report and are safe for timeout diagnostics.
func (l *BargeInLedger) Check(contract BargeInContract) BargeInValidationReport {
	if l == nil {
		return BargeInValidationReport{
			Violations: []BargeInViolation{{Boundary: "ledger", Detail: "ledger is nil"}},
			Unresolved: []string{"ledger"},
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	check := newBargeInCheck(l)
	check.inputs(contract.Inputs)
	check.responses(contract.Responses)
	check.tools(contract.Tools)
	check.sessionTerminal(contract.RequireSessionTerminal)
	observed := append([]BargeInEventSummary(nil), l.events...)
	return BargeInValidationReport{
		Valid:      len(check.violations) == 0,
		Violations: check.violations,
		Observed:   observed,
		Unresolved: check.unresolved,
	}
}

// bargeInCheck accumulates one Check evaluation. The ledger lock is held by
// the caller for the lifetime of the value; the ledger itself is not mutated.
type bargeInCheck struct {
	ledger        *BargeInLedger
	violations    []BargeInViolation
	unresolved    []string
	unresolvedSet map[string]struct{}
}

func newBargeInCheck(l *BargeInLedger) *bargeInCheck {
	unresolved := l.unresolvedLocked()
	unresolvedSet := make(map[string]struct{}, len(unresolved))
	for _, identity := range unresolved {
		unresolvedSet[identity] = struct{}{}
	}
	return &bargeInCheck{
		ledger:        l,
		violations:    append([]BargeInViolation(nil), l.violations...),
		unresolved:    unresolved,
		unresolvedSet: unresolvedSet,
	}
}

func (c *bargeInCheck) violate(boundary, detail string) {
	c.violations = append(c.violations, BargeInViolation{Boundary: boundary, Detail: detail})
}

func (c *bargeInCheck) addUnresolved(identity string) {
	if _, exists := c.unresolvedSet[identity]; exists {
		return
	}
	c.unresolvedSet[identity] = struct{}{}
	c.unresolved = append(c.unresolved, identity)
}

// reportUnexpected flags observed identities absent from a non-empty contract.
func reportUnexpected[S any](c *bargeInCheck, observed map[string]S, expected map[string]struct{}, boundary, label string) {
	if len(expected) == 0 {
		return
	}
	for id := range observed {
		if _, ok := expected[id]; !ok {
			c.violate(boundary, fmt.Sprintf("unexpected %s identity %q", label, id))
		}
	}
}

// expectIdentity records one contract identity and reports duplicates.
func (c *bargeInCheck) expectIdentity(expected map[string]struct{}, id, boundary, label string) {
	if _, exists := expected[id]; exists {
		c.violate(boundary, fmt.Sprintf("%s %q is expected more than once", label, id))
	}
	expected[id] = struct{}{}
}

func (c *bargeInCheck) inputs(expectations []BargeInInputExpectation) {
	expected := make(map[string]struct{}, len(expectations))
	for _, expectation := range expectations {
		if expectation.ID == "" || expectation.TurnID == "" {
			c.violate("contract.inputs", "expected input identity and turn identity are required")
			continue
		}
		c.expectIdentity(expected, expectation.ID, "contract.inputs", "input")
		c.input(expectation)
	}
	reportUnexpected(c, c.ledger.inputs, expected, "input", "input")
}

func (c *bargeInCheck) input(expected BargeInInputExpectation) {
	state := c.ledger.inputs[expected.ID]
	if state == nil {
		c.violate("contract.inputs", fmt.Sprintf("missing input %q", expected.ID))
		c.addUnresolved("input:" + expected.ID + ":missing")
		return
	}
	if state.turnID != expected.TurnID {
		c.violate("contract.inputs", fmt.Sprintf("input %q has wrong turn identity", expected.ID))
	}
	if len(state.appendGroups) != 1 {
		c.violate("input.append", fmt.Sprintf("input %q has %d append groups, want exactly one", expected.ID, len(state.appendGroups)))
	}
	if !state.nonEmpty {
		c.violate("input.append", fmt.Sprintf("input %q has no non-empty append", expected.ID))
	}
	if state.commit == 0 {
		c.violate("input.commit", fmt.Sprintf("input %q has no commit", expected.ID))
	}
	if state.userTurn == 0 {
		c.violate("user.turn", fmt.Sprintf("input %q has no user-turn representation", expected.ID))
	}
}

func (c *bargeInCheck) responses(expectations []BargeInResponseExpectation) {
	expected := make(map[string]struct{}, len(expectations))
	for _, expectation := range expectations {
		if expectation.ID == "" || expectation.InputID == "" || expectation.TurnID == "" {
			c.violate("contract.responses", "expected response identity, input identity, and turn identity are required")
			continue
		}
		c.expectIdentity(expected, expectation.ID, "contract.responses", "response")
		c.response(expectation)
	}
	reportUnexpected(c, c.ledger.responses, expected, "response", "response")
}

func (c *bargeInCheck) response(expected BargeInResponseExpectation) {
	state := c.ledger.responses[expected.ID]
	if state == nil {
		c.violate("response.created", fmt.Sprintf("missing response %q", expected.ID))
		c.addUnresolved("response:" + expected.ID + ":missing")
		return
	}
	if state.inputID != expected.InputID || state.turnID != expected.TurnID {
		c.violate("response.created", fmt.Sprintf("response %q has wrong owner identity", expected.ID))
	}
	if state.terminal == 0 {
		c.violate("response.terminal", fmt.Sprintf("response %q has unresolved terminal disposition", expected.ID))
	} else if expected.Disposition != "" && state.disposition != expected.Disposition {
		c.violate("response.terminal", fmt.Sprintf("response %q disposition is %q, want %q", expected.ID, state.disposition, expected.Disposition))
	}
	c.responseCancellation(expected, state)
	c.responseOutput(expected, state)
}

func (c *bargeInCheck) responseCancellation(expected BargeInResponseExpectation, state *bargeInResponseState) {
	if state.cancel > 0 {
		interruptingInput := c.ledger.inputs[state.cancelInputID]
		if interruptingInput == nil || !interruptingInput.nonEmpty {
			c.violate("response.cancel", fmt.Sprintf("response %q references unknown or empty interrupting input %q", expected.ID, state.cancelInputID))
		} else if state.cancelInputID == state.inputID {
			c.violate("response.cancel", fmt.Sprintf("response %q cancellation does not identify a distinct interrupting input", expected.ID))
		}
	}
	if expected.RequireCancel && state.cancel == 0 {
		c.violate("response.cancel", fmt.Sprintf("response %q is missing required cancellation", expected.ID))
	}
	if expected.ForbidCancel && state.cancel != 0 {
		c.violate("response.cancel", fmt.Sprintf("response %q was cancelled although completion had precedence", expected.ID))
	}
}

func (c *bargeInCheck) responseOutput(expected BargeInResponseExpectation, state *bargeInResponseState) {
	if expected.RequireOutput && state.outputCount == 0 {
		c.violate("response.output", fmt.Sprintf("response %q has no non-empty output before interruption", expected.ID))
	}
	if expected.ForbidOutput && state.outputCount != 0 {
		c.violate("response.output", fmt.Sprintf("response %q emitted %d non-empty output events although output is forbidden", expected.ID, state.outputCount))
	}
	if expected.RequireContinuation && state.continuation == 0 {
		c.violate("continuation", fmt.Sprintf("response %q has no continuation identity", expected.ID))
	}
}

func (c *bargeInCheck) tools(expectations []BargeInToolExpectation) {
	expected := make(map[string]struct{}, len(expectations))
	for _, expectation := range expectations {
		if expectation.ID == "" || expectation.ResponseID == "" || expectation.TurnID == "" {
			c.violate("contract.tools", "expected tool identity, response identity, and turn identity are required")
			continue
		}
		c.expectIdentity(expected, expectation.ID, "contract.tools", "tool call")
		c.tool(expectation)
	}
	reportUnexpected(c, c.ledger.tools, expected, "tool.call", "tool-call")
}

func (c *bargeInCheck) tool(expected BargeInToolExpectation) {
	state := c.ledger.tools[expected.ID]
	if state == nil {
		c.violate("tool.call", fmt.Sprintf("missing tool call %q", expected.ID))
		c.addUnresolved("tool:" + expected.ID + ":missing")
		return
	}
	if state.responseID != expected.ResponseID || state.turnID != expected.TurnID {
		c.violate("tool.call", fmt.Sprintf("tool call %q has wrong owner identity", expected.ID))
	}
	if state.result == 0 {
		c.violate("tool.result", fmt.Sprintf("tool call %q has unresolved result disposition", expected.ID))
	} else if expected.Disposition != "" && state.disposition != expected.Disposition {
		c.violate("tool.result", fmt.Sprintf("tool call %q disposition is %q, want %q", expected.ID, state.disposition, expected.Disposition))
	}
	if expected.ForbidResultAfterCancel {
		response := c.ledger.responses[state.responseID]
		if response != nil && response.cancel > 0 && state.result > response.cancel && state.disposition == BargeInDispositionDelivered {
			c.violate("tool.result", fmt.Sprintf("tool result for %q was delivered after response cancellation", expected.ID))
		}
	}
}

func (c *bargeInCheck) sessionTerminal(required bool) {
	session := c.ledger.session
	if required && session == nil {
		c.violate("session.terminal", "missing terminal observation")
	}
	if session != nil && session.clean {
		c.cleanSuccessUnresolved()
	}
	if !required && session == nil {
		c.unresolved = removeUnresolved(c.unresolved, "session:terminal")
	}
	sort.Strings(c.unresolved)
	if len(c.unresolved) > 0 && session != nil {
		c.violate("session.terminal", "terminal observation arrived with unresolved ledger identities")
	}
}

func (c *bargeInCheck) cleanSuccessUnresolved() {
	for _, response := range c.ledger.responses {
		if response.terminal == 0 {
			c.violate("session.terminal", fmt.Sprintf("clean success has unresolved response %q", response.id))
		}
	}
	for _, tool := range c.ledger.tools {
		if tool.result == 0 {
			c.violate("session.terminal", fmt.Sprintf("clean success has unresolved tool call %q", tool.id))
		}
	}
}

func removeUnresolved(unresolved []string, target string) []string {
	filtered := unresolved[:0]
	for _, identity := range unresolved {
		if identity != target {
			filtered = append(filtered, identity)
		}
	}
	return filtered
}

// Validate returns nil only when every observed and expected identity has one
// documented disposition and the session terminal contract is satisfied.
func (l *BargeInLedger) Validate(contract BargeInContract) error {
	report := l.Check(contract)
	if report.Valid {
		return nil
	}
	return &BargeInValidationError{Report: report}
}

// ObservedSequence returns safe event summaries in the order observed.
func (l *BargeInLedger) ObservedSequence() []BargeInEventSummary {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]BargeInEventSummary(nil), l.events...)
}

// UnresolvedIdentities returns deterministic identity labels for work that has
// not reached its required boundary. It is intended for timeout diagnostics.
func (l *BargeInLedger) UnresolvedIdentities() []string {
	if l == nil {
		return []string{"ledger"}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unresolvedLocked()
}

func (l *BargeInLedger) unresolvedLocked() []string {
	var unresolved []string
	for id, input := range l.inputs {
		if input.commit == 0 {
			unresolved = append(unresolved, "input:"+id+":commit")
		}
		if input.userTurn == 0 {
			unresolved = append(unresolved, "input:"+id+":user-turn")
		}
	}
	for id, response := range l.responses {
		if response.terminal == 0 {
			unresolved = append(unresolved, "response:"+id+":terminal")
		}
	}
	for id, tool := range l.tools {
		if tool.result == 0 {
			unresolved = append(unresolved, "tool:"+id+":result")
		}
	}
	if l.session == nil {
		unresolved = append(unresolved, "session:terminal")
	}
	sort.Strings(unresolved)
	return unresolved
}

var ErrBargeInWait = errors.New("barge-in wait failed")

// BargeInWaitError is returned by WaitFor when a named event gate is cancelled
// or times out. The error is deliberately diagnostic before the integration
// package's outer deadline fires.
type BargeInWaitError struct {
	Boundary   string
	Timeout    time.Duration
	Cause      error
	Observed   []BargeInEventSummary
	Unresolved []string
}

func (e *BargeInWaitError) Error() string {
	if e == nil {
		return "<nil>"
	}
	observed := make([]string, 0, len(e.Observed))
	for _, event := range e.Observed {
		observed = append(observed, event.String())
	}
	verb := "cancelled"
	if errors.Is(e.Cause, context.DeadlineExceeded) {
		verb = "timed out"
	}
	return fmt.Sprintf("barge-in wait %q %s after %s: observed=[%s]; unresolved=%v",
		e.Boundary, verb, e.Timeout, strings.Join(observed, ","), e.Unresolved)
}

func (e *BargeInWaitError) Unwrap() error {
	if e == nil {
		return ErrBargeInWait
	}
	return errors.Join(ErrBargeInWait, e.Cause)
}

// WaitFor waits for one event gate with an explicit bounded timeout. A closed
// or nil signal is handled without polling or sleeping; the caller's context
// remains the shared cancellation path for command, stream, and fixture work.
func (l *BargeInLedger) WaitFor(ctx context.Context, boundary string, signal <-chan struct{}, timeout time.Duration) error {
	if strings.TrimSpace(boundary) == "" {
		return fmt.Errorf("%w: wait boundary is required", ErrBargeInWait)
	}
	if timeout <= 0 {
		return fmt.Errorf("%w: wait %q requires a positive timeout", ErrBargeInWait, boundary)
	}
	if ctx == nil {
		return fmt.Errorf("%w: wait %q requires a context", ErrBargeInWait, boundary)
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-signal:
		return nil
	case <-waitCtx.Done():
		// Prefer a signal that became ready at the same boundary as the
		// deadline; this avoids turning a successfully released gate into a
		// flaky timeout.
		select {
		case <-signal:
			return nil
		default:
		}
		return l.waitError(boundary, timeout, waitCtx.Err())
	}
}

func (l *BargeInLedger) waitError(boundary string, timeout time.Duration, cause error) error {
	return &BargeInWaitError{
		Boundary:   boundary,
		Timeout:    timeout,
		Cause:      cause,
		Observed:   l.ObservedSequence(),
		Unresolved: l.UnresolvedIdentities(),
	}
}

func validResponseDisposition(disposition BargeInDisposition) bool {
	switch disposition {
	case BargeInDispositionCompleted, BargeInDispositionCancelled, BargeInDispositionFailed:
		return true
	default:
		return false
	}
}

func validToolDisposition(disposition BargeInDisposition) bool {
	switch disposition {
	case BargeInDispositionDelivered, BargeInDispositionRejected, BargeInDispositionCancelled, BargeInDispositionFailed:
		return true
	default:
		return false
	}
}

func validSessionDisposition(disposition BargeInDisposition) bool {
	switch disposition {
	case BargeInDispositionClean, BargeInDispositionFailed, BargeInDispositionCancelled:
		return true
	default:
		return false
	}
}
