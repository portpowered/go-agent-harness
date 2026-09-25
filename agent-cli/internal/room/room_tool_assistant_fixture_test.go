package room

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const roomToolAssistantProofPath = "/tmp/room-proof-s2s-room-tool-wielding-participants.txt"

func TestReadManifest_ToolAssistantFixtureDefinesBoundedIsolatedParticipants(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return the test source path")
	}
	manifestPath := filepath.Join(filepath.Dir(sourcePath), "..", "..", "docs", "room-tool-assistant.json")
	manifest, err := ReadManifest(manifestPath, ValidationOptions{
		LookupCredential: func(name string) (string, bool) {
			return "fixture-credential", name == fixtureCredentialEnv
		},
	})
	if err != nil {
		t.Fatalf("ReadManifest(%q): %v", manifestPath, err)
	}

	if manifest.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", manifest.SchemaVersion, SchemaVersion)
	}
	if manifest.Room.MaxTurns != 2 || manifest.Room.MaxDuration != 60*time.Second {
		t.Fatalf("room bounds = %+v, want max_turns=2 and max_duration=60s", manifest.Room)
	}
	if len(manifest.Participants) != 2 {
		t.Fatalf("participants = %d, want exactly two", len(manifest.Participants))
	}

	customer := manifest.Participants[0]
	if customer.ID != "customer" || customer.Provider != fixtureProvider || customer.Model != "gpt-realtime-2.1-mini" || customer.APIKeyEnv != fixtureCredentialEnv || customer.Voice != "marin" {
		t.Fatalf("customer participant = %+v", customer)
	}
	if customer.Tools == nil || len(customer.Tools) != 0 {
		t.Fatalf("customer tools = %#v, want explicit empty list", customer.Tools)
	}
	if !strings.Contains(customer.SystemPrompt, "no tools") {
		t.Fatalf("customer system prompt = %q, want an explicit no-tools instruction", customer.SystemPrompt)
	}
	if !strings.Contains(customer.OpeningPrompt, roomToolAssistantProofPath) || !strings.Contains(customer.OpeningPrompt, "ROOMPROOF") || !strings.Contains(customer.OpeningPrompt, "exec") {
		t.Fatalf("customer opening prompt = %q, want proof path, content, and exec request", customer.OpeningPrompt)
	}

	assistant := manifest.Participants[1]
	if assistant.ID != "assistant" || assistant.Provider != fixtureProvider || assistant.Model != "gpt-realtime-2.1-mini" || assistant.APIKeyEnv != fixtureCredentialEnv || assistant.Voice != "cedar" {
		t.Fatalf("assistant participant = %+v", assistant)
	}
	if len(assistant.Tools) != 1 || assistant.Tools[0] != "exec" {
		t.Fatalf("assistant tools = %#v, want [exec]", assistant.Tools)
	}
	if !strings.Contains(assistant.SystemPrompt, roomToolAssistantProofPath) || !strings.Contains(assistant.SystemPrompt, "ROOMPROOF") || !strings.Contains(assistant.SystemPrompt, "exactly once") || !strings.Contains(assistant.SystemPrompt, "exec") {
		t.Fatalf("assistant system prompt = %q, want one exact exec proof instruction", assistant.SystemPrompt)
	}

	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("fixture disappeared after loading: %v", err)
	}
}

const (
	fixtureCredentialEnv = "OPENAI_API_KEY"
	fixtureProvider      = "openai"
)
