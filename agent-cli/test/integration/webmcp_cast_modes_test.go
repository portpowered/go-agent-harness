package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestWebMCPCastFunctionRoutesMediaAndTabModes proves the complete functional
// boundary from the single provider-facing tool through the stateful broker to
// the exact selected browser target. It also locks the backward-compatible tab
// default and rejects unknown modes before any target mutation.
func TestWebMCPCastFunctionRoutesMediaAndTabModes(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "cast-mode-browser", Product: "fixture", Loopback: true}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "youtube-tab",
		Type:      "page",
		URL:       "https://www.youtube.com/watch?v=fixture",
		Origin:    "https://www.youtube.com",
	}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(target,
			testkit.WithInitialCatalog(webmcp.ToolDescriptor{
				Name: "youtube_get_player_state", FrameID: "youtube-frame",
				InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			}),
			testkit.WithCastDevices(webmcp.CastDevice{Name: "Office TV", ID: "sink-office"}),
		),
	))
	t.Cleanup(func() { _ = runtime.Close() })
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: webMCPDeviceDiscoverer{candidate: candidate},
		IDs:        testkit.NewDeterministicIDs(),
	})
	t.Cleanup(func() { _ = broker.Close() })
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}); err != nil {
		t.Fatalf("select YouTube fixture: %v", err)
	}

	toolSet := webmcpTools.NewBrokerToolSet(broker, true)
	calls := []struct {
		id        string
		arguments string
		wantMode  webmcp.CastMode
	}{
		{id: "cast-default", arguments: `{"device_name":"Office TV"}`, wantMode: webmcp.CastModeTab},
		{id: "cast-media", arguments: `{"device_name":"Office TV","mode":"media"}`, wantMode: webmcp.CastModeMedia},
		{id: "cast-tab", arguments: `{"device_name":"Office TV","mode":"tab"}`, wantMode: webmcp.CastModeTab},
	}
	for _, call := range calls {
		response, err := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
			ID: call.id, Name: webmcp.CastTabToolName, Arguments: call.arguments,
		})
		if err != nil {
			t.Fatalf("%s execute: %v", call.id, err)
		}
		envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
		if err != nil || !envelope.OK {
			t.Fatalf("%s result = %s decode=%v", call.id, response.Content, err)
		}
		var result struct {
			DeviceName string          `json:"device_name"`
			Mode       webmcp.CastMode `json:"mode"`
			Status     string          `json:"status"`
		}
		if err := json.Unmarshal(envelope.Data, &result); err != nil {
			t.Fatalf("%s decode data: %v", call.id, err)
		}
		if result.DeviceName != "Office TV" || result.Mode != call.wantMode || result.Status != "cast_started" {
			t.Fatalf("%s data = %+v, want device=Office TV mode=%s status=cast_started", call.id, result, call.wantMode)
		}
	}

	var castOperations []testkit.Operation
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationCastTab || operation.Kind == testkit.OperationCastMedia {
			castOperations = append(castOperations, operation)
		}
	}
	if len(castOperations) != 3 ||
		castOperations[0].Kind != testkit.OperationCastTab ||
		castOperations[1].Kind != testkit.OperationCastMedia ||
		castOperations[2].Kind != testkit.OperationCastTab {
		t.Fatalf("Cast mode target operations = %+v", castOperations)
	}
	for _, operation := range castOperations {
		if operation.TargetID != target.ID || operation.DeviceName != "Office TV" {
			t.Fatalf("Cast operation escaped selected target/device: %+v", operation)
		}
	}

	beforeInvalid := len(runtime.Operations())
	response, err := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
		ID: "cast-invalid", Name: webmcp.CastTabToolName,
		Arguments: `{"device_name":"Office TV","mode":"desktop"}`,
	})
	if err != nil {
		t.Fatalf("invalid mode execute: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil || envelope.OK {
		t.Fatalf("invalid mode result = %s decode=%v", response.Content, err)
	}
	if afterInvalid := len(runtime.Operations()); afterInvalid != beforeInvalid {
		t.Fatalf("invalid mode mutated target: operations before=%d after=%d", beforeInvalid, afterInvalid)
	}
}
