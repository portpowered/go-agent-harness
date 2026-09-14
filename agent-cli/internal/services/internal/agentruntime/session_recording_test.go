package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestRunSessionWithRecordingDirectoryRejectsNonEmptyDestinationBeforeConnect(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capture")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(destination, "customer.txt")
	want := []byte("keep this file")
	if err := os.WriteFile(sentinel, want, 0o644); err != nil {
		t.Fatal(err)
	}
	inferencer := &recordingAdmissionProbe{}
	err := RunSessionWithRecordingDirectory(context.Background(), io.Discard, SessionRunOptions{ModelCatalog: testModelCatalog(),
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "test-key",
		ConfigDir:         t.TempDir(),
		SessionInferencer: inferencer,
	}, destination)
	if !errors.Is(err, transcript.ErrRecordingDestinationNotEmpty) || !errors.Is(err, transcript.ErrRecordingDestination) {
		t.Fatalf("error = %v, want destination identities", err)
	}
	if inferencer.connects != 0 {
		t.Fatalf("connects = %d, want zero", inferencer.connects)
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("sentinel = %q, err %v, want %q", got, readErr, want)
	}
}

func TestRunSessionWithRecordingDirectoryPublishesPublicEvidence(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "nested", "capture")
	inferencer := &scriptedSessionInferencer{events: []messages.StreamMessage{
		{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue("recording-session", "gpt-realtime")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("recorded response")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}}
	err := RunSessionWithRecordingDirectory(context.Background(), io.Discard, SessionRunOptions{ModelCatalog: testModelCatalog(),
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "test-key",
		ConfigDir:         t.TempDir(),
		SessionInferencer: inferencer,
	}, destination)
	if err != nil {
		t.Fatalf("recorded session: %v", err)
	}
	for _, name := range []string{"client.transcript.jsonl", "agent.transcript.jsonl", "manifest.json"} {
		data, readErr := os.ReadFile(filepath.Join(destination, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if len(data) == 0 {
			t.Fatalf("recording artifact %s is empty", name)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"artifacts"`) {
		t.Fatalf("manifest = %s, want published evidence metadata", manifest)
	}
}

type recordingAdmissionProbe struct{ connects int }

func (p *recordingAdmissionProbe) ConnectSession(context.Context) (messages.Session, error) {
	p.connects++
	return nil, errors.New("provider connection should not be attempted")
}
