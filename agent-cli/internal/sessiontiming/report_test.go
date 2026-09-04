package sessiontiming

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestAnalyzeCaptureReconstructsToolChainAndResetsPlaybackAtUserTurn(t *testing.T) {
	audio := base64.StdEncoding.EncodeToString(make([]byte, 24000))
	capture := gwtesting.SessionCapture{
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1"},
		Records: []gwtesting.CapturedSessionEvent{
			record(1, 100, "server_to_client", "input_audio_buffer.committed", `{}`),
			record(2, 110, "server_to_client", "response.created", `{"response":{"id":"r1"}}`),
			record(3, 200, "server_to_client", "response.output_audio.delta", payload(map[string]any{"response_id": "r1", "delta": audio})),
			record(4, 300, "server_to_client", "response.output_audio.delta", payload(map[string]any{"response_id": "r1", "delta": audio})),
			record(5, 310, "server_to_client", "response.function_call_arguments.done", `{"response_id":"r1","call_id":"c1","name":"webmcp_list_tools"}`),
			record(6, 330, "client_to_server", "conversation.item.create", `{"item":{"type":"function_call_output","call_id":"c1"}}`),
			record(7, 331, "client_to_server", "response.create", `{}`),
			record(8, 400, "server_to_client", "response.created", `{"response":{"id":"r2"}}`),
			record(9, 450, "server_to_client", "response.function_call_arguments.done", `{"response_id":"r2","call_id":"c2","name":"webmcp_invoke"}`),
			record(10, 500, "client_to_server", "conversation.item.create", `{"item":{"type":"function_call_output","call_id":"c2"}}`),
			record(11, 501, "client_to_server", "response.create", `{}`),
			record(12, 550, "server_to_client", "response.created", `{"response":{"id":"r3"}}`),
			record(13, 1300, "server_to_client", "response.output_audio.delta", payload(map[string]any{"response_id": "r3", "delta": audio})),
			record(14, 1310, "server_to_client", "response.done", `{"response":{"id":"r3","audio":{"output":{"format":{"rate":24000}}}}}`),
			record(15, 5000, "server_to_client", "input_audio_buffer.committed", `{}`),
			record(16, 5010, "server_to_client", "response.created", `{"response":{"id":"r4"}}`),
			record(17, 5100, "server_to_client", "response.output_audio.delta", payload(map[string]any{"response_id": "r4", "delta": audio})),
			record(18, 5110, "server_to_client", "response.done", `{"response":{"id":"r4"}}`),
		},
	}

	report, err := AnalyzeCapture(capture)
	if err != nil {
		t.Fatal(err)
	}
	if report.SampleRateHz != 24000 || len(report.Responses) != 4 || len(report.Tools) != 2 {
		t.Fatalf("report topology = rate %d responses %d tools %d", report.SampleRateHz, len(report.Responses), len(report.Tools))
	}
	if got := report.Responses[0]; got.AudioDurationMS != 1000 || got.AudioBurstRatio != 10 || got.TurnIndex != 1 {
		t.Fatalf("first response timing = %+v", got)
	}
	if got := report.Responses[2]; got.EstimatedAudibleGapMS != 100 || got.EstimatedQueueDelayMS != 0 || got.TurnIndex != 1 {
		t.Fatalf("tool continuation playback timing = %+v", got)
	}
	if got := report.Responses[3]; got.EstimatedAudibleGapMS != 0 || got.TurnIndex != 2 {
		t.Fatalf("new user turn retained stale playback timeline: %+v", got)
	}
	if got := report.Tools[0]; got.ExecutionMS == nil || *got.ExecutionMS != 20 || got.ResultToFirstOutputMS == nil || *got.ResultToFirstOutputMS != 120 {
		t.Fatalf("first tool timing = %+v", got)
	}
	if got := report.Tools[0]; got.ResultToRequestMS == nil || *got.ResultToRequestMS != 1 || got.RequestToCreatedMS == nil || *got.RequestToCreatedMS != 69 || got.CreatedToFirstOutputMS == nil || *got.CreatedToFirstOutputMS != 50 {
		t.Fatalf("first tool attribution = %+v", got)
	}
	if got := report.Tools[1]; got.ExecutionMS == nil || *got.ExecutionMS != 50 || got.ResultToFirstOutputMS == nil || *got.ResultToFirstOutputMS != 800 {
		t.Fatalf("second tool timing = %+v", got)
	}
	if got := report.Tools[1]; got.ResultToFirstAudioMS == nil || *got.ResultToFirstAudioMS != 800 {
		t.Fatalf("second tool audio continuation = %+v", got)
	}
	if got := report.Summary.InputToFirstOutputMS; got.Count != 2 || got.P95MS != 100 {
		t.Fatalf("input latency summary = %+v", got)
	}
	if got := report.Summary.EstimatedAudibleGapMS; got.Count != 1 || got.MaxMS != 100 {
		t.Fatalf("audible gap summary = %+v", got)
	}
}

func TestAnalyzeCaptureRejectsMalformedAudioDelta(t *testing.T) {
	capture := gwtesting.SessionCapture{Records: []gwtesting.CapturedSessionEvent{
		record(1, 0, "server_to_client", "response.created", `{"response":{"id":"r1"}}`),
		record(2, 1, "server_to_client", "response.output_audio.delta", `{"response_id":"r1","delta":"%%%"}`),
	}}
	if _, err := AnalyzeCapture(capture); err == nil {
		t.Fatal("malformed provider audio was accepted")
	}
}

func record(sequence int, timestamp int64, direction, eventType, body string) gwtesting.CapturedSessionEvent {
	return gwtesting.CapturedSessionEvent{Sequence: sequence, TimestampMs: timestamp, Direction: gwtesting.SessionEventDirection(direction), Type: eventType, Payload: json.RawMessage(body)}
}

func payload(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
