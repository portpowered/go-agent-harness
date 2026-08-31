package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

type replayCorpusSpec struct {
	filename   string
	sampleRate int
}

// replayCorpusSpecs is the finite set of corpus IDs understood by the offline
// probe runner. The v2a/v2e and v3a IDs map to committed WAVs; v3c keeps its
// named synthetic IDs because that vertical's replay fixtures intentionally
// carry their own synthetic event stream.
var replayCorpusSpecs = map[string]replayCorpusSpec{
	"utterance-hello-there": {filename: "utt_short_16k.wav", sampleRate: wavio.Rate16kHz},
	"truncated_16k":         {filename: "truncated_16k.wav", sampleRate: wavio.Rate16kHz},
	"truncated_24k":         {filename: "truncated_24k.wav", sampleRate: wavio.Rate24kHz},
	"overlap_16k":           {filename: "overlap_16k.wav", sampleRate: wavio.Rate16kHz},
	"overlap_24k":           {filename: "overlap_24k.wav", sampleRate: wavio.Rate24kHz},
}

var replaySyntheticCorpusIDs = map[string]struct{}{
	"v3c-utterance-1": {},
	"v3c-utterance-2": {},
	"v3c-utterance-3": {},
}

// replayCorpusLookup accepts only corpus IDs declared by the offline probe
// contract. An arbitrary non-empty ID must not make a scenario appear
// executable when no corpus or fixture-backed synthetic identity exists.
type replayCorpusLookup struct{}

func (replayCorpusLookup) Has(id string) bool {
	if _, ok := replayCorpusSpecs[id]; ok {
		return true
	}
	_, ok := replaySyntheticCorpusIDs[id]
	return ok
}

func replayCorpusPath(id string) (string, error) {
	spec, ok := replayCorpusSpecs[id]
	if !ok {
		return "", fmt.Errorf("audio corpus %q has no committed WAV mapping", id)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locate committed audio corpus %q: %w", id, err)
	}
	for directory := workingDir; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "go-agent-loop", "testdata", "audio", spec.filename)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return "", fmt.Errorf("audio corpus %q is not available under go-agent-loop/testdata/audio", id)
}

// scenarioReplayCorpusID returns the single real corpus a replay can inject.
// Synthetic multi-turn corpora (such as v3c's three named utterances) remain
// represented by their authored fixture events; the v3a and earlier
// single-audio cases resolve to committed WAVs here.
func scenarioReplayCorpusID(scenario probe.Scenario) (string, bool) {
	var corpusID string
	count := 0
	for _, step := range scenario.Steps {
		kind := step.Kind
		if kind == "" {
			kind = step.Type
		}
		if kind != probe.StepSendAudio {
			continue
		}
		count++
		candidate := step.CorpusID
		if candidate == "" {
			candidate = step.Corpus.CorpusID
		}
		if corpusID == "" {
			corpusID = candidate
		}
		if corpusID != candidate {
			return "", false
		}
	}
	if count != 1 {
		return "", false
	}
	if _, ok := replayCorpusSpecs[corpusID]; !ok {
		return "", false
	}
	return corpusID, true
}

func replayRecordPayload(record gatewaytesting.CapturedSessionEvent) []byte {
	if len(record.Payload) != 0 {
		return record.Payload
	}
	return record.Data
}

// injectReplayCorpusAudio creates the short-lived capture used by the replay
// probe. Committed fixtures retain sanitized placeholders; this function
// resolves their scenario corpus to the committed WAV and replaces append
// payloads with frame-sized, little-endian PCM. When a cancellation is
// present, the original append slots stay before it so the observed cancel
// latency remains tied to the actual first append; remaining frames follow the
// cancel as continued user input.
func injectReplayCorpusAudio(capture gatewaytesting.SessionCapture, corpusID string) (gatewaytesting.SessionCapture, error) {
	spec, ok := replayCorpusSpecs[corpusID]
	if !ok {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("audio corpus %q has no replay injection mapping", corpusID)
	}
	path, err := replayCorpusPath(corpusID)
	if err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	wavBytes, err := os.ReadFile(path)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("read audio corpus %q: %w", corpusID, err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("decode audio corpus %q: %w", corpusID, err)
	}
	if rate != spec.sampleRate {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("audio corpus %q sample rate = %d, want %d", corpusID, rate, spec.sampleRate)
	}
	if len(samples) == 0 {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("audio corpus %q contains no PCM16 samples", corpusID)
	}
	frames := replayPCMFrames(samples)
	appendSlots := 0
	hasCancel := false
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		if record.Type == "input_audio_buffer.append" {
			appendSlots++
		}
		if isResponseCancelEventType(record.Type) {
			hasCancel = true
		}
	}
	if appendSlots == 0 {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay fixture has no input_audio_buffer.append slot for corpus %q", corpusID)
	}
	if hasCancel && len(frames) < appendSlots {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("audio corpus %q has %d frames but replay fixture reserves %d append slots before response.cancel", corpusID, len(frames), appendSlots)
	}

	records := make([]gatewaytesting.CapturedSessionEvent, 0, len(capture.Records)+len(frames)-appendSlots)
	frameIndex := 0
	firstAppend := true
	suffixInserted := false
	for _, record := range capture.Records {
		if record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "input_audio_buffer.append" {
			if hasCancel {
				if suffixInserted {
					continue
				}
				appendRecord, frameErr := replayAudioAppendRecord(record, frames[frameIndex])
				if frameErr != nil {
					return gatewaytesting.SessionCapture{}, frameErr
				}
				records = append(records, appendRecord)
				frameIndex++
			} else if firstAppend {
				for _, frame := range frames {
					appendRecord, frameErr := replayAudioAppendRecord(record, frame)
					if frameErr != nil {
						return gatewaytesting.SessionCapture{}, frameErr
					}
					records = append(records, appendRecord)
				}
				frameIndex = len(frames)
			}
			firstAppend = false
			continue
		}
		records = append(records, record)
		if hasCancel && !suffixInserted && record.Direction == gatewaytesting.DirectionClientToServer && isResponseCancelEventType(record.Type) {
			for frameIndex < len(frames) {
				appendRecord, frameErr := replayAudioAppendRecord(record, frames[frameIndex])
				if frameErr != nil {
					return gatewaytesting.SessionCapture{}, frameErr
				}
				records = append(records, appendRecord)
				frameIndex++
			}
			suffixInserted = true
		}
	}
	if frameIndex != len(frames) {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay fixture did not receive all %q PCM frames: injected %d of %d", corpusID, frameIndex, len(frames))
	}
	for index := range records {
		records[index].Sequence = index + 1
	}
	injected := capture
	injected.Records = records
	if err := validateInjectedReplayAudio(injected, frames); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	sealed, err := gatewaytesting.SealSessionCapture(injected)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("seal injected replay capture: %w", err)
	}
	return sealed, nil
}

func replayPCMFrames(samples []int16) [][]byte {
	frames := make([][]byte, 0, (len(samples)+audio.FrameSize-1)/audio.FrameSize)
	for start := 0; start < len(samples); start += audio.FrameSize {
		frame := make([]int16, audio.FrameSize)
		copy(frame, samples[start:])
		frames = append(frames, replayPCM16LE(frame))
	}
	return frames
}

func replayPCM16LE(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func replayAudioAppendRecord(template gatewaytesting.CapturedSessionEvent, pcm []byte) (gatewaytesting.CapturedSessionEvent, error) {
	payload, err := json.Marshal(struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}{Type: "input_audio_buffer.append", Audio: base64.StdEncoding.EncodeToString(pcm)})
	if err != nil {
		return gatewaytesting.CapturedSessionEvent{}, fmt.Errorf("encode replay audio append: %w", err)
	}
	template.PayloadType = gatewaytesting.SessionPayloadTypeWebSocketMessage
	template.Payload = payload
	template.Data = nil
	template.Type = "input_audio_buffer.append"
	return template, nil
}

func validateInjectedReplayAudio(capture gatewaytesting.SessionCapture, frames [][]byte) error {
	actual := make([]byte, 0, len(frames)*audio.FrameSize*2)
	appendCount := 0
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != "input_audio_buffer.append" {
			continue
		}
		appendCount++
		var event struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
		}
		if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
			return fmt.Errorf("decode injected input_audio_buffer.append payload: %w", err)
		}
		if event.Type != "input_audio_buffer.append" || event.Audio == "" {
			return fmt.Errorf("injected input_audio_buffer.append payload is missing its audio field")
		}
		pcm, err := base64.StdEncoding.DecodeString(event.Audio)
		if err != nil {
			return fmt.Errorf("decode injected input audio: %w", err)
		}
		actual = append(actual, pcm...)
	}
	expected := make([]byte, 0, len(frames)*audio.FrameSize*2)
	for _, frame := range frames {
		expected = append(expected, frame...)
	}
	if appendCount != len(frames) {
		return fmt.Errorf("injected replay has %d append payloads for %d PCM frames", appendCount, len(frames))
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("injected replay append payloads do not equal the resolved corpus PCM")
	}
	return nil
}

// replayTranscriptFromCapture extracts the server-to-client transcript text
// from a loaded session capture so transcript expectations can be evaluated
// offline.
func replayTranscriptFromCapture(capture gatewaytesting.SessionCapture) string {
	var builder strings.Builder
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta":
		default:
			continue
		}
		var envelope struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(replayRecordPayload(record), &envelope) != nil {
			continue
		}
		builder.WriteString(envelope.Delta)
	}
	return builder.String()
}

// replayExecFunc returns a network-free ExecFunc that replays the recorded
// session fixture matching the scenario name or ID.
func replayExecFunc(fixtures map[string]string) probe.ExecFunc {
	return func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		fixture, err := replayFixtureForScenario(fixtures, scenario)
		if err != nil {
			return probe.ObservationSnapshot{}, err
		}
		capture, err := gatewaytesting.LoadSessionCapture(fixture)
		if err != nil {
			return probe.ObservationSnapshot{}, fmt.Errorf("load replay fixture %q: %w", fixture, err)
		}
		replayCapture := capture
		corpusID, injected := scenarioReplayCorpusID(scenario)
		if injected {
			replayCapture, err = injectReplayCorpusAudio(capture, corpusID)
			if err != nil {
				return probe.ObservationSnapshot{}, err
			}
		}
		return observationFromSessionCapture(ctx, scenario, replayCapture, fixture, !injected)
	}
}

// replayFixtureForScenario resolves both the exact authored name and the
// filename spelling used by committed s2s fixtures. Those fixtures predate
// the probe scenario IDs and use underscores where the scenario documents use
// hyphens, so a directory replay must normalize the two representations before
// declaring a scenario unmatched.
func replayFixtureForScenario(fixtures map[string]string, scenario probe.Scenario) (string, error) {
	for _, candidate := range []string{scenario.Name, scenario.ID, scenarioName(scenario)} {
		if fixture, ok := fixtures[candidate]; ok {
			return fixture, nil
		}
	}

	want := normalizeReplayFixtureName(scenarioName(scenario))
	var matched string
	for key, fixture := range fixtures {
		if normalizeReplayFixtureName(key) != want {
			continue
		}
		if matched != "" && matched != fixture {
			return "", fmt.Errorf("multiple recorded fixtures match scenario %q", scenarioName(scenario))
		}
		matched = fixture
	}
	if matched != "" {
		return matched, nil
	}
	if len(fixtures) == 1 {
		for _, only := range fixtures {
			return only, nil
		}
	}
	return "", fmt.Errorf("no recorded fixture matches scenario %q", scenarioName(scenario))
}

func normalizeReplayFixtureName(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "_", "-")
}
