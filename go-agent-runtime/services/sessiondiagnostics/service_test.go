package sessiondiagnostics

import "testing"

func TestTypedErrorsExposeStableMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "closed", err: ErrClosed, want: "session diagnostics lifecycle is closed"},
		{name: "malformed", err: ErrMalformedSequence, want: "malformed session diagnostics sequence"},
		{name: "retry exhausted", err: ErrRetryExhausted, want: "session diagnostics retry budget exhausted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("error message = %q, want %q", got, tt.want)
			}
		})
	}
}
