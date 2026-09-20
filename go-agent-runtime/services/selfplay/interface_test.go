package selfplay

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunFuncWithoutRunnerReturnsStableError(t *testing.T) {
	var run RunFunc
	_, err := run.Run(context.Background(), Request{})
	if !errors.Is(err, ErrRunnerRequired) {
		t.Fatalf("Run error = %v, want ErrRunnerRequired", err)
	}
}

func TestRunFuncReturnsServiceResultAndCause(t *testing.T) {
	wantErr := errors.New("service failure")
	want := Result{StopReason: StopFailure, Customer: SideResult{Role: RoleCustomer}}
	run := RunFunc(func(context.Context, Request) (Result, error) {
		return want, wantErr
	})

	got, err := run.Run(context.Background(), Request{MaxTurns: 2})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want wrapped service cause", err)
	}
	if got != want {
		t.Fatalf("Run result = %#v, want %#v", got, want)
	}
}

func TestUnsupportedModelErrorPreservesCategoryAndContext(t *testing.T) {
	err := &UnsupportedModelError{Provider: "openai", Model: "text-only"}
	if !errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("error %v does not preserve ErrUnsupportedModel", err)
	}
	for _, value := range []string{"openai", "text-only", "realtime-capable"} {
		if !strings.Contains(err.Error(), value) {
			t.Fatalf("error %q does not include %q", err, value)
		}
	}
}

func TestUnsupportedModelErrorNilReceiverHasStableText(t *testing.T) {
	var err *UnsupportedModelError
	if got := err.Error(); got != ErrUnsupportedModel.Error() {
		t.Fatalf("nil receiver error = %q, want %q", got, ErrUnsupportedModel)
	}
}
