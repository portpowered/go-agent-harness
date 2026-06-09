package sessionfixturevalidator

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestRun_ValidFixtureDirectory_PrintsSuccessSummary(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	writeCapture(t, filepath.Join(root, "root.session.json"), validSyntheticCapture())
	writeCapture(t, filepath.Join(nested, "nested.session.json"), validSyntheticCapture())
	if err := os.WriteFile(filepath.Join(root, "ignored.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run([]string{root}, &stdout, &stderr)

	if err != nil {
		t.Fatalf("Run returned error, want nil: %v; stderr=%s", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "validated 2 session fixture file(s): ok") {
		t.Fatalf("stdout = %q, want success summary for two fixture files", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRun_InvalidFixtureFile_PrintsFilePathAndReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.session.json")
	capture := validSyntheticCapture()
	capture.Session.FixtureProvenance = ""
	writeCapture(t, path, capture)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run([]string{path}, &stdout, &stderr)

	if !errors.Is(err, ErrValidationFailed) {
		t.Fatalf("Run error = %v, want ErrValidationFailed", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	got := stderr.String()
	if !strings.Contains(got, path) || !strings.Contains(got, "session.fixture_provenance") {
		t.Fatalf("stderr = %q, want file path and validation reason", got)
	}
}

func TestRun_HelpTextMentionsFixtureHygieneRules(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run([]string{"-h"}, &stdout, &stderr)

	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Run error = %v, want flag.ErrHelp", err)
	}
	help := stderr.String()
	for _, want := range []string{"session.fixture_provenance", "raw audio", "credential-like", "websocket_message", "stream_message"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help text = %q, want substring %q", help, want)
		}
	}
}

func validSyntheticCapture() gatewaytesting.SessionCapture {
	return gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "grok", Model: "grok-realtime"},
		Session: gatewaytesting.SessionMetadata{
			ID:                "sess_sanitized",
			StartedAtUTC:      time.Now().UTC().Format(time.RFC3339Nano),
			FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic,
		},
		Records: []gatewaytesting.CapturedSessionEvent{{
			Sequence:    1,
			Direction:   gatewaytesting.DirectionServerToClient,
			Type:        string(messages.StreamTypeTextDelta),
			PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage,
			Payload:     json.RawMessage(`{"type":"TEXT.DELTA","value":{"type":"delta_text","content":"hello"}}`),
		}},
	}
}

func writeCapture(t *testing.T, path string, capture gatewaytesting.SessionCapture) {
	t.Helper()

	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write capture: %v", err)
	}
}
