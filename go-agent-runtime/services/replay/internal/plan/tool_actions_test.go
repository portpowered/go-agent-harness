package plan

import (
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"testing"
)

func TestPlannerLetsRuntimeReproduceToolOutputsAndImages(t *testing.T) {
	for _, audio := range []bool{false, true} {
		opening := []string{"conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"describe image"}]}}`}
		records := []gatewaytesting.CapturedSessionEvent{clientRecord(opening[0], opening[1])}
		if audio {
			records = []gatewaytesting.CapturedSessionEvent{
				clientRecord(replayAppend, `{"type":"input_audio_buffer.append","audio":"AQACAA=="}`),
				clientRecord("input_audio_buffer.commit", `{"type":"input_audio_buffer.commit"}`),
			}
		}
		records = append(records,
			clientRecord("response.create", `{"type":"response.create"}`),
			serverRecord("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"call-image","name":"read_image","arguments":"{}"}`),
			clientRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-image","output":"metadata"}}`),
			clientRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}}`),
			clientRecord("response.create", `{"type":"response.create"}`),
		)
		plan, err := New().LoadLivePlan(t.Context(), writePlanCapture(t, records...))
		if err != nil {
			t.Fatal(err)
		}
		if audio {
			if len(plan.AudioTurns) != 1 || len(plan.AudioTurns[0].Chunks) != 1 || len(plan.AudioTurns[0].Chunks[0]) != 2 {
				t.Fatalf("audio plan changed: %+v", plan)
			}
		} else if !plan.OpeningPromptPresent || plan.OpeningPrompt != "describe image" {
			t.Fatalf("text plan changed: %+v", plan)
		}
	}
}

func TestPlannerRejectsOrphanToolOutput(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"orphan","output":"result"}}`),
	)
	if _, err := New().LoadLivePlan(t.Context(), path); err == nil {
		t.Fatal("orphan tool output accepted")
	}
}

func TestInitialToolsDescribeFirstAdvertisementOnly(t *testing.T) {
	records := []gatewaytesting.CapturedSessionEvent{
		clientRecord(replaySessionUpdate, `{"type":"session.update","session":{"tools":[{"name":"exec"}]}}`),
		clientRecord(replaySessionUpdate, `{"type":"session.update","session":{"tools":[{"name":"later_tool"}]}}`),
	}
	names, known := initialToolNames(records)
	if !known || len(names) != 1 || names[0] != "exec" {
		t.Fatalf("initial tools = %v, known=%t", names, known)
	}
	names, known = initialToolNames([]gatewaytesting.CapturedSessionEvent{clientRecord(replaySessionUpdate, `{"type":"session.update","session":{}}`)})
	if !known || len(names) != 0 {
		t.Fatalf("empty advertisement = %v, known=%t", names, known)
	}
	if _, known = initialToolNames(nil); known {
		t.Fatal("missing advertisement was reported as known")
	}
}
