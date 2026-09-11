package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
)

const (
	candidateRevisionEnvironment = "C54_CANDIDATE_REVISION"
	sourceRevisionEnvironment    = "C54_SOURCE_REVISION"
	firstCredentialName          = "C54_FIRST_KEY"
	secondCredentialName         = "C54_SECOND_KEY"
	missingCredentialName        = "C54_MISSING_KEY"
	secretValue                  = "c54-secret-value-must-not-escape"
)

const jsonFixture = `{
  "schema_version": 1,
  "room": {
    "max_turns": 3,
    "max_duration": " 30s ",
    "recording": {"enabled": true, "directory": "  /tmp/c54-evidence  "}
  },
  "participants": [
    {
      "kind": " AGENT ",
      "id": " alpha ",
      "system_prompt": " Start the room ",
      "opening_prompt": "  Start the room  ",
      "provider": " OPENAI ",
      "model": " gpt-realtime ",
      "api_key_env": " C54_FIRST_KEY ",
      "voice": " alloy ",
      "tools": [" SLEEP "],
      "browserTools": {
        "backend": " webmcp ",
        "connection": {
          "cdp_url": " http://127.0.0.1:9222/json/version?token=cdp-secret#fragment-secret ",
          "ws_endpoint": " ws://127.0.0.1:9222/devtools/browser/browser-secret?token=ws-secret#fragment-secret ",
          "user_data_dir": " /tmp/c54-browser ",
          "allow_process_scan": true,
          "allow_remote_cdp": false
        },
        "selection": {
          "browser": " browser-1 ",
          "tab": " tab-1 ",
          "origin": " https://cube.example ",
          "auto_select": " single ",
          "activate_tab": true,
          "persist": false
        },
        "policy": {
          "allowed_origins": [" https://cube.example ", "https://docs.example"],
          "denied_origins": ["https://blocked.example"],
          "approval": " always ",
          "cancel_on_interrupt": " always "
        },
        "limits": {
          "invocation_timeout": " 2m30s ",
          "max_input_bytes": 1024,
          "max_result_bytes": 2048,
          "serialize_per_target": false
        },
        "recording": {
          "enabled": true,
          "include_arguments": false,
          "include_results": false,
          "redact_url_query": true,
          "redact_url_fragment": true
        },
        "replay": {"path": " /tmp/c54-replay.jsonl ", "strict": false}
      }
    },
    {
      "id": " beta ",
      "system_prompt": " Answer the room ",
      "provider": " openai ",
      "model": " gpt-realtime ",
      "api_key_env": " C54_SECOND_KEY ",
      "voice": " alloy ",
      "tools": ["sleep"]
    }
  ]
}`

const yamlFixture = `schema_version: 1
room:
  max_turns: 3
  max_duration: 30s
  recording:
    enabled: true
    directory: "  /tmp/c54-evidence  "
participants:
  - kind: agent
    id: alpha
    system_prompt: "Start the room"
    opening_prompt: "Start the room"
    provider: openai
    model: gpt-realtime
    api_key_env: C54_FIRST_KEY
    voice: alloy
    tools: [sleep]
    browserTools:
      backend: webmcp
      connection:
        cdp_url: "http://127.0.0.1:9222/json/version?token=cdp-secret#fragment-secret"
        ws_endpoint: "ws://127.0.0.1:9222/devtools/browser/browser-secret?token=ws-secret#fragment-secret"
        user_data_dir: /tmp/c54-browser
        allow_process_scan: true
        allow_remote_cdp: false
      selection:
        browser: browser-1
        tab: tab-1
        origin: https://cube.example
        auto_select: single
        activate_tab: true
        persist: false
      policy:
        allowed_origins: [https://cube.example, https://docs.example]
        denied_origins: [https://blocked.example]
        approval: always
        cancel_on_interrupt: always
      limits:
        invocation_timeout: 2m30s
        max_input_bytes: 1024
        max_result_bytes: 2048
        serialize_per_target: false
      recording:
        enabled: true
        include_arguments: false
        include_results: false
        redact_url_query: true
        redact_url_fragment: true
      replay:
        path: /tmp/c54-replay.jsonl
        strict: false
  - id: beta
    system_prompt: "Answer the room"
    provider: openai
    model: gpt-realtime
    api_key_env: C54_SECOND_KEY
    voice: alloy
    tools: [sleep]
`

const defaultsFixture = `{
  "schema_version": 1,
  "room": {"max_turns": 1},
  "participants": [
    {"id":"alpha","system_prompt":"Start","opening_prompt":"Start","provider":"openai","model":"gpt-realtime","api_key_env":"C54_FIRST_KEY","tools":[],"browserTools":{}},
    {"id":"beta","system_prompt":"Answer","provider":"openai","model":"gpt-realtime","api_key_env":"C54_SECOND_KEY","tools":[]}
  ]
}`

type report struct {
	Schema                   string           `json:"schema"`
	ConstructedVia           string           `json:"constructed_via"`
	CandidateRevision        string           `json:"candidate_revision,omitempty"`
	SourceRevision           string           `json:"source_revision,omitempty"`
	JSONYAMLEqual            bool             `json:"json_yaml_equal"`
	FileAdmission            bool             `json:"file_admission"`
	AdmitAliases             bool             `json:"admit_aliases"`
	RegistrySnapshotIsolated bool             `json:"registry_snapshot_isolated"`
	Normalized               normalizedReport `json:"normalized"`
	TypedErrors              typedErrorReport `json:"typed_errors"`
	Redaction                redactionReport  `json:"redaction"`
	Lifecycle                lifecycleReport  `json:"lifecycle"`
}

type normalizedReport struct {
	SchemaVersion       int      `json:"schema_version"`
	MaxTurns            int      `json:"max_turns"`
	MaxDuration         string   `json:"max_duration"`
	RecordingDirectory  string   `json:"recording_directory"`
	ParticipantKinds    []string `json:"participant_kinds"`
	ParticipantIDs      []string `json:"participant_ids"`
	Provider            string   `json:"provider"`
	Model               string   `json:"model"`
	Tools               []string `json:"tools"`
	BrowserCDPURL       string   `json:"browser_cdp_url"`
	BrowserWSPath       string   `json:"browser_ws_path"`
	BrowserTimeout      string   `json:"browser_timeout"`
	BrowserMaxInput     int      `json:"browser_max_input"`
	BrowserMaxResult    int      `json:"browser_max_result"`
	BrowserDefaultMatch bool     `json:"browser_default_match"`
}

type typedErrorReport struct {
	MissingCredentialCause   bool   `json:"missing_credential_cause"`
	MissingCredentialField   string `json:"missing_credential_field"`
	MissingCredentialValue   string `json:"missing_credential_value"`
	MissingCredentialProblem string `json:"missing_credential_problem"`
	UnknownNestedCause       bool   `json:"unknown_nested_cause"`
	UnknownNestedField       string `json:"unknown_nested_field"`
	UnsafeEndpointCause      bool   `json:"unsafe_endpoint_cause"`
	UnsafeEndpointField      string `json:"unsafe_endpoint_field"`
	NoOpenerCause            bool   `json:"no_opener_cause"`
	MultipleDocumentCause    bool   `json:"multiple_document_cause"`
}

type redactionReport struct {
	SerializedSecretFree  bool `json:"serialized_secret_free"`
	ErrorSecretFree       bool `json:"error_secret_free"`
	EndpointQueryRemoved  bool `json:"endpoint_query_removed"`
	WebsocketPathRedacted bool `json:"websocket_path_redacted"`
}

type lifecycleReport struct {
	Validated bool `json:"validated"`
	Closed    bool `json:"closed"`
}

func credentialLookup(name string) (string, bool) {
	switch name {
	case firstCredentialName, secondCredentialName:
		return secretValue, true
	default:
		return "", false
	}
}

func newProvider(registry rooms.ValidationRegistry) roomwire.ManifestAdmission {
	return roomwire.NewManifestProviderFromRegistry(registry, credentialLookup)
}

func run() (report, error) {
	registry := roomwire.NewValidationRegistry(
		[]string{"openai"},
		map[string][]string{"openai": {"gpt-realtime"}},
		[]string{"sleep"},
		map[string][]string{"openai": {"alloy"}},
	)
	provider := newProvider(registry)
	if provider == nil {
		return report{}, errors.New("rooms/wire returned a nil manifest provider")
	}
	delete(registry.Providers, "openai")

	jsonManifest, err := provider.Parse([]byte(jsonFixture))
	if err != nil {
		return report{}, fmt.Errorf("parse JSON fixture: %w", err)
	}
	yamlManifest, err := provider.Parse([]byte(yamlFixture))
	if err != nil {
		return report{}, fmt.Errorf("parse YAML fixture: %w", err)
	}
	if !reflect.DeepEqual(jsonManifest, yamlManifest) {
		return report{}, fmt.Errorf("JSON and YAML normalized manifests differ")
	}

	filePath := filepath.Join(os.TempDir(), "audio-runtime-c54-room-document-admission.yaml")
	if err := os.WriteFile(filePath, []byte(yamlFixture), 0o600); err != nil {
		return report{}, fmt.Errorf("write temporary manifest: %w", err)
	}
	defer os.Remove(filePath)
	fileManifest, err := provider.Read(filePath)
	if err != nil {
		return report{}, fmt.Errorf("read file fixture: %w", err)
	}
	if !reflect.DeepEqual(fileManifest, yamlManifest) {
		return report{}, fmt.Errorf("file admission changed normalized manifest")
	}
	admitted, err := provider.Admit([]byte(jsonFixture))
	if err != nil {
		return report{}, fmt.Errorf("admit alias: %w", err)
	}
	fileAdmitted, err := provider.AdmitFile(filePath)
	if err != nil {
		return report{}, fmt.Errorf("admit file alias: %w", err)
	}
	if !reflect.DeepEqual(admitted, jsonManifest) || !reflect.DeepEqual(fileAdmitted, yamlManifest) {
		return report{}, errors.New("admission aliases changed the public value")
	}
	if err := provider.Validate(admitted); err != nil {
		return report{}, fmt.Errorf("provider validation: %w", err)
	}

	defaults, err := provider.Parse([]byte(defaultsFixture))
	if err != nil {
		return report{}, fmt.Errorf("parse browser-default fixture: %w", err)
	}
	if defaults.Participants[0].BrowserTools == nil || !reflect.DeepEqual(*defaults.Participants[0].BrowserTools, rooms.BrowserToolsDefaults{}.Config()) {
		return report{}, errors.New("browser empty-object defaults differ from the public runtime defaults")
	}

	missingData := strings.Replace(jsonFixture, secondCredentialName, missingCredentialName, 1)
	_, missingErr := provider.Parse([]byte(missingData))
	missingTyped := typedError(missingErr)
	if missingErr == nil || !errors.Is(missingErr, rooms.ErrCredential) || missingTyped == nil || missingTyped.Field != "participants[1].api_key_env" || missingTyped.Value != "" || strings.Contains(missingErr.Error(), secretValue) {
		return report{}, fmt.Errorf("credential redaction/type oracle failed: %v", missingErr)
	}

	unknownData := strings.Replace(jsonFixture, `"cdp_url":`, `"unknown_nested": true, "cdp_url":`, 1)
	_, unknownErr := provider.Parse([]byte(unknownData))
	unknownTyped := typedError(unknownErr)
	if unknownErr == nil || !errors.Is(unknownErr, rooms.ErrInvalidDocument) || unknownTyped == nil || unknownTyped.Field != "document" || strings.Contains(unknownErr.Error(), "manifestBrowserConnection") || !strings.Contains(unknownErr.Error(), "a participant's browserTools") {
		return report{}, fmt.Errorf("unknown nested-field oracle failed: %v", unknownErr)
	}

	unsafeData := strings.Replace(jsonFixture, "http://127.0.0.1:9222", "file:///tmp", 1)
	_, unsafeErr := provider.Parse([]byte(unsafeData))
	unsafeTyped := typedError(unsafeErr)
	if unsafeErr == nil || !errors.Is(unsafeErr, rooms.ErrInvalidBrowserEndpoint) || unsafeTyped == nil || unsafeTyped.Field != "participants[0].browserTools.connection.cdp_url" || strings.Contains(unsafeErr.Error(), secretValue) {
		return report{}, fmt.Errorf("unsafe endpoint oracle failed: %v", unsafeErr)
	}

	noOpenerData := strings.Replace(jsonFixture, `"opening_prompt": "  Start the room  ",`, "", 1)
	_, noOpenerErr := provider.Parse([]byte(noOpenerData))
	noOpenerTyped := typedError(noOpenerErr)
	if noOpenerErr == nil || !errors.Is(noOpenerErr, rooms.ErrNoRoomOpener) || noOpenerTyped == nil || noOpenerTyped.Field != "participants" {
		return report{}, fmt.Errorf("no-opener oracle failed: %v", noOpenerErr)
	}

	_, multipleErr := provider.Parse(append([]byte(jsonFixture), []byte("\n---\nschema_version: 1\n")...))
	multipleTyped := typedError(multipleErr)
	if multipleErr == nil || !errors.Is(multipleErr, rooms.ErrInvalidDocument) || multipleTyped == nil || multipleTyped.Field != "document" {
		return report{}, fmt.Errorf("multiple-document oracle failed: %v", multipleErr)
	}

	serialized, err := json.Marshal(jsonManifest)
	if err != nil {
		return report{}, fmt.Errorf("marshal normalized manifest: %w", err)
	}
	serializedText := string(serialized)
	serializedSecretFree := !strings.Contains(serializedText, secretValue)
	endpointQueryRemoved := !strings.Contains(serializedText, "token=") && !strings.Contains(serializedText, "fragment-secret") && !strings.Contains(serializedText, "browser-secret")
	websocketPathRedacted := strings.Contains(serializedText, `"ws_endpoint":"ws://127.0.0.1:9222/%3Credacted%3E"`)
	if !serializedSecretFree || !endpointQueryRemoved || !websocketPathRedacted {
		return report{}, fmt.Errorf("normalized JSON redaction oracle failed: %s", serializedText)
	}

	if os.Getenv("C54_WRONG_NORMALIZED_ORACLE") == "1" {
		if jsonManifest.Room.MaxDuration == 30*time.Second {
			return report{}, errors.New("wrong oracle: expected normalized duration to be 29s")
		}
	}
	if os.Getenv("C54_WRONG_ERROR_ORACLE") == "1" {
		if missingTyped.Value == "" {
			return report{}, errors.New("wrong oracle: expected credential name/value to be echoed")
		}
	}
	if os.Getenv("C54_WRONG_REGISTRY_ORACLE") == "1" {
		return report{}, errors.New("wrong oracle: expected registry mutation to alter the provider snapshot")
	}
	if os.Getenv("C54_WRONG_REDACTION_ORACLE") == "1" {
		if serializedSecretFree {
			return report{}, errors.New("wrong oracle: expected serialized credential material")
		}
	}

	return report{
		Schema:                   "audio-runtime.c54.public-admission/v1",
		ConstructedVia:           "rooms/wire.NewManifestProviderFromRegistry",
		CandidateRevision:        os.Getenv(candidateRevisionEnvironment),
		SourceRevision:           os.Getenv(sourceRevisionEnvironment),
		JSONYAMLEqual:            true,
		FileAdmission:            true,
		AdmitAliases:             true,
		RegistrySnapshotIsolated: true,
		Normalized: normalizedReport{
			SchemaVersion:       jsonManifest.SchemaVersion,
			MaxTurns:            jsonManifest.Room.MaxTurns,
			MaxDuration:         jsonManifest.Room.MaxDuration.String(),
			RecordingDirectory:  jsonManifest.Room.RecordingDirectory(),
			ParticipantKinds:    []string{string(jsonManifest.Participants[0].Kind), string(jsonManifest.Participants[1].Kind)},
			ParticipantIDs:      []string{jsonManifest.Participants[0].ID, jsonManifest.Participants[1].ID},
			Provider:            jsonManifest.Participants[0].Provider,
			Model:               jsonManifest.Participants[0].Model,
			Tools:               append([]string(nil), jsonManifest.Participants[0].Tools...),
			BrowserCDPURL:       "http://127.0.0.1:9222/json/version",
			BrowserWSPath:       "ws://127.0.0.1:9222/%3Credacted%3E",
			BrowserTimeout:      jsonManifest.Participants[0].BrowserTools.Limits.InvocationTimeout.String(),
			BrowserMaxInput:     jsonManifest.Participants[0].BrowserTools.Limits.MaxInputBytes,
			BrowserMaxResult:    jsonManifest.Participants[0].BrowserTools.Limits.MaxResultBytes,
			BrowserDefaultMatch: reflect.DeepEqual(*defaults.Participants[0].BrowserTools, rooms.BrowserToolsDefaults{}.Config()),
		},
		TypedErrors: typedErrorReport{
			MissingCredentialCause:   true,
			MissingCredentialField:   missingTyped.Field,
			MissingCredentialValue:   missingTyped.Value,
			MissingCredentialProblem: missingTyped.Problem,
			UnknownNestedCause:       true,
			UnknownNestedField:       unknownTyped.Field,
			UnsafeEndpointCause:      true,
			UnsafeEndpointField:      unsafeTyped.Field,
			NoOpenerCause:            true,
			MultipleDocumentCause:    true,
		},
		Redaction: redactionReport{
			SerializedSecretFree:  serializedSecretFree,
			ErrorSecretFree:       !strings.Contains(missingErr.Error(), secretValue),
			EndpointQueryRemoved:  endpointQueryRemoved,
			WebsocketPathRedacted: websocketPathRedacted,
		},
		Lifecycle: lifecycleReport{Validated: true, Closed: provider.Close() == nil},
	}, nil
}

func typedError(err error) *rooms.ValidationError {
	var value *rooms.ValidationError
	if errors.As(err, &value) {
		return value
	}
	return nil
}

func main() {
	value, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "C54 consumer failure: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "C54 consumer report: %v\n", err)
		os.Exit(1)
	}
}
