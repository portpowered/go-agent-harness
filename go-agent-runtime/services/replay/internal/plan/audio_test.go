package plan

import (
	"slices"
	"strings"
	"testing"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func audioTurnRecords() []gatewaytesting.CapturedSessionEvent {
	return []gatewaytesting.CapturedSessionEvent{
		clientRecord("input_audio_buffer.append", `{"type":"input_audio_buffer.append","audio":"AQACAA=="}`),
		clientRecord("input_audio_buffer.commit", `{"type":"input_audio_buffer.commit"}`),
		clientRecord("response.create", `{"type":"response.create"}`),
	}
}

func TestPlannerPreservesMultipleAudioTurnsAndShortChunks(t *testing.T) {
	records := append(audioTurnRecords(), audioTurnRecords()...)
	plan, err := New().LoadLivePlan(t.Context(), writePlanCapture(t, records...))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.AudioTurns) != 2 {
		t.Fatalf("turns=%+v", plan.AudioTurns)
	}
	for _, turn := range plan.AudioTurns {
		if len(turn.Chunks) != 1 || !slices.Equal(turn.Chunks[0], []int16{1, 2}) {
			t.Fatalf("PCM chunk altered: %+v", turn.Chunks)
		}
	}
}

func TestAudioPlanRejectsInvalidBoundariesAndPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records []gatewaytesting.CapturedSessionEvent
		want    string
	}{
		{name: "missing commit", records: audioTurnRecords()[:1], want: "missing input_audio_buffer.commit"},
		{name: "missing response", records: audioTurnRecords()[:2], want: "missing response.create"},
		{name: "bad codec", records: []gatewaytesting.CapturedSessionEvent{clientRecord("input_audio_buffer.append", `{"type":"input_audio_buffer.append","audio":"!"}`)}, want: "decode audio append"},
		{name: "mismatched commit", records: []gatewaytesting.CapturedSessionEvent{audioTurnRecords()[0], clientRecord("input_audio_buffer.commit", `{"type":"response.cancel"}`)}, want: "payload type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := replayAudioPlan("fixture", tc.records); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("plan error=%v, want %s", err, tc.want)
			}
		})
	}
}

func TestAudioCursorEnforcesRemainingSampleBudget(t *testing.T) {
	cursor := audioCursor{path: "fixture", actions: audioTurnRecords(), remaining: 1}
	if _, err := cursor.nextTurn(); err == nil || !strings.Contains(err.Error(), "bounded replay capacity") {
		t.Fatalf("sample budget error=%v", err)
	}
}
