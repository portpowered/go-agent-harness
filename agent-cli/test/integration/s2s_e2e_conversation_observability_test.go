package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// The s2s-e2e-conversation-observability lane proves that the durable
// artifacts of one CLI session command — never live test instrumentation —
// demonstrate that a multi-turn spoken conversation happened. The replay
// capture below is derived from the committed depth-4 corpus, while its
// redacted audio fields are restored at runtime. The CLI is invoked once with
// repeatable --audio-in-turn flags, so the recording bundle contains all turns
// in one session-log.jsonl.

const (
	// observabilityTurnCount is the number of conversation turns driven and
	// asserted. The PRD requires >= 3; the committed corpus has 4.
	observabilityTurnCount = 4

	// observabilityRMSThreshold is the documented minimum RMS energy (PCM16
	// linear scale) for recorded reply audio established by the depth-3 proof:
	// voiced speech measures ~2000, digital silence measures 0.
	observabilityRMSThreshold = 500.0
)

var observabilityInputTranscripts = []string{
	"Remember the word ZEPHYR.",
	"What is the weather like?",
	"What was the word?",
	"Spell that word backwards.",
}

// observabilityReplies are the full assistant replies each turn must show in
// the session log, in conversation order.
var observabilityReplies = []string{
	"ZEPHYR noted. I will remember it.",
	"Sunny and mild today.",
	"The word was ZEPHYR.",
	"Backwards it is RYHPEZ.",
}

// observabilityLogEntry is intentionally local to the proof. The test models
// the on-disk contract rather than importing the recorder's private types.
type observabilityLogEntry struct {
	TurnIndex int `json:"turn_index"`
	Input     struct {
		Text          string   `json:"text"`
		AudioBytes    uint64   `json:"audio_bytes"`
		Committed     bool     `json:"committed"`
		AudioSegments []string `json:"audio_segments"`
	} `json:"input"`
	Response struct {
		Text          string   `json:"text"`
		Complete      bool     `json:"complete"`
		AudioBytes    uint64   `json:"audio_bytes"`
		AudioSegments []string `json:"audio_segments"`
	} `json:"response"`
}

// buildObservabilityReplayFixture creates one replay capture for the complete
// conversation. The committed fixture redacts raw audio by policy and has no
// output-audio or input-ASR payloads, so this test-only materialization adds
// those provider observations and replaces each turn's frame placeholders
// with the one normalized PCM payload sent by --audio-in-turn.
func buildObservabilityReplayFixture(t *testing.T) string {
	t.Helper()

	capture := captureCopy(t, locateCLIFixture(t, multiturnPositiveFixture))
	records := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records)+12)
	audioTurn := 0
	responseTurn := 0
	appendWritten := false
	for _, record := range capture.Records {
		switch record.Type {
		case "input_audio_buffer.append":
			if appendWritten {
				continue
			}
			if audioTurn >= observabilityTurnCount {
				t.Fatalf("observability fixture has more audio turns than expected: %d", audioTurn+1)
			}
			record.Payload = observabilityAudioAppendPayload(t, audioTurn+1)
			appendWritten = true
		case "response.created":
			responseTurn++
			if responseTurn > observabilityTurnCount {
				t.Fatalf("observability fixture has more responses than expected: %d", responseTurn)
			}
			records = append(records, observabilityInputTranscriptRecords(t, responseTurn)...)
		case "response.output_audio.done":
			if responseTurn == 0 {
				t.Fatal("observability fixture has output audio before its response")
			}
			records = append(records, observabilityOutputAudioRecord(t, responseTurn))
		}

		records = append(records, record)
		if record.Type == "input_audio_buffer.commit" {
			audioTurn++
			appendWritten = false
		}
	}
	if audioTurn != observabilityTurnCount {
		t.Fatalf("observability fixture contains %d committed audio turns, want %d", audioTurn, observabilityTurnCount)
	}
	if responseTurn != observabilityTurnCount {
		t.Fatalf("observability fixture contains %d responses, want %d", responseTurn, observabilityTurnCount)
	}

	for index := range records {
		records[index].Sequence = index + 1
		records[index].TimestampMs = int64(index + 1)
	}
	capture.Records = records

	path := filepath.Join(t.TempDir(), "observability_multiturn.session.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal observability fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write observability fixture: %v", err)
	}
	return path
}

func observabilityAudioAppendPayload(t *testing.T, turn int) json.RawMessage {
	t.Helper()
	return observabilityJSONPayload(t, map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(observabilityReferenceUtterance(t, turn)),
	})
}

func observabilityOutputAudioRecord(t *testing.T, turn int) gwtesting.CapturedSessionEvent {
	t.Helper()
	return gwtesting.CapturedSessionEvent{
		Direction:   gwtesting.DirectionServerToClient,
		Type:        "response.output_audio.delta",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload: observabilityJSONPayload(t, map[string]any{
			"type":  "response.output_audio.delta",
			"delta": base64.StdEncoding.EncodeToString(observabilityReferenceUtterance(t, turn)),
		}),
	}
}

func observabilityInputTranscriptRecords(t *testing.T, turn int) []gwtesting.CapturedSessionEvent {
	t.Helper()

	text := observabilityInputTranscripts[turn-1]
	split := strings.LastIndex(text, " ")
	if split < 0 {
		split = len(text)
	}
	return []gwtesting.CapturedSessionEvent{
		{
			Direction:   gwtesting.DirectionServerToClient,
			Type:        "conversation.item.input_audio_transcription.delta",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload: observabilityJSONPayload(t, map[string]any{
				"type":  "conversation.item.input_audio_transcription.delta",
				"delta": text[:split],
			}),
		},
		{
			Direction:   gwtesting.DirectionServerToClient,
			Type:        "conversation.item.input_audio_transcription.delta",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload: observabilityJSONPayload(t, map[string]any{
				"type":  "conversation.item.input_audio_transcription.delta",
				"delta": text[split:],
			}),
		},
		{
			Direction:   gwtesting.DirectionServerToClient,
			Type:        "conversation.item.input_audio_transcription.completed",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload: observabilityJSONPayload(t, map[string]any{
				"type":       "conversation.item.input_audio_transcription.completed",
				"transcript": text,
			}),
		},
	}
}

func observabilityJSONPayload(t *testing.T, value map[string]any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal observability provider event: %v", err)
	}
	return data
}

// runObservabilityConversation drives all four turns through one invocation
// of the shipped CLI session command. The command writes no evidence consumed
// by the verifier; only the finalized recording directory is returned to it.
func runObservabilityConversation(t *testing.T, fixturePath, recordDir string) [][]byte {
	t.Helper()

	args := []string{"--replay", fixturePath, "--record-dir", recordDir}
	references := make([][]byte, 0, observabilityTurnCount)
	for turn := 1; turn <= observabilityTurnCount; turn++ {
		wavPath := locateCLIFixture(t, multiturnTurnWAVs[turn-1])
		references = append(references, observabilityReferenceUtterance(t, turn))
		args = append(args, "--audio-in-turn", wavPath)
	}

	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("multi-turn session command over replay: %v", err)
	}
	return references
}

// observabilityReferenceUtterance returns the exact normalized PCM16 byte
// stream sent by the scheduled audio-input path for one corpus WAV.
func observabilityReferenceUtterance(t *testing.T, turn int) []byte {
	t.Helper()

	frames := multiturnAudioFrames(t, locateCLIFixture(t, multiturnTurnWAVs[turn-1]))
	total := 0
	for _, frame := range frames {
		total += len(frame)
	}
	utterance := make([]byte, 0, total)
	for _, frame := range frames {
		utterance = append(utterance, frame...)
	}
	return utterance
}

// pcm16LERMS returns the root-mean-square energy of little-endian PCM16 bytes
// on the linear scale used by the repo's audio proofs.
func pcm16LERMS(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}
	var energy float64
	count := 0
	for offset := 0; offset+1 < len(pcm); offset += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(pcm[offset:])))
		energy += sample * sample
		count++
	}
	if count == 0 {
		return 0
	}
	return math.Sqrt(energy / float64(count))
}

// assertConversationArtifactEvidence reconstructs the conversation purely
// from one finalized recording directory and proves, per ordered session-log
// entry: the input transcript, committed input audio, full response text,
// non-empty recorded reply audio above the silence threshold, and manifest
// integrity. Every violation names the turn or artifact involved; all
// violations are returned joined so a redacted negative control can identify
// each missing evidence class.
func assertConversationArtifactEvidence(root string, wantInputs, wantReplies []string, referenceUtterances [][]byte) error {
	logPath := filepath.Join(root, "session-log.jsonl")
	sessionLogBytes, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Errorf("session log unreadable: %w", err)
	}
	trimmed := bytes.TrimSpace(sessionLogBytes)
	if len(trimmed) == 0 {
		return errors.New("session log is empty; no conversation turns are recorded")
	}

	lines := bytes.Split(trimmed, []byte("\n"))
	var violations []error
	if len(lines) < 3 {
		violations = append(violations, fmt.Errorf("session log holds %d turns, want >= 3", len(lines)))
	}
	if len(lines) != len(wantReplies) {
		violations = append(violations, fmt.Errorf("session log holds %d turns, want %d driven turns", len(lines), len(wantReplies)))
	}
	if len(wantInputs) != len(wantReplies) || len(referenceUtterances) != len(wantReplies) {
		violations = append(violations, fmt.Errorf("proof expectations are not aligned: inputs=%d replies=%d utterances=%d", len(wantInputs), len(wantReplies), len(referenceUtterances)))
	}

	entries := make([]observabilityLogEntry, 0, len(lines))
	for index, line := range lines {
		var entry observabilityLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			violations = append(violations, fmt.Errorf("session log turn %d is not valid JSON: %w", index+1, err))
			continue
		}
		entries = append(entries, entry)
	}

	limit := len(entries)
	if limit > len(wantReplies) {
		limit = len(wantReplies)
	}
	for index := 0; index < limit; index++ {
		turn := index + 1
		entry := entries[index]
		if entry.TurnIndex != turn {
			violations = append(violations, fmt.Errorf("turn %d: session log turn_index = %d, want %d", turn, entry.TurnIndex, turn))
		}
		if index < len(wantInputs) && entry.Input.Text != wantInputs[index] {
			violations = append(violations, fmt.Errorf("turn %d: input transcript = %q, want %q", turn, entry.Input.Text, wantInputs[index]))
		}
		if !entry.Input.Committed {
			violations = append(violations, fmt.Errorf("turn %d: session log records no committed user input", turn))
		}
		if len(entry.Input.AudioSegments) == 0 {
			violations = append(violations, fmt.Errorf("turn %d: session log lists no recorded input audio segments", turn))
		} else if index < len(referenceUtterances) {
			inputAudio, readErr := readListedRecordingSegments(root, entry.Input.AudioSegments, entry.Input.AudioBytes)
			if readErr != nil {
				violations = append(violations, fmt.Errorf("turn %d: input audio: %w", turn, readErr))
			} else if !bytes.Equal(inputAudio, referenceUtterances[index]) {
				violations = append(violations, fmt.Errorf("turn %d: recorded utterance audio (%d bytes) does not match the expected spoken utterance (%d bytes)", turn, len(inputAudio), len(referenceUtterances[index])))
			}
		}
		if entry.Response.Text != wantReplies[index] {
			violations = append(violations, fmt.Errorf("turn %d: session log reply = %q, want %q", turn, entry.Response.Text, wantReplies[index]))
		}
		if !entry.Response.Complete {
			violations = append(violations, fmt.Errorf("turn %d: session log marks the reply incomplete", turn))
		}
		if len(entry.Response.AudioSegments) == 0 {
			violations = append(violations, fmt.Errorf("turn %d: session log lists no recorded output audio segments", turn))
			continue
		}
		outputAudio, readErr := readListedRecordingSegments(root, entry.Response.AudioSegments, entry.Response.AudioBytes)
		if readErr != nil {
			violations = append(violations, fmt.Errorf("turn %d: output audio: %w", turn, readErr))
			continue
		}
		if len(outputAudio) == 0 {
			violations = append(violations, fmt.Errorf("turn %d: no recorded output audio found; the reply was not captured", turn))
		} else if rms := pcm16LERMS(outputAudio); rms <= observabilityRMSThreshold {
			violations = append(violations, fmt.Errorf("turn %d: recorded reply RMS = %.1f, want > %.1f (silence threshold)", turn, rms, observabilityRMSThreshold))
		}
	}

	if manifestErr := verifyManifestHashes(root); manifestErr != nil {
		violations = append(violations, fmt.Errorf("recording manifest: %w", manifestErr))
	}
	return errors.Join(violations...)
}

// readListedRecordingSegments concatenates exactly the segment paths named by
// one session-log entry and checks their total against that entry's byte count.
func readListedRecordingSegments(dir string, segments []string, wantBytes uint64) ([]byte, error) {
	var combined []byte
	for _, relative := range segments {
		clean := filepath.Clean(filepath.FromSlash(relative))
		if filepath.IsAbs(clean) || clean != filepath.FromSlash(relative) {
			return nil, fmt.Errorf("segment path %q is not relative and normalized", relative)
		}
		data, err := os.ReadFile(filepath.Join(dir, clean))
		if err != nil {
			return nil, fmt.Errorf("read segment %s: %w", relative, err)
		}
		combined = append(combined, data...)
	}
	if uint64(len(combined)) != wantBytes {
		return combined, fmt.Errorf("segments hold %d bytes, session log accounts %d", len(combined), wantBytes)
	}
	return combined, nil
}

// verifyManifestHashes re-hashes every regular artifact listed by the
// recording manifest against its recorded digest, proving the bundle still
// describes exactly the bytes on disk.
func verifyManifestHashes(dir string) error {
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("manifest unreadable: %w", err)
	}
	var manifest struct {
		Artifacts []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	listedSessionLog := false
	for _, artifact := range manifest.Artifacts {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(artifact.Path)))
		if err != nil {
			return fmt.Errorf("listed artifact %s missing: %w", artifact.Path, err)
		}
		sum := sha256Hex(data)
		if sum != artifact.SHA256 {
			return fmt.Errorf("artifact %s hash mismatch: manifest %s, disk %s", artifact.Path, artifact.SHA256, sum)
		}
		if artifact.Path == "session-log.jsonl" {
			listedSessionLog = true
		}
	}
	if !listedSessionLog {
		return errors.New("manifest does not list session-log.jsonl")
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func copyArtifactTree(t *testing.T, source, destination string) {
	t.Helper()

	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(source, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy artifact tree %s -> %s: %v", source, destination, err)
	}
}

func truncateSessionLog(t *testing.T, path string, keep int) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session log for truncation: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if keep <= 0 || keep > len(lines) {
		t.Fatalf("truncate session log keep=%d with %d lines", keep, len(lines))
	}
	truncated := append(bytes.Join(lines[:keep], []byte("\n")), '\n')
	if err := os.WriteFile(path, truncated, 0o644); err != nil {
		t.Fatalf("truncate session log: %v", err)
	}
}

func silenceBytes(n int) []byte {
	return make([]byte, n)
}

// TestSessionCommandConversationObservabilityProvesConversationFromArtifactsOnly
// is the lane's proof of record: after one multi-turn replayed session
// completes, the one recording bundle left by the CLI — read exclusively,
// post-hoc, with no access to any live event stream — reconstructs the ordered
// input transcripts and full replies and proves audible reply energy.
func TestSessionCommandConversationObservabilityProvesConversationFromArtifactsOnly(t *testing.T) {
	fixturePath := buildObservabilityReplayFixture(t)
	root := t.TempDir()
	references := runObservabilityConversation(t, fixturePath, root)

	if err := assertConversationArtifactEvidence(root, observabilityInputTranscripts, observabilityReplies, references); err != nil {
		t.Fatalf("on-disk artifacts do not prove the conversation: %v", err)
	}
}

// TestSessionCommandConversationObservabilityNegativeControlFailsTruncatedArtifacts
// proves the artifact-only assertions are not vacuous: against a truncated
// session log and redacted reply audio copied from the same combined bundle,
// the identical assertion fails and names both missing evidence classes.
func TestSessionCommandConversationObservabilityNegativeControlFailsTruncatedArtifacts(t *testing.T) {
	fixturePath := buildObservabilityReplayFixture(t)
	root := t.TempDir()
	references := runObservabilityConversation(t, fixturePath, root)

	negativeRoot := filepath.Join(t.TempDir(), "negative")
	copyArtifactTree(t, root, negativeRoot)
	truncateSessionLog(t, filepath.Join(negativeRoot, "session-log.jsonl"), observabilityTurnCount-1)
	redactedReplyPath := filepath.Join(negativeRoot, "audio", "out-001.pcm")
	replyAudio, err := os.ReadFile(redactedReplyPath)
	if err != nil {
		t.Fatalf("read negative control reply audio: %v", err)
	}
	if err := os.WriteFile(redactedReplyPath, silenceBytes(len(replyAudio)), 0o644); err != nil {
		t.Fatalf("redact negative control reply audio: %v", err)
	}

	err = assertConversationArtifactEvidence(negativeRoot, observabilityInputTranscripts, observabilityReplies, references)
	if err == nil {
		t.Fatal("artifact assertions passed on a truncated, redacted artifact set; the check is vacuous")
	}
	message := err.Error()
	for _, want := range []string{"session log", "turn 2", "RMS"} {
		if !strings.Contains(message, want) {
			t.Fatalf("negative-control violation should name %q, got: %s", want, message)
		}
	}
	t.Logf("negative control rejected as expected: %v", err)
}
