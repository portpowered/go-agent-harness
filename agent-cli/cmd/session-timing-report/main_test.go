package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRequiresCapture(t *testing.T) {
	if err := run(nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires -capture") {
		t.Fatalf("missing capture error = %v", err)
	}
}

func TestRunRejectsMissingCapture(t *testing.T) {
	if err := run([]string{"-capture", t.TempDir() + "/missing.json"}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing capture was accepted")
	}
}
