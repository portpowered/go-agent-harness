//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sessiontiming"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// TestGPTRealtime21BinaryAudioAndToolRoundTrip is the billed, real-provider
// counterpart to the hermetic burst regression. It crosses the compiled CLI,
// WAV ingress, OpenAI Realtime websocket, tool execution/continuation, capture,
// and WAV egress boundaries and compares the provider bytes with both files.
func TestGPTRealtime21BinaryAudioAndToolRoundTrip(t *testing.T) {
	if os.Getenv("OPENAI_REALTIME_21_LIVE") != "1" {
		t.Skip("set OPENAI_REALTIME_21_LIVE=1 to run the billed gpt-realtime-2.1 scenario")
	}
	if os.Getenv("AGENT_MODEL__OPENAI__API_KEY") == "" {
		t.Fatal("AGENT_MODEL__OPENAI__API_KEY is required")
	}

	root := repositoryRoot(t)
	tmp := t.TempDir()
	binaryPath := filepath.Join(tmp, "yui")
	build := exec.Command("go", "build", "-o", binaryPath, "./agent-cli/cmd/yui")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}

	capturePath := filepath.Join(tmp, "gpt-realtime-2.1-tool.json")
	outputPath := filepath.Join(tmp, "gpt-realtime-2.1-tool.wav")
	inputPath := filepath.Join(root, "go-agent-loop", "testdata", "audio", "utt_short_24k.wav")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "session",
		"--provider", "openai",
		"--model", "gpt-realtime-2.1",
		"--reasoning-effort", "low",
		"--record", capturePath,
		"--audio-in", inputPath,
		"--audio-out", outputPath,
		"--max-duration", "60s",
		"--system-prompt", "For every user turn, first call list_dir with path dot. After the tool result, answer aloud.",
	)
	cmd.Dir = filepath.Join(root, "agent-cli")
	cmd.Env = os.Environ()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("live binary: %v\n%s", err, output)
	}

	var capture struct {
		Provider struct {
			Model string `json:"model"`
		} `json:"provider"`
		Records []struct {
			Type      string          `json:"type"`
			Direction string          `json:"direction"`
			Payload   json.RawMessage `json:"payload"`
		} `json:"records"`
	}
	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Provider.Model != "gpt-realtime-2.1" {
		t.Fatalf("captured model = %q", capture.Provider.Model)
	}

	var ingress, egress []byte
	toolCall, toolResult, audioDone := 0, 0, 0
	reasoningLow := false
	for _, record := range capture.Records {
		var payload map[string]any
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", record.Type, err)
		}
		switch record.Type {
		case "session.update":
			session, _ := payload["session"].(map[string]any)
			reasoning, _ := session["reasoning"].(map[string]any)
			reasoningLow = reasoning["effort"] == "low"
		case "input_audio_buffer.append":
			chunk, err := base64.StdEncoding.DecodeString(payload["audio"].(string))
			if err != nil {
				t.Fatal(err)
			}
			ingress = append(ingress, chunk...)
		case "response.output_audio.delta":
			chunk, err := base64.StdEncoding.DecodeString(payload["delta"].(string))
			if err != nil {
				t.Fatal(err)
			}
			egress = append(egress, chunk...)
		case "response.output_audio.done":
			audioDone++
		case "response.output_item.done":
			item, _ := payload["item"].(map[string]any)
			if item["type"] == "function_call" && item["name"] == "list_dir" {
				toolCall++
			}
		case "conversation.item.create":
			item, _ := payload["item"].(map[string]any)
			if item["type"] == "function_call_output" {
				toolResult++
			}
		}
	}
	if !reasoningLow || toolCall != 1 || toolResult != 1 || audioDone < 1 {
		t.Fatalf("wire contract: reasoning_low=%v tool_calls=%d tool_results=%d audio_done=%d", reasoningLow, toolCall, toolResult, audioDone)
	}
	protectedCapture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load protected live capture: %v", err)
	}
	timing, err := sessiontiming.AnalyzeCapture(protectedCapture)
	if err != nil {
		t.Fatalf("analyze live timing: %v", err)
	}
	assertLiveTimingBudget(t, timing)
	t.Logf("sanitized gpt-realtime-2.1 timing: input_to_output=%+v response_generation=%+v tool_execution=%+v result_to_request=%+v request_to_created=%+v continuation_generation=%+v result_to_output=%+v result_to_audio=%+v max_burst_ratio=%.2f max_queue_delay_ms=%d",
		timing.Summary.InputToFirstOutputMS,
		timing.Summary.ResponseToFirstOutputMS,
		timing.Summary.ToolExecutionMS,
		timing.Summary.ToolResultToRequestMS,
		timing.Summary.ToolRequestToCreatedMS,
		timing.Summary.ToolCreatedToFirstOutputMS,
		timing.Summary.ToolResultToFirstOutputMS,
		timing.Summary.ToolResultToFirstAudioMS,
		timing.Summary.MaxAudioBurstRatio,
		timing.Summary.MaxEstimatedQueueDelayMS,
	)
	inputPCM := wavData(t, inputPath)
	if len(ingress) < len(inputPCM) || !bytes.Equal(ingress[:len(inputPCM)], inputPCM) || len(ingress)-len(inputPCM) >= 1440 {
		t.Fatalf("ingress mismatch: wire=%d fixture=%d", len(ingress), len(inputPCM))
	}
	for _, b := range ingress[len(inputPCM):] {
		if b != 0 {
			t.Fatal("ingress frame padding was not silence")
		}
	}
	if outputPCM := wavData(t, outputPath); !bytes.Equal(egress, outputPCM) {
		t.Fatalf("egress mismatch: wire=%d wav=%d", len(egress), len(outputPCM))
	}
}

func assertLiveTimingBudget(t *testing.T, report sessiontiming.Report) {
	t.Helper()
	if report.Summary.ToolCallCount != 1 || report.Summary.UnfinishedToolCallCount != 0 {
		t.Fatalf("tool timing topology = %+v", report.Summary)
	}
	if report.Summary.ToolResultToFirstAudioMS.Count != 1 {
		t.Fatalf("tool produced no correlated spoken continuation: tools=%+v", report.Tools)
	}
	// The live gate keeps provider variability separate from harness-owned
	// latency. These are intentionally generous billed-test ceilings: tighter
	// deterministic timing belongs in the hermetic process-edge suite.
	checks := []struct {
		name string
		got  int64
		max  int64
	}{
		{name: "input commit to first output", got: report.Summary.InputToFirstOutputMS.MaxMS, max: 10_000},
		{name: "tool execution", got: report.Summary.ToolExecutionMS.MaxMS, max: 5_000},
		{name: "tool result to continuation request", got: report.Summary.ToolResultToRequestMS.MaxMS, max: 250},
		{name: "continuation request to response created", got: report.Summary.ToolRequestToCreatedMS.MaxMS, max: 5_000},
		{name: "continuation response generation", got: report.Summary.ToolCreatedToFirstOutputMS.MaxMS, max: 10_000},
		{name: "tool result to first continuation audio", got: report.Summary.ToolResultToFirstAudioMS.MaxMS, max: 15_000},
	}
	for _, check := range checks {
		if check.got > check.max {
			t.Errorf("%s latency = %dms, want <= %dms; summary=%+v", check.name, check.got, check.max, report.Summary)
		}
	}
}

func wavData(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for offset := 12; offset+8 <= len(raw); {
		size := int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		start, end := offset+8, offset+8+size
		if end > len(raw) {
			t.Fatalf("invalid WAV chunk in %s", path)
		}
		if string(raw[offset:offset+4]) == "data" {
			return raw[start:end]
		}
		offset = end + size%2
	}
	t.Fatalf("WAV data chunk missing in %s", path)
	return nil
}
