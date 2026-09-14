package room

import (
	"context"
	"errors"
	"testing"
)

func TestCompatibilityBoundariesPreservePublicDefaultsAndFailures(t *testing.T) {
	for _, test := range []struct {
		input ParticipantKind
		want  ParticipantKind
	}{
		{input: "", want: ParticipantKindAgent},
		{input: " AGENT ", want: ParticipantKindAgent},
		{input: " customer ", want: ParticipantKindHuman},
		{input: " HUMAN ", want: ParticipantKindHuman},
		{input: " custom ", want: "custom"},
	} {
		if got := NormalizeParticipantKind(test.input); got != test.want {
			t.Fatalf("NormalizeParticipantKind(%q) = %q, want %q", test.input, got, test.want)
		}
	}

	var nilMesh *Mesh
	if nilMesh.Context() == nil {
		t.Fatal("nil Mesh Context returned nil context")
	}
	select {
	case <-nilMesh.Done():
	default:
		t.Fatal("nil Mesh Done channel is not closed")
	}
	if err := nilMesh.Join(context.Background(), "participant"); !errors.Is(err, ErrMeshClosed) {
		t.Fatalf("nil Mesh Join error = %v, want ErrMeshClosed", err)
	}
	if err := nilMesh.AddParticipant(context.Background(), "participant"); !errors.Is(err, ErrMeshClosed) {
		t.Fatalf("nil Mesh AddParticipant error = %v, want ErrMeshClosed", err)
	}
	if err := nilMesh.RemoveParticipant("participant"); !errors.Is(err, ErrMeshClosed) {
		t.Fatalf("nil Mesh RemoveParticipant error = %v, want ErrMeshClosed", err)
	}
	if err := nilMesh.Leave("participant"); !errors.Is(err, ErrMeshClosed) {
		t.Fatalf("nil Mesh Leave error = %v, want ErrMeshClosed", err)
	}
	if _, err := nilMesh.Peers("participant"); !errors.Is(err, ErrMeshClosed) {
		t.Fatalf("nil Mesh Peers error = %v, want ErrMeshClosed", err)
	}
	if _, err := nilMesh.RemotePeers("participant"); !errors.Is(err, ErrMeshClosed) {
		t.Fatalf("nil Mesh RemotePeers error = %v, want ErrMeshClosed", err)
	}
	if _, err := nilMesh.Pair("first", "second"); !errors.Is(err, ErrMeshClosed) {
		t.Fatalf("nil Mesh Pair error = %v, want ErrMeshClosed", err)
	}
	if nilMesh.Participants() != nil || nilMesh.ParticipantIDs() != nil || nilMesh.Pairs() != nil || nilMesh.PairCount() != 0 {
		t.Fatal("nil Mesh returned mutable membership state")
	}
	if err := nilMesh.Close(); err != nil {
		t.Fatalf("nil Mesh Close: %v", err)
	}

	if _, err := NewPairSpec("same", "same"); !errors.Is(err, ErrMeshInvalidPair) {
		t.Fatalf("same-participant PairSpec error = %v, want ErrMeshInvalidPair", err)
	}
	if _, err := NewPCM16Mixer(context.Background(), DefaultPCM16Format(), DefaultPCM16Format()); !errors.Is(err, ErrMixerInvalidFormat) {
		t.Fatalf("multiple mixer formats error = %v, want ErrMixerInvalidFormat", err)
	}

	var nilMixer *PCM16Mixer
	if err := nilMixer.Advance(context.Background()); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer Advance error = %v, want ErrMixerClosed", err)
	}
	if nilMixer.Format() != (PCM16Format{}) || nilMixer.FrameBytes() != 0 || nilMixer.Frames() != nil || nilMixer.Inputs() != nil {
		t.Fatal("nil mixer exposed mutable or non-zero state")
	}
	if _, err := nilMixer.Input("participant"); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer Input error = %v, want ErrMixerClosed", err)
	}
	if _, err := nilMixer.AddInputWriter("participant"); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer AddInputWriter error = %v, want ErrMixerClosed", err)
	}
	if err := nilMixer.AddInput("participant"); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer AddInput error = %v, want ErrMixerClosed", err)
	}
	if err := nilMixer.RemoveInput("participant"); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer RemoveInput error = %v, want ErrMixerClosed", err)
	}
	if err := nilMixer.WriteContext(context.Background(), "participant", []byte{1, 2}); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer WriteContext error = %v, want ErrMixerClosed", err)
	}
	if _, err := nilMixer.WriteContextWithDisposition(context.Background(), "participant", []byte{1, 2}); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer WriteContextWithDisposition error = %v, want ErrMixerClosed", err)
	}
	if _, err := nilMixer.WriteContextWithDispositionAndObserver(context.Background(), "participant", []byte{1, 2}, nil); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer observed write error = %v, want ErrMixerClosed", err)
	}
	if _, err := nilMixer.ReadFrame(context.Background()); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer ReadFrame error = %v, want ErrMixerClosed", err)
	}
	if _, err := nilMixer.ReadFrameWithSources(context.Background()); !errors.Is(err, ErrMixerClosed) {
		t.Fatalf("nil mixer ReadFrameWithSources error = %v, want ErrMixerClosed", err)
	}
	if !errors.Is(nilMixer.Err(), ErrMixerClosed) {
		t.Fatalf("nil mixer Err = %v, want ErrMixerClosed", nilMixer.Err())
	}
	if err := nilMixer.Close(); err != nil {
		t.Fatalf("nil mixer Close: %v", err)
	}
}
