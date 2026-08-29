//go:build live

// Opt-in live confirmation for the mixed-modality --record-dir-only session
// repaired by this lane. The default hermetic tests prove the behavior without
// credentials; this test performs exactly one finite scheduled turn when
// billing is explicitly enabled.
package integration

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

const (
	mixedModalityLiveModel     = "gpt-realtime-2.1-mini"
	mixedModalityLiveMaxRun    = 60 * time.Second
	mixedModalityLiveOptIn     = "AGENT_HARNESS_LIVE_MIXED_MODALITY"
	mixedModalityLiveArtifact  = "AGENT_HARNESS_LIVE_MIXED_MODALITY_ARTIFACT_DIR"
	mixedModalityLiveSystem    = "You are a terse visual assistant. Answer in five words or fewer. For the first spoken question, use the supplied image and say red square and blue diagonal when those facts are visible. Answer later spoken turns briefly, and do not call tools."
	mixedModalityLiveImageName = "red-square-blue-diagonal.png"
)

type mixedModalityLiveRun struct {
	stdout    string
	stderr    string
	err       error
	recordDir string
}

type mixedModalityLiveRecordingEntry struct {
	TurnIndex int `json:"turn_index"`
	Input     struct {
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

// TestLiveMixedModalityRecordDirOnlyWithImage is the one live confirmation for
// this lane. The image is queued first, the finite scheduled audio supplies
// the only response boundary, and the finalized recording must identify both
// authored visual facts. There is intentionally no --record capture file.
func TestLiveMixedModalityRecordDirOnlyWithImage(t *testing.T) {
	apiKey := requireMixedModalityLiveOptIn(t)
	imagePath := writeMixedModalityImage(t)
	audioPath := locateCLIFixture(t, visionDescribeQuestionWAV)
	run := runMixedModalityLiveSession(t, apiKey, imagePath, audioPath)
	if run.err != nil {
		t.Fatalf("record-dir-only mixed-modality live command failed: %v", run.err)
	}

	entries := assertMixedModalityLiveRecordingBundle(t, run.recordDir, 1)
	transcripts := make([]string, 0, len(entries))
	for _, entry := range entries {
		transcripts = append(transcripts, entry.Response.Text)
	}
	assertMixedModalityGrounding(t, transcripts, run.stdout)
	retainMixedModalityLiveArtifacts(t, run, "record-dir-only")
	logMixedModalityLiveEvidence(t, entries, imagePath)
}

func requireMixedModalityLiveOptIn(t *testing.T) string {
	t.Helper()
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the live mixed-modality confirmation")
	}
	if os.Getenv(mixedModalityLiveOptIn) != "1" {
		t.Skip(mixedModalityLiveOptIn + "!=1; this live test bills real API usage and must be opted into explicitly")
	}
	return apiKey
}

func runMixedModalityLiveSession(t *testing.T, apiKey, imagePath, audioPath string) mixedModalityLiveRun {
	t.Helper()
	workDir := t.TempDir()
	// This confirmation measures the image/audio boundary itself. Disable the
	// default registry so a model cannot elect an unrelated tool continuation
	// and make the response count exceed the supplied audio-turn count.
	writeSessionToolConfig(t, workDir, false)
	recordDir := filepath.Join(workDir, "mixed-modality-recording")

	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production CLI composition: %v", err)
	}
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	args := []string{
		"--config-dir", workDir,
		"session",
		"--provider", "openai",
		"--model", mixedModalityLiveModel,
		"--api-key", apiKey,
		"--record-dir", recordDir,
		"--max-duration", mixedModalityLiveMaxRun.String(),
		"--system-prompt", mixedModalityLiveSystem,
		"--image", imagePath,
		"--audio-in-turn", audioPath,
	}
	rootCmd.SetArgs(args)

	ctx, cancel := context.WithTimeout(context.Background(), mixedModalityLiveMaxRun+15*time.Second)
	defer cancel()
	runErr := rootCmd.ExecuteContext(ctx)
	return mixedModalityLiveRun{
		stdout:    stdout.String(),
		stderr:    stderr.String(),
		err:       runErr,
		recordDir: recordDir,
	}
}

func assertMixedModalityGrounding(t *testing.T, transcripts []string, cliOutput string) {
	t.Helper()
	transcript := strings.ToLower(strings.Join(transcripts, " "))
	output := strings.ToLower(cliOutput)
	for _, term := range []string{"red", "square", "blue", "diagonal"} {
		if !strings.Contains(transcript, term) || !strings.Contains(output, term) {
			t.Fatalf("mixed-modality live output missing grounded %q (transcript=%q cli_output=%q)", term, strings.Join(transcripts, " "), cliOutput)
		}
	}
}

// assertMixedModalityLiveRecordingBundle checks the durable per-turn proof
// without assuming that a live provider emits one output file per response.
// Realtime output is chunked, so the manifest may contain several output PCM
// artifacts for one turn; session-log entries carry the turn-level boundary.
func assertMixedModalityLiveRecordingBundle(t *testing.T, destination string, turns int) []mixedModalityLiveRecordingEntry {
	t.Helper()
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read mixed-modality recording manifest: %v", err)
	}
	var manifest struct {
		Artifacts []struct {
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode mixed-modality recording manifest: %v", err)
	}
	inputArtifacts, outputArtifacts := make([]string, 0, turns), make([]string, 0)
	for _, artifact := range manifest.Artifacts {
		switch {
		case strings.HasPrefix(artifact.Path, "audio/in-"):
			inputArtifacts = append(inputArtifacts, artifact.Path)
		case strings.HasPrefix(artifact.Path, "audio/out-"):
			outputArtifacts = append(outputArtifacts, artifact.Path)
		}
	}
	if len(inputArtifacts) != turns || len(outputArtifacts) == 0 {
		t.Fatalf("mixed-modality recording audio artifacts = input:%d output:%d, want %d inputs and non-empty output", len(inputArtifacts), len(outputArtifacts), turns)
	}
	for _, artifactPath := range append(inputArtifacts, outputArtifacts...) {
		data, err := os.ReadFile(filepath.Join(destination, artifactPath))
		if err != nil {
			t.Fatalf("read mixed-modality audio artifact %q: %v", artifactPath, err)
		}
		if len(data) == 0 {
			t.Fatalf("mixed-modality audio artifact %q is empty", artifactPath)
		}
	}

	logFile, err := os.Open(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("open mixed-modality session log: %v", err)
	}
	defer logFile.Close()
	entries := make([]mixedModalityLiveRecordingEntry, 0, turns)
	scanner := bufio.NewScanner(logFile)
	for scanner.Scan() {
		var entry mixedModalityLiveRecordingEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode mixed-modality session log entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read mixed-modality session log: %v", err)
	}
	if len(entries) != turns {
		t.Fatalf("mixed-modality session log entries = %d, want %d", len(entries), turns)
	}
	for index, entry := range entries {
		if entry.TurnIndex != index+1 || !entry.Input.Committed || entry.Input.AudioBytes == 0 || !entry.Response.Complete || entry.Response.AudioBytes == 0 {
			t.Fatalf("mixed-modality session log entry %d lacks committed input and completed output audio: %#v", index+1, entry)
		}
	}
	return entries
}

func writeMixedModalityImage(t *testing.T) string {
	t.Helper()
	const size = 64
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 245, G: 245, B: 245, A: 255})
		}
	}
	red := color.RGBA{R: 220, G: 24, B: 40, A: 255}
	for y := 8; y < 32; y++ {
		for x := 8; x < 32; x++ {
			img.SetRGBA(x, y, red)
		}
	}
	blue := color.RGBA{R: 20, G: 70, B: 220, A: 255}
	for x := 5; x < 59; x++ {
		centerY := 58 - x
		for offset := -1; offset <= 1; offset++ {
			y := centerY + offset
			if y >= 0 && y < size {
				img.SetRGBA(x, y, blue)
			}
		}
	}

	path := filepath.Join(t.TempDir(), mixedModalityLiveImageName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create mixed-modality image: %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("encode mixed-modality image: %v", err)
	}
	return path
}

func retainMixedModalityLiveArtifacts(t *testing.T, run mixedModalityLiveRun, label string) {
	t.Helper()
	root := strings.TrimSpace(os.Getenv(mixedModalityLiveArtifact))
	if root == "" {
		return
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create mixed-modality live artifact directory: %v", err)
	}
	destination := filepath.Join(root, label)
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatalf("create mixed-modality live %s artifact directory: %v", label, err)
	}
	if err := copyMixedModalityPath(run.recordDir, filepath.Join(destination, "recording")); err != nil {
		t.Fatalf("retain mixed-modality live %s recording: %v", label, err)
	}
	t.Logf("retained mixed-modality live %s recording at <artifact-dir>/%s/recording", label, label)
}

func copyMixedModalityPath(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyMixedModalityPath(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func logMixedModalityLiveEvidence(t *testing.T, entries []mixedModalityLiveRecordingEntry, imagePath string) {
	t.Helper()
	imageBytes, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read mixed-modality image for evidence: %v", err)
	}
	digest := sha256.Sum256(imageBytes)
	transcripts := make([]string, 0, len(entries))
	inputAudioBytes, outputAudioBytes := uint64(0), uint64(0)
	for _, entry := range entries {
		transcripts = append(transcripts, entry.Response.Text)
		inputAudioBytes += entry.Input.AudioBytes
		outputAudioBytes += entry.Response.AudioBytes
	}
	transcript := strings.TrimSpace(strings.Join(transcripts, " | "))
	if len(transcript) > 240 {
		transcript = transcript[:240] + "..."
	}
	t.Logf("sanitized mixed-modality live evidence: composition=record-dir-only model=%s max_duration=%s input_audio_bytes=%d output_audio_bytes=%d terminal_response_count=%d recording=validated exit=0 image_sha256=%s transcript=%q", mixedModalityLiveModel, mixedModalityLiveMaxRun, inputAudioBytes, outputAudioBytes, len(entries), hex.EncodeToString(digest[:]), transcript)
}
