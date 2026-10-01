package sessionstate

import (
	"fmt"
	"testing"
)

func TestResponseStateTransitions(t *testing.T) {
	ops := map[string]func(*Response){
		"start":        func(r *Response) { r.Start("resp-new") },
		"cancel":       (*Response).Cancel,
		"clearCancel":  (*Response).ClearCancel,
		"end":          func(r *Response) { r.End(false) },
		"end ack":      func(r *Response) { r.End(true) },
		"start cancel": func(r *Response) { r.Start("resp-new"); r.Cancel() },
	}
	for _, test := range []struct {
		from ResponsePhase
		op   string
		want ResponsePhase
	}{
		{ResponseIdle, "start", ResponseInFlight},
		{ResponseCompleted, "start", ResponseInFlight},
		{ResponseInterrupted, "start", ResponseInFlight},
		{ResponseCancelling, "start", ResponseInFlight},
		{ResponseInFlight, "cancel", ResponseCancelling},
		{ResponseCancelling, "cancel", ResponseCancelling},
		// A cancel with nothing in flight suppresses late untagged output.
		{ResponseIdle, "cancel", ResponseInterrupted},
		// A completed response keeps its terminal accounting.
		{ResponseCompleted, "cancel", ResponseCompleted},
		{ResponseInterrupted, "cancel", ResponseInterrupted},
		{ResponseCancelling, "clearCancel", ResponseInFlight},
		{ResponseInterrupted, "clearCancel", ResponseIdle},
		{ResponseCompleted, "clearCancel", ResponseCompleted},
		{ResponseInFlight, "end", ResponseCompleted},
		{ResponseCancelling, "end", ResponseInterrupted},
		// An orphan end (no start) is a completed turn unless a cancel targets it.
		{ResponseIdle, "end", ResponseCompleted},
		{ResponseInterrupted, "end", ResponseInterrupted},
		// An acknowledgement never counts as the assistant turn.
		{ResponseInFlight, "end ack", ResponseIdle},
		{ResponseCancelling, "end ack", ResponseIdle},
		{ResponseIdle, "start cancel", ResponseCancelling},
	} {
		t.Run(fmt.Sprintf("%d %s", test.from, test.op), func(t *testing.T) {
			state := Response{Phase: test.from, ID: "resp-old", HasOutput: true}
			ops[test.op](&state)
			if state.Phase != test.want {
				t.Fatalf("phase = %d, want %d", state.Phase, test.want)
			}
			if state.InFlight() != (test.want == ResponseInFlight || test.want == ResponseCancelling) {
				t.Fatalf("inFlight = %t for phase %d", state.InFlight(), state.Phase)
			}
			if state.CancelSent() != (test.want == ResponseCancelling || test.want == ResponseInterrupted) {
				t.Fatalf("cancelSent = %t for phase %d", state.CancelSent(), state.Phase)
			}
		})
	}
}

func TestResponseStateStartAndEndResetIdentityAndOutput(t *testing.T) {
	state := Response{Phase: ResponseCompleted, ID: "resp-old", HasOutput: true}
	state.Start("resp-new")
	if state.ID != "resp-new" || state.HasOutput {
		t.Fatalf("after start = %+v, want the new identity and no output", state)
	}
	state.HasOutput = true
	state.End(false)
	if state.ID != "" || !state.HasOutput {
		t.Fatalf("after end = %+v, want identity cleared and output kept for terminal accounting", state)
	}
	state.Start("resp-ack")
	state.HasOutput = true
	state.End(true)
	if state.HasOutput {
		t.Fatal("acknowledgement end kept its output, want it cleared")
	}
}

func TestContinuationStateTransitions(t *testing.T) {
	for _, test := range []struct {
		name      string
		from      ContinuationPhase
		echoed    bool
		op        func(*Continuation)
		want      ContinuationPhase
		wantID    string
		wantEcho  bool
		wantEnded bool
	}{
		{name: "request", from: ContinuationNone, op: (*Continuation).Request, want: ContinuationRequested},
		{name: "tagged start binds without request", from: ContinuationNone,
			op: func(c *Continuation) { c.OnResponseStart("resp-c", true, false) }, want: ContinuationBoundTagged, wantID: "resp-c", wantEcho: true},
		{name: "untagged start guesses pending request", from: ContinuationRequested,
			op: func(c *Continuation) { c.OnResponseStart("resp-c", false, false) }, want: ContinuationBoundGuessed, wantID: "resp-c"},
		{name: "untagged start never guesses once purpose is echoed", from: ContinuationRequested, echoed: true,
			op: func(c *Continuation) { c.OnResponseStart("resp-user", false, false) }, want: ContinuationRequested, wantEcho: true},
		{name: "untagged start without request is ordinary", from: ContinuationNone,
			op: func(c *Continuation) { c.OnResponseStart("resp-x", false, false) }, want: ContinuationNone},
		{name: "acknowledgement start never binds", from: ContinuationRequested,
			op: func(c *Continuation) { c.OnResponseStart("resp-ack", false, true) }, want: ContinuationRequested},
		{name: "end bound tagged", from: ContinuationBoundTagged, op: (*Continuation).End, want: ContinuationEnded},
		{name: "end bound guessed", from: ContinuationBoundGuessed, op: (*Continuation).End, want: ContinuationEnded},
		{name: "end requested is a no-op", from: ContinuationRequested, op: (*Continuation).End, want: ContinuationRequested},
		{name: "rejection releases guessed binding", from: ContinuationBoundGuessed, op: (*Continuation).Rejected, want: ContinuationRequested},
		{name: "rejection keeps tagged binding", from: ContinuationBoundTagged, op: (*Continuation).Rejected, want: ContinuationBoundTagged, wantID: "resp-old"},
		{name: "take ended", from: ContinuationEnded, op: func(c *Continuation) { c.TakeEnded() }, want: ContinuationNone, wantEnded: true},
		{name: "reset", from: ContinuationBoundTagged, op: (*Continuation).Reset, want: ContinuationNone},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := Continuation{Phase: test.from, PurposeEchoed: test.echoed}
			if state.Bound() {
				state.ResponseID = "resp-old"
			}
			test.op(&state)
			if state.Phase != test.want || state.ResponseID != test.wantID || state.PurposeEchoed != test.wantEcho {
				t.Fatalf("state = %+v, want phase %d id %q echoed %t", state, test.want, test.wantID, test.wantEcho)
			}
		})
	}
	state := Continuation{Phase: ContinuationEnded}
	if !state.TakeEnded() || state.TakeEnded() {
		t.Fatal("takeEnded must report the end exactly once")
	}
}

func TestContinuationStateOwnsOnlyBoundResponse(t *testing.T) {
	state := Continuation{}
	if state.Owns("") {
		t.Fatal("unbound continuation owned untagged output")
	}
	state.Request()
	state.OnResponseStart("resp-c", false, false)
	for id, want := range map[string]bool{"": true, "resp-c": true, "resp-other": false} {
		if got := state.Owns(id); got != want {
			t.Fatalf("owns(%q) = %t, want %t", id, got, want)
		}
	}
}

func TestAcknowledgementStateTransitions(t *testing.T) {
	for _, test := range []struct {
		name string
		from AcknowledgementPhase
		op   func(*Acknowledgement)
		want AcknowledgementPhase
	}{
		{"request", AcknowledgementNone, (*Acknowledgement).Request, AcknowledgementOutstanding},
		{"request clears cancel", AcknowledgementCancelled, (*Acknowledgement).Request, AcknowledgementOutstanding},
		{"tagged output opens", AcknowledgementNone, (*Acknowledgement).ObserveTagged, AcknowledgementOutstanding},
		{"tagged output keeps cancel", AcknowledgementCancelled, (*Acknowledgement).ObserveTagged, AcknowledgementCancelled},
		{"cancel outstanding", AcknowledgementOutstanding, (*Acknowledgement).Cancel, AcknowledgementCancelled},
		{"cancel idle is a no-op", AcknowledgementNone, (*Acknowledgement).Cancel, AcknowledgementNone},
		{"end outstanding", AcknowledgementOutstanding, (*Acknowledgement).End, AcknowledgementEnded},
		{"end cancelled", AcknowledgementCancelled, (*Acknowledgement).End, AcknowledgementEnded},
		{"end idle is a no-op", AcknowledgementNone, (*Acknowledgement).End, AcknowledgementNone},
		{"reject", AcknowledgementCancelled, (*Acknowledgement).Reject, AcknowledgementNone},
		{"take ended", AcknowledgementEnded, func(a *Acknowledgement) { a.TakeEnded() }, AcknowledgementNone},
		{"take not ended", AcknowledgementOutstanding, func(a *Acknowledgement) { a.TakeEnded() }, AcknowledgementOutstanding},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := Acknowledgement{Phase: test.from}
			test.op(&state)
			if state.Phase != test.want {
				t.Fatalf("phase = %d, want %d", state.Phase, test.want)
			}
			wantOutstanding := test.want == AcknowledgementOutstanding || test.want == AcknowledgementCancelled
			if state.Outstanding() != wantOutstanding {
				t.Fatalf("outstanding = %t, want %t", state.Outstanding(), wantOutstanding)
			}
		})
	}
}

// The barge-in guards combine the three machines; this pins the combined
// predicates for each reachable combination.
func TestSessionRunStateBargeInPredicates(t *testing.T) {
	for _, test := range []struct {
		name          string
		Response      ResponsePhase
		Ack           AcknowledgementPhase
		wantActive    bool
		wantCancelPnd bool
	}{
		{"idle", ResponseIdle, AcknowledgementNone, false, false},
		{"response live", ResponseInFlight, AcknowledgementNone, true, false},
		{"response cancelling", ResponseCancelling, AcknowledgementNone, true, true},
		{"interrupted response ended", ResponseInterrupted, AcknowledgementNone, false, false},
		{"acknowledgement requested after completion", ResponseCompleted, AcknowledgementOutstanding, true, false},
		{"acknowledgement cancelled before it started", ResponseCompleted, AcknowledgementCancelled, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := State{Response: Response{Phase: test.Response}, Ack: Acknowledgement{Phase: test.Ack}}
			if state.ResponseActive() != test.wantActive || state.CancelPending() != test.wantCancelPnd {
				t.Fatalf("active=%t cancelPending=%t, want %t/%t", state.ResponseActive(), state.CancelPending(), test.wantActive, test.wantCancelPnd)
			}
		})
	}
}

func TestSessionRunStateNoteCancelSentTargetsLiveResponseAndAcknowledgement(t *testing.T) {
	state := State{}
	state.Ack.Request()
	state.NoteCancelSent()
	if !state.Ack.Cancelled() || state.Response.Phase != ResponseInterrupted || state.ResponseIDs.Len() != 0 {
		t.Fatalf("cancel before the acknowledgement started = %+v, want acknowledgement cancelled and no identity", state)
	}
	state = State{}
	state.Response.Start("resp-live")
	state.NoteCancelSent()
	if state.Response.Phase != ResponseCancelling || state.ResponseIDs.Get("resp-live") != ResponseIDCancelled {
		t.Fatalf("cancel of a live response = %+v, want cancelling and its identity recorded", state)
	}
}

func TestResponseIDLedger(t *testing.T) {
	var ledger ResponseIDLedger
	ledger.Mark("", ResponseIDTerminal)
	if ledger.Len() != 0 {
		t.Fatal("ledger recorded an empty identity")
	}
	ledger.Mark("resp-a", ResponseIDCancelled)
	if ledger.Closed("resp-a") {
		t.Fatal("cancelled identity reported closed")
	}
	ledger.Mark("resp-a", ResponseIDTerminal)
	ledger.Mark("resp-a", ResponseIDCancelled)
	if ledger.Get("resp-a") != ResponseIDTerminal || ledger.Len() != 1 {
		t.Fatalf("resp-a = %d (len %d), want terminal to be final and recorded once", ledger.Get("resp-a"), ledger.Len())
	}
	for i := range ResponseIDRetention {
		ledger.Mark(fmt.Sprintf("resp-%03d", i), ResponseIDRetired)
	}
	if ledger.Len() != ResponseIDRetention || ledger.Get("resp-a") != ResponseIDUnknown {
		t.Fatalf("ledger len = %d, resp-a = %d; want the oldest identity evicted at the bound", ledger.Len(), ledger.Get("resp-a"))
	}
	if !ledger.Closed(fmt.Sprintf("resp-%03d", ResponseIDRetention-1)) {
		t.Fatal("most recent identity was not retained")
	}
}

func TestSessionPhaseObservesFirstOpenOnly(t *testing.T) {
	phase := SessionAwaitingOpen
	if !phase.ObserveOpen() || phase.ObserveOpen() {
		t.Fatal("observeOpen must report only the first open")
	}
	closed := SessionClosed
	if closed.ObserveOpen() || closed != SessionClosed {
		t.Fatal("a closed session reopened")
	}
}
