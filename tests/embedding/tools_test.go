package embedding_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	toolservice "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	toolswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

func TestEmptyToolCapabilityDoesNotDiscoverHostWorkspace(t *testing.T) {
	host := toolswire.NewService()
	capability, err := host.Resolve(context.Background(), toolservice.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if capability.Executor != nil || len(capability.Definitions) != 0 {
		t.Fatal("empty host request acquired implicit tools")
	}
	if capability.WorkspaceDir != "" || len(capability.AdditionalDirs) != 0 {
		t.Fatalf("empty host request acquired filesystem scope: %q %q", capability.WorkspaceDir, capability.AdditionalDirs)
	}
}

func TestToolDefinitionsRequireAnExecutionRoute(t *testing.T) {
	host := toolswire.NewService()
	_, err := host.Resolve(context.Background(), toolservice.Request{
		WorkDir:     t.TempDir(),
		Definitions: []messages.ToolDefinition{{Name: "host_tool", Description: "host-owned capability"}},
	})
	if err == nil {
		t.Fatal("tool definitions were admitted without an executor or default tool surface")
	}
}

func TestPublicStrictReplayRunsFromIndependentModule(t *testing.T) {
	fixture := filepath.Join("testdata", "replay")
	marker := filepath.Join(fixture, "exec-marker")
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("strict replay fixture already has executable-tool marker %q", marker)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	service := runtimeReplayWire.NewStrictService()
	var output bytes.Buffer
	result, err := service.Run(t.Context(), &output, runtimeReplay.StrictRequest{
		BundlePath: fixture,
		Provider:   "openai",
		Model:      "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "strict embedding continuation" {
		t.Fatalf("output=%q, want recorded continuation", output.String())
	}
	if result.WireEvents != 16 || result.ToolCalls != 1 {
		t.Fatalf("result=%+v, want 16 wire events and one recorded tool", result)
	}
	if !result.Scope.Protocol || !result.Scope.Tools || !result.Scope.RecordedPCM || result.Scope.DeviceExecution {
		t.Fatalf("scope=%+v, want protocol/tools/recorded PCM and no device execution", result.Scope)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict replay constructed executable-tool side effect %q: %v", marker, err)
	}
}

func TestPublicStrictReplayExposesPreparedEvidenceAndRejectsIncompleteBundle(t *testing.T) {
	service := runtimeReplayWire.NewStrictService()
	prepared, err := service.Prepare(t.Context(), runtimeReplay.StrictRequest{BundlePath: filepath.Join("testdata", "replay")})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Clock == nil || prepared.Dialer == nil || prepared.ToolExecutor == nil {
		t.Fatalf("prepared=%+v, want deterministic clock and hermetic dependencies", prepared)
	}
	if prepared.Scope.DeviceExecution {
		t.Fatal("prepared strict replay advertises device execution")
	}
	if err := prepared.Close(); !errors.Is(err, runtimeReplay.ErrBundleIncomplete) {
		t.Fatalf("close before replay=%v, want incomplete evidence", err)
	}

	empty := t.TempDir()
	_, err = service.Run(t.Context(), &bytes.Buffer{}, runtimeReplay.StrictRequest{BundlePath: empty})
	if !errors.Is(err, runtimeReplay.ErrBundleIncomplete) || !strings.Contains(err.Error(), "timeline.jsonl") {
		t.Fatalf("missing timeline error=%v, want bounded incomplete diagnostic", err)
	}
}

func TestPublicStrictPreparedCompletionCannotBeForged(t *testing.T) {
	prepared, err := runtimeReplayWire.NewStrictService().Prepare(t.Context(), runtimeReplay.StrictRequest{
		BundlePath: filepath.Join("testdata", "replay"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reflect.TypeOf(prepared).FieldByName("Complete"); ok {
		t.Fatal("public prepared contract exposes mutable completion callback")
	}
	build, ok := reflect.TypeOf(runtimeReplay.StrictPreparedBuilder{}).MethodByName("Build")
	if !ok || build.Type.NumIn() != 9 {
		t.Fatalf("public builder signature=%v, want no caller-supplied validator", build.Type)
	}
	forged := runtimeReplay.StrictPrepared{
		Capture:      prepared.Capture,
		Dialer:       prepared.Dialer,
		ToolExecutor: prepared.ToolExecutor,
		Audio:        prepared.Audio,
		Clock:        prepared.Clock,
		Scope:        prepared.Scope,
		WireEvents:   prepared.WireEvents,
		ToolCalls:    prepared.ToolCalls,
	}
	if err := forged.ValidateComplete(); !errors.Is(err, runtimeReplay.ErrBundleIncomplete) {
		t.Fatalf("forged completion error=%v, want incomplete evidence", err)
	}
	preparedWithoutEvidence := runtimeReplay.StrictPreparedBuilder{}.Build(
		prepared.Capture,
		prepared.Dialer,
		prepared.ToolExecutor,
		prepared.Audio,
		prepared.Clock,
		prepared.Scope,
		prepared.WireEvents,
		prepared.ToolCalls,
	)
	if err := preparedWithoutEvidence.ValidateComplete(); !errors.Is(err, runtimeReplay.ErrBundleIncomplete) {
		t.Fatalf("public builder without consumed evidence error=%v, want incomplete evidence", err)
	}
}

func TestPublicStrictReplayRejectsNilContext(t *testing.T) {
	service := runtimeReplayWire.NewStrictService()
	//lint:ignore SA1012 Exercise nil-context rejection at the public boundary.
	_, err := service.Prepare(nil, runtimeReplay.StrictRequest{BundlePath: filepath.Join("testdata", "replay")})
	if !errors.Is(err, runtimeReplay.ErrBundleIncomplete) {
		t.Fatalf("nil context error=%v, want incomplete error", err)
	}
}

func TestPublicStrictReplayPreparedDialerPreservesInputPacketOrder(t *testing.T) {
	prepared, err := runtimeReplayWire.NewStrictService().Prepare(t.Context(), runtimeReplay.StrictRequest{BundlePath: filepath.Join("testdata", "replay")})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := prepared.Dialer.Dial("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	var sentTypes []string
	var audioPayload string
	for _, record := range prepared.Capture.Records {
		if record.Direction == "client_to_server" {
			if err := conn.WriteMessage(1, record.Payload); err != nil {
				t.Fatal(err)
			}
			sentTypes = append(sentTypes, record.Type)
			if record.Type == "input_audio_buffer.append" {
				audioPayload = string(record.Payload)
			}
			continue
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := strings.Join(sentTypes, ","), "session.update,input_audio_buffer.append,input_audio_buffer.commit,response.create,conversation.item.create,response.create"; got != want {
		t.Fatalf("sent provider packet types=%q, want %q", got, want)
	}
	if want := `{"type":"input_audio_buffer.append","audio":"AQD+/wMA"}`; audioPayload != want {
		t.Fatalf("sent input audio payload=%q, want %q", audioPayload, want)
	}
	if _, err := prepared.ToolExecutor.Execute(t.Context(), messages.ToolCall{ID: "call-embed-1", Name: "exec", Arguments: `{"path":"tests/embedding/testdata/replay/exec-marker"}`}); err != nil {
		t.Fatal(err)
	}
	if err := prepared.ValidateComplete(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicStrictReplayRejectsMissingRecordedToolResult(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "replay", "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 {
		t.Fatal("fixture unexpectedly short")
	}
	lines[len(lines)-2] = strings.Replace(lines[len(lines)-2], "\"runtime_kind\":\"tool_result\"", "\"runtime_kind\":\"unrelated\"", 1)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "timeline.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = runtimeReplayWire.NewStrictService().Run(t.Context(), &bytes.Buffer{}, runtimeReplay.StrictRequest{BundlePath: directory})
	if !errors.Is(err, runtimeReplay.ErrBundleIncomplete) || !strings.Contains(err.Error(), "has no result") {
		t.Fatalf("missing tool result error=%v, want causal incomplete diagnostic", err)
	}
}

func TestPublicStrictReplayRejectsMissingFinalResponseDone(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "replay", "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	mutated := replaceLastResponseDone(t, data)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "timeline.jsonl"), mutated, 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	_, err = runtimeReplayWire.NewStrictService().Run(t.Context(), &output, runtimeReplay.StrictRequest{BundlePath: directory})
	if !errors.Is(err, runtimeReplay.ErrBundleIncomplete) || !strings.Contains(err.Error(), "terminal response.done") {
		t.Fatalf("missing terminal error=%v, want causal incomplete diagnostic", err)
	}
	if output.Len() != 0 {
		t.Fatalf("missing terminal replay produced %d bytes of success output", output.Len())
	}
}

func replaceLastResponseDone(t *testing.T, data []byte) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line, err := replaceResponseDoneLine(lines[index])
		if err != nil {
			t.Fatal(err)
		}
		if line == "" {
			continue
		}
		lines[index] = line
		return []byte(strings.Join(lines, "\n") + "\n")
	}
	t.Fatal("fixture has no response.done event")
	return nil
}

func replaceResponseDoneLine(line string) (string, error) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return "", err
	}
	runtimeKind, ok := decodeJSONText(event["runtime_kind"])
	if !ok || runtimeKind != "provider_wire_receive" {
		return "", nil
	}
	var encoded []byte
	if err := json.Unmarshal(event["payload"], &encoded); err != nil {
		return "", err
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return "", err
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(wire["payload"], &message); err != nil {
		return "", err
	}
	messageType, ok := decodeJSONText(message["type"])
	if !ok || messageType != "response.done" {
		return "", nil
	}
	message["type"] = json.RawMessage(`"response.output_text.done"`)
	messageBytes, err := json.Marshal(message)
	if err != nil {
		return "", err
	}
	wire["payload"] = messageBytes
	wireBytes, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	event["payload"], err = json.Marshal(wireBytes)
	if err != nil {
		return "", err
	}
	lineBytes, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	return string(lineBytes), nil
}

func decodeJSONText(raw json.RawMessage) (string, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}
