package consumer_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors/wire"
)

func TestExternalConsumerUsesOnlyPublicRoomErrorContract(t *testing.T) {
	first := wire.NewService()
	second := wire.NewService()
	if first == nil || second == nil {
		t.Fatal("wire.NewService returned nil")
	}

	secret := "external-room-secret"
	sentinel := errors.New("provider failure: " + secret)
	failure := first.ParticipantFailure(sessionroomerrors.ParticipantFailureRequest{
		ParticipantID: "agent-a",
		Cause:         sentinel,
		Secrets:       []string{secret},
	})
	if !errors.Is(failure, sentinel) {
		t.Fatal("errors.Is did not reach the original provider cause")
	}
	var participant sessionroomerrors.ParticipantFailure
	if !errors.As(failure, &participant) || participant.ParticipantID() != "agent-a" {
		t.Fatalf("participant = %v, want agent-a", participant)
	}
	if strings.Contains(failure.Error(), secret) {
		t.Fatalf("participant error leaked secret: %q", failure.Error())
	}
	if got := first.ParticipantFailureReason(sessionroomerrors.ParticipantFailureReasonRequest{Error: fmt.Errorf("wrapped: %w", failure)}); strings.Contains(got, secret) || !strings.Contains(got, "provider failure") {
		t.Fatalf("failure reason = %q, want sanitized provider cause", got)
	}

	for _, test := range []struct {
		name string
		in   sessionroomerrors.ParticipantFailureReasonRequest
		want string
	}{
		{name: "close", in: sessionroomerrors.ParticipantFailureReasonRequest{CloseReason: "close detail"}, want: "close detail"},
		{name: "transport", in: sessionroomerrors.ParticipantFailureReasonRequest{TransportDisconnected: true}, want: "transport disconnected"},
		{name: "participant", in: sessionroomerrors.ParticipantFailureReasonRequest{TerminationReason: sessionroomerrors.ParticipantTerminationDisconnected}, want: "participant disconnected"},
		{name: "default", in: sessionroomerrors.ParticipantFailureReasonRequest{}, want: "participant failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := first.ParticipantFailureReason(test.in); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}

	result := first.FailureResult(failure, []string{secret})
	if result.Participants == nil || result.Error == "" || result.Reason != sessionroomerrors.RoomTerminationFailed {
		t.Fatalf("failed room result = %+v", result)
	}
	if strings.Contains(result.Error, secret) {
		t.Fatalf("failed room result leaked secret: %q", result.Error)
	}
	result.Participants["mutated"] = struct{}{}
	if other := second.FailureResult(nil, nil); len(other.Participants) != 0 {
		t.Fatalf("Wire services share result state: %+v", other.Participants)
	}
}
