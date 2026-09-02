//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// TestEAC24Through33CaptureIntegrity audits the customer-recorded provider
// edge without executing any captured tool. It is intentionally manual because
// the recordings are private and not committed; set EAC_CAPTURE_DIR to the
// directory containing eac24.json through eac33.json.
func TestEAC24Through33CaptureIntegrity(t *testing.T) {
	directory := os.Getenv("EAC_CAPTURE_DIR")
	if directory == "" {
		t.Skip("set EAC_CAPTURE_DIR to audit eac24.json through eac33.json")
	}
	for number := 24; number <= 33; number++ {
		number := number
		t.Run(fmt.Sprintf("eac%d", number), func(t *testing.T) {
			capturePath := filepath.Join(directory, fmt.Sprintf("eac%d.json", number))
			capture, err := gatewaytesting.LoadSessionCapture(capturePath)
			if err != nil {
				t.Fatalf("load protected capture: %v", err)
			}
			auditEACCapture(t, capture, number <= 28)
		})
	}
}

func auditEACCapture(t *testing.T, capture gatewaytesting.SessionCapture, expectAudio bool) {
	t.Helper()
	inputBytes, outputBytes := 0, 0
	audioResponses := map[string]int{}
	audioDone := map[string]bool{}
	toolCalls := map[string]bool{}
	toolResults := map[string]bool{}
	rateLimited := false

	for index, record := range capture.Records {
		if record.Sequence != index+1 {
			t.Fatalf("record %d sequence = %d, want %d", index, record.Sequence, index+1)
		}
		var payload map[string]any
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("record %d %s payload: %v", record.Sequence, record.Type, err)
		}
		switch record.Type {
		case "input_audio_buffer.append":
			inputBytes += decodedEACAudioBytes(t, record.Sequence, payload, "audio")
		case "response.output_audio.delta":
			outputBytes += decodedEACAudioBytes(t, record.Sequence, payload, "delta")
			if responseID, _ := payload["response_id"].(string); responseID != "" {
				audioResponses[responseID]++
			}
		case "response.output_audio.done":
			if responseID, _ := payload["response_id"].(string); responseID != "" {
				audioDone[responseID] = true
			}
		case "response.output_item.done":
			item, _ := payload["item"].(map[string]any)
			if item["type"] == "function_call" {
				if callID, _ := item["call_id"].(string); callID != "" {
					toolCalls[callID] = true
				}
			}
		case "conversation.item.create":
			item, _ := payload["item"].(map[string]any)
			if item["type"] == "function_call_output" {
				if callID, _ := item["call_id"].(string); callID != "" {
					toolResults[callID] = true
				}
			}
		case "error":
			providerError, _ := payload["error"].(map[string]any)
			errorType, _ := providerError["type"].(string)
			code, _ := providerError["code"].(string)
			message, _ := providerError["message"].(string)
			rateLimited = providers.SessionErrorClassification(errorType, code, message) == providers.ErrorClassRateLimited
		}
	}

	for responseID := range audioResponses {
		if !audioDone[responseID] {
			t.Errorf("audio response %s has deltas without response.output_audio.done", responseID)
		}
	}
	for callID := range toolCalls {
		if !toolResults[callID] {
			t.Errorf("tool call %s has no function_call_output", callID)
		}
	}
	if expectAudio {
		if inputBytes == 0 || outputBytes == 0 {
			t.Fatalf("audio capture is empty: input_bytes=%d output_bytes=%d", inputBytes, outputBytes)
		}
		if rateLimited {
			t.Fatal("audio capture unexpectedly terminated for quota exhaustion")
		}
		return
	}
	if !rateLimited {
		t.Fatal("expected the quota-only capture to contain a classified rate-limit error")
	}
}

func decodedEACAudioBytes(t *testing.T, sequence int, payload map[string]any, field string) int {
	t.Helper()
	encoded, _ := payload[field].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("record %d field %s is not valid base64: %v", sequence, field, err)
	}
	if len(decoded)%2 != 0 {
		t.Fatalf("record %d field %s has odd PCM16 byte count %d", sequence, field, len(decoded))
	}
	return len(decoded)
}
