package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteRefusal_PlainText(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteRefusal(&buf, "I cannot assist with that."); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	if !strings.Contains(got, "[REFUSAL]") {
		t.Errorf("expected [REFUSAL] prefix, got %q", got)
	}
	if !strings.Contains(got, "I cannot assist with that.") {
		t.Errorf("expected refusal text in output, got %q", got)
	}
	// bytes.Buffer is not a TTY, so no ANSI codes
	if strings.Contains(got, "\033[") {
		t.Errorf("expected no ANSI escape codes for non-TTY writer, got %q", got)
	}
}

func TestWriteRefusal_EmptyText(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteRefusal(&buf, ""); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty refusal, got %q", buf.String())
	}
}

func TestWriteRefusal_TrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteRefusal(&buf, "Refused."); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected trailing newline, got %q", got)
	}
}

func TestWriteRefusalJSON_StructuredEvent(t *testing.T) {
	var buf bytes.Buffer
	err := WriteRefusalJSON(&buf, "I cannot assist with that.", "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var evt refusalEventJSON
	if err := json.Unmarshal(buf.Bytes(), &evt); err != nil {
		t.Fatalf("failed to unmarshal refusal JSON: %v", err)
	}
	if evt.Type != "refusal" {
		t.Errorf("expected type %q, got %q", "refusal", evt.Type)
	}
	if evt.Message != "I cannot assist with that." {
		t.Errorf("expected message %q, got %q", "I cannot assist with that.", evt.Message)
	}
	if evt.Model != "gpt-4o" {
		t.Errorf("expected model %q, got %q", "gpt-4o", evt.Model)
	}
	if evt.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestWriteRefusalJSON_EmptyText(t *testing.T) {
	var buf bytes.Buffer
	err := WriteRefusalJSON(&buf, "", "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty refusal, got %q", buf.String())
	}
}

func TestWriteRefusalJSON_TrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	err := WriteRefusalJSON(&buf, "Refused.", "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected trailing newline, got %q", got)
	}
}

func TestWriteRefusalJSON_NoStdoutContamination(t *testing.T) {
	// Verify the JSON output does not contain [REFUSAL] prefix (that's for human-readable mode)
	var buf bytes.Buffer
	err := WriteRefusalJSON(&buf, "Refused.", "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(buf.String(), "[REFUSAL]") {
		t.Error("JSON output should not contain [REFUSAL] prefix")
	}
}
