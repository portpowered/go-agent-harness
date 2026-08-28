package probe

import (
	"context"
	"errors"
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

func TestBargeInLedgerRejectsIdentityAndTerminalMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]BargeInEvent) []BargeInEvent
		want   string
	}{
		{
			name: "dropped input",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				for index := len(events) - 1; index >= 0; index-- {
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
				for index, event := range events {
					if event.Kind == BargeInEventResponseCancel && event.ResponseID == "r1" {
						duplicate := event
						duplicate.Sequence++
						events = append(events[:index+1], append([]BargeInEvent{duplicate}, events[index+1:]...)...)
						for sequence := range events {
							events[sequence].Sequence = sequence + 1
						}
						return events
					}
				}
				return events
			},
			want: "duplicate cancellation",
		},
		{
			name: "stale output",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				for index, event := range events {
					if event.Kind == BargeInEventResponseTerminal && event.ResponseID == "r1" {
						stale := BargeInEvent{Kind: BargeInEventResponseOutput, ResponseID: "r1", Bytes: 4, NonEmpty: true}
						events = append(events[:index+1], append([]BargeInEvent{stale}, events[index+1:]...)...)
						for sequence := range events {
							events[sequence].Sequence = sequence + 1
						}
						return events
					}
				}
				return events
			},
			want: "stale output after cancellation",
		},
		{
			name: "wrong response identity",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				for index := range events {
					if events[index].Kind == BargeInEventResponseTerminal && events[index].ResponseID == "r2" {
						events[index].ResponseID = "r1"
						return events
					}
				}
				return events
			},
			want: `response "r1" received duplicate terminal disposition`,
		},
		{
			name: "orphaned tool result",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				for index := range events {
					if events[index].Kind == BargeInEventToolResult {
						events[index].ToolCallID = "orphan-c1"
						return events
					}
				}
				return events
			},
			want: `tool result references unknown call "orphan-c1"`,
		},
		{
			name: "pending at close",
			mutate: func(events []BargeInEvent) []BargeInEvent {
				for index := range events {
					if events[index].Kind == BargeInEventResponseTerminal && events[index].ResponseID == "r4" {
						copy(events[index:], events[index+1:])
						events = events[:len(events)-1]
						for sequence := range events {
							events[sequence].Sequence = sequence + 1
						}
						return events
					}
				}
				return events
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
