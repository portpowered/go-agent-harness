//go:build live

// Opt-in live confirmation for the two mixed-modality session compositions
// repaired by this lane. The default hermetic tests prove the behavior without
// credentials; these tests are deliberately limited to one finite run and one
// two-turn scheduled run when billing is explicitly enabled.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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
	capture     gwtesting.SessionCapture
	stdout      string
	stderr      string
	err         error
	capturePath string
	recordDir   string
}

type mixedModalityLiveObservation struct {
	eventTypes              []string
	imageItemCount          int
	encodedImageOccurrences int
	audioAppendIndices      []int
	commitIndices           []int
	responseCreateIndices   []int
	responseDoneIndices     []int
	completedResponses      int
	inputAudioBytes         int
	outputAudioBytes        int
	transcripts             []string
}

// TestLiveMixedModalityFiniteAudioWithImage is the one live finite-turn
// confirmation for this lane. The image is queued first, the spoken question
// supplies the only response boundary, and the final transcript must identify
// both authored visual facts.
func TestLiveMixedModalityFiniteAudioWithImage(t *testing.T) {
	apiKey := requireMixedModalityLiveOptIn(t)
	imagePath := writeMixedModalityImage(t)
	audioPath := locateCLIFixture(t, visionDescribeQuestionWAV)
	run := runMixedModalityLiveSession(t, apiKey, imagePath, []string{audioPath}, false)
	if run.err != nil {
		t.Fatalf("finite mixed-modality live command failed: %v", run.err)
	}

	observation := assertMixedModalityLiveWireContract(t, run.capture, imagePath, []string{audioPath}, false)
	assertMixedModalityGrounding(t, observation.transcripts, run.stdout)
	retainMixedModalityLiveArtifacts(t, run, "finite")
	logMixedModalityLiveEvidence(t, run.capture, observation, imagePath, "finite")
}

// TestLiveMixedModalityScheduledAudioWithImage is the one live scheduled
// confirmation. Two finite spoken turns make a duplicated or standalone image
// observable while the first response remains the image-grounded turn.
func TestLiveMixedModalityScheduledAudioWithImage(t *testing.T) {
	apiKey := requireMixedModalityLiveOptIn(t)
	imagePath := writeMixedModalityImage(t)
	audioPaths := []string{
		locateCLIFixture(t, visionDescribeQuestionWAV),
		locateCLIFixture(t, visionDescribeQuestionWAV),
	}
	run := runMixedModalityLiveSession(t, apiKey, imagePath, audioPaths, true)
	if run.err != nil {
		t.Fatalf("scheduled mixed-modality live command failed: %v", run.err)
	}

	observation := assertMixedModalityLiveWireContract(t, run.capture, imagePath, audioPaths, true)
	if len(observation.transcripts) < 2 {
		t.Fatalf("scheduled live transcript count = %d, want at least two", len(observation.transcripts))
	}
	assertMixedModalityGrounding(t, observation.transcripts[:1], run.stdout)
	assertCLILiveRecordingBundle(t, run.recordDir, len(audioPaths))
	retainMixedModalityLiveArtifacts(t, run, "scheduled")
	logMixedModalityLiveEvidence(t, run.capture, observation, imagePath, "scheduled")
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

func runMixedModalityLiveSession(t *testing.T, apiKey, imagePath string, audioPaths []string, scheduled bool) mixedModalityLiveRun {
	t.Helper()
	workDir := t.TempDir()
	// This confirmation measures the image/audio boundary itself. Disable the
	// default registry so a model cannot elect an unrelated tool continuation
	// and make the response count exceed the supplied audio-turn count.
	writeSessionToolConfig(t, workDir, false)
	capturePath := filepath.Join(workDir, "mixed-modality.session.json")
	recordDir := ""
	if scheduled {
		recordDir = filepath.Join(workDir, "mixed-modality-recording")
	}

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
		"--record", capturePath,
		"--max-duration", mixedModalityLiveMaxRun.String(),
		"--system-prompt", mixedModalityLiveSystem,
		"--image", imagePath,
	}
	if scheduled {
		args = append(args, "--record-dir", recordDir)
		for _, audioPath := range audioPaths {
			args = append(args, "--audio-in-turn", audioPath)
		}
	} else {
		args = append(args, "--audio-in", audioPaths[0])
	}
	rootCmd.SetArgs(args)

	ctx, cancel := context.WithTimeout(context.Background(), mixedModalityLiveMaxRun+15*time.Second)
	defer cancel()
	runErr := rootCmd.ExecuteContext(ctx)
	capture, loadErr := gwtesting.LoadSessionCapture(capturePath)
	if loadErr != nil {
		t.Fatalf("load mixed-modality live capture (run error: %v): %v", runErr, loadErr)
	}
	return mixedModalityLiveRun{
		capture:     capture,
		stdout:      stdout.String(),
		stderr:      stderr.String(),
		err:         runErr,
		capturePath: capturePath,
		recordDir:   recordDir,
	}
}

func assertMixedModalityLiveWireContract(t *testing.T, capture gwtesting.SessionCapture, imagePath string, audioPaths []string, scheduled bool) mixedModalityLiveObservation {
	t.Helper()
	if capture.Provider.Name != "openai" || capture.Provider.Model != mixedModalityLiveModel {
		t.Fatalf("mixed-modality live provider = (%q, %q), want (openai, %q)", capture.Provider.Name, capture.Provider.Model, mixedModalityLiveModel)
	}

	observation := mixedModalityLiveObservation{
		eventTypes:  make([]string, 0, len(capture.Records)),
		transcripts: make([]string, 0, len(audioPaths)),
	}
	imageBytes, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read mixed-modality image for wire assertion: %v", err)
	}
	expectedImage := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
	for index, record := range capture.Records {
		prefix := "S"
		if record.Direction == gwtesting.DirectionClientToServer {
			prefix = "C"
		}
		observation.eventTypes = append(observation.eventTypes, prefix+":"+record.Type)
		payload := mixedModalityLivePayload(record)
		if len(payload) == 0 {
			t.Fatalf("mixed-modality live capture record %d (%s) has an empty payload", index, record.Type)
		}

		if record.Direction == gwtesting.DirectionClientToServer {
			switch record.Type {
			case "conversation.item.create":
				var event struct {
					Item struct {
						Type    string `json:"type"`
						Content []struct {
							Type     string `json:"type"`
							ImageURL string `json:"image_url"`
						} `json:"content"`
					} `json:"item"`
				}
				mixedModalityLiveUnmarshal(t, payload, &event, "image item")
				if event.Item.Type != "message" {
					continue
				}
				for _, part := range event.Item.Content {
					if part.Type != "input_image" {
						continue
					}
					observation.imageItemCount++
					observation.encodedImageOccurrences += strings.Count(part.ImageURL, expectedImage)
				}
			case "input_audio_buffer.append":
				var event struct {
					Audio string `json:"audio"`
				}
				mixedModalityLiveUnmarshal(t, payload, &event, "audio append")
				decoded, decodeErr := base64.StdEncoding.DecodeString(event.Audio)
				if decodeErr != nil || len(decoded) == 0 {
					t.Fatalf("mixed-modality live audio append %d is empty or invalid: %v", len(observation.audioAppendIndices)+1, decodeErr)
				}
				observation.audioAppendIndices = append(observation.audioAppendIndices, index)
				observation.inputAudioBytes += len(decoded)
			case "input_audio_buffer.commit":
				observation.commitIndices = append(observation.commitIndices, index)
			case "response.create":
				observation.responseCreateIndices = append(observation.responseCreateIndices, index)
			}
			continue
		}

		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.output_audio_transcript.done":
			var event struct {
				Transcript string `json:"transcript"`
			}
			mixedModalityLiveUnmarshal(t, payload, &event, "assistant transcript")
			if strings.TrimSpace(event.Transcript) != "" {
				observation.transcripts = append(observation.transcripts, strings.TrimSpace(event.Transcript))
			}
		case "response.output_audio.delta", "response.audio.delta":
			var event struct {
				Delta string `json:"delta"`
			}
			mixedModalityLiveUnmarshal(t, payload, &event, "assistant audio")
			decoded, decodeErr := base64.StdEncoding.DecodeString(event.Delta)
			if decodeErr != nil || len(decoded) == 0 {
				t.Fatalf("mixed-modality live assistant audio is empty or invalid: %v", decodeErr)
			}
			observation.outputAudioBytes += len(decoded)
		case "response.done":
			var event struct {
				Response struct {
					Status string `json:"status"`
				} `json:"response"`
				Status string `json:"status"`
			}
			mixedModalityLiveUnmarshal(t, payload, &event, "response.done")
			status := event.Response.Status
			if status == "" {
				status = event.Status
			}
			observation.responseDoneIndices = append(observation.responseDoneIndices, index)
			if status != "completed" {
				t.Fatalf("mixed-modality live response.done status = %q at record %d, want completed", status, index)
			}
			observation.completedResponses++
		}
	}

	wantTurns := len(audioPaths)
	if observation.imageItemCount != 1 || observation.encodedImageOccurrences != 1 {
		t.Fatalf("mixed-modality live image items = %d, encoded image occurrences = %d; want one image item and one payload", observation.imageItemCount, observation.encodedImageOccurrences)
	}
	if len(observation.audioAppendIndices) == 0 || observation.inputAudioBytes == 0 {
		t.Fatalf("mixed-modality live audio appends = %d, bytes = %d; want non-empty audio", len(observation.audioAppendIndices), observation.inputAudioBytes)
	}
	if len(observation.commitIndices) != wantTurns || len(observation.responseCreateIndices) != wantTurns || observation.completedResponses != wantTurns {
		t.Fatalf("mixed-modality live boundaries = appends:%d commits:%d responses:%d completed:%d; want %d commit/create/completed turns", len(observation.audioAppendIndices), len(observation.commitIndices), len(observation.responseCreateIndices), observation.completedResponses, wantTurns)
	}
	if observation.outputAudioBytes == 0 || len(observation.transcripts) < wantTurns {
		t.Fatalf("mixed-modality live assistant output = audio:%d transcript_turns:%d; want non-empty output for %d turns", observation.outputAudioBytes, len(observation.transcripts), wantTurns)
	}
	appendCountPerTurn := make([]int, 0, wantTurns)
	for _, audioPath := range audioPaths {
		frameCount := len(multiturnAudioFrames(t, audioPath))
		if frameCount == 0 {
			t.Fatalf("mixed-modality live WAV %q produced no audio frames", filepath.Base(audioPath))
		}
		if scheduled {
			// ScheduledAudioInput reads each finite WAV as one turn-sized
			// payload; the finite source intentionally streams frame-sized
			// appends for its standalone input path.
			appendCountPerTurn = append(appendCountPerTurn, 1)
		} else {
			appendCountPerTurn = append(appendCountPerTurn, frameCount)
		}
	}
	expectedAppendCount := 0
	for _, appendCount := range appendCountPerTurn {
		expectedAppendCount += appendCount
	}
	if len(observation.audioAppendIndices) != expectedAppendCount {
		if scheduled {
			t.Fatalf("mixed-modality live audio append count = %d, want one non-empty append per scheduled WAV (%d)", len(observation.audioAppendIndices), expectedAppendCount)
		}
		t.Fatalf("mixed-modality live audio append count = %d, want %d frames from the supplied WAV input", len(observation.audioAppendIndices), expectedAppendCount)
	}

	imageIndex := mixedModalityLiveEventIndex(capture, "conversation.item.create", gwtesting.DirectionClientToServer, 0)
	if imageIndex < 0 {
		t.Fatal("mixed-modality live capture has no outbound image item")
	}
	if imageIndex >= observation.audioAppendIndices[0] {
		t.Fatalf("mixed-modality live image index = %d, first audio append = %d; image must be queued first", imageIndex, observation.audioAppendIndices[0])
	}
	appendOffset := 0
	for turn, appendCount := range appendCountPerTurn {
		lastAppend := observation.audioAppendIndices[appendOffset+appendCount-1]
		if observation.commitIndices[turn] <= lastAppend || observation.responseCreateIndices[turn] <= observation.commitIndices[turn] || observation.responseDoneIndices[turn] <= observation.responseCreateIndices[turn] {
			t.Fatalf("mixed-modality live turn %d boundary order = append:%d commit:%d response:%d done:%d; want append < commit < response.create < response.done", turn+1, lastAppend, observation.commitIndices[turn], observation.responseCreateIndices[turn], observation.responseDoneIndices[turn])
		}
		if turn > 0 && observation.audioAppendIndices[appendOffset] <= observation.responseDoneIndices[turn-1] {
			t.Fatalf("mixed-modality live turn %d first append at %d crossed before turn %d response.done at %d", turn+1, observation.audioAppendIndices[appendOffset], turn, observation.responseDoneIndices[turn-1])
		}
		appendOffset += appendCount
	}
	return observation
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

func writeMixedModalityImage(t *testing.T) string {
	t.Helper()
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 245, G: 245, B: 245, A: 255})
		}
	}
	red := color.RGBA{R: 220, G: 24, B: 40, A: 255}
	for y := 32; y < 128; y++ {
		for x := 32; x < 128; x++ {
			img.SetRGBA(x, y, red)
		}
	}
	blue := color.RGBA{R: 20, G: 70, B: 220, A: 255}
	for x := 24; x < 232; x++ {
		centerY := 232 - x
		for offset := -5; offset <= 5; offset++ {
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

func mixedModalityLiveEventIndex(capture gwtesting.SessionCapture, eventType string, direction gwtesting.SessionEventDirection, occurrence int) int {
	seen := 0
	for index, record := range capture.Records {
		if record.Type != eventType || record.Direction != direction {
			continue
		}
		if seen == occurrence {
			return index
		}
		seen++
	}
	return -1
}

func mixedModalityLivePayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func mixedModalityLiveUnmarshal(t *testing.T, payload []byte, destination any, description string) {
	t.Helper()
	if err := json.Unmarshal(payload, destination); err != nil {
		t.Fatalf("decode mixed-modality live %s: %v", description, err)
	}
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
	if err := copyMixedModalityPath(run.capturePath, filepath.Join(destination, "session.json")); err != nil {
		t.Fatalf("retain mixed-modality live %s capture: %v", label, err)
	}
	if run.recordDir != "" {
		if err := copyMixedModalityPath(run.recordDir, filepath.Join(destination, "recording")); err != nil {
			t.Fatalf("retain mixed-modality live %s recording: %v", label, err)
		}
	}
	t.Logf("retained mixed-modality live %s capture at <artifact-dir>/%s/session.json", label, label)
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

func logMixedModalityLiveEvidence(t *testing.T, capture gwtesting.SessionCapture, observation mixedModalityLiveObservation, imagePath, composition string) {
	t.Helper()
	imageBytes, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read mixed-modality image for evidence: %v", err)
	}
	digest := sha256.Sum256(imageBytes)
	eventOrder := strings.Join(observation.eventTypes, ">")
	if len(eventOrder) > 1200 {
		eventOrder = eventOrder[:1200] + "..."
	}
	transcript := strings.TrimSpace(strings.Join(observation.transcripts, " | "))
	if len(transcript) > 240 {
		transcript = transcript[:240] + "..."
	}
	t.Logf("sanitized mixed-modality live evidence: composition=%s model=%s max_duration=%s input_audio_bytes=%d audio_append_count=%d commit_count=%d response_create_count=%d image_item_count=%d encoded_image_occurrences=%d output_audio_bytes=%d terminal_response_count=%d exit=0 image_sha256=%s event_order=%s transcript=%q", composition, capture.Provider.Model, mixedModalityLiveMaxRun, observation.inputAudioBytes, len(observation.audioAppendIndices), len(observation.commitIndices), len(observation.responseCreateIndices), observation.imageItemCount, observation.encodedImageOccurrences, observation.outputAudioBytes, observation.completedResponses, hex.EncodeToString(digest[:]), eventOrder, transcript)
}
