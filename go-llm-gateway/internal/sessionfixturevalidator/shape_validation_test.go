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

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

var updateShapeGolden = flag.Bool("update", false, "update session fixture validator golden files")

func TestValidateSessionCaptureShapes_ErrorPathTable(t *testing.T) {
	call := providerRecord("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"call-weather","name":"weather","arguments":"{}"}`)
	result := providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-weather","output":"sunny"}}`)
	terminal := providerRecord("response.done", `{"type":"response.done"}`)

	tests := []struct {
		name       string
		valid      gatewaytesting.SessionCapture
		invalid    gatewaytesting.SessionCapture
		fieldPath  string
		reasonPart string
	}{
		{
			name: "missing audio payload",
			valid: gatewaytesting.SessionCapture{
				Version:  gatewaytesting.SessionCaptureVersion,
				Provider: gatewaytesting.SessionProviderMetadata{Name: "grok", Model: "grok-realtime"},
				Session:  gatewaytesting.SessionMetadata{FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic},
				Records:  []gatewaytesting.CapturedSessionEvent{streamRecord("AUDIO.DELTA", `{"type":"AUDIO.DELTA","value":{"type":"delta_audio","content":"AQI="}}`)},
			},
			invalid: gatewaytesting.SessionCapture{
				Version:  gatewaytesting.SessionCaptureVersion,
				Provider: gatewaytesting.SessionProviderMetadata{Name: "grok", Model: "grok-realtime"},
				Session:  gatewaytesting.SessionMetadata{FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic},
				Records:  []gatewaytesting.CapturedSessionEvent{streamRecord("AUDIO.DELTA", `{"type":"AUDIO.DELTA","value":{"type":"delta_audio"}}`)},
			},
			fieldPath:  "records[0].payload.value.content",
			reasonPart: "non-empty audio payload",
		},
		{
			name:       "call without result",
			valid:      providerRecordedCapture(call, providerRecord("response.created", `{"type":"response.created"}`), result, terminal),
			invalid:    providerRecordedCapture(call, terminal),
			fieldPath:  "records[0].payload.call_id",
			reasonPart: "no matching tool result",
		},
		{
			name:       "result without call",
			valid:      providerRecordedCapture(call, providerRecord("response.created", `{"type":"response.created"}`), result, terminal),
			invalid:    providerRecordedCapture(result, terminal),
			fieldPath:  "records[0].payload.item.call_id",
			reasonPart: "no matching tool call",
		},
		{
			name:       "missing terminal",
			valid:      providerRecordedCapture(providerRecord("session.created", `{"type":"session.created","session":{"id":"sess-shape"}}`), terminal),
			invalid:    providerRecordedCapture(providerRecord("session.created", `{"type":"session.created","session":{"id":"sess-shape"}}`)),
			fieldPath:  "records[*].type",
			reasonPart: "recognized success or error terminal",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validPath := filepath.Join(t.TempDir(), "valid.session.json")
			invalidPath := filepath.Join(t.TempDir(), "invalid.session.json")
			writeCapture(t, validPath, test.valid)
			writeCapture(t, invalidPath, test.invalid)

			validResult, err := ValidatePaths([]string{validPath})
			if err != nil {
				t.Fatalf("ValidatePaths(valid) returned error: %v", err)
			}
			if validResult.FilesScanned != 1 || len(validResult.Errors) != 0 {
				t.Fatalf("valid result = %#v, want one file and no errors", validResult)
			}

			invalidResult, err := ValidatePaths([]string{invalidPath})
			if err != nil {
				t.Fatalf("ValidatePaths(invalid) returned traversal error: %v", err)
			}
			requireShapeValidationError(t, invalidResult.Errors, invalidPath, test.fieldPath, test.reasonPart)

			var stdout, stderr bytes.Buffer
			commandErr := Run([]string{invalidPath}, &stdout, &stderr)
			if !errors.Is(commandErr, ErrValidationFailed) {
				t.Fatalf("Run(invalid) error = %v, want ErrValidationFailed", commandErr)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("Run(invalid) stdout=%q stderr=%q, want no stdout and diagnostics", stdout.String(), stderr.String())
			}
		})
	}
}

func TestValidateSessionCaptureShapes_RejectsEmptyProviderAudioPayload(t *testing.T) {
	valid := providerRecordedCapture(
		providerRecord("response.output_audio.delta", `{"type":"response.output_audio.delta","delta":"AQI="}`),
		providerRecord("response.done", `{"type":"response.done"}`),
	)
	invalid := providerRecordedCapture(
		providerRecord("response.output_audio.delta", `{"type":"response.output_audio.delta","delta":""}`),
		providerRecord("response.done", `{"type":"response.done"}`),
	)

	validPath := filepath.Join(t.TempDir(), "valid-audio.session.json")
	invalidPath := filepath.Join(t.TempDir(), "empty-audio.session.json")
	writeCapture(t, validPath, valid)
	writeCapture(t, invalidPath, invalid)

	validResult, err := ValidatePaths([]string{validPath})
	if err != nil || len(validResult.Errors) != 0 {
		t.Fatalf("valid provider audio result = %#v, err=%v; want no errors", validResult, err)
	}
	invalidResult, err := ValidatePaths([]string{invalidPath})
	if err != nil {
		t.Fatalf("ValidatePaths(empty audio) returned traversal error: %v", err)
	}
	requireShapeValidationError(t, invalidResult.Errors, invalidPath, "records[0].payload.delta", "non-empty audio payload")
}

func TestValidateSessionCaptureShapes_RequiresExactPersistedCallIDs(t *testing.T) {
	tests := []struct {
		name       string
		call       gatewaytesting.CapturedSessionEvent
		result     gatewaytesting.CapturedSessionEvent
		fieldPath  string
		reasonPart string
	}{
		{
			name:       "output item id is not call id",
			call:       providerRecord("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","id":"call-fallback"}}`),
			result:     providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-fallback","output":"orphan"}}`),
			fieldPath:  "records[0].payload.item.call_id",
			reasonPart: "requires a non-empty persisted call identifier",
		},
		{
			name:       "output item id-like field is not call id",
			call:       providerRecord("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","item_id":"call-fallback"}}`),
			result:     providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-fallback","output":"orphan"}}`),
			fieldPath:  "records[0].payload.item.call_id",
			reasonPart: "requires a non-empty persisted call identifier",
		},
		{
			name:       "function arguments item call id is not top level call id",
			call:       providerRecord("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","item":{"call_id":"call-fallback"}}`),
			result:     providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-fallback","output":"orphan"}}`),
			fieldPath:  "records[0].payload.call_id",
			reasonPart: "requires a non-empty persisted call identifier",
		},
		{
			name:       "function arguments item id-like field is not top level call id",
			call:       providerRecord("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","item_id":"call-fallback"}`),
			result:     providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-fallback","output":"orphan"}}`),
			fieldPath:  "records[0].payload.call_id",
			reasonPart: "requires a non-empty persisted call identifier",
		},
		{
			name:       "empty output item call id does not fall back",
			call:       providerRecord("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"","id":"call-fallback"}}`),
			result:     providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-fallback","output":"orphan"}}`),
			fieldPath:  "records[0].payload.item.call_id",
			reasonPart: "requires a non-empty persisted call identifier",
		},
		{
			name:       "mismatched persisted call ids do not pair",
			call:       providerRecord("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"call-one"}`),
			result:     providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-two","output":"mismatch"}}`),
			fieldPath:  "records[0].payload.call_id",
			reasonPart: "no matching tool result",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid-call-id.session.json")
			writeCapture(t, path, providerRecordedCapture(test.call, test.result, providerRecord("response.done", `{"type":"response.done"}`)))

			result, err := ValidatePaths([]string{path})
			if err != nil {
				t.Fatalf("ValidatePaths returned traversal error: %v", err)
			}
			if len(result.Errors) == 0 {
				t.Fatalf("ValidatePaths result = %#v, want exact-call-ID validation errors", result)
			}
			requireShapeValidationError(t, result.Errors, path, test.fieldPath, test.reasonPart)
		})
	}
}

func TestValidateSessionCaptureShapes_AcceptsExactOutputItemCallID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "valid-output-item-call.session.json")
	writeCapture(t, path, providerRecordedCapture(
		providerRecord("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call-exact"}}`),
		providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-exact","output":"matched"}}`),
		providerRecord("response.done", `{"type":"response.done"}`),
	))

	result, err := ValidatePaths([]string{path})
	if err != nil {
		t.Fatalf("ValidatePaths returned traversal error: %v", err)
	}
	if result.FilesScanned != 1 || len(result.Errors) != 0 {
		t.Fatalf("ValidatePaths result = %#v, want one valid exact-call-ID fixture", result)
	}
}

func TestRun_MultiViolationReportMatchesGolden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "multi-violation.session.json")
	writeCapture(t, path, providerRecordedCapture(
		providerRecord("response.output_audio.delta", `{"type":"response.output_audio.delta"}`),
		providerRecord("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"call-missing-result"}`),
		providerRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"result-without-call","output":"orphan"}}`),
	))

	var stdout, stderr bytes.Buffer
	err := Run([]string{path}, &stdout, &stderr)
	if !errors.Is(err, ErrValidationFailed) {
		t.Fatalf("Run returned error = %v, want ErrValidationFailed", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	got := strings.ReplaceAll(stderr.String(), path, "multi-violation.session.json")
	goldenPath := filepath.Join(repoPathFromHere("testdata/golden/multi-violation.stderr"))
	if *updateShapeGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0644); err != nil {
			t.Fatalf("update golden file: %v", err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file: %v; rerun with -update to regenerate", err)
	}
	if got != string(want) {
		t.Fatalf("stderr report differs from golden\n got:\n%s\nwant:\n%s", got, want)
	}
}

func providerRecordedCapture(records ...gatewaytesting.CapturedSessionEvent) gatewaytesting.SessionCapture {
	capture := validSyntheticCapture()
	capture.Session.FixtureProvenance = gatewaytesting.SessionFixtureProvenanceProviderRecorded
	capture.Records = records
	return capture
}

func providerRecord(eventType, payload string) gatewaytesting.CapturedSessionEvent {
	return gatewaytesting.CapturedSessionEvent{
		Type:        eventType,
		PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(payload),
	}
}

func streamRecord(eventType, payload string) gatewaytesting.CapturedSessionEvent {
	return gatewaytesting.CapturedSessionEvent{
		Type:        eventType,
		PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage,
		Payload:     json.RawMessage(payload),
	}
}

func requireShapeValidationError(t *testing.T, errs []gatewaytesting.SessionFixtureValidationError, file, fieldPath, reasonPart string) {
	t.Helper()
	for _, err := range errs {
		if err.File == file && err.FieldPath == fieldPath && strings.Contains(err.Reason, reasonPart) {
			return
		}
	}
	t.Fatalf("missing shape validation error for file=%q field=%q reason containing %q; got %#v", file, fieldPath, reasonPart, errs)
}
