package metricsreplay

import "testing"

func TestMetricContractValidation(t *testing.T) {
	for _, direction := range []Direction{DirectionInput, DirectionOutput} {
		if !direction.Valid() {
			t.Fatalf("direction %q is not valid", direction)
		}
	}
	if Direction("sideways").Valid() {
		t.Fatal("unknown direction was accepted")
	}
	for _, modality := range []Modality{ModalityAudio, ModalityText, ModalityImage, ModalityTool} {
		if !modality.Valid() {
			t.Fatalf("modality %q is not valid", modality)
		}
	}
	if Modality("video").Valid() {
		t.Fatal("unknown modality was accepted")
	}
	for _, direction := range []WireDirection{WireDirectionClientToServer, WireDirectionServerToClient} {
		if !direction.Valid() {
			t.Fatalf("wire direction %q is not valid", direction)
		}
	}
	if WireDirection("peer_to_peer").Valid() {
		t.Fatal("unknown wire direction was accepted")
	}
}

func TestMetricContractErrorCodes(t *testing.T) {
	for _, err := range []errorCode{
		ErrMissingClock,
		ErrMissingRunner,
		ErrMissingLoader,
		ErrMissingSinkFactory,
		ErrMissingFixture,
		ErrMalformedFixture,
		ErrInvalidSnapshot,
	} {
		if err.Error() == "" {
			t.Fatalf("error code %q has an empty diagnostic", err)
		}
	}
}
