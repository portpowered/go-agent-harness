package roomaudiodiagnostics_test

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics"
)

func TestVocabularyAndPCMClassification(t *testing.T) {
	if roomaudiodiagnostics.EventRoomAudioIngress != "room_audio_ingress" || roomaudiodiagnostics.EventRoomAudioIngressSummary != "room_audio_ingress_summary" {
		t.Fatalf("event vocabulary changed: %q/%q", roomaudiodiagnostics.EventRoomAudioIngress, roomaudiodiagnostics.EventRoomAudioIngressSummary)
	}
	if roomaudiodiagnostics.FieldSourcePeer != "source_peer" || roomaudiodiagnostics.FieldContentLoss != "content_loss" {
		t.Fatalf("field vocabulary changed: %q/%q", roomaudiodiagnostics.FieldSourcePeer, roomaudiodiagnostics.FieldContentLoss)
	}
	if roomaudiodiagnostics.Delivered != "delivered" || roomaudiodiagnostics.Backpressured != "backpressured" || roomaudiodiagnostics.Rejected != "rejected" {
		t.Fatalf("disposition vocabulary changed")
	}
	if roomaudiodiagnostics.MixedSource != "room-mix" || roomaudiodiagnostics.NoPeerSource != "none" || roomaudiodiagnostics.MaxFirstEvents != 32 {
		t.Fatalf("synthetic source/bound changed")
	}
	if roomaudiodiagnostics.PCM16(nil).Contentful() || roomaudiodiagnostics.PCM16([]byte{0, 0, 0}).Contentful() {
		t.Fatal("silent PCM classified as contentful")
	}
	if !roomaudiodiagnostics.PCM16([]byte{0, 1}).Contentful() {
		t.Fatal("non-zero PCM classified as silent")
	}
}

func TestInvalidDispositionErrorIsTyped(t *testing.T) {
	err := roomaudiodiagnostics.InvalidDispositionError{Value: roomaudiodiagnostics.Disposition("future")}
	if !errors.Is(err, roomaudiodiagnostics.ErrInvalidDisposition) || err.Error() == "" {
		t.Fatalf("invalid disposition error = %v", err)
	}
}
