package audio

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSessionAudioFailureCapsuleRoundTripAndIntegrity(t *testing.T) {
	s := simulatedScenario(48000, []int{480, 240, 960})
	s.Acoustic = AcousticSpec{DelaySamples: 7, GainQ15: 24000, NearEnd: []int16{1, 2, 3}, Background: []int16{-1, -2, -3}}
	r, output := openSimulatedOutput(t, s)
	provider := int16Samples(-1000, 1680)
	if err := output.WriteSamples(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(3); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "capsule")
	if err := WriteDuplexFailureCapsule(dir, s, provider, r); err != nil {
		t.Fatal(err)
	}
	capsule, err := LoadDuplexFailureCapsule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !capsule.Manifest.Finalized || capsule.Manifest.CallbackCount != 3 || len(capsule.Events) != 6 {
		t.Fatalf("manifest/events = %+v/%d", capsule.Manifest, len(capsule.Events))
	}
	if replay, err := ReplayDuplexFailureCapsule(dir); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(replay.Trace(), r.Trace()) {
		t.Fatal("capsule replay trace changed")
	}

	path := filepath.Join(dir, "audio", "playback-rendered.pcm")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDuplexFailureCapsule(dir); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("tampered capsule error = %v", err)
	}
}

func TestFailureCapsuleRejectsSchemaAndUnfinalizedManifest(t *testing.T) {
	for _, manifest := range []string{`{"schema_version":99,"finalized":true}`, `{"schema_version":1,"finalized":false}`, `{"schema_version":1,"finalized":true,"artifacts":{}}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "run-manifest.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDuplexFailureCapsule(dir); err == nil {
			t.Fatalf("accepted manifest %s", manifest)
		}
	}
}
