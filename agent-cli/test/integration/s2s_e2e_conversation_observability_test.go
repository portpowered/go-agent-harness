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
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// The s2s-e2e-conversation-observability lane proves that the durable
// artifacts of the CLI session command alone — never live test
// instrumentation — demonstrate that a multi-turn spoken conversation happened.
// Each turn is driven through the shipped session surface exactly like the
// depth-4 proof (one CLI invocation per turn over a replayed slice), but with
// --record-dir so every invocation leaves a recording directory containing a
// machine-readable session log, both-side frame transcripts, recorded input
// utterance audio, and recorded reply audio. The assertions then re-read ONLY
// those on-disk artifacts.

const (
	// observabilityTurnCount is the number of conversation turns driven and
	// asserted. The PRD requires >= 3; the committed corpus has 4.
	observabilityTurnCount = 4

	// observabilityRMSThreshold is the documented minimum RMS energy (PCM16
	// linear scale) for recorded reply audio established by the depth-3 proof:
	// voiced speech measures ~2000, digital silence measures 0.
	observabilityRMSThreshold = 500.0
)

// observabilityReplies are the full assistant replies each turn must show in
// the session logs, in conversation order.
var observabilityReplies = []string{
	"ZEPHYR noted. I will remember it.",
	"Sunny and mild today.",
	"The word was ZEPHYR.",
	"Backwards it is RYHPEZ.",
}

// observabilityReplyDeltas split each reply into streamed deltas so the
// fixtures exercise delta accumulation, mirroring the depth-4 captures.
var observabilityReplyDeltas = [][]string{
	{"ZEPHYR noted.", " I will remember it."},
	{"Sunny and mild", " today."},
	{"The word was ", "ZEPHYR."},
	{"Backwards it is ", "RYHPEZ."},
}

// buildObservabilityTurnFixture materializes one single-turn replay fixture
// whose outbound records expect the exact paced PCM16 frames of the committed
// per-turn corpus WAV and whose inbound records deliver the turn's scripted
// spoken reply: text deltas plus a terminal text.done, real voiced output
// audio, and response.done. The reply audio reuses the turn's own corpus
// samples, which clear the documented RMS threshold by a wide margin.
func buildObservabilityTurnFixture(t *testing.T, turn int) string {
	t.Helper()

	wavPath := locateCLIFixture(t, multiturnTurnWAVs[turn-1])
	frames := multiturnAudioFrames(t, wavPath)
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed turn WAV %s: %v", wavPath, err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed turn WAV %s: %v", wavPath, err)
	}
	replyPCM := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(replyPCM[i*2:], uint16(sample))
	}
	replyB64 := base64.StdEncoding.EncodeToString(replyPCM)

	sessionID := fmt.Sprintf("sess_observability_turn%d", turn)
	responseID := fmt.Sprintf("resp_observability_turn%d", turn)
	var records []gwtesting.CapturedSessionEvent
	sequence := 0
	add := func(direction gwtesting.SessionEventDirection, eventType string, payload string) {
		sequence++
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    sequence,
			Direction:   direction,
			TimestampMs: int64(sequence),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(payload),
		})
	}

	add(gwtesting.DirectionClientToServer, "session.update",
		`{"type":"session.update","session":{"model":"gpt-realtime","type":"realtime"}}`)
	add(gwtesting.DirectionServerToClient, "session.created",
		fmt.Sprintf(`{"type":"session.created","session":{"id":%q,"model":"gpt-realtime"}}`, sessionID))
	for _, frame := range frames {
		add(gwtesting.DirectionClientToServer, "input_audio_buffer.append",
			fmt.Sprintf(`{"type":"input_audio_buffer.append","audio":%q}`, base64.StdEncoding.EncodeToString(frame)))
	}
	add(gwtesting.DirectionClientToServer, "input_audio_buffer.commit", `{"type":"input_audio_buffer.commit"}`)
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, "response.created",
		fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, responseID))
	add(gwtesting.DirectionServerToClient, "response.output_item.added",
		fmt.Sprintf(`{"type":"response.output_item.added","response_id":%q,"output_index":0,"item":{"type":"message","role":"assistant","id":"item_assistant_turn%d"}}`, responseID, turn))
	for _, delta := range observabilityReplyDeltas[turn-1] {
		add(gwtesting.DirectionServerToClient, "response.output_text.delta",
			fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, delta))
	}
	add(gwtesting.DirectionServerToClient, "response.output_text.done",
		fmt.Sprintf(`{"type":"response.output_text.done","text":%q}`, observabilityReplies[turn-1]))
	add(gwtesting.DirectionServerToClient, "response.output_audio.delta",
		fmt.Sprintf(`{"type":"response.output_audio.delta","delta":%q}`, replyB64))
	add(gwtesting.DirectionServerToClient, "response.output_audio.done", `{"type":"response.output_audio.done"}`)
	add(gwtesting.DirectionServerToClient, "response.done",
		fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed"}}`, responseID))
	add(gwtesting.DirectionServerToClient, "session.closed",
		fmt.Sprintf(`{"type":"session.closed","session_id":%q,"reason":"fixture_complete"}`, sessionID))

	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session:  gwtesting.SessionMetadata{ID: sessionID},
		Records:  records,
	}
	path := filepath.Join(t.TempDir(), fmt.Sprintf("observability_turn%d.session.json", turn))
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal observability fixture turn %d: %v", turn, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write observability fixture turn %d: %v", turn, err)
	}
	return path
}

// runObservabilityTurn drives one conversation turn through the shipped CLI
// session command with --record-dir. Artifacts are final when the command
// returns because the recording bundle is finalized synchronously inside the
// command; no stdout content participates in any assertion.
func runObservabilityTurn(t *testing.T, fixturePath, wavPath, recordDir string) {
	t.Helper()

	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	cmd.SetOut(os.Stderr)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"--replay", fixturePath, "--audio-in", wavPath, "--record-dir", recordDir})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("session command turn over replay with --record-dir: %v", err)
	}
}

// observabilityReferenceUtterance returns the exact PCM16 byte stream the
// committed corpus WAV contributes to the wire (frame-identical to what the
// paced file source sends), for byte comparison against recorded input audio.
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

// assertConversationArtifactEvidence reconstructs the conversation purely from
// the on-disk recording directories under root and proves, per turn in order:
// an intact manifest whose hashes match the emitted bytes, a session log
// entry carrying the expected full reply text, the exact committed user
// utterance audio, non-empty recorded reply audio above the silence threshold,
// and at least three logged turns overall. Every violation names the turn and
// artifact involved; all violations are returned joined.
func assertConversationArtifactEvidence(root string, wantReplies []string, referenceUtterances [][]byte) error {
	dirEntries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read artifact root %s: %w", root, err)
	}
	names := make([]string, 0, len(dirEntries))
	for _, entry := range dirEntries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	var violations []error
	if len(names) < 3 {
		violations = append(violations, fmt.Errorf("artifact set holds %d turn directories, want >= 3", len(names)))
	}
	if len(names) != len(wantReplies) {
		violations = append(violations, fmt.Errorf("artifact set holds %d turn directories, want %d drove turns", len(names), len(wantReplies)))
	}
	for index, name := range names {
		violation := assertTurnArtifactEvidence(index+1, name, filepath.Join(root, name), wantReplies[index], referenceUtterances[index])
		if violation != nil {
			violations = append(violations, violation)
		}
	}
	return errors.Join(violations...)
}

// assertTurnArtifactEvidence verifies one turn's recording directory contents.
func assertTurnArtifactEvidence(turn int, name, dir, wantReply string, referenceUtterance []byte) error {
	sessionLogBytes, err := os.ReadFile(filepath.Join(dir, "session-log.jsonl"))
	if err != nil {
		return fmt.Errorf("%s: session log unreadable: %w", name, err)
	}
	raw := string(bytes.TrimSpace(sessionLogBytes))
	lines := bytes.Split(bytes.TrimSpace(sessionLogBytes), []byte("\n"))
	if len(lines) != 1 || len(bytes.TrimSpace(lines[0])) == 0 {
		return fmt.Errorf("%s: session log holds %d entries (%q), want exactly 1 for a single-turn session", name, len(lines), raw)
	}
	var entry struct {
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
	if err := json.Unmarshal(lines[0], &entry); err != nil {
		return fmt.Errorf("%s: decode session log entry: %w", name, err)
	}
	if entry.TurnIndex != 1 {
		// Every recording directory holds exactly one single-turn session, so
		// its log starts at the session-local index 1. The conversation ORDER
		// is carried by the ordered turn-NN directory sequence together with
		// the exact utterance bytes pinned inside each directory below.
		return fmt.Errorf("%s: session log turn_index = %d, want 1 for a single-turn session; raw entry: %s", name, entry.TurnIndex, raw)
	}
	if !entry.Input.Committed {
		return fmt.Errorf("%s: session log records no committed user input for this turn", name)
	}
	if entry.Response.Text != wantReply {
		return fmt.Errorf("%s: session log reply = %q, want %q", name, entry.Response.Text, wantReply)
	}
	if !entry.Response.Complete {
		return fmt.Errorf("%s: session log marks the reply incomplete; the turn did not finish cleanly", name)
	}

	inputAudio, err := readRecordingSegments(dir, "audio/in-", entry.Input.AudioBytes)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if !bytes.Equal(inputAudio, referenceUtterance) {
		return fmt.Errorf("%s: recorded utterance audio (%d bytes) does not match the expected spoken utterance (%d bytes)", name, len(inputAudio), len(referenceUtterance))
	}
	outputAudio, err := readRecordingSegments(dir, "audio/out-", entry.Response.AudioBytes)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if len(outputAudio) == 0 {
		return fmt.Errorf("%s: no recorded output audio found; the reply was not captured", name)
	}
	rms := pcm16LERMS(outputAudio)
	if rms <= observabilityRMSThreshold {
		return fmt.Errorf("%s: recorded reply RMS = %.1f, want > %.1f (silence threshold)", name, rms, observabilityRMSThreshold)
	}

	if violation := verifyTurnManifestHashes(dir); violation != nil {
		return fmt.Errorf("%s: %w", name, violation)
	}
	return nil
}

// readRecordingSegments concatenates the named segment family inside one
// recording directory, requiring the total size to match the expectation from
// the session log.
func readRecordingSegments(dir, prefix string, wantBytes uint64) ([]byte, error) {
	matches, err := filepath.Glob(filepath.Join(dir, prefix+"*.pcm"))
	if err != nil {
		return nil, fmt.Errorf("list segments under %s: %w", dir, err)
	}
	sort.Strings(matches)
	var combined []byte
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		combined = append(combined, data...)
	}
	if uint64(len(combined)) != wantBytes {
		return combined, fmt.Errorf("recorded %s segments hold %d bytes, session log accounts %d", prefix, len(combined), wantBytes)
	}
	return combined, nil
}

// verifyTurnManifestHashes re-hashes every regular artifact listed by the
// recording manifest against its recorded digest, proving the bundle still
// describes exactly the bytes on disk.
func verifyTurnManifestHashes(dir string) error {
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

// driveObservabilityConversation runs the full four-turn conversation through
// the shipped CLI session command into fresh per-turn recording directories
// under root and returns the reference utterance PCM per turn.
func driveObservabilityConversation(t *testing.T, root string) [][]byte {
	t.Helper()

	references := make([][]byte, 0, observabilityTurnCount)
	for turn := 1; turn <= observabilityTurnCount; turn++ {
		fixture := buildObservabilityTurnFixture(t, turn)
		wavPath := locateCLIFixture(t, multiturnTurnWAVs[turn-1])
		references = append(references, observabilityReferenceUtterance(t, turn))
		runObservabilityTurn(t, fixture, wavPath, filepath.Join(root, fmt.Sprintf("turn-%02d", turn)))
	}
	return references
}

// TestSessionCommandConversationObservabilityProvesConversationFromArtifactsOnly
// is the lane's proof of record: after the multi-turn replayed conversation
// completes, the recording directories left behind by the session command —
// read exclusively, post-hoc, with no access to any live event stream —
// reconstruct the whole conversation: ordered turns, the exact spoken
// utterances that went in, the full replies that came out, and audible reply
// energy above the documented silence threshold.
func TestSessionCommandConversationObservabilityProvesConversationFromArtifactsOnly(t *testing.T) {
	root := t.TempDir()
	references := driveObservabilityConversation(t, root)

	if err := assertConversationArtifactEvidence(root, observabilityReplies, references); err != nil {
		t.Fatalf("on-disk artifacts do not prove the conversation: %v", err)
	}
}

// TestSessionCommandConversationObservabilityNegativeControlFailsTruncatedArtifacts
// proves the artifact-only assertions are not vacuous: against a truncated and
// redacted copy of the same artifact set — one turn's session log removed and
// another turn's reply audio replaced with digital silence — the identical
// assertion fails and names the missing evidence.
func TestSessionCommandConversationObservabilityNegativeControlFailsTruncatedArtifacts(t *testing.T) {
	root := t.TempDir()
	references := driveObservabilityConversation(t, root)

	negativeRoot := filepath.Join(t.TempDir(), "negative")
	copyArtifactTree(t, root, negativeRoot)
	if err := os.Remove(filepath.Join(negativeRoot, "turn-04", "session-log.jsonl")); err != nil {
		t.Fatalf("truncate negative control session log: %v", err)
	}
	redactedReplyPath := filepath.Join(negativeRoot, "turn-02", "audio", "out-000.pcm")
	if err := os.WriteFile(redactedReplyPath, silenceBytes(lenOf(redactedReplyPath)), 0o644); err != nil {
		t.Fatalf("redact negative control reply audio: %v", err)
	}

	err := assertConversationArtifactEvidence(negativeRoot, observabilityReplies, references)
	if err == nil {
		t.Fatal("artifact assertions passed on a truncated, redacted artifact set; the check is vacuous")
	}
	message := err.Error()
	for _, want := range []string{"turn-04", "session log", "turn-02", "RMS"} {
		if !bytes.Contains([]byte(message), []byte(want)) {
			t.Fatalf("negative-control violation should name %q, got: %s", want, message)
		}
	}
	t.Logf("negative control rejected as expected: %v", err)
}

func lenOf(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(data)
}

func silenceBytes(n int) []byte {
	return make([]byte, n)
}
