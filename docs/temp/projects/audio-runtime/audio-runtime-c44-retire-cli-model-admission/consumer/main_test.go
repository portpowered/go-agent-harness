package main

import "testing"

func TestExternalModelAdmissionContract(t *testing.T) {
	result, err := run()
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if result.Schema != "audio-runtime-c44-model-admission/v1" {
		t.Fatalf("schema = %q", result.Schema)
	}
	if len(result.Observations) != 5 {
		t.Fatalf("observations = %d, want 5", len(result.Observations))
	}
}
