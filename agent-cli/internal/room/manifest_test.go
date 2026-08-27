package room

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseManifest_NormalizesValidJSONAndYAMLWithoutCredentials(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")

	jsonData := validManifestData(t, func(document map[string]any) {
		document["participants"].([]any)[0].(map[string]any)["provider"] = " OPENAI "
		document["participants"].([]any)[0].(map[string]any)["voice"] = nil
	})
	manifest, err := ParseManifest(jsonData)
	if err != nil {
		t.Fatalf("ParseManifest JSON: %v", err)
	}
	if manifest.Room.MaxTurns != 3 || manifest.Room.MaxDuration != 30*time.Second {
		t.Fatalf("JSON bounds = %+v", manifest.Room)
	}
	assertNormalizedParticipants(t, manifest)

	yamlData := []byte(`schema_version: 1
room:
  max_duration: 2m
participants:
  - id: customer
    system_prompt: "Ask for a trip"
    provider: OPENAI
    model: gpt-realtime
    api_key_env: ROOM_CUSTOMER_KEY
    tools: []
  - id: assistant
    system_prompt: "Answer the customer"
    provider: openai
    model: gpt-realtime
    api_key_env: ROOM_ASSISTANT_KEY
    tools: []
`)
	manifest, err = ParseManifest(yamlData)
	if err != nil {
		t.Fatalf("ParseManifest YAML: %v", err)
	}
	if manifest.Room.MaxDuration != 2*time.Minute || manifest.Room.MaxTurns != 0 {
		t.Fatalf("YAML bounds = %+v", manifest.Room)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal normalized manifest: %v", err)
	}
	if strings.Contains(string(encoded), "customer-secret") || strings.Contains(string(encoded), "assistant-secret") || strings.Contains(string(encoded), "api_key") && strings.Contains(string(encoded), "secret") {
		t.Fatalf("normalized manifest contains credentials: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"max_duration":"2m0s"`) {
		t.Fatalf("normalized manifest does not preserve duration: %s", encoded)
	}
}

func TestParseManifest_EmptyToolsAndOmittedVoiceDoNotRequireUpstreamRegistries(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	manifest, err := ParseManifest(validManifestData(t, nil))
	if err != nil {
		t.Fatalf("tool-free manifest should validate without registries: %v", err)
	}
	for _, participant := range manifest.Participants {
		if participant.Voice != "" || participant.Tools == nil || len(participant.Tools) != 0 {
			t.Fatalf("normalized no-tool participant = %+v", participant)
		}
	}
}

func TestReadManifest_ReadsAndValidatesFile(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	path := filepath.Join(t.TempDir(), "room.yaml")
	if err := os.WriteFile(path, validManifestYAML(), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(manifest.Participants) != 2 {
		t.Fatalf("participants = %d, want 2", len(manifest.Participants))
	}
}

func TestParseManifest_RejectsMalformedShapeWithAttributedFields(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")

	tests := []struct {
		name      string
		mutate    func(map[string]any)
		field     string
		cause     error
		secretful string
	}{
		{
			name:   "unsupported schema",
			mutate: func(document map[string]any) { document["schema_version"] = 2 },
			field:  "schema_version",
			cause:  ErrUnsupportedSchema,
		},
		{
			name:   "missing room",
			mutate: func(document map[string]any) { delete(document, "room") },
			field:  "room",
			cause:  ErrMissingBound,
		},
		{
			name: "missing bounds",
			mutate: func(document map[string]any) {
				document["room"] = map[string]any{}
			},
			field: "room",
			cause: ErrMissingBound,
		},
		{
			name: "invalid turns",
			mutate: func(document map[string]any) {
				document["room"].(map[string]any)["max_turns"] = 0
			},
			field: "room.max_turns",
			cause: ErrInvalidBound,
		},
		{
			name: "invalid duration",
			mutate: func(document map[string]any) {
				document["room"] = map[string]any{"max_duration": "not-a-duration"}
			},
			field: "room.max_duration",
			cause: ErrInvalidBound,
		},
		{
			name: "too few participants",
			mutate: func(document map[string]any) {
				document["participants"] = []any{document["participants"].([]any)[0]}
			},
			field: "participants",
			cause: ErrTooFewParticipants,
		},
		{
			name: "empty participant ID",
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["id"] = "  "
			},
			field: "participants[0].id",
			cause: ErrInvalidParticipant,
		},
		{
			name: "duplicate participant ID",
			mutate: func(document map[string]any) {
				document["participants"].([]any)[1].(map[string]any)["id"] = "customer"
			},
			field: "participants[1].id",
			cause: ErrDuplicateParticipant,
		},
		{
			name: "missing prompt",
			mutate: func(document map[string]any) {
				delete(document["participants"].([]any)[1].(map[string]any), "system_prompt")
			},
			field: "participants[1].system_prompt",
			cause: ErrInvalidParticipant,
		},
		{
			name: "missing provider",
			mutate: func(document map[string]any) {
				delete(document["participants"].([]any)[1].(map[string]any), "provider")
			},
			field: "participants[1].provider",
			cause: ErrInvalidParticipant,
		},
		{
			name: "missing model",
			mutate: func(document map[string]any) {
				delete(document["participants"].([]any)[1].(map[string]any), "model")
			},
			field: "participants[1].model",
			cause: ErrInvalidParticipant,
		},
		{
			name: "missing credential name",
			mutate: func(document map[string]any) {
				delete(document["participants"].([]any)[1].(map[string]any), "api_key_env")
			},
			field: "participants[1].api_key_env",
			cause: ErrCredential,
		},
		{
			name: "missing tools list",
			mutate: func(document map[string]any) {
				delete(document["participants"].([]any)[1].(map[string]any), "tools")
			},
			field: "participants[1].tools",
			cause: ErrInvalidParticipant,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := parseFixtureError(t, test.mutate, ValidationOptions{})
			assertManifestError(t, err, test.field, test.cause)
		})
	}
}

func TestParseManifest_RejectsMissingCredentialWithoutEchoingSecret(t *testing.T) {
	const pastedSecret = "sk-pasted-secret"
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	manifestData := validManifestData(t, func(document map[string]any) {
		document["participants"].([]any)[1].(map[string]any)["api_key_env"] = pastedSecret
	})
	// The value is deliberately an env-name-shaped string but is not set. The
	// error must identify the field without reflecting the pasted credential.
	err := func() error {
		_, err := ParseManifest(manifestData)
		return err
	}()
	assertManifestError(t, err, "participants[1].api_key_env", ErrCredential)
	if strings.Contains(err.Error(), pastedSecret) {
		t.Fatalf("credential value leaked in error: %v", err)
	}
}

func TestParseManifest_UsesAvailableProviderModelToolAndVoiceRegistries(t *testing.T) {
	options := NewValidationRegistry(
		[]string{"openai"},
		map[string][]string{"openai": {"gpt-realtime"}},
		[]string{"sleep"},
		map[string][]string{"openai": {"alloy"}},
	).Options()
	options.LookupCredential = func(string) (string, bool) { return "secret-that-must-not-escape", true }
	valid := validManifestData(t, func(document map[string]any) {
		for _, raw := range document["participants"].([]any) {
			raw.(map[string]any)["provider"] = "openai"
			raw.(map[string]any)["model"] = "gpt-realtime"
			raw.(map[string]any)["tools"] = []any{"sleep"}
			raw.(map[string]any)["voice"] = "alloy"
		}
	})
	if _, err := ParseManifest(valid, options); err != nil {
		t.Fatalf("registered manifest: %v", err)
	}

	tests := []struct {
		name   string
		field  string
		cause  error
		mutate func(map[string]any)
	}{
		{
			name:  "provider",
			field: "participants[1].provider",
			cause: ErrUnknownProvider,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[1].(map[string]any)["provider"] = "unknown-provider"
			},
		},
		{
			name:  "model",
			field: "participants[1].model",
			cause: ErrUnknownModel,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[1].(map[string]any)["model"] = "unknown-model"
			},
		},
		{
			name:  "tool",
			field: "participants[1].tools[0]",
			cause: ErrUnknownTool,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[1].(map[string]any)["tools"] = []any{"unknown-tool"}
			},
		},
		{
			name:  "voice",
			field: "participants[1].voice",
			cause: ErrUnknownVoice,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[1].(map[string]any)["voice"] = "unknown-voice"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := parseFixtureError(t, test.mutate, options)
			assertManifestError(t, err, test.field, test.cause)
			if strings.Contains(err.Error(), "secret-that-must-not-escape") {
				t.Fatalf("credential leaked in registry error: %v", err)
			}
		})
	}
}

func TestParseManifest_RejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	unknown := append(validManifestYAML(), []byte("unknown: sk-secret\n")...)
	if _, err := ParseManifest(unknown); err == nil || !errors.Is(err, ErrInvalidDocument) || !strings.Contains(err.Error(), "document") {
		t.Fatalf("unknown field error = %v", err)
	}
	multiple := append(validManifestYAML(), []byte("---\nschema_version: 1\n")...)
	if _, err := ParseManifest(multiple); err == nil || !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("multiple document error = %v", err)
	}
}

func TestManifestValidate_RejectsNilToolsInDirectNormalizedValue(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		Room:          Room{MaxTurns: 1},
		Participants: []Participant{
			{ID: "a", SystemPrompt: "a", Provider: "openai", Model: "gpt", APIKeyEnv: "A"},
			{ID: "b", SystemPrompt: "b", Provider: "openai", Model: "gpt", APIKeyEnv: "B", Tools: []string{}},
		},
	}
	t.Setenv("A", "a")
	t.Setenv("B", "b")
	err := manifest.Validate()
	assertManifestError(t, err, "participants[0].tools", ErrInvalidParticipant)
}

func validManifestYAML() []byte {
	return []byte(`schema_version: 1
room:
  max_turns: 3
  max_duration: 30s
participants:
  - id: customer
    system_prompt: "Ask for a trip"
    provider: openai
    model: gpt-realtime
    api_key_env: ROOM_CUSTOMER_KEY
    tools: []
  - id: assistant
    system_prompt: "Answer the customer"
    provider: openai
    model: gpt-realtime
    api_key_env: ROOM_ASSISTANT_KEY
    tools: []
`)
}

func validManifestData(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	document := map[string]any{
		"schema_version": 1,
		"room": map[string]any{
			"max_turns":    3,
			"max_duration": "30s",
		},
		"participants": []any{
			map[string]any{
				"id":            "customer",
				"system_prompt": "Ask for a trip",
				"provider":      "openai",
				"model":         "gpt-realtime",
				"api_key_env":   "ROOM_CUSTOMER_KEY",
				"tools":         []any{},
			},
			map[string]any{
				"id":            "assistant",
				"system_prompt": "Answer the customer",
				"provider":      "openai",
				"model":         "gpt-realtime",
				"api_key_env":   "ROOM_ASSISTANT_KEY",
				"tools":         []any{},
			},
		},
	}
	if mutate != nil {
		mutate(document)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal manifest fixture: %v", err)
	}
	return data
}

func parseFixtureError(t *testing.T, mutate func(map[string]any), options ValidationOptions) error {
	t.Helper()
	data := validManifestData(t, mutate)
	_, err := ParseManifest(data, options)
	return err
}

func assertManifestError(t *testing.T, err error, field string, cause error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error for %s", field)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, cause)
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if validationErr.Field != field {
		t.Fatalf("error field = %q, want %q", validationErr.Field, field)
	}
}

func assertNormalizedManifest(t *testing.T, manifest Manifest) {
	t.Helper()
	if manifest.SchemaVersion != SchemaVersion || manifest.Room.MaxTurns != 3 || manifest.Room.MaxDuration != 30*time.Second {
		t.Fatalf("normalized manifest header = %+v", manifest)
	}
	assertNormalizedParticipants(t, manifest)
}

func assertNormalizedParticipants(t *testing.T, manifest Manifest) {
	t.Helper()
	if len(manifest.Participants) != 2 || manifest.Participants[0].Provider != "openai" {
		t.Fatalf("normalized participants = %+v", manifest.Participants)
	}
	for _, participant := range manifest.Participants {
		if participant.APIKeyEnv == "" || strings.Contains(participant.APIKeyEnv, "secret") {
			t.Fatalf("normalized participant credential field = %+v", participant)
		}
	}
}
