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

// eacCaptureAudit accumulates the provider-edge evidence of one capture.
type eacCaptureAudit struct {
	inputBytes, outputBytes int
	audioResponses          map[string]int
	audioDone               map[string]bool
	toolCalls               map[string]bool
	toolResults             map[string]bool
	rateLimited             bool
}

func auditEACCapture(t *testing.T, capture gatewaytesting.SessionCapture, expectAudio bool) {
	t.Helper()
	audit := &eacCaptureAudit{
		audioResponses: map[string]int{},
		audioDone:      map[string]bool{},
		toolCalls:      map[string]bool{},
		toolResults:    map[string]bool{},
	}
	for index, record := range capture.Records {
		if record.Sequence != index+1 {
			t.Fatalf("record %d sequence = %d, want %d", index, record.Sequence, index+1)
		}
		var payload map[string]any
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("record %d %s payload: %v", record.Sequence, record.Type, err)
		}
		audit.observe(t, record.Sequence, record.Type, payload)
	}
	audit.assert(t, expectAudio)
}

func (a *eacCaptureAudit) observe(t *testing.T, sequence int, recordType string, payload map[string]any) {
	t.Helper()
	switch recordType {
	case "input_audio_buffer.append":
		a.inputBytes += decodedEACAudioBytes(t, sequence, payload, "audio")
	case "response.output_audio.delta":
		a.outputBytes += decodedEACAudioBytes(t, sequence, payload, "delta")
		if responseID := eacString(payload, "response_id"); responseID != "" {
			a.audioResponses[responseID]++
		}
	case "response.output_audio.done":
		if responseID := eacString(payload, "response_id"); responseID != "" {
			a.audioDone[responseID] = true
		}
	case "response.output_item.done":
		item := eacObject(payload, "item")
		if callID := eacString(item, "call_id"); item["type"] == "function_call" && callID != "" {
			a.toolCalls[callID] = true
		}
	case "conversation.item.create":
		item := eacObject(payload, "item")
		if callID := eacString(item, "call_id"); item["type"] == "function_call_output" && callID != "" {
			a.toolResults[callID] = true
		}
	case "error":
		providerError := eacObject(payload, "error")
		a.rateLimited = providers.SessionErrorClassification(
			eacString(providerError, "type"),
			eacString(providerError, "code"),
			eacString(providerError, "message"),
		) == providers.ErrorClassRateLimited
	}
}

func (a *eacCaptureAudit) assert(t *testing.T, expectAudio bool) {
	t.Helper()
	for responseID := range a.audioResponses {
		if !a.audioDone[responseID] {
			t.Errorf("audio response %s has deltas without response.output_audio.done", responseID)
		}
	}
	for callID := range a.toolCalls {
		if !a.toolResults[callID] {
			t.Errorf("tool call %s has no function_call_output", callID)
		}
	}
	if expectAudio {
		if a.inputBytes == 0 || a.outputBytes == 0 {
			t.Fatalf("audio capture is empty: input_bytes=%d output_bytes=%d", a.inputBytes, a.outputBytes)
		}
		if a.rateLimited {
			t.Fatal("audio capture unexpectedly terminated for quota exhaustion")
		}
		return
	}
	if !a.rateLimited {
		t.Fatal("expected the quota-only capture to contain a classified rate-limit error")
	}
}

// eacString returns the string at key, or "" when it is absent or not a
// string (the same zero value an unchecked assertion yields).
func eacString(payload map[string]any, key string) string {
	value, ok := payload[key].(string)
	if !ok {
		return ""
	}
	return value
}

// eacObject returns the JSON object at key, or nil when it is absent.
func eacObject(payload map[string]any, key string) map[string]any {
	value, ok := payload[key].(map[string]any)
	if !ok {
		return nil
	}
	return value
}

func decodedEACAudioBytes(t *testing.T, sequence int, payload map[string]any, field string) int {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(eacString(payload, field))
	if err != nil {
		t.Fatalf("record %d field %s is not valid base64: %v", sequence, field, err)
	}
	if len(decoded)%2 != 0 {
		t.Fatalf("record %d field %s has odd PCM16 byte count %d", sequence, field, len(decoded))
	}
	return len(decoded)
}
