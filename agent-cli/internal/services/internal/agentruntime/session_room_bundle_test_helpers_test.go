package agentruntime

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

const roomBundleManifestPath = roomevidence.ManifestPath

type roomTimelineEntry struct {
	TOffsetMS   float64           `json:"t_offset_ms"`
	TUnixMS     int64             `json:"t_unix_ms"`
	Event       string            `json:"event"`
	Participant string            `json:"participant,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}

type roomBundleManifest struct {
	SchemaVersion     int                                      `json:"schema_version"`
	Finalized         bool                                     `json:"finalized"`
	Timing            roomBundleTiming                         `json:"timing"`
	Bounds            roomBundleBounds                         `json:"bounds"`
	TerminationReason RoomTerminationReason                    `json:"termination_reason"`
	Reason            RoomTerminationReason                    `json:"reason,omitempty"`
	Participants      map[string]roomBundleParticipantManifest `json:"participants"`
	TurnCounts        map[string]int                           `json:"turn_counts"`
	AudioFormat       roomBundleAudioFormat                    `json:"audio_format"`
	RoomMix           string                                   `json:"room_mix"`
	RoomTimeline      string                                   `json:"room_timeline"`
	RoomLatency       string                                   `json:"room_latency,omitempty"`
	Artifacts         map[string]string                        `json:"artifacts"`
	RecordingStatus   *transcript.RecordingStatus              `json:"recording_status,omitempty"`
	DegradedArtifacts map[string]string                        `json:"degraded_artifacts,omitempty"`
	Error             string                                   `json:"error,omitempty"`
}

type roomBundleAudioFormat struct {
	SampleRate      int    `json:"sample_rate"`
	Channels        int    `json:"channels"`
	Encoding        string `json:"encoding"`
	SampleWidthBits int    `json:"sample_width_bits"`
	ByteOrder       string `json:"byte_order"`
}

type roomBundleTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
	ClockBase string `json:"clock_base"`
}

type roomBundleBounds struct {
	MaxTurns    int    `json:"max_turns,omitempty"`
	MaxDuration string `json:"max_duration,omitempty"`
}

type roomBundleArtifactPaths struct {
	WAV         string `json:"wav"`
	Diagnostics string `json:"diagnostics"`
	Deltas      string `json:"deltas"`
	SentPCM     string `json:"sent_pcm"`
	ReceivedPCM string `json:"received_pcm"`
	Events      string `json:"events"`
	Capture     string `json:"capture,omitempty"`
}

type roomBundleParticipantManifest struct {
	ID                     string                       `json:"id"`
	Kind                   room.ParticipantKind         `json:"kind"`
	SystemPrompt           string                       `json:"system_prompt"`
	OpeningPrompt          string                       `json:"opening_prompt,omitempty"`
	Provider               string                       `json:"provider"`
	Model                  string                       `json:"model"`
	APIKeyEnv              string                       `json:"api_key_env"`
	Voice                  string                       `json:"voice,omitempty"`
	Tools                  []string                     `json:"tools"`
	BrowserTools           *room.BrowserToolsConfig     `json:"browser_tools,omitempty"`
	CompletedTurns         int                          `json:"completed_turns"`
	TerminationReason      ParticipantTerminationReason `json:"termination_reason"`
	Reason                 ParticipantTerminationReason `json:"reason,omitempty"`
	TerminationTrigger     string                       `json:"termination_trigger"`
	TerminationDisposition string                       `json:"termination_disposition"`
	Classification         string                       `json:"classification"`
	TerminalReason         string                       `json:"terminal_reason"`
	TerminalProvenance     string                       `json:"terminal_provenance"`
	OutputState            string                       `json:"output_state"`
	Connected              bool                         `json:"connected"`
	InputDevice            string                       `json:"input_device,omitempty"`
	OutputDevice           string                       `json:"output_device,omitempty"`
	Error                  string                       `json:"error,omitempty"`
	Artifacts              roomBundleArtifactPaths      `json:"artifacts"`
	RecordingStatus        *transcript.RecordingStatus  `json:"recording_status,omitempty"`
	DegradedArtifacts      map[string]string            `json:"degraded_artifacts,omitempty"`
}

func readRoomBundleFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read room bundle %s: %v", path, err)
	}
	return data
}

func readRoomBundleJSONLLines(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	data := readRoomBundleFile(t, path)
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) == 0 || (len(lines) == 1 && len(lines[0]) == 0) {
		t.Fatalf("room bundle JSONL %s is empty", path)
	}
	result := make([]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !json.Valid(line) {
			t.Fatalf("room bundle JSONL %s contains invalid line %q", path, line)
		}
		result = append(result, append(json.RawMessage(nil), line...))
	}
	return result
}
