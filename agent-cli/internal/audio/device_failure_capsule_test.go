package audio

import (
	"context"
	"encoding/json"
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
	if !reflect.DeepEqual(capsule.Captured, r.CapturedSamples()) {
		t.Fatal("capsule did not retain the capture-device output tap")
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

func TestFailureCapsuleLoadsAndReplaysVersionOneWithoutCaptureTap(t *testing.T) {
	scenario := simulatedScenario(16000, []int{480})
	registry, output := openSimulatedOutput(t, scenario)
	provider := int16Samples(100, 480)
	if err := output.WriteSamples(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := registry.Advance(1); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "v1-capsule")
	if err := WriteDuplexFailureCapsule(dir, scenario, provider, registry); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "run-manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest DuplexFailureCapsuleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = 1
	delete(manifest.Artifacts, "audio/capture-generated.pcm")
	if err := os.Remove(filepath.Join(dir, "audio", "capture-generated.pcm")); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(manifestBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	capsule, err := LoadDuplexFailureCapsule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if capsule.Captured != nil {
		t.Fatalf("version-one capsule unexpectedly synthesized capture evidence: %v", capsule.Captured)
	}
	if _, err := ReplayDuplexFailureCapsule(dir); err != nil {
		t.Fatalf("replay version-one capsule: %v", err)
	}
}

func TestFailureCapsuleRejectsSchemaAndUnfinalizedManifest(t *testing.T) {
	for _, manifest := range []string{`{"schema_version":99,"finalized":true}`, `{"schema_version":2,"finalized":false}`, `{"schema_version":2,"finalized":true,"artifacts":{}}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "run-manifest.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDuplexFailureCapsule(dir); err == nil {
			t.Fatalf("accepted manifest %s", manifest)
		}
	}
}
