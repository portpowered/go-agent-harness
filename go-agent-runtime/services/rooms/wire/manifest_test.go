package wire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const wireManifestJSON = `{
  "schema_version": 1,
  "room": {"max_turns": 2, "max_duration": "1m"},
  "participants": [
    {
      "id": " opener ",
      "system_prompt": " Start the room ",
      "opening_prompt": " Begin ",
      "provider": " OPENAI ",
      "model": " gpt-realtime ",
      "api_key_env": " ROOM_KEY ",
      "voice": " alloy ",
      "tools": [" SLEEP "]
    },
    {
      "id": " responder ",
      "system_prompt": " Answer the room ",
      "provider": " openai ",
      "model": " gpt-realtime ",
      "api_key_env": "ROOM_KEY",
      "tools": []
    }
  ]
}`

func TestManifestConstructorsAndRegistryValidation(t *testing.T) {
	registry := registryForWireTest(t)
	manifest := registryProviderForWireTest(t)
	providerAliasesForWireTest(t, registry, manifest)
}

func registryForWireTest(t *testing.T) rooms.ValidationRegistry {
	t.Helper()
	providers := []string{" OPENAI "}
	models := map[string][]string{" OPENAI ": {" gpt-realtime "}}
	tools := []string{" SLEEP "}
	voices := map[string][]string{" OPENAI ": {" alloy "}}
	registry := NewValidationRegistry(providers, models, tools, voices)

	requireRegistryEntry(t, registry.Providers, "openai", "provider")
	requireNestedRegistryEntry(t, registry.Models, "openai", "gpt-realtime", "model")
	requireRegistryEntry(t, registry.Tools, "sleep", "tool")
	requireNestedRegistryEntry(t, registry.Voices, "openai", "alloy", "voice")

	providers[0] = "mutated-provider"
	models[" OPENAI "][0] = "mutated-model"
	tools[0] = "mutated-tool"
	voices[" OPENAI "][0] = "mutated-voice"
	requireRegistryEntry(t, registry.Providers, "openai", "copied provider")
	requireNestedRegistryEntry(t, registry.Models, "openai", "gpt-realtime", "copied model")
	requireRegistryEntry(t, registry.Tools, "sleep", "copied tool")
	requireNestedRegistryEntry(t, registry.Voices, "openai", "alloy", "copied voice")

	emptyRegistry := NewValidationRegistry(nil, nil, nil, nil)
	if emptyRegistry.Providers != nil || emptyRegistry.Models != nil || emptyRegistry.Tools != nil || emptyRegistry.Voices != nil {
		t.Fatalf("nil registry inputs = %#v, want nil maps", emptyRegistry)
	}
	return registry
}

func registryProviderForWireTest(t *testing.T) rooms.Manifest {
	t.Helper()
	lookupCredential := func(name string) (string, bool) {
		if name == "ROOM_KEY" {
			return "secret-value-that-must-not-be-retained", true
		}
		return "", false
	}
	registry := NewValidationRegistry([]string{"openai"}, map[string][]string{"openai": {"gpt-realtime"}}, []string{"sleep"}, map[string][]string{"openai": {"alloy"}})
	provider := NewManifestProviderFromRegistry(registry, lookupCredential)
	delete(registry.Providers, "openai")
	delete(registry.Models, "openai")
	delete(registry.Tools, "sleep")
	delete(registry.Voices, "openai")

	manifest, err := provider.Parse([]byte(wireManifestJSON))
	requireNoError(t, err, "Parse")
	if manifest.SchemaVersion != rooms.SchemaVersion || manifest.Room.MaxTurns != 2 || manifest.Room.MaxDuration != time.Minute {
		t.Fatalf("normalized manifest = %#v", manifest)
	}
	if got := manifest.Participants[0].Provider; got != "openai" {
		t.Fatalf("normalized provider = %q, want openai", got)
	}
	if got := manifest.Participants[0].Tools; len(got) != 1 || got[0] != "sleep" {
		t.Fatalf("normalized tools = %#v, want [sleep]", got)
	}
	requireNoError(t, provider.Validate(manifest), "Validate")
	_, err = provider.Admit([]byte(wireManifestJSON))
	requireNoError(t, err, "Admit")
	requireNoError(t, provider.Close(), "Close")

	unknownTool := strings.Replace(wireManifestJSON, `" SLEEP "`, `" MISSING "`, 1)
	_, err = provider.Parse([]byte(unknownTool))
	requireValidationError(t, err, rooms.ErrUnknownTool, "participants[0].tools[0]", "unknown-tool")

	missingCredential := strings.Replace(wireManifestJSON, `"ROOM_KEY"`, `"MISSING_KEY"`, 1)
	_, err = provider.Parse([]byte(missingCredential))
	requireValidationError(t, err, rooms.ErrCredential, "participants[1].api_key_env", "missing-credential")
	if strings.Contains(err.Error(), "secret-value-that-must-not-be-retained") {
		t.Fatalf("credential value leaked in error: %v", err)
	}
	return manifest
}

func providerAliasesForWireTest(t *testing.T, registry rooms.ValidationRegistry, manifest rooms.Manifest) {
	t.Helper()
	optionsProvider := NewManifestProvider(registry.Options())
	_, err := optionsProvider.Parse([]byte(wireManifestJSON))
	requireNoError(t, err, "NewManifestProvider Parse")
	descriptiveProvider := NewManifestAdmission(registry.Options())
	_, err = descriptiveProvider.Admit([]byte(wireManifestJSON))
	requireNoError(t, err, "NewManifestAdmission Admit")
	lookupCredential := func(string) (string, bool) { return "secret", true }
	registryProvider := NewManifestAdmissionFromRegistry(registry, lookupCredential)
	manifestPath := filepath.Join(t.TempDir(), "room.json")
	requireNoError(t, os.WriteFile(manifestPath, []byte(wireManifestJSON), 0o600), "write manifest")
	_, err = registryProvider.Read(manifestPath)
	requireNoError(t, err, "Read")
	_, err = registryProvider.AdmitFile(manifestPath)
	requireNoError(t, err, "AdmitFile")
	invalid := manifest
	invalid.Room.MaxTurns = -1
	if err := registryProvider.Validate(invalid); !errors.Is(err, rooms.ErrInvalidBound) {
		t.Fatalf("invalid Validate() error = %v, want invalid bound", err)
	}
	requireNoError(t, registryProvider.Close(), "registry provider Close")
}

func requireRegistryEntry(t *testing.T, values map[string]struct{}, key, label string) {
	t.Helper()
	if _, ok := values[key]; !ok {
		t.Fatalf("%s registry = %#v, want %q", label, values, key)
	}
}

func requireNestedRegistryEntry(t *testing.T, values map[string]map[string]struct{}, provider, key, label string) {
	t.Helper()
	if _, ok := values[provider][key]; !ok {
		t.Fatalf("%s registry = %#v, want %s/%s", label, values, provider, key)
	}
}

func requireNoError(t *testing.T, err error, label string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s() error = %v", label, err)
	}
}

func requireValidationError(t *testing.T, err, cause error, field, label string) {
	t.Helper()
	var validationErr *rooms.ValidationError
	if err == nil || !errors.Is(err, cause) || !errors.As(err, &validationErr) || validationErr.Field != field {
		t.Fatalf("%s error = %v, want typed %s error", label, err, field)
	}
}

func TestOtherRoomWireConstructorsRemainInert(t *testing.T) {
	if NewService(Dependencies{}) == nil {
		t.Fatal("NewService() returned nil")
	}
	if NewLatencyService() == nil {
		t.Fatal("NewLatencyService() returned nil")
	}
	factory := NewMediaFactory(nil)
	if factory == nil {
		t.Fatal("NewMediaFactory() returned nil")
	}
	if _, err := factory.OpenMedia(context.Background(), rooms.Participant{}, rooms.AudioFormat{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("inert media factory error = %v, want service unavailable", err)
	}
}
