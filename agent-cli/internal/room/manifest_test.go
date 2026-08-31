package room

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	yamlv3 "gopkg.in/yaml.v3"
)

func TestParseManifest_NormalizesValidJSONAndYAMLWithoutCredentials(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")

	jsonData := validManifestData(t, func(document map[string]any) {
		document["participants"].([]any)[0].(map[string]any)["provider"] = " OPENAI "
		document["participants"].([]any)[0].(map[string]any)["voice"] = nil
		document["participants"].([]any)[0].(map[string]any)["opening_prompt"] = "  Start the room  "
	})
	manifest, err := ParseManifest(jsonData)
	if err != nil {
		t.Fatalf("ParseManifest JSON: %v", err)
	}
	if manifest.Room.MaxTurns != 3 || manifest.Room.MaxDuration != 30*time.Second {
		t.Fatalf("JSON bounds = %+v", manifest.Room)
	}
	assertNormalizedParticipants(t, manifest)
	if manifest.Participants[0].OpeningPrompt != "Start the room" {
		t.Fatalf("opening prompt = %q, want normalized prompt", manifest.Participants[0].OpeningPrompt)
	}

	yamlData := []byte(`schema_version: 1
room:
  max_duration: 2m
participants:
  - id: customer
    system_prompt: "Ask for a trip"
    opening_prompt: "Ask for a trip to start the room"
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
	if strings.Count(string(encoded), "opening_prompt") != 1 {
		t.Fatalf("YAML fixture opening prompt count changed unexpectedly: %s", encoded)
	}
	if manifest.Participants[1].OpeningPrompt != "" {
		t.Fatalf("assistant unexpectedly gained an opening prompt: %+v", manifest.Participants[1])
	}
	yamlEncoded, err := yamlv3.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal normalized YAML: %v", err)
	}
	if !strings.Contains(string(yamlEncoded), "max_duration: 2m0s") || strings.Contains(string(yamlEncoded), "customer-secret") || strings.Contains(string(yamlEncoded), "assistant-secret") {
		t.Fatalf("normalized YAML bounds or credentials = %s", yamlEncoded)
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

// TestParseManifest_RejectsAllAgentRoomWithNoDesignatedOpener guards the
// silent-all-agent-room defect directly at the manifest-validation choke
// point: a room with only agent participants and no opening_prompt anywhere
// has nobody to speak first, so every participant waits for someone else and
// the room idles until its bound expires — zero turns, zero audio, but no
// upfront error. Catching this here, before ResolveRoomLaunchPlan or
// RunRoomWithResult ever dial a provider, is what stops the run from
// burning real provider money before reporting the outcome.
func TestParseManifest_RejectsAllAgentRoomWithNoDesignatedOpener(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	manifestData := validManifestData(t, func(document map[string]any) {
		// The shared fixture normally designates "customer" as the opener;
		// strip it so neither participant has one.
		delete(document["participants"].([]any)[0].(map[string]any), "opening_prompt")
	})
	_, err := ParseManifest(manifestData)
	assertManifestError(t, err, "participants", ErrNoRoomOpener)
	if !strings.Contains(err.Error(), "opening_prompt") {
		t.Fatalf("error = %v, want actionable guidance naming opening_prompt", err)
	}
}

// TestParseManifest_HumanParticipantExemptsAllAgentOpenerRequirement confirms
// the check does not over-trigger: a human participant can always speak
// first on their own initiative, so a manifest that pairs one with an agent
// remains valid even though no participant sets opening_prompt.
func TestParseManifest_HumanParticipantExemptsAllAgentOpenerRequirement(t *testing.T) {
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	manifestData := []byte(`{
  "schema_version": 1,
  "room": {"max_turns": 1},
  "participants": [
    {"kind": "human", "id": "customer", "system_prompt": "Human customer", "input_device": "fake:input", "output_device": "fake:output", "tools": []},
    {"id": "assistant", "system_prompt": "Answer", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_ASSISTANT_KEY", "tools": []}
  ]
}`)
	if _, err := ParseManifest(manifestData); err != nil {
		t.Fatalf("ParseManifest with human participant: %v", err)
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

// TestParseManifest_UnknownParticipantFieldErrorNeverLeaksInternalTypeName
// guards against a Go implementation type name reaching user-facing text.
// yaml.v3's KnownFields(true) formats an unrecognized field as `field X not
// found in type room.manifestParticipant` -- room.manifestParticipant is
// this package's unexported decode-target struct, never something a
// manifest author wrote or should ever see. sanitizeManifestDecodeError
// must rewrite it to name the manifest section instead (here,
// "a participant").
func TestParseManifest_UnknownParticipantFieldErrorNeverLeaksInternalTypeName(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	withUnknownParticipantField := strings.Replace(
		string(validManifestYAML()),
		"  - id: customer\n",
		"  - id: customer\n    bogus_field: 1\n",
		1,
	)
	_, err := ParseManifest([]byte(withUnknownParticipantField))
	if err == nil || !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("unknown participant field error = %v, want ErrInvalidDocument", err)
	}
	if strings.Contains(err.Error(), "manifestParticipant") {
		t.Fatalf("error leaked the internal Go type name: %v", err)
	}
	if !strings.Contains(err.Error(), "a participant") {
		t.Fatalf("error = %v, want it to still name the manifest section (\"a participant\")", err)
	}
}

// TestSanitizeManifestDecodeError_LeavesUnrelatedTextUnchanged is the
// no-over-triggering guard for the same fix: an error with no internal
// "type room.X" fragment (the overwhelming majority of manifest validation
// errors) must pass through byte-for-byte.
func TestSanitizeManifestDecodeError_LeavesUnrelatedTextUnchanged(t *testing.T) {
	err := errors.New(`participants[0].api_key_env: environment variable is unset or empty`)
	if got := sanitizeManifestDecodeError(err); got != err.Error() {
		t.Fatalf("sanitizeManifestDecodeError(%q) = %q, want it unchanged", err.Error(), got)
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

func TestParseManifest_RequiresDeviceSelectorsForHumanParticipants(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{
			name:  "input device",
			field: "participants[0].input_device",
			mutate: func(document map[string]any) {
				participant := document["participants"].([]any)[0].(map[string]any)
				participant["kind"] = "human"
				delete(participant, "provider")
				delete(participant, "model")
				delete(participant, "api_key_env")
				participant["output_device"] = "fake:output"
			},
		},
		{
			name:  "output device",
			field: "participants[0].output_device",
			mutate: func(document map[string]any) {
				participant := document["participants"].([]any)[0].(map[string]any)
				participant["kind"] = "human"
				delete(participant, "provider")
				delete(participant, "model")
				delete(participant, "api_key_env")
				participant["input_device"] = "fake:input"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := parseFixtureError(t, test.mutate, ValidationOptions{})
			assertManifestError(t, err, test.field, ErrInvalidParticipant)
		})
	}
}

func TestParseManifest_PreservesRecordingPolicyAndDestination(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	manifest, err := ParseManifest(validManifestData(t, func(document map[string]any) {
		document["room"].(map[string]any)["recording"] = map[string]any{
			"enabled":   true,
			"directory": "  /tmp/room-evidence  ",
		}
	}))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if !manifest.Room.RecordingEnabled() || manifest.Room.RecordingDirectory() != "/tmp/room-evidence" {
		t.Fatalf("recording policy = %+v, want enabled destination", manifest.Room.Recording)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if !strings.Contains(string(encoded), `"recording"`) || !strings.Contains(string(encoded), `"directory":"/tmp/room-evidence"`) {
		t.Fatalf("encoded recording policy = %s, want normalized directory spelling", encoded)
	}
}

func TestParseManifest_RecordingDisabledDoesNotCreateEvidencePolicy(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	manifest, err := ParseManifest(validManifestData(t, func(document map[string]any) {
		document["room"].(map[string]any)["recording"] = map[string]any{"enabled": false}
	}))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Room.Recording == nil || manifest.Room.Recording.Enabled == nil || manifest.Room.RecordingEnabled() {
		t.Fatalf("recording policy = %+v, want explicit disabled policy", manifest.Room.Recording)
	}
	if got := manifest.Room.RecordingDirectory(); got != "" {
		t.Fatalf("disabled recording destination = %q, want empty", got)
	}
}

func TestParseManifest_RejectsRecordingDestinationWhenDisabled(t *testing.T) {
	err := parseFixtureError(t, func(document map[string]any) {
		document["room"].(map[string]any)["recording"] = map[string]any{
			"enabled":   false,
			"directory": "room-evidence",
		}
	}, ValidationOptions{LookupCredential: func(string) (string, bool) { return "secret", true }})
	assertManifestError(t, err, "room.recording.directory", ErrInvalidRecording)
}

func validManifestYAML() []byte {
	return []byte(`schema_version: 1
room:
  max_turns: 3
  max_duration: 30s
participants:
  - id: customer
    system_prompt: "Ask for a trip"
    opening_prompt: "Ask for a trip"
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
				"id":             "customer",
				"system_prompt":  "Ask for a trip",
				"opening_prompt": "Ask for a trip",
				"provider":       "openai",
				"model":          "gpt-realtime",
				"api_key_env":    "ROOM_CUSTOMER_KEY",
				"tools":          []any{},
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
