package integration

// The s2s-e2e-vision-describe vertical is verified exclusively through the
// shipped 'agent session' CLI over the hermetic record/replay transport: a
// customer speaks a question by voice about a committed image and the agent's
// reply names content that only exists in that image. No internal Go function
// is called by the assertions below.
//
// Fixture structure (agent-cli/test/integration/testdata/s2s-e2e-vision-describe):
//   - the committed capture redacts raw audio fields and the image data URL
//     per the repo fixture-hygiene policy; the exact PCM16 frames of the
//     committed corpus WAV, the deterministic PNG pixels, and the scripted
//     spoken reply are injected at load, mirroring the multiturn lane's
//     runtime re-injection convention.
//   - wire order under test: session handshake, conversation.item.create with
//     the image part, one input_audio_buffer.append per streamed corpus frame,
//     input_audio_buffer.commit plus exactly one response.create at
//     end-of-turn, then the grounded spoken reply and provider close.

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	visionDescribeFixtureRelativePath = "s2s-e2e-vision-describe/s2s_e2e_vision_describe.session.json"
	visionDescribeQuestionWAV         = "vision_describe_question.wav"
)

// visionDescribeContentMarkers name the authored pixel facts of the
// deterministic synthetic image: a magenta top-left pixel, a cyan
// bottom-right pixel, and a navy fill on a four by four grid. A reply that
// does not come from the actual image content cannot contain them.
var visionDescribeContentMarkers = []string{
	"four by four grid",
	"magenta pixel in the top left corner",
	"cyan pixel in the bottom right corner",
}

// visionDescribeFixturePath locates the committed lane fixture.
func visionDescribeFixturePath(t *testing.T) string {
	t.Helper()
	return locateCLIFixture(t, visionDescribeFixtureRelativePath)
}

// visionDescribePNG renders the deterministic synthetic image: a four by
// four grid with a magenta top-left pixel, a cyan bottom-right pixel, and a
// navy fill. Encoding is deterministic, so the committed fixture's redacted
// image part and the --image file always describe the same bytes.
func visionDescribePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	navy := color.NRGBA{R: 0, G: 0, B: 128, A: 255}
	for y := range 4 {
		for x := range 4 {
			img.SetNRGBA(x, y, navy)
		}
	}
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 0, B: 255, A: 255})
	img.SetNRGBA(3, 3, color.NRGBA{R: 0, G: 255, B: 255, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode vision describe PNG: %v", err)
	}
	return buf.Bytes()
}

// visionReplySamples is the scripted spoken reply: a deterministic square
// wave whose RMS sits far above the silence threshold.
func visionReplySamples() []int16 {
	samples := make([]int16, 960)
	for i := range samples {
		if (i/48)%2 == 0 {
			samples[i] = 6000
		} else {
			samples[i] = -6000
		}
	}
	return samples
}

func visionPCMBytes(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	return pcm
}

// visionRewritePayload decodes one record payload, applies the mutation, and
// re-encodes it. The replay transport compares parsed JSON, so key order is
// free.
func visionRewritePayload(t *testing.T, raw json.RawMessage, mutate func(payload map[string]any)) json.RawMessage {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode fixture payload %s: %v", raw, err)
	}
	mutate(payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode fixture payload: %v", err)
	}
	return encoded
}

// buildVisionDescribeFixture materializes the runtime replay fixture from the
// committed redacted capture: the deterministic PNG becomes the input_image
// data URL, the append marker expands into one real frame per corpus WAV
// frame, and the scripted spoken reply audio is injected. When transcript is
// non-nil it replaces the recorded reply text (negative control variant).
func buildVisionDescribeFixture(t *testing.T, transcript []string) string {
	t.Helper()

	capture := captureCopy(t, visionDescribeFixturePath(t))
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(visionDescribePNG(t))
	frames := multiturnAudioFrames(t, locateCLIFixture(t, visionDescribeQuestionWAV))
	replyAudio := base64.StdEncoding.EncodeToString(visionPCMBytes(visionReplySamples()))

	records := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records)+len(frames))
	transcriptDelta := 0
	for _, record := range capture.Records {
		switch record.Type {
		case "conversation.item.create":
			record.Payload = visionRewritePayload(t, record.Payload, func(payload map[string]any) {
				item, ok := payload["item"].(map[string]any)
				if !ok {
					t.Fatalf("fixture conversation.item.create payload missing item: %s", record.Payload)
				}
				content, ok := item["content"].([]any)
				if !ok || len(content) == 0 {
					t.Fatalf("fixture conversation.item.create payload missing content: %s", record.Payload)
				}
				part, ok := content[0].(map[string]any)
				if !ok || part["type"] != "input_image" {
					t.Fatalf("fixture first content part is not an image part: %s", record.Payload)
				}
				part["image_url"] = dataURL
			})
		case "input_audio_buffer.append":
			for _, frame := range frames {
				frameRecord := record
				frameRecord.Payload = visionRewritePayload(t, record.Payload, func(payload map[string]any) {
					payload["audio"] = base64.StdEncoding.EncodeToString(frame)
				})
				records = append(records, frameRecord)
			}
			continue
		case "response.output_audio.delta":
			record.Payload = visionRewritePayload(t, record.Payload, func(payload map[string]any) {
				payload["delta"] = replyAudio
			})
		case "response.output_audio_transcript.delta":
			if transcript != nil {
				if transcriptDelta >= len(transcript) {
					t.Fatalf("transcript override shorter than fixture delta count")
				}
				text := transcript[transcriptDelta]
				transcriptDelta++
				record.Payload = visionRewritePayload(t, record.Payload, func(payload map[string]any) {
					payload["delta"] = text
				})
			}
		case "response.output_audio_transcript.done":
			if transcript != nil {
				record.Payload = visionRewritePayload(t, record.Payload, func(payload map[string]any) {
					payload["transcript"] = strings.Join(transcript, "")
				})
			}
		}
		records = append(records, record)
	}

	sequence := 0
	capture.Records = resequencedBatch(records, &sequence)
	path := filepath.Join(t.TempDir(), "vision-describe.session.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal vision describe fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write vision describe fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("vision describe fixture rejected by the session replayer dialer: %v", err)
	}
	return path
}

// runVisionDescribeSession drives the shipped session command with a replayed
// voice utterance, a committed image, and a recording directory, and returns
// the captured stdout. It waits for the terminal close marker so
// asynchronous terminal formatting is always observed.
func runVisionDescribeSession(t *testing.T, fixturePath, wavPath, imagePath, audioOutPath string) (string, error) {
	return runVisionDescribeSessionMode(t, fixturePath, wavPath, imagePath, audioOutPath, true)
}

func runVisionDescribeSessionWithoutRecordingDirectory(t *testing.T, fixturePath, wavPath, imagePath string) (string, error) {
	return runVisionDescribeSessionMode(t, fixturePath, wavPath, imagePath, "", false)
}

func runVisionDescribeSessionMode(t *testing.T, fixturePath, wavPath, imagePath, audioOutPath string, withRecordingDirectory bool) (string, error) {
	t.Helper()
	stdout := &syncBuffer{}
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	cmd.SetOut(stdout)
	cmd.SetErr(os.Stderr)
	args := []string{
		"--replay", fixturePath,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--audio-in", wavPath,
		"--image", imagePath,
	}
	if withRecordingDirectory {
		args = append(args, "--record-dir", t.TempDir())
	}
	if audioOutPath != "" {
		args = append(args, "--audio-out", audioOutPath)
	}
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stdout.String(), "[session closed:") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return stdout.String(), err
}

// assertVisionDescribeGrounded is the lane's grounding assertion: the session
// must complete cleanly and its transcript must name the authored pixel
// facts of the committed image.
func assertVisionDescribeGrounded(output string) error {
	if !strings.Contains(output, "[session closed: fixture_complete]") {
		return fmt.Errorf("vision session did not complete cleanly, got:\n%s", output)
	}
	for _, marker := range visionDescribeContentMarkers {
		if !strings.Contains(output, marker) {
			return fmt.Errorf("transcript missing image-grounded content %q, got:\n%s", marker, output)
		}
	}
	return nil
}

// TestVisionDescribeFixtureIsWellFormed proves the committed fixture passes
// the shared capture validation surface, carries an image part on the first
// user turn, keeps raw audio redacted behind a runtime re-injection marker,
// and terminates with the provider close.
func TestVisionDescribeFixtureIsWellFormed(t *testing.T) {
	path := visionDescribeFixturePath(t)
	if violations := gwtesting.ValidateSessionCaptureFile(path); len(violations) > 0 {
		t.Fatalf("committed vision describe fixture failed validation: %v", violations)
	}
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		t.Fatalf("load committed vision describe fixture: %v", err)
	}

	imageTurns, appendMarkers, responseCreates, closed := 0, 0, 0, false
	audioAppendSeen := false
	for _, record := range capture.Records {
		switch record.Type {
		case "conversation.item.create":
			var payload struct {
				Item struct {
					Content []struct {
						Type     string          `json:"type"`
						ImageURL json.RawMessage `json:"image_url"`
					} `json:"content"`
				} `json:"item"`
			}
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				t.Fatalf("decode conversation.item.create payload: %v", err)
			}
			for _, part := range payload.Item.Content {
				if part.Type == "input_image" {
					imageTurns++
					var imageURL struct {
						Redacted bool `json:"redacted"`
					}
					if err := json.Unmarshal(part.ImageURL, &imageURL); err != nil || !imageURL.Redacted {
						t.Fatalf("committed image part must keep the data URL redacted for hygiene, got %s", part.ImageURL)
					}
				}
			}
		case "input_audio_buffer.append":
			appendMarkers++
			audioAppendSeen = true
			var payload struct {
				Audio struct {
					Redacted bool `json:"redacted"`
				} `json:"audio"`
			}
			if err := json.Unmarshal(record.Payload, &payload); err != nil || !payload.Audio.Redacted {
				t.Fatalf("committed append record must redact raw audio, got %s", record.Payload)
			}
		case "response.create":
			responseCreates++
			if !audioAppendSeen {
				t.Fatalf("committed fixture requests response.create before the voice turn; image and audio would be separate turns")
			}
		case "session.closed":
			closed = true
			if !strings.Contains(string(record.Payload), "fixture_complete") {
				t.Fatalf("session.closed payload missing fixture_complete reason: %s", record.Payload)
			}
		}
	}
	if imageTurns != 1 {
		t.Fatalf("fixture carries %d image turns, want exactly 1 on the first user turn", imageTurns)
	}
	if appendMarkers != 1 {
		t.Fatalf("fixture carries %d audio append markers, want exactly 1 re-injection marker", appendMarkers)
	}
	if responseCreates != 1 {
		t.Fatalf("fixture carries %d response.create events, want exactly one after audio begins", responseCreates)
	}
	if !closed {
		t.Fatalf("fixture never terminates with session.closed")
	}
}

// TestVisionDescribePNGIsDeterministicWithKnownContent proves the synthetic
// image is byte-stable across generations and really carries the authored
// pixel facts the grounding assertion demands.
func TestVisionDescribePNGIsDeterministicWithKnownContent(t *testing.T) {
	first := visionDescribePNG(t)
	second := visionDescribePNG(t)
	if !bytes.Equal(first, second) {
		t.Fatal("vision describe PNG generation is not deterministic")
	}

	img, err := png.Decode(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("decode vision describe PNG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 4 || bounds.Dy() != 4 {
		t.Fatalf("vision describe PNG bounds = %v, want 4x4", bounds)
	}
	visionAssertPixel(t, img, 0, 0, color.NRGBA{R: 255, G: 0, B: 255, A: 255})
	visionAssertPixel(t, img, 3, 3, color.NRGBA{R: 0, G: 255, B: 255, A: 255})
	visionAssertPixel(t, img, 1, 1, color.NRGBA{R: 0, G: 0, B: 128, A: 255})
	visionAssertPixel(t, img, 2, 2, color.NRGBA{R: 0, G: 0, B: 128, A: 255})
}

func visionAssertPixel(t *testing.T, img image.Image, x, y int, want color.NRGBA) {
	t.Helper()
	got := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	if got != want {
		t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, want)
	}
}

// TestSessionCommandVisionDescribeGroundsReplyInCommittedImage is the lane's
// main path: the customer's voice question and the committed image travel
// the real session CLI over record/replay, the reply names the image's
// authored pixel facts, and the recorded spoken reply is non-silent.
func TestSessionCommandVisionDescribeGroundsReplyInCommittedImage(t *testing.T) {
	fixture := buildVisionDescribeFixture(t, nil)
	wavPath := locateCLIFixture(t, visionDescribeQuestionWAV)
	imagePath := filepath.Join(t.TempDir(), "vision-describe.png")
	if err := os.WriteFile(imagePath, visionDescribePNG(t), 0o600); err != nil {
		t.Fatalf("write synthetic image: %v", err)
	}
	audioOutPath := filepath.Join(t.TempDir(), "vision-reply.wav")

	out, runErr := runVisionDescribeSession(t, fixture, wavPath, imagePath, audioOutPath)
	if runErr != nil {
		t.Fatalf("vision describe session failed: %v\nstdout:\n%s", runErr, out)
	}
	if err := assertVisionDescribeGrounded(out); err != nil {
		t.Fatal(err)
	}

	wavBytes, err := os.ReadFile(audioOutPath)
	if err != nil {
		t.Fatalf("read recorded reply WAV: %v", err)
	}
	_, reply, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse recorded reply WAV: %v", err)
	}
	if len(reply) == 0 {
		t.Fatalf("--audio-out recorded zero samples; the grounded turn delivered no spoken audio\nstdout:\n%s", out)
	}
	var energy float64
	for _, sample := range reply {
		energy += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(energy / float64(len(reply)))
	if rms < 500.0 {
		t.Fatalf("recorded reply RMS = %.1f, want > 500 (non-silent spoken reply); %d samples", rms, len(reply))
	}
}

// TestSessionCommandVisionDescribeWithoutRecordingDirectoryStreamsCombinedTurn
// exercises the ordinary public image-plus-audio invocation. The replay
// transport requires every client frame in order, so a premature image-only
// response or an omitted audio commit fails this test before the grounded
// transcript can be observed.
func TestSessionCommandVisionDescribeWithoutRecordingDirectoryStreamsCombinedTurn(t *testing.T) {
	fixture := buildVisionDescribeFixture(t, nil)
	wavPath := locateCLIFixture(t, visionDescribeQuestionWAV)
	imagePath := filepath.Join(t.TempDir(), "vision-describe.png")
	if err := os.WriteFile(imagePath, visionDescribePNG(t), 0o600); err != nil {
		t.Fatalf("write synthetic image: %v", err)
	}

	out, runErr := runVisionDescribeSessionWithoutRecordingDirectory(t, fixture, wavPath, imagePath)
	if runErr != nil {
		t.Fatalf("ordinary image-plus-audio session failed: %v\nstdout:\n%s", runErr, out)
	}
	if err := assertVisionDescribeGrounded(out); err != nil {
		t.Fatal(err)
	}
}

// TestSessionCommandVisionDescribeWithoutImageFailsTypedReplay is the first
// negative control: without --image the image turn can never be sent, so the
// replay diverges with the typed mismatch error instead of producing a
// grounded reply. This proves the grounded outcome requires the image path.
func TestSessionCommandVisionDescribeWithoutImageFailsTypedReplay(t *testing.T) {
	fixture := buildVisionDescribeFixture(t, nil)
	wavPath := locateCLIFixture(t, visionDescribeQuestionWAV)

	stdout := &syncBuffer{}
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	cmd.SetOut(stdout)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"--replay", fixture, "--audio-in", wavPath})
	runErr := cmd.ExecuteContext(t.Context())
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stdout.String(), "[session closed:") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if runErr == nil {
		t.Fatalf("run without --image succeeded; expected the typed replay divergence\nstdout:\n%s", stdout.String())
	}
	if !errors.Is(runErr, providers.ErrReplayMismatch) {
		t.Fatalf("run without --image failed with %v, want typed replay mismatch", runErr)
	}
	for _, marker := range visionDescribeContentMarkers {
		if strings.Contains(stdout.String(), marker) {
			t.Fatalf("run without --image produced image-grounded content %q; grounding is not discriminative\nstdout:\n%s", marker, stdout.String())
		}
	}
}

// TestVisionGroundingAssertionFailsOnGenericReply is the second negative
// control: an otherwise identical run whose recorded reply is generic (it
// names no image content) must FAIL the grounding assertion, proving the
// assertion discriminates image-grounded answers from any successful reply.
func TestVisionGroundingAssertionFailsOnGenericReply(t *testing.T) {
	fixture := buildVisionDescribeFixture(t, []string{"I hear your question ", "clearly."})
	wavPath := locateCLIFixture(t, visionDescribeQuestionWAV)
	imagePath := filepath.Join(t.TempDir(), "vision-describe.png")
	if err := os.WriteFile(imagePath, visionDescribePNG(t), 0o600); err != nil {
		t.Fatalf("write synthetic image: %v", err)
	}

	out, runErr := runVisionDescribeSession(t, fixture, wavPath, imagePath, "")
	if runErr != nil {
		t.Fatalf("generic-reply control run should complete cleanly: %v\nstdout:\n%s", runErr, out)
	}
	if err := assertVisionDescribeGrounded(out); err == nil {
		t.Fatalf("grounding assertion passed on a generic reply; it does not discriminate image grounding\nstdout:\n%s", out)
	}
}
