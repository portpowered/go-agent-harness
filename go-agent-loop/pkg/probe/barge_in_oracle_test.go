package probe

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func validBargeInEvents() ([]BargeInEvent, BargeInContract) {
	sequence := 0
	events := make([]BargeInEvent, 0, 32)
	add := func(event BargeInEvent) {
		sequence++
		event.Sequence = sequence
		events = append(events, event)
	}
	input := func(id, turn string) {
		add(BargeInEvent{Kind: BargeInEventInputAppend, InputID: id, TurnID: turn, AppendGroupID: id, Bytes: 4, NonEmpty: true})
		add(BargeInEvent{Kind: BargeInEventInputCommit, InputID: id, TurnID: turn})
		add(BargeInEvent{Kind: BargeInEventUserTurn, InputID: id, TurnID: turn})
	}
	input("i1", "t1")
	add(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r1", InputID: "i1", TurnID: "t1"})
	add(BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r1", Bytes: 4, NonEmpty: true})

	input("i2", "t2")
	add(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r1", InputID: "i2"})
	add(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCancelled})
	add(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r2", InputID: "i2", TurnID: "t2"})
	add(BargeInEvent{Kind: BargeInEventContinuation, ResponseID: "r2", InputID: "i2", TurnID: "t2"})
	add(BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r2", Bytes: 4, NonEmpty: true})
	add(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r2", Disposition: BargeInDispositionCompleted})

	input("i3", "t3")
	add(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r3", InputID: "i3", TurnID: "t3"})
	add(BargeInEvent{Kind: BargeInEventToolCall, ResponseID: "r3", TurnID: "t3", ToolCallID: "c1"})
	input("i4", "t4")
	add(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r3", InputID: "i4"})
	add(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r3", Disposition: BargeInDispositionCancelled})
	add(BargeInEvent{Kind: BargeInEventToolResult, ResponseID: "r3", TurnID: "t3", ToolCallID: "c1", Disposition: BargeInDispositionDelivered})
	add(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r4", InputID: "i4", TurnID: "t4"})
	add(BargeInEvent{Kind: BargeInEventContinuation, ResponseID: "r4", InputID: "i4", TurnID: "t4"})
	add(BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r4", Bytes: 4, NonEmpty: true})
	add(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r4", Disposition: BargeInDispositionCompleted})
	add(BargeInEvent{Kind: BargeInEventSessionTerminal, Disposition: BargeInDispositionClean, Clean: true})

	contract := BargeInContract{
		Inputs: []BargeInInputExpectation{
			{ID: "i1", TurnID: "t1"}, {ID: "i2", TurnID: "t2"},
			{ID: "i3", TurnID: "t3"}, {ID: "i4", TurnID: "t4"},
		},
		Responses: []BargeInResponseExpectation{
			{ID: "r1", InputID: "i1", TurnID: "t1", Disposition: BargeInDispositionCancelled, RequireCancel: true, RequireOutput: true},
			{ID: "r2", InputID: "i2", TurnID: "t2", Disposition: BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true, RequireContinuation: true},
			{ID: "r3", InputID: "i3", TurnID: "t3", Disposition: BargeInDispositionCancelled, RequireCancel: true},
			{ID: "r4", InputID: "i4", TurnID: "t4", Disposition: BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true, RequireContinuation: true},
		},
		Tools: []BargeInToolExpectation{
			{ID: "c1", ResponseID: "r3", TurnID: "t3", Disposition: BargeInDispositionDelivered},
		},
		RequireSessionTerminal: true,
	}
	return events, contract
}

func replayBargeInEvents(events []BargeInEvent) *BargeInLedger {
	ledger := NewBargeInLedger()
	for _, event := range events {
		ledger.Observe(event)
	}
	return ledger
}

func TestBargeInLedgerAcceptsIdentityAwareCollisionMatrix(t *testing.T) {
	events, contract := validBargeInEvents()
	ledger := replayBargeInEvents(events)
	report := ledger.Check(contract)
	if !report.Valid {
		t.Fatalf("valid collision matrix failed: %+v", report)
	}
	if len(report.Observed) != len(events) {
		t.Fatalf("observed events = %d, want %d", len(report.Observed), len(events))
	}
	if len(report.Unresolved) != 0 {
		t.Fatalf("unresolved identities = %v", report.Unresolved)
	}
	if err := ledger.Validate(contract); err != nil {
		t.Fatalf("valid collision matrix returned error: %v", err)
	}
}

func bargeInKindForResponse(kind BargeInEventKind, responseID string) func(BargeInEvent) bool {
	return func(event BargeInEvent) bool {
		return event.Kind == kind && event.ResponseID == responseID
	}
}

func bargeInKind(kind BargeInEventKind) func(BargeInEvent) bool {
	return func(event BargeInEvent) bool { return event.Kind == kind }
}

func firstBargeInEventIndex(events []BargeInEvent, match func(BargeInEvent) bool) int {
	for index, event := range events {
		if match(event) {
			return index
		}
	}
	return -1
}

func renumberBargeInEvents(events []BargeInEvent) []BargeInEvent {
	for index := range events {
		events[index].Sequence = index + 1
	}
	return events
}

// insertAfterFirstBargeInEvent inserts build(match) after the first matching
// event and renumbers the stream; a stream without a match is unchanged.
func insertAfterFirstBargeInEvent(events []BargeInEvent, match func(BargeInEvent) bool, build func(BargeInEvent) BargeInEvent) []BargeInEvent {
	index := firstBargeInEventIndex(events, match)
	if index < 0 {
		return events
	}
	return renumberBargeInEvents(slices.Insert(events, index+1, build(events[index])))
}

// removeFirstBargeInEvent drops the first matching event and renumbers.
func removeFirstBargeInEvent(events []BargeInEvent, match func(BargeInEvent) bool) []BargeInEvent {
	index := firstBargeInEventIndex(events, match)
	if index < 0 {
		return events
	}
	return renumberBargeInEvents(slices.Delete(events, index, index+1))
}

// updateFirstBargeInEvent edits the first matching event in place.
func updateFirstBargeInEvent(events []BargeInEvent, match func(BargeInEvent) bool, update func(*BargeInEvent)) []BargeInEvent {
	if index := firstBargeInEventIndex(events, match); index >= 0 {
		update(&events[index])
	}
	return events
}

func TestBargeInLedgerRejectsIdentityAndTerminalMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]BargeInEvent) []BargeInEvent
		want   string
	}{
		{
			name: "dropped input",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				for index := range events {
					if events[index].InputID == "i2" {
						events[index].InputID = "missing-i2"
					}
				}
				return events
			},
			want: `missing input "i2"`,
		},
		{
			name: "duplicate cancellation",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				return insertAfterFirstBargeInEvent(events, bargeInKindForResponse(BargeInEventResponseCancel, "r1"),
					func(event BargeInEvent) BargeInEvent { return event })
			},
			want: "duplicate cancellation",
		},
		{
			name: "stale output",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				return insertAfterFirstBargeInEvent(events, bargeInKindForResponse(BargeInEventResponseTerminal, "r1"),
					func(BargeInEvent) BargeInEvent {
						return BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r1", Bytes: 4, NonEmpty: true}
					})
			},
			want: "stale output after cancellation",
		},
		{
			name: "wrong response identity",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				return updateFirstBargeInEvent(events, bargeInKindForResponse(BargeInEventResponseTerminal, "r2"),
					func(event *BargeInEvent) { event.ResponseID = "r1" })
			},
			want: `response "r1" received duplicate terminal disposition`,
		},
		{
			name: "orphaned tool result",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				return updateFirstBargeInEvent(events, bargeInKind(BargeInEventToolResult),
					func(event *BargeInEvent) { event.ToolCallID = "orphan-c1" })
			},
			want: `tool result references unknown call "orphan-c1"`,
		},
		{
			name: "pending at close",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				return removeFirstBargeInEvent(events, bargeInKindForResponse(BargeInEventResponseTerminal, "r4"))
			},
			want: "terminal observation arrived with unresolved ledger identities",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			events, contract := validBargeInEvents()
			events = testCase.mutate(events)
			err := replayBargeInEvents(events).Validate(contract)
			if err == nil {
				t.Fatal("mutation unexpectedly passed")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestBargeInLedgerRejectsCleanUnresolvedAndUndocumentedResults(t *testing.T) {
	events, contract := validBargeInEvents()
	for index := range events {
		if events[index].Kind == BargeInEventToolResult {
			events[index].Disposition = BargeInDispositionRejected
			events[index].Reason = ""
			break
		}
	}
	err := replayBargeInEvents(events).Validate(contract)
	if err == nil || !strings.Contains(err.Error(), "no rejection or cancellation reason") {
		t.Fatalf("unreasoned tool rejection error = %v", err)
	}

	events, contract = validBargeInEvents()
	for index := range events {
		if events[index].Kind == BargeInEventSessionTerminal {
			events[index].Clean = false
			break
		}
	}
	err = replayBargeInEvents(events).Validate(contract)
	if err == nil || !strings.Contains(err.Error(), "clean terminal observation did not assert clean success") {
		t.Fatalf("clean-but-unresolved terminal error = %v", err)
	}
}

func TestBargeInLedgerRejectsConfiguredPostCancelToolDelivery(t *testing.T) {
	events, contract := validBargeInEvents()
	contract.Tools[0].ForbidResultAfterCancel = true
	err := replayBargeInEvents(events).Validate(contract)
	if err == nil || !strings.Contains(err.Error(), `tool result for "c1" was delivered after response cancellation`) {
		t.Fatalf("post-cancel tool delivery error = %v, want an explicit delivery-after-cancellation violation", err)
	}
}

func TestBargeInLedgerRejectsForbiddenTurnStartOutput(t *testing.T) {
	events, contract := validBargeInEvents()
	contract.Responses[0].RequireOutput = false
	contract.Responses[0].ForbidOutput = true
	err := replayBargeInEvents(events).Validate(contract)
	if err == nil || !strings.Contains(err.Error(), `response "r1" emitted 1 non-empty output events although output is forbidden`) {
		t.Fatalf("forbidden turn-start output error = %v", err)
	}
}

func TestBargeInLedgerWaitForReportsBoundedDiagnostics(t *testing.T) {
	ledger := NewBargeInLedger()
	ledger.Observe(BargeInEvent{Sequence: 1, Kind: BargeInEventInputAppend, InputID: "i-wait", TurnID: "t-wait", Bytes: 2, NonEmpty: true})
	start := time.Now()
	err := ledger.WaitFor(context.Background(), "response.created", nil, 20*time.Millisecond)
	if err == nil {
		t.Fatal("nil event gate unexpectedly passed")
	}
	var waitErr *BargeInWaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("error type = %T, want *BargeInWaitError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrBargeInWait) {
		t.Fatalf("wait error = %v, want deadline and wait classifications", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("wait took %s, want a bounded return", elapsed)
	}
	for _, want := range []string{"response.created", "1:input.append", "input:i-wait:commit", "session:terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("wait error = %v, want diagnostic %q", err, want)
		}
	}
}

func TestBargeInCoordinatorSharesCancellationAndJoinsWorkers(t *testing.T) {
	ledger := NewBargeInLedger()
	coordinator, err := NewBargeInCoordinator(context.Background(), time.Second, ledger)
	if err != nil {
		t.Fatal(err)
	}
	workerExited := make(chan struct{})
	coordinator.Go(func(ctx context.Context) {
		<-ctx.Done()
		close(workerExited)
	})
	if err := coordinator.StopAndWait("stream drain"); err != nil {
		t.Fatalf("stop and wait returned error: %v", err)
	}
	select {
	case <-workerExited:
	case <-time.After(time.Second):
		t.Fatal("context-aware worker did not observe coordinator cancellation")
	}
	if err := coordinator.WaitForWorkers("stream drain"); err != nil {
		t.Fatalf("joined workers returned error: %v", err)
	}
}

func TestBargeInCoordinatorRejectsUnboundedConfiguration(t *testing.T) {
	if _, err := NewBargeInCoordinator(context.Background(), 0, NewBargeInLedger()); !errors.Is(err, ErrBargeInWait) {
		t.Fatalf("zero timeout error = %v, want ErrBargeInWait", err)
	}
}

type bargeInTestStream struct {
	ledger   *BargeInLedger
	sequence int
}

func newBargeInTestStream() *bargeInTestStream {
	return &bargeInTestStream{ledger: NewBargeInLedger()}
}

func (s *bargeInTestStream) observe(event BargeInEvent) {
	s.sequence++
	event.Sequence = s.sequence
	s.ledger.Observe(event)
}

func (s *bargeInTestStream) input(id, turn string) {
	s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: id, TurnID: turn, AppendGroupID: id, Bytes: 4, NonEmpty: true})
	s.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: id, TurnID: turn})
	s.observe(BargeInEvent{Kind: BargeInEventUserTurn, InputID: id, TurnID: turn})
}

func (s *bargeInTestStream) response(id, inputID, turn string) {
	s.input(inputID, turn)
	s.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: id, InputID: inputID, TurnID: turn})
}

func requireBargeInViolation(t *testing.T, ledger *BargeInLedger, want string) {
	t.Helper()
	err := ledger.Validate(BargeInContract{})
	if err == nil {
		t.Fatal("malformed event unexpectedly passed validation")
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("validation error = %v, want %q", err, want)
	}
}

type bargeInMalformedCase struct {
	name string
	run  func(*bargeInTestStream)
	want string
}

func runBargeInMalformedCases(t *testing.T, cases []bargeInMalformedCase) {
	t.Helper()
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stream := newBargeInTestStream()
			testCase.run(stream)
			requireBargeInViolation(t, stream.ledger, testCase.want)
		})
	}
}

// TestBargeInLedgerRejectsMalformedSequencingAndInputEvidence covers malformed normalized sequencing, input append, commit, and user-turn evidence.
func TestBargeInLedgerRejectsMalformedSequencingAndInputEvidence(t *testing.T) {
	runBargeInMalformedCases(t, []bargeInMalformedCase{
		{name: "non-positive sequence", want: "event sequence must be positive", run: func(s *bargeInTestStream) {
			s.ledger.Observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Sequence: 0})
		}},
		{name: "non-monotonic sequence", want: "is not after", run: func(s *bargeInTestStream) {
			s.ledger.Observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Sequence: 2})
			s.ledger.Observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Sequence: 2})
		}},
		{name: "unknown event kind", want: "unknown event kind", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: "provider.unknown"})
		}},
		{name: "event after session terminal", want: "event occurred after session terminal observation", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventSessionTerminal, Disposition: BargeInDispositionFailed})
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1"})
		}},
		{name: "append missing identity", want: "input identity is required", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, TurnID: "t1", Bytes: 1})
		}},
		{name: "append negative bytes and missing turn", want: "input byte count must not be negative", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", Bytes: -1})
		}},
		{name: "append changes turn identity", want: "changed turn identity", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t2"})
		}},
		{name: "append learns initially missing turn", want: "turn identity is required", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", Bytes: 1})
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Bytes: 1})
		}},
		{name: "append after commit", want: "appended after commit", run: func(s *bargeInTestStream) {
			s.input("i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Bytes: 1})
		}},
		{name: "commit unknown input", want: "commit references unknown input", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: "missing", TurnID: "t1"})
		}},
		{name: "duplicate commit", want: "committed more than once", run: func(s *bargeInTestStream) {
			s.input("i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: "i1", TurnID: "t1"})
		}},
		{name: "commit wrong turn", want: "wrong turn identity", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Bytes: 1})
			s.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: "i1", TurnID: "t2"})
		}},
		{name: "commit empty input", want: "committed without non-empty audio", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: "i1", TurnID: "t1"})
		}},
		{name: "user turn unknown input", want: "user turn references unknown input", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventUserTurn, InputID: "missing", TurnID: "t1"})
		}},
		{name: "user turn before commit", want: "precedes commit", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Bytes: 1})
			s.observe(BargeInEvent{Kind: BargeInEventUserTurn, InputID: "i1", TurnID: "t1"})
		}},
		{name: "duplicate user turn", want: "duplicate user-turn representation", run: func(s *bargeInTestStream) {
			s.input("i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventUserTurn, InputID: "i1", TurnID: "t1"})
		}},
		{name: "user turn wrong turn", want: "user turn for input \"i1\" has wrong turn identity", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Bytes: 1})
			s.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: "i1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventUserTurn, InputID: "i1", TurnID: "t2"})
		}},
	})
}

// TestBargeInLedgerRejectsMalformedResponseEvidence covers malformed normalized response creation, output, cancellation, and terminal evidence.
func TestBargeInLedgerRejectsMalformedResponseEvidence(t *testing.T) {
	runBargeInMalformedCases(t, []bargeInMalformedCase{
		{name: "response missing identity", want: "response identity is required", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventResponseCreated, InputID: "i1", TurnID: "t1"})
		}},
		{name: "duplicate response", want: "created more than once", run: func(s *bargeInTestStream) {
			s.input("i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r1", InputID: "i1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r1", InputID: "i1", TurnID: "t1"})
		}},
		{name: "response unknown input", want: "references unknown input", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r1", InputID: "missing", TurnID: "t1"})
		}},
		{name: "response before input commit", want: "was created before input", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", Bytes: 1})
			s.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r1", InputID: "i1", TurnID: "t1"})
		}},
		{name: "response wrong owner turn", want: "wrong owner turn", run: func(s *bargeInTestStream) {
			s.input("i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r1", InputID: "i1", TurnID: "t2"})
		}},
		{name: "output unknown response", want: "output references unknown response", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "missing", Bytes: 1})
		}},
		{name: "empty and negative output", want: "output byte count must not be negative", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r1", Bytes: -1})
		}},
		{name: "stale output after cancel", want: "stale output after cancellation", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r1", InputID: "i2"})
			s.observe(BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r1", Bytes: 1})
		}},
		{name: "stale output after terminal", want: "stale output after terminality", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCompleted})
			s.observe(BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r1", Bytes: 1})
		}},
		{name: "cancel unknown response", want: "cancellation references unknown response", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "missing", InputID: "i2"})
		}},
		{name: "cancel missing input identity", want: "cancellation must identify interrupting input", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r1"})
		}},
		{name: "cancel after terminal", want: "cancelled after terminality", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCompleted})
			s.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r1", InputID: "i2"})
		}},
		{name: "duplicate cancel", want: "duplicate cancellation", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r1", InputID: "i2"})
			s.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r1", InputID: "i2"})
		}},
		{name: "terminal unknown response", want: "terminal references unknown response", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "missing", Disposition: BargeInDispositionCompleted})
		}},
		{name: "duplicate terminal", want: "duplicate terminal disposition", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCompleted})
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCompleted})
		}},
		{name: "undocumented response disposition", want: "undocumented terminal disposition", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: unknownLabel})
		}},
		{name: "failed response without reason", want: "has no reason", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionFailed})
		}},
		{name: "cancelled response ends completed", want: "ended as \"completed\"", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r1", InputID: "i2"})
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCompleted})
		}},
		{name: "cancelled disposition without cancel", want: "marked cancelled without a cancellation event", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCancelled})
		}},
	})
}

// TestBargeInLedgerRejectsMalformedToolContinuationAndSessionEvidence covers malformed normalized tool, continuation, and session terminal evidence.
func TestBargeInLedgerRejectsMalformedToolContinuationAndSessionEvidence(t *testing.T) {
	runBargeInMalformedCases(t, []bargeInMalformedCase{
		{name: "tool call missing identity", want: "tool-call identity is required", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ResponseID: "r1", TurnID: "t1"})
		}},
		{name: "duplicate tool call", want: "issued more than once", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
		}},
		{name: "tool call unknown response", want: "references unknown response", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "missing", TurnID: "t1"})
		}},
		{name: "tool call after response terminal", want: "issued after response terminality", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCompleted})
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
		}},
		{name: "tool call wrong owner turn", want: "has wrong owner turn", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t2"})
		}},
		{name: "tool result unknown call", want: "tool result references unknown call", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "missing", Disposition: BargeInDispositionDelivered})
		}},
		{name: "duplicate tool result", want: "duplicate result disposition", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1", Disposition: BargeInDispositionDelivered})
			s.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1", Disposition: BargeInDispositionDelivered})
		}},
		{name: "tool result wrong response", want: "wrong response identity", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "c1", ResponseID: "r2", TurnID: "t1", Disposition: BargeInDispositionDelivered})
		}},
		{name: "tool result wrong turn", want: "wrong turn identity", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "c1", ResponseID: "r1", TurnID: "t2", Disposition: BargeInDispositionDelivered})
		}},
		{name: "undocumented tool disposition", want: "undocumented result disposition", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1", Disposition: unknownLabel})
		}},
		{name: "rejected tool result without reason", want: "no rejection or cancellation reason", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1", Disposition: BargeInDispositionRejected})
		}},
		{name: "continuation unknown response", want: "continuation references unknown response", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventContinuation, ResponseID: "missing", InputID: "i1"})
		}},
		{name: "continuation unknown input", want: "continuation references unknown input", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventContinuation, ResponseID: "r1", InputID: "missing"})
		}},
		{name: "continuation wrong input identity", want: "attributed to the wrong input", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.input("i2", "t2")
			s.observe(BargeInEvent{Kind: BargeInEventContinuation, ResponseID: "r1", InputID: "i2", TurnID: "t2"})
		}},
		{name: "duplicate continuation", want: "duplicate continuation identity", run: func(s *bargeInTestStream) {
			s.response("r1", "i1", "t1")
			s.observe(BargeInEvent{Kind: BargeInEventContinuation, ResponseID: "r1", InputID: "i1", TurnID: "t1"})
			s.observe(BargeInEvent{Kind: BargeInEventContinuation, ResponseID: "r1", InputID: "i1", TurnID: "t1"})
		}},
		{name: "duplicate session terminal", want: "event occurred after session terminal observation", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventSessionTerminal, Disposition: BargeInDispositionFailed})
			s.observe(BargeInEvent{Kind: BargeInEventSessionTerminal, Disposition: BargeInDispositionFailed})
		}},
		{name: "undocumented session disposition", want: "undocumented session terminal disposition", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventSessionTerminal, Disposition: unknownLabel})
		}},
		{name: "clean disposition without clean assertion", want: "did not assert clean success", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventSessionTerminal, Disposition: BargeInDispositionClean})
		}},
		{name: "clean assertion with failed disposition", want: "asserted clean success with a non-clean disposition", run: func(s *bargeInTestStream) {
			s.observe(BargeInEvent{Kind: BargeInEventSessionTerminal, Disposition: BargeInDispositionFailed, Clean: true})
		}},
	})
}

func TestBargeInLedgerReportsContractBoundaryViolations(t *testing.T) {
	stream := newBargeInTestStream()
	stream.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", AppendGroupID: "g1", Bytes: 1, NonEmpty: true})
	stream.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i1", TurnID: "t1", AppendGroupID: "g2", Bytes: 1, NonEmpty: true})
	stream.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: "i1", TurnID: "t1"})
	stream.observe(BargeInEvent{Kind: BargeInEventUserTurn, InputID: "i1", TurnID: "t1"})
	stream.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i2", TurnID: "t2"})
	stream.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: "i2", TurnID: "t2"})
	stream.observe(BargeInEvent{Kind: BargeInEventUserTurn, InputID: "i2", TurnID: "t2"})
	stream.observe(BargeInEvent{Kind: BargeInEventInputAppend, InputID: "i-extra", TurnID: "t-extra", Bytes: 1, NonEmpty: true})
	stream.observe(BargeInEvent{Kind: BargeInEventInputCommit, InputID: "i-extra", TurnID: "t-extra"})
	stream.observe(BargeInEvent{Kind: BargeInEventUserTurn, InputID: "i-extra", TurnID: "t-extra"})

	stream.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r1", InputID: "i1", TurnID: "t1"})
	stream.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r1", InputID: "i1"})
	stream.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1"})
	stream.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "c1", ResponseID: "r1", TurnID: "t1", Disposition: BargeInDispositionDelivered})
	stream.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r1", Disposition: BargeInDispositionCompleted})

	stream.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r2", InputID: "i2", TurnID: "t2"})
	stream.observe(BargeInEvent{Kind: BargeInEventResponseCancel, ResponseID: "r2", InputID: "missing"})
	stream.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c2", ResponseID: "r2", TurnID: "t2"})

	stream.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r4", InputID: "i-extra", TurnID: "t-extra"})
	stream.observe(BargeInEvent{Kind: BargeInEventResponseCreated, ResponseID: "r-extra", InputID: "i-extra", TurnID: "t-extra"})
	stream.observe(BargeInEvent{Kind: BargeInEventToolCall, ToolCallID: "c-extra", ResponseID: "r4", TurnID: "t-extra"})
	stream.observe(BargeInEvent{Kind: BargeInEventToolResult, ToolCallID: "c-extra", ResponseID: "r4", TurnID: "t-extra", Disposition: BargeInDispositionDelivered})
	stream.observe(BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r4", Bytes: 1, NonEmpty: true})
	stream.observe(BargeInEvent{Kind: BargeInEventResponseTerminal, ResponseID: "r4", Disposition: BargeInDispositionCompleted})

	contract := BargeInContract{
		Inputs: []BargeInInputExpectation{
			{ID: "i1", TurnID: "wrong-turn"},
			{ID: "i1", TurnID: "wrong-turn"},
			{ID: "i2", TurnID: "t2"},
			{ID: "i3", TurnID: "t3"},
			{ID: "i3", TurnID: "t3"},
			{},
		},
		Responses: []BargeInResponseExpectation{
			{ID: "r1", InputID: "i2", TurnID: "t2", Disposition: BargeInDispositionCancelled, ForbidCancel: true, RequireOutput: true},
			{ID: "r1", InputID: "i2", TurnID: "t2"},
			{ID: "r2", InputID: "i2", TurnID: "t2", RequireOutput: true, RequireContinuation: true},
			{ID: "r4", InputID: "i-extra", TurnID: "t-extra", RequireCancel: true, ForbidOutput: true},
			{ID: "r3", InputID: "i3", TurnID: "t3"},
			{ID: "r3", InputID: "i3", TurnID: "t3"},
			{},
		},
		Tools: []BargeInToolExpectation{
			{ID: "c1", ResponseID: "r2", TurnID: "t2", Disposition: BargeInDispositionRejected, ForbidResultAfterCancel: true},
			{ID: "c2", ResponseID: "r2", TurnID: "t2", Disposition: BargeInDispositionDelivered},
			{ID: "c3", ResponseID: "r3", TurnID: "t3"},
			{ID: "c3", ResponseID: "r3", TurnID: "t3"},
			{},
		},
		RequireSessionTerminal: true,
	}

	err := stream.ledger.Validate(contract)
	if err == nil {
		t.Fatal("contract boundary mutations unexpectedly passed")
	}
	for _, want := range []string{
		`input "i1" is expected more than once`,
		`input "i1" has wrong turn identity`,
		`input "i1" has 2 append groups`,
		`input "i2" has no non-empty append`,
		`missing input "i3"`,
		`unexpected input identity "i-extra"`,
		`response "r1" is expected more than once`,
		`response "r1" has wrong owner identity`,
		`response "r1" disposition is`,
		`cancellation does not identify a distinct interrupting input`,
		`response "r1" was cancelled although completion had precedence`,
		`response "r1" has no non-empty output before interruption`,
		`response "r2" references unknown or empty interrupting input "missing"`,
		`response "r2" has unresolved terminal disposition`,
		`response "r2" has no continuation identity`,
		`response "r4" is missing required cancellation`,
		`response "r4" emitted 1 non-empty output events although output is forbidden`,
		`missing response "r3"`,
		`unexpected response identity "r-extra"`,
		`tool call "c1" has wrong owner identity`,
		`tool call "c1" disposition is`,
		`tool result for "c1" was delivered after response cancellation`,
		`tool call "c2" has unresolved result disposition`,
		`missing tool call "c3"`,
		`unexpected tool-call identity "c-extra"`,
		`missing terminal observation`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("validation error = %v, want %q", err, want)
		}
	}
}

func TestBargeInLedgerOptionalTerminalAndNilSafety(t *testing.T) {
	ledger := NewBargeInLedger()
	report := ledger.Check(BargeInContract{})
	if !report.Valid || len(report.Unresolved) != 0 {
		t.Fatalf("optional session terminal report = %+v, want valid with no unresolved identities", report)
	}

	var nilLedger *BargeInLedger
	if report := nilLedger.Check(BargeInContract{}); report.Valid || len(report.Unresolved) != 1 || report.Unresolved[0] != "ledger" {
		t.Fatalf("nil ledger report = %+v, want a ledger violation", report)
	}
	if got := nilLedger.ObservedSequence(); got != nil {
		t.Fatalf("nil ledger observed sequence = %v, want nil", got)
	}
	if got := nilLedger.UnresolvedIdentities(); len(got) != 1 || got[0] != "ledger" {
		t.Fatalf("nil ledger unresolved identities = %v, want [ledger]", got)
	}

	var nilValidationError *BargeInValidationError
	if got := nilValidationError.Error(); got != nilErrorText {
		t.Fatalf("nil validation error string = %q, want <nil>", got)
	}
	var nilWaitError *BargeInWaitError
	if got := nilWaitError.Error(); got != nilErrorText {
		t.Fatalf("nil wait error string = %q, want <nil>", got)
	}
	if !errors.Is(nilWaitError, ErrBargeInWait) {
		t.Fatal("nil wait error did not unwrap to ErrBargeInWait")
	}
}

func TestBargeInLedgerWaitForBoundaries(t *testing.T) {
	ledger := NewBargeInLedger()
	var missingContext context.Context
	ready := make(chan struct{})
	close(ready)
	if err := ledger.WaitFor(context.Background(), "ready", ready, time.Second); err != nil {
		t.Fatalf("ready event gate returned error: %v", err)
	}

	for _, testCase := range []struct {
		name string
		ctx  context.Context
		gate string
		time time.Duration
		want string
	}{
		{name: "missing boundary", ctx: context.Background(), gate: "", time: time.Second, want: "wait boundary is required"},
		{name: "non-positive timeout", ctx: context.Background(), gate: "ready", time: 0, want: "requires a positive timeout"},
		{name: "missing context", ctx: missingContext, gate: "ready", time: time.Second, want: "requires a context"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := ledger.WaitFor(testCase.ctx, testCase.gate, ready, testCase.time); err == nil || !errors.Is(err, ErrBargeInWait) || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("wait error = %v, want ErrBargeInWait containing %q", err, testCase.want)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err := ledger.WaitFor(cancelled, "cancelled", make(chan struct{}), time.Second)
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancelled wait error = %v, want bounded cancelled diagnostic", err)
	}
}

func TestBargeInCoordinatorCoversSignalAndBoundedTeardown(t *testing.T) {
	ledger := NewBargeInLedger()
	coordinator, err := NewBargeInCoordinator(context.Background(), time.Second, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.Context() == nil {
		t.Fatal("coordinator context is nil")
	}
	ready := make(chan struct{})
	close(ready)
	if err := coordinator.WaitFor("coordinator gate", ready); err != nil {
		t.Fatalf("coordinator signal gate returned error: %v", err)
	}
	if err := coordinator.WaitForWorkers("no workers"); err != nil {
		t.Fatalf("empty worker set returned error: %v", err)
	}

	release := make(chan struct{})
	workerCoordinator, err := NewBargeInCoordinator(context.Background(), 20*time.Millisecond, ledger)
	if err != nil {
		t.Fatal(err)
	}
	workerCoordinator.Go(func(context.Context) {
		<-release
	})
	if err := workerCoordinator.WaitForWorkers("blocked worker"); err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "blocked worker") {
		t.Fatalf("blocked worker wait error = %v, want bounded deadline diagnostic", err)
	}
	close(release)
	awaitCoordinatorWorkerAccounting(t, workerCoordinator)
	if err := workerCoordinator.WaitForWorkers("released worker"); err != nil {
		t.Fatalf("released worker join returned error: %v", err)
	}

	stopRelease := make(chan struct{})
	stopCoordinator, err := NewBargeInCoordinator(context.Background(), 20*time.Millisecond, ledger)
	if err != nil {
		t.Fatal(err)
	}
	stopCoordinator.Go(func(context.Context) {
		<-stopRelease
	})
	if err := stopCoordinator.StopAndWait("blocked stop"); err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "blocked stop") {
		t.Fatalf("blocked stop error = %v, want bounded deadline diagnostic", err)
	}
	close(stopRelease)
	awaitCoordinatorWorkerAccounting(t, stopCoordinator)
	if err := stopCoordinator.WaitForWorkers("released stop"); err != nil {
		t.Fatalf("released stop worker join returned error: %v", err)
	}

	expired, err := NewBargeInCoordinator(context.Background(), time.Nanosecond, ledger)
	if err != nil {
		t.Fatal(err)
	}
	<-expired.Context().Done()
	if err := expired.WaitFor("expired gate", make(chan struct{})); err == nil || !errors.Is(err, ErrBargeInWait) {
		t.Fatalf("expired coordinator wait error = %v, want ErrBargeInWait", err)
	}
}

func TestBargeInCoordinatorRejectsNilReceiverAndParent(t *testing.T) {
	var missingParent context.Context
	if _, err := NewBargeInCoordinator(missingParent, time.Second, NewBargeInLedger()); err == nil || !errors.Is(err, ErrBargeInWait) {
		t.Fatalf("nil parent error = %v, want ErrBargeInWait", err)
	}

	var coordinator *BargeInCoordinator
	if coordinator.Context() != nil {
		t.Fatal("nil coordinator returned a context")
	}
	coordinator.Go(nil)
	for _, err := range []error{
		coordinator.WaitFor("nil", make(chan struct{})),
		coordinator.WaitForWorkers("nil"),
		coordinator.StopAndWait("nil"),
	} {
		if err == nil || !errors.Is(err, ErrBargeInWait) {
			t.Fatalf("nil coordinator error = %v, want ErrBargeInWait", err)
		}
	}

	valid, err := NewBargeInCoordinator(context.Background(), time.Second, NewBargeInLedger())
	if err != nil {
		t.Fatal(err)
	}
	valid.Go(nil)
}
