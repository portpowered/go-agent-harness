package sessionroomerrors

import "testing"

func TestContractTypesRemainHostNeutral(t *testing.T) {
	var _ ParticipantFailure = participantFailureProbe{}
	var _ Service = serviceProbe{}
	if _, ok := AsParticipantFailure(nil); ok {
		t.Fatal("nil error unexpectedly exposed a participant failure")
	}
}

type participantFailureProbe struct{}

func (participantFailureProbe) Error() string         { return "failure" }
func (participantFailureProbe) ParticipantID() string { return "participant" }

type serviceProbe struct{}

func (serviceProbe) ParticipantFailure(ParticipantFailureRequest) error { return nil }
func (serviceProbe) ParticipantFailureReason(ParticipantFailureReasonRequest) string {
	return ""
}
func (serviceProbe) Sanitize(error, []string) string { return "" }
func (serviceProbe) FailureResult(error, []string) RoomFailureResult {
	return RoomFailureResult{}
}
