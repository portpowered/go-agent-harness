package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors"
)

type typedCause struct{ message string }

func (e *typedCause) Error() string { return e.message }

func TestParticipantFailurePreservesIdentityAndCause(t *testing.T) {
	service := New()
	sentinel := errors.New("provider sentinel")
	cause := &typedCause{message: "typed provider failure"}
	wrapped := fmt.Errorf("outer: %w", cause)
	failure := service.ParticipantFailure(sessionroomerrors.ParticipantFailureRequest{
		ParticipantID: "agent-a",
		Cause:         errors.Join(sentinel, wrapped),
	})

	if !errors.Is(failure, sentinel) {
		t.Fatal("participant failure lost errors.Is identity")
	}
	var gotCause *typedCause
	if !errors.As(failure, &gotCause) || gotCause != cause {
		t.Fatal("participant failure lost errors.As identity")
	}
	var identity sessionroomerrors.ParticipantFailure
	ok := errors.As(failure, &identity)
	if !ok || identity.ParticipantID() != "agent-a" {
		t.Fatalf("participant identity = %q, want agent-a", identity.ParticipantID())
	}
	if !strings.Contains(failure.Error(), "agent-a") {
		t.Fatalf("failure message = %q, want participant identity", failure.Error())
	}
}

func TestParticipantFailureCopiesSecretsAndRedactsNestedCredentials(t *testing.T) {
	service := New()
	secrets := []string{"short-secret", "long-short-secret"}
	failure := service.ParticipantFailure(sessionroomerrors.ParticipantFailureRequest{
		ParticipantID: "agent-a",
		Cause:         errors.New("authorization: Bearer long-short-secret and short-secret"),
		Secrets:       secrets,
	})
	secrets[0] = "mutated"
	secrets[1] = "also-mutated"

	message := failure.Error()
	if strings.Contains(message, "long-short-secret") || strings.Contains(message, "short-secret") {
		t.Fatalf("failure leaked credential: %q", message)
	}
	if !strings.Contains(message, "[REDACTED]") {
		t.Fatalf("failure message = %q, want redaction", message)
	}
	if reason := service.ParticipantFailureReason(sessionroomerrors.ParticipantFailureReasonRequest{Error: failure}); strings.Contains(reason, "secret") {
		t.Fatalf("participant reason leaked nested credential: %q", reason)
	}
}

func TestSanitizeRedactsSuppliedAndMarkerCredentials(t *testing.T) {
	service := New()
	value := errors.New("authorization: Bearer raw-token, api_key=explicit-token x-api-key: header-token")
	got := service.Sanitize(value, []string{"raw-token", "explicit-token", "header-token"})
	for _, secret := range []string{"raw-token", "explicit-token", "header-token"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized message %q contains %q", got, secret)
		}
	}
	if strings.Count(got, "[REDACTED]") < 3 {
		t.Fatalf("sanitized message = %q, want all credentials redacted", got)
	}
}

func TestParticipantFailureReasonUsesStableFallbackPrecedence(t *testing.T) {
	service := New()
	cases := []struct {
		name string
		in   sessionroomerrors.ParticipantFailureReasonRequest
		want string
	}{
		{name: "cause", in: sessionroomerrors.ParticipantFailureReasonRequest{Error: errors.New("provider failed")}, want: "provider failed"},
		{name: "close", in: sessionroomerrors.ParticipantFailureReasonRequest{CloseReason: " close detail ", TransportDisconnected: true}, want: "close detail"},
		{name: "transport", in: sessionroomerrors.ParticipantFailureReasonRequest{TransportDisconnected: true, TerminationReason: sessionroomerrors.ParticipantTerminationDisconnected}, want: "transport disconnected"},
		{name: "participant", in: sessionroomerrors.ParticipantFailureReasonRequest{TerminationReason: sessionroomerrors.ParticipantTerminationDisconnected}, want: "participant disconnected"},
		{name: "failure", in: sessionroomerrors.ParticipantFailureReasonRequest{}, want: "participant failure"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := service.ParticipantFailureReason(test.in); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFailureResultHasStableFailedRoomProjection(t *testing.T) {
	service := New()
	result := service.FailureResult(errors.New("room failed"), nil)
	if result.TerminationReason != sessionroomerrors.RoomTerminationFailed || result.Reason != sessionroomerrors.RoomTerminationFailed {
		t.Fatalf("result reasons = %q/%q", result.TerminationReason, result.Reason)
	}
	if result.Error != "room failed" {
		t.Fatalf("result error = %q", result.Error)
	}
	if result.Participants == nil {
		t.Fatal("failed room result has nil participants map")
	}
	result.Participants["caller"] = struct{}{}
	other := service.FailureResult(nil, nil)
	if len(other.Participants) != 0 {
		t.Fatalf("service state leaked across results: %+v", other.Participants)
	}
}

func TestIndependentServicesRemainConcurrentAndIsolated(t *testing.T) {
	services := []*Service{New(), New()}
	const iterations = 40
	var wait sync.WaitGroup
	for index, service := range services {
		index, service := index, service
		wait.Add(1)
		go func() {
			defer wait.Done()
			for count := 0; count < iterations; count++ {
				failure := service.ParticipantFailure(sessionroomerrors.ParticipantFailureRequest{
					ParticipantID: fmt.Sprintf("participant-%d", index),
					Cause:         errors.New("failure"),
				})
				var identity sessionroomerrors.ParticipantFailure
				want := fmt.Sprintf("participant-%d", index)
				if !errors.As(failure, &identity) || identity == nil || identity.ParticipantID() != want {
					t.Errorf("identity = %#v, want %q", identity, want)
					return
				}
			}
		}()
	}
	wait.Wait()
}
