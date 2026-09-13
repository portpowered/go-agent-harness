package providerliveness_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness"
)

func TestErrorIdentityFactsAndTimerAdapters(t *testing.T) {
	err := &providerliveness.Error{
		Classification:     providerliveness.SilentProviderEmptyResponseClassification,
		ResponseID:         "response-contract",
		TerminalReason:     "terminal_failure",
		TerminalProvenance: "session",
		OutputState:        "none",
		Usage:              providerliveness.TokenUsage{PromptTokens: 2, CompletionTokens: 0, TotalTokens: 2, ReasoningTokens: 1},
	}
	if !errors.Is(err, providerliveness.ErrSilentProviderEmptyResponse) {
		t.Fatal("empty sentinel identity was not preserved")
	}
	var typed *providerliveness.Error
	if !errors.As(err, &typed) || typed.ResponseID != "response-contract" {
		t.Fatalf("typed identity = %#v", typed)
	}
	if (providerliveness.TimerFunc{}).Stop() {
		t.Fatal("nil TimerFunc stop reported success")
	}
}

func TestContractErrorsAndFailureBridges(t *testing.T) {
	empty := &providerliveness.Error{}
	if empty.Error() != "silent_provider_empty_response: provider response produced no observable output" {
		t.Fatalf("empty error string = %q", empty.Error())
	}
	timeout := &providerliveness.Error{Classification: providerliveness.SilentProviderTimeoutClassification}
	if !errors.Is(timeout, providerliveness.ErrSilentProviderTimeout) || timeout.Error() == empty.Error() {
		t.Fatalf("timeout identity/string = %v / %q", timeout, timeout.Error())
	}
	var nilError *providerliveness.Error
	if nilError.Error() == "" || nilError.Unwrap() != nil {
		t.Fatal("nil typed error methods are not safe")
	}
	called := false
	stop := false
	timer := providerliveness.TimerFunc{Channel: make(chan time.Time), StopFunc: func() bool { stop = true; return true }}
	if timer.C() == nil || !timer.Stop() || !stop {
		t.Fatal("TimerFunc did not preserve timer behavior")
	}
	if (providerliveness.ClockFunc(nil)).NewTimer(time.Second) != nil {
		t.Fatal("nil ClockFunc created a timer")
	}
	clock := providerliveness.ClockFunc(func(time.Duration) providerliveness.Timer { called = true; return timer })
	if clock.NewTimer(time.Second) == nil || !called {
		t.Fatal("ClockFunc did not invoke its factory")
	}

	events := make(chan struct{}, 1)
	events <- struct{}{}
	want := errors.New("bridge failure")
	out := (providerliveness.FailureBridge{Events: events, Failure: func() error { return want }}).Errors(context.Background())
	if got := <-out; !errors.Is(got, want) {
		t.Fatalf("failure bridge = %v, want %v", got, want)
	}
	first, second := make(chan error, 1), make(chan error, 1)
	first <- want
	merged := (providerliveness.ErrorChannels{First: first, Second: second}).Merge(context.Background())
	if got := <-merged; !errors.Is(got, want) {
		t.Fatalf("merge bridge = %v, want %v", got, want)
	}
	cancel, cancelFn := context.WithCancel(context.Background())
	cancelFn()
	cancelled := (providerliveness.FailureBridge{Events: make(chan struct{}), Failure: func() error { return want }}).Errors(cancel)
	if _, ok := <-cancelled; ok {
		t.Fatal("cancelled bridge emitted a value")
	}
	if (providerliveness.FailureBridge{Events: nil, Failure: func() error { return want }}).Errors(context.Background()) != nil {
		t.Fatal("nil event channel created a bridge")
	}
	if (providerliveness.FailureBridge{Events: make(chan struct{}), Failure: nil}).Errors(context.Background()) != nil {
		t.Fatal("nil failure callback created a bridge")
	}
	closedFirst, closedSecond := make(chan error), make(chan error)
	close(closedFirst)
	close(closedSecond)
	closed := (providerliveness.ErrorChannels{First: closedFirst, Second: closedSecond}).Merge(context.Background())
	if _, ok := <-closed; ok {
		t.Fatal("closed merge emitted a value")
	}
	withNil, withErr := make(chan error, 2), make(chan error, 1)
	withNil <- nil
	withNil <- want
	withErrMerge := (providerliveness.ErrorChannels{First: withNil, Second: withErr}).Merge(context.Background())
	if got := <-withErrMerge; !errors.Is(got, want) {
		t.Fatalf("nil-error merge = %v, want %v", got, want)
	}
	mergeContext, stopMerge := context.WithCancel(context.Background())
	mergeCancelled := (providerliveness.ErrorChannels{First: make(chan error), Second: make(chan error)}).Merge(mergeContext)
	stopMerge()
	if _, ok := <-mergeCancelled; ok {
		t.Fatal("cancelled merge emitted a value")
	}
}
