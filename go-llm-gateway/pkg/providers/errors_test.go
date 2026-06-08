package providers

import (
	"context"
	"errors"
	"testing"
)

func TestErrorClassification_DistinguishesRuntimeOutcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "context cancellation", err: context.Canceled, want: ErrorClassCancellation},
		{name: "taxonomy cancellation", err: ErrCancellation, want: ErrorClassCancellation},
		{name: "replay mismatch", err: ErrReplayMismatch, want: ErrorClassReplayMismatch},
		{name: "partial output", err: ErrPartialOutput, want: ErrorClassPartialOutput},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ErrorClassification(tc.err); got != tc.want {
				t.Fatalf("ErrorClassification(%v) = %q, want %q", tc.err, got, tc.want)
			}
			for _, providerClass := range []error{ErrProviderRejected, ErrTransport, ErrInvalidRequest, ErrUnsupportedRequest} {
				if errors.Is(tc.err, providerClass) {
					t.Fatalf("%v should not match provider class %v", tc.err, providerClass)
				}
			}
		})
	}
}
