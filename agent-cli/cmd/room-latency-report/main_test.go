package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPrintsReportDerivedFromFinalizedBundle(t *testing.T) {
	destination := t.TempDir()
	manifest := `{"finalized":true,"artifacts":{"room.latency":"room-latency.json"}}`
	bundle := `{"schema_version":1,"format":{"sample_rate_hz":24000,"channels":1,"frame_duration_ns":20000000},"events":[]}`
	if err := os.WriteFile(filepath.Join(destination, "run-manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "room-latency.json"), []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-out", destination}, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"eligible_count": 0`) {
		t.Fatalf("report did not contain the derived empty report: %s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestRunRequiresDestination(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err == nil || err.Error() != "room latency report requires -out" {
		t.Fatalf("run error = %v, want missing destination error", err)
	}
}
