package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const (
	callAlpha = "call-alpha"
	callBravo = "call-bravo"
)

func TestLiveUnresolvedToolResultsErrorFormatsAndClassifies(t *testing.T) {
	err := &session.LiveUnresolvedToolResultsError{CallIDs: []string{callAlpha, callBravo}}
	if got, want := err.Error(), "tool results were not delivered for 2 unresolved call(s): call-alpha, call-bravo"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, session.ErrLiveUnresolvedToolResults) {
		t.Fatal("unresolved error does not classify as ErrLiveUnresolvedToolResults")
	}
	ids := err.UnresolvedCallIDs()
	ids[0] = "mutated"
	if err.CallIDs[0] != callAlpha {
		t.Fatal("UnresolvedCallIDs returned the error's backing slice")
	}
	var empty *session.LiveUnresolvedToolResultsError
	if empty.Error() != session.ErrLiveUnresolvedToolResults.Error() || empty.UnresolvedCallIDs() != nil {
		t.Fatalf("nil unresolved error = %q / %v, want the sentinel text and no IDs", empty.Error(), empty.UnresolvedCallIDs())
	}
}

func TestLiveContinuationErrorsAnnotateProviderContext(t *testing.T) {
	statuses := map[string]string{callBravo: "failed"}
	codes := map[string]string{callBravo: "rate_limit"}
	details := map[string]string{callBravo: " slow down "}
	tool := &session.LiveToolContinuationError{CallIDs: []string{callBravo, " ", callAlpha}, ProviderStatuses: statuses, ProviderCodes: codes, ProviderDetails: details}
	want := "tool continuation was not completed for 3 call(s): call-alpha, call-bravo (status=failed; code=rate_limit; detail=slow down)"
	if got := tool.Error(); got != want {
		t.Fatalf("tool continuation Error() = %q, want %q", got, want)
	}
	if !errors.Is(tool, session.ErrLiveToolContinuationIncomplete) {
		t.Fatal("tool continuation error lost its sentinel")
	}
	image := &session.LiveImageContinuationError{CallIDs: []string{callAlpha}}
	if got := image.Error(); got != "image tool continuation was not completed for 1 call(s): call-alpha" {
		t.Fatalf("image continuation Error() = %q", got)
	}
	if !errors.Is(image, session.ErrLiveImageContinuationIncomplete) {
		t.Fatal("image continuation error lost its sentinel")
	}
	if got := (&session.LiveToolContinuationError{}).Error(); got != session.ErrLiveToolContinuationIncomplete.Error() {
		t.Fatalf("empty tool continuation Error() = %q", got)
	}
	if got := (&session.LiveImageContinuationError{}).Error(); got != session.ErrLiveImageContinuationIncomplete.Error() {
		t.Fatalf("empty image continuation Error() = %q", got)
	}
}

func TestLiveScheduledAudioIncompleteErrorReportsCounters(t *testing.T) {
	err := &session.LiveScheduledAudioIncompleteError{Completed: 1, Dispatched: 2, Scheduled: 3, ProviderStatus: "transport", ProviderErrorCode: "closed", ProviderDetails: "provider_closed"}
	want := "scheduled audio session ended before all turns completed: completed=1 dispatched=2 scheduled=3 (status=transport; code=closed; detail=provider_closed)"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, session.ErrLiveScheduledAudioIncomplete) {
		t.Fatal("scheduled audio error lost its sentinel")
	}
	var empty *session.LiveScheduledAudioIncompleteError
	if empty.Error() != session.ErrLiveScheduledAudioIncomplete.Error() {
		t.Fatalf("nil scheduled audio error = %q", empty.Error())
	}
	if got := session.ErrLiveUserCancellation.Error(); got != "live session cancelled by user" {
		t.Fatalf("user cancellation text = %q", got)
	}
}

func TestLiveEventSinkFuncPublishesAndToleratesNil(t *testing.T) {
	var received session.LiveEvent
	sinkErr := errors.New("sink full")
	sink := session.LiveEventSinkFunc(func(_ context.Context, event session.LiveEvent) error {
		received = event
		return sinkErr
	})
	if err := sink.Publish(context.Background(), session.LiveEvent{SessionID: "s1"}); !errors.Is(err, sinkErr) {
		t.Fatalf("Publish error = %v, want the callback error", err)
	}
	if received.SessionID != "s1" {
		t.Fatalf("callback received %+v", received)
	}
	var nilSink session.LiveEventSinkFunc
	if err := nilSink.Publish(context.Background(), session.LiveEvent{}); err != nil {
		t.Fatalf("nil sink Publish = %v, want nil", err)
	}
}
