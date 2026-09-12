package captureclaim_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
)

func TestClaimErrorPreservesClassificationCauseAndRedaction(t *testing.T) {
	cause := errors.New("write failed")
	err := &captureclaim.ClaimError{
		Kind: captureclaim.ErrDestinationClaimed,
		Path: "/tmp/capture.json",
		Holder: &captureclaim.ClaimHolder{
			RequestedPath: "/tmp/capture.json",
			PID:           42,
			Host:          "worker",
			StartedAtUTC:  "2026-09-12T00:00:00Z",
		},
		Err: cause,
	}
	if !errors.Is(err, captureclaim.ErrDestinationClaimed) || !errors.Is(err, cause) {
		t.Fatalf("claim error lost identity: %v", err)
	}
	message := err.Error()
	if !strings.Contains(message, "pid=42") || !strings.Contains(message, "host=\"worker\"") || strings.Contains(message, "write failed") {
		t.Fatalf("claim error redaction = %q", message)
	}
	if got := (captureclaim.ClaimHolder{}).RequestedPath; got != "" {
		t.Fatalf("zero holder path = %q", got)
	}
}

func TestClaimErrorRendersEveryPublicClassification(t *testing.T) {
	cause := errors.New("filesystem cause")
	cases := []struct {
		name string
		err  *captureclaim.ClaimError
		want string
	}{
		{name: "occupied", err: &captureclaim.ClaimError{Kind: captureclaim.ErrDestinationOccupied, Path: "/tmp/artifact"}, want: "will not be replaced"},
		{name: "claimed without holder", err: &captureclaim.ClaimError{Kind: captureclaim.ErrDestinationClaimed, Path: "/tmp/artifact"}, want: "holder identity unavailable"},
		{name: "lost", err: &captureclaim.ClaimError{Kind: captureclaim.ErrClaimLost, Path: "/tmp/artifact"}, want: "claim was lost"},
		{name: "lost with cause", err: &captureclaim.ClaimError{Kind: captureclaim.ErrClaimLost, Path: "/tmp/artifact", Err: cause}, want: "filesystem cause"},
		{name: "unavailable", err: &captureclaim.ClaimError{Path: "/tmp/artifact"}, want: "unavailable"},
		{name: "unavailable with cause", err: &captureclaim.ClaimError{Path: "/tmp/artifact", Err: cause}, want: "filesystem cause"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.err.Error(); !strings.Contains(got, testCase.want) {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
	var nilErr *captureclaim.ClaimError
	if got := nilErr.Error(); got != "session recording destination is unavailable" {
		t.Fatalf("nil error = %q", got)
	}
	if nilErr.Unwrap() != nil {
		t.Fatal("nil error unexpectedly unwraps")
	}
	for _, testCase := range []struct {
		err  error
		want string
	}{
		{err: captureclaim.ErrInvalidDestination, want: "capture claim destination is required"},
		{err: captureclaim.ErrDestinationOccupied, want: "session recording destination is occupied"},
		{err: captureclaim.ErrDestinationClaimed, want: "session recording destination is already claimed"},
		{err: captureclaim.ErrClaimLost, want: "session recording claim was lost"},
	} {
		if got := testCase.err.Error(); got != testCase.want {
			t.Errorf("sentinel error = %q, want %q", got, testCase.want)
		}
	}
}
