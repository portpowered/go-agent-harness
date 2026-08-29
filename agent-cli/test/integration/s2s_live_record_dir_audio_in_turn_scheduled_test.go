package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newCLIScheduledBoundaryAgent(t *testing.T, server transport.Dialer) *cli.AgentCLI {
	t.Helper()
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI scheduled session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	return agentCLI
}

func newCLIGroundedScheduledBoundaryAgent(t *testing.T, server transport.Dialer) *cli.AgentCLI {
	t.Helper()
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("create grounded hermetic OpenAI scheduled session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortSessionInferencer, sessionInferencer),
	)
	if err != nil {
		t.Fatalf("initialize grounded production CLI: %v", err)
	}
	return agentCLI
}

func newCLIServerVADBoundaryAgent(t *testing.T, server transport.Dialer) *cli.AgentCLI {
	t.Helper()
	createResponse := false
	provider := oaiprovider.New(
		oaiprovider.WithAPIKey("test-key"),
		oaiprovider.WithModel("gpt-realtime"),
		oaiprovider.WithRealtimeBaseURL("wss://hermetic.openai.test/v1/realtime"),
		oaiprovider.WithWebSocketDialer(server),
	)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(provider))
	if err != nil {
		t.Fatalf("create hermetic OpenAI server-VAD session gateway: %v", err)
	}
	sessionInferencer := inference.NewSessionGatewayInferencer(
		sessionGateway,
		inference.WithSessionRequest(inference.SessionRequest{Config: models.SessionConfig{
			Model: "gpt-realtime",
			TurnDetection: &models.TurnDetectionConfig{
				Type:           "server_vad",
				CreateResponse: &createResponse,
			},
		}}),
	)
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize server-VAD CLI: %v", err)
	}
	return agentCLI
}

func scheduledBoundaryArgs(configDir, recordDir string, audioPaths ...string) []string {
	args := []string{
		"--config-dir", configDir,
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--max-duration", "10s",
	}
	for _, path := range audioPaths {
		args = append(args, "--audio-in-turn", path)
	}
	return args
}

// TestSessionCommand_LiveScheduledAudioWithoutPromptSendsGroundingAndTools
// exercises the exact no-prompt command form that previously skipped the
// instruction composition boundary. The production CLI graph owns the default
// registry; only the OpenAI session transport is replaced by the hermetic
// provider-shaped connection.
func TestSessionCommand_LiveScheduledAudioWithoutPromptSendsGroundingAndTools(t *testing.T) {
	server := newCLILiveScheduledBoundaryServer(true)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIGroundedScheduledBoundaryAgent(t, server)
	configDir := t.TempDir()
	recordDir := filepath.Join(t.TempDir(), "grounded-recording")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs(scheduledBoundaryArgs(
		configDir,
		recordDir,
		locateCLIFixture(t, "multiturn_turn1.wav"),
	))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- rootCmd.ExecuteContext(ctx) }()

	select {
	case <-server.sessionCreated:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the no-prompt live SESSION.OPEN fixture")
	}

	timeline, _, _, _, _ := server.snapshots()
	for _, event := range []string{"out:input_audio_buffer.append", "out:input_audio_buffer.commit", "out:response.create"} {
		if containsTimeline(timeline, event) {
			t.Fatalf("no-prompt scheduled audio crossed provider before configuration: %v", timeline)
		}
	}

	groundedReady := server.waitForSessionUpdates(2*time.Second, func(updates []json.RawMessage) bool {
		for _, raw := range updates {
			var update struct {
				Instructions string `json:"instructions"`
				Tools        []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			if json.Unmarshal(raw, &update) == nil && strings.Contains(update.Instructions, "Tool-grounding requirements:") && len(update.Tools) > 0 {
				return true
			}
		}
		return false
	})
	if !groundedReady {
		timeline, _, _, _, _ := server.snapshots()
		updates := server.sessionUpdatesSnapshot()
		t.Fatalf("no-prompt scheduled route did not send a combined grounding/tool update; timeline=%v updates=%v", timeline, updates)
	}

	timeline, _, _, _, _ = server.snapshots()
	updates := server.sessionUpdatesSnapshot()
	groundedUpdateIndex := -1
	for index, raw := range updates {
		var update struct {
			Instructions string `json:"instructions"`
			Tools        []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(raw, &update); err != nil {
			t.Fatalf("decode no-prompt scheduled session.update %d: %v", index, err)
		}
		if !strings.Contains(update.Instructions, "Tool-grounding requirements:") {
			continue
		}
		if groundedUpdateIndex >= 0 {
			t.Fatalf("no-prompt scheduled route sent grounding more than once: updates=%v", updates)
		}
		groundedUpdateIndex = index
		if strings.Count(update.Instructions, "Tool-grounding requirements:") != 1 {
			t.Fatalf("no-prompt grounding policy count = %d, want 1; instructions=%q", strings.Count(update.Instructions, "Tool-grounding requirements:"), update.Instructions)
		}
		if strings.Contains(update.Instructions, "No tools are currently registered") {
			t.Fatalf("no-prompt grounding instructions contradict advertised tools: %q", update.Instructions)
		}
		advertised := make(map[string]bool, len(update.Tools))
		for _, tool := range update.Tools {
			advertised[tool.Name] = true
		}
		for _, name := range []string{"read_file", "exec"} {
			if !advertised[name] {
				t.Fatalf("no-prompt scheduled session.update omitted %q: %#v", name, update.Tools)
			}
		}
	}
	if groundedUpdateIndex < 0 {
		t.Fatalf("no-prompt scheduled route sent no instruction-bearing tool update: %v", updates)
	}
	groundedWireIndex := indexOfTimeline(timeline, "out:session.update", groundedUpdateIndex)
	if groundedWireIndex < 0 {
		t.Fatalf("no-prompt grounding update is absent from outbound timeline: %v", timeline)
	}
	for _, event := range []string{"out:input_audio_buffer.append", "out:input_audio_buffer.commit", "out:response.create"} {
		if eventIndex := indexOfTimeline(timeline, event, 0); eventIndex >= 0 && groundedWireIndex >= eventIndex {
			t.Fatalf("grounding/tool session.update index=%d does not precede %s index=%d: %v", groundedWireIndex, event, eventIndex, timeline)
		}
	}

	server.releaseSessionUpdated()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("no-prompt production CLI session error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no-prompt production CLI session did not complete")
	}

	timeline, _, providerErrors, _, serverVADEnabled := server.snapshots()
	if len(providerErrors) != 0 {
		t.Fatalf("no-prompt provider errors = %v; timeline=%v", providerErrors, timeline)
	}
	if serverVADEnabled {
		t.Fatalf("no-prompt scheduled session left server VAD enabled: %v", timeline)
	}
	firstAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 0)
	firstCommit := indexOfTimeline(timeline, "out:input_audio_buffer.commit", 0)
	firstResponse := indexOfTimeline(timeline, "out:response.create", 0)
	if firstAppend < 0 || firstCommit < 0 || firstResponse < 0 || !(groundedWireIndex < firstAppend && groundedWireIndex < firstCommit && groundedWireIndex < firstResponse) {
		t.Fatalf("no-prompt configuration did not precede the first spoken turn: update=%d append=%d commit=%d response=%d timeline=%v", groundedWireIndex, firstAppend, firstCommit, firstResponse, timeline)
	}
}

// TestSessionCommand_LiveScheduledImageAudioAttachesImagesToFirstTurn proves
// the shipped CLI composition for --image plus repeated --audio-in-turn. The
// image item must be queued before the first audio append without its own
// response request, and the second scheduled turn must not receive another
// image item or overtake the first terminal response.
func TestSessionCommand_LiveScheduledImageAudioAttachesImagesToFirstTurn(t *testing.T) {
	server := newCLILiveScheduledBoundaryServer(false)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIScheduledBoundaryAgent(t, server)
	imagePath := readImageFixturePath(t)
	secondImagePath := filepath.Join(filepath.Dir(imagePath), "fixture.jpeg")
	if _, err := os.Stat(secondImagePath); err != nil {
		t.Fatalf("second image fixture: %v", err)
	}
	recordDir := filepath.Join(t.TempDir(), "image-scheduled-recording")
	firstAudio := locateCLIFixture(t, "multiturn_turn1.wav")
	secondAudio := locateCLIFixture(t, "multiturn_turn2.wav")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--max-duration", "10s",
		"--image", imagePath,
		"--image", secondImagePath,
		"--audio-in-turn", firstAudio,
		"--audio-in-turn", secondAudio,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		timeline, outbound, providerErrors, _, _ := server.snapshots()
		t.Fatalf("execute image-plus-scheduled command: %v; provider errors=%v; timeline=%v; outbound=%v", err, providerErrors, timeline, audioLengthsFromOutbound(outbound))
	}

	timeline, outbound, providerErrors, dialCount, serverVADEnabled := server.snapshots()
	if dialCount != 1 || serverVADEnabled || len(providerErrors) != 0 {
		t.Fatalf("image scheduled provider state = dials:%d server_vad:%t errors:%v timeline=%v; want one clean client-owned session", dialCount, serverVADEnabled, providerErrors, timeline)
	}
	if countTimeline(timeline, "out:conversation.item.create") != 1 {
		t.Fatalf("image item count = %d, want exactly one first-turn item: %v", countTimeline(timeline, "out:conversation.item.create"), timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 2 ||
		countTimeline(timeline, "out:input_audio_buffer.commit") != 2 ||
		countTimeline(timeline, "out:response.create") != 2 ||
		countTimeline(timeline, "in:response.done") != 2 ||
		countTimeline(timeline, "in:response.output_audio.delta") != 2 {
		t.Fatalf("image scheduled lifecycle = %v, want two complete audio/output turns", timeline)
	}
	imageIndex := indexOfTimeline(timeline, "out:conversation.item.create", 0)
	firstAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 0)
	firstResponse := indexOfTimeline(timeline, "out:response.create", 0)
	firstDone := indexOfTimeline(timeline, "in:response.done", 0)
	secondAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 1)
	if imageIndex < 0 || firstAppend < 0 || firstResponse < 0 || firstDone < 0 || secondAppend < 0 ||
		!(imageIndex < firstAppend && firstAppend < firstResponse && firstResponse < firstDone && firstDone < secondAppend) {
		t.Fatalf("image scheduled wire order = %v, want image < first append < response.create < response.done < second append", timeline)
	}

	var imageItems []struct {
		Item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				ImageURL string `json:"image_url"`
				Text     string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	for _, event := range outbound {
		if event.typeName != "conversation.item.create" {
			continue
		}
		var payload struct {
			Item struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type     string `json:"type"`
					ImageURL string `json:"image_url"`
					Text     string `json:"text"`
				} `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal(event.payload, &payload); err != nil {
			t.Fatalf("decode first-turn image item: %v", err)
		}
		imageItems = append(imageItems, payload)
	}
	if len(imageItems) != 1 {
		t.Fatalf("decoded image items = %d, want one", len(imageItems))
	}
	item := imageItems[0].Item
	if item.Type != "message" || item.Role != "user" {
		t.Fatalf("first-turn image item = %#v, want one user message", item)
	}
	imageParts := make([]struct {
		Type     string `json:"type"`
		ImageURL string `json:"image_url"`
		Text     string `json:"text"`
	}, 0, len(item.Content))
	instructionCount := 0
	for _, part := range item.Content {
		if part.Type == "input_text" {
			if strings.TrimSpace(part.Text) != "" {
				instructionCount++
			}
			continue
		}
		imageParts = append(imageParts, part)
	}
	if instructionCount != 1 || len(imageParts) != 2 {
		t.Fatalf("first-turn image content = %#v, want one context instruction and two image parts", item.Content)
	}
	for index, wantMIME := range []string{"data:image/png;", "data:image/jpeg;"} {
		part := imageParts[index]
		if part.Type != "input_image" || !strings.HasPrefix(part.ImageURL, wantMIME) {
			t.Fatalf("first-turn image part %d = %#v, want input_image with %q URL", index, part, wantMIME)
		}
	}
	assertCLILiveRecordingBundle(t, recordDir, 2)
}

func assertScheduledBoundaryOrder(t *testing.T, timeline []string, turns int) {
	t.Helper()
	for turn := 0; turn < turns; turn++ {
		appendIndex := indexOfTimeline(timeline, "out:input_audio_buffer.append", turn)
		commitIndex := indexOfTimeline(timeline, "out:input_audio_buffer.commit", turn)
		responseIndex := indexOfTimeline(timeline, "out:response.create", turn)
		doneIndex := indexOfTimeline(timeline, "in:response.done", turn)
		if appendIndex < 0 || commitIndex < 0 || responseIndex < 0 || doneIndex < 0 {
			t.Fatalf("scheduled turn %d is missing its boundary from %v", turn+1, timeline)
		}
		if !(appendIndex < commitIndex && commitIndex < responseIndex && responseIndex < doneIndex) {
			t.Fatalf("scheduled turn %d lifecycle order = %v, want append < commit < response.create < response.done", turn+1, timeline)
		}
	}
}

func equalDuration24kSilenceFixture(t *testing.T, speechPath string) string {
	t.Helper()
	wavBytes, err := os.ReadFile(speechPath)
	if err != nil {
		t.Fatalf("read 24 kHz speech fixture: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("decode 24 kHz speech fixture: %v", err)
	}
	if rate != wavio.Rate24kHz || len(samples) == 0 {
		t.Fatalf("speech fixture format = rate %d, samples %d; want non-empty 24 kHz PCM16 mono", rate, len(samples))
	}
	for index := range samples {
		samples[index] = 0
	}

	var silence bytes.Buffer
	if err := wavio.Write(&silence, wavio.Rate24kHz, samples); err != nil {
		t.Fatalf("encode equal-duration 24 kHz silence fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "exact-silence-24k.wav")
	if err := os.WriteFile(path, silence.Bytes(), 0o600); err != nil {
		t.Fatalf("write equal-duration 24 kHz silence fixture: %v", err)
	}
	return path
}

// TestSessionCommand_LiveScheduledAudioDoesNotCrossDelayedSessionUpdated
// drives the shipped command surface against a transport that withholds the
// initial configuration acknowledgement. The provider boundary must remain
// free of scheduled input until the observable session.updated event arrives.
func TestSessionCommand_LiveScheduledAudioDoesNotCrossDelayedSessionUpdated(t *testing.T) {
	server := newCLILiveScheduledBoundaryServer(true)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIScheduledBoundaryAgent(t, server)
	recordDir := filepath.Join(t.TempDir(), "delayed-ack-recording")
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs(scheduledBoundaryArgs(
		t.TempDir(),
		recordDir,
		locateCLIFixture(t, "multiturn_turn1.wav"),
	))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- rootCmd.ExecuteContext(ctx) }()

	select {
	case <-server.sessionCreated:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the live SESSION.OPEN fixture")
	}
	timeline, _, _, _, _ := server.snapshots()
	for _, event := range []string{"out:input_audio_buffer.append", "out:input_audio_buffer.commit", "out:response.create"} {
		if containsTimeline(timeline, event) {
			t.Fatalf("scheduled boundary crossed provider before session.updated: %v", timeline)
		}
	}

	server.releaseSessionUpdated()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("delayed-ack production CLI session error: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delayed-ack production CLI session did not complete")
	}

	timeline, outbound, providerErrors, dialCount, serverVADEnabled := server.snapshots()
	if dialCount != 1 {
		t.Fatalf("delayed-ack provider dial count = %d, want 1; timeline=%v", dialCount, timeline)
	}
	if serverVADEnabled {
		t.Fatalf("scheduled session update left server VAD enabled: %v", timeline)
	}
	if len(providerErrors) != 0 {
		t.Fatalf("delayed-ack provider errors = %v; timeline=%v", providerErrors, timeline)
	}
	updatedIndex := indexOfTimeline(timeline, "in:session.updated", 0)
	appendIndex := indexOfTimeline(timeline, "out:input_audio_buffer.append", 0)
	if updatedIndex < 0 || appendIndex < 0 || updatedIndex >= appendIndex {
		t.Fatalf("delayed-ack session.updated did not precede the first append: %v", timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 1 || countTimeline(timeline, "out:input_audio_buffer.commit") != 1 || countTimeline(timeline, "out:response.create") != 1 {
		t.Fatalf("delayed-ack scheduled boundary duplicated or missing: %v", timeline)
	}
	assertScheduledBoundaryOrder(t, timeline, 1)
	appendAudio := audioPayloadsFromOutbound(outbound)
	if len(appendAudio) != 1 || len(appendAudio[0]) == 0 || !hasNonZeroPCM(appendAudio[0]) {
		t.Fatalf("delayed-ack first scheduled append = %v; want one non-empty PCM payload", audioLengths(appendAudio))
	}
	for _, output := range []string{stdout.String(), stderr.String()} {
		if strings.Contains(output, "input_audio_buffer_commit_empty") || strings.Contains(output, "conversation_already_has_active_response") {
			t.Fatalf("delayed-ack CLI output contains a provider collision: %q", output)
		}
	}
	assertCLILiveRecordingBundle(t, recordDir, 1)
}

// TestSessionCommand_LiveScheduledAudioSpeechThenExactSilence keeps a real
// 24 kHz speech fixture and an equal-duration all-zero 24 kHz fixture in one
// persistent production-CLI session. The transport would auto-commit a speech
// stop while Server VAD is enabled, but explicit null keeps both turns under
// the client's one-commit/one-response boundary.
func TestSessionCommand_LiveScheduledAudioSpeechThenExactSilence(t *testing.T) {
	speechPath := locateCorpusWAV(t, "truncated_24k")
	silencePath := equalDuration24kSilenceFixture(t, speechPath)
	server := newCLILiveScheduledBoundaryServer(false)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIScheduledBoundaryAgent(t, server)
	recordDir := filepath.Join(t.TempDir(), "speech-silence-recording")
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs(scheduledBoundaryArgs(t.TempDir(), recordDir, speechPath, silencePath))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := rootCmd.ExecuteContext(ctx)
	if err != nil {
		timeline, outbound, providerErrors, _, _ := server.snapshots()
		t.Fatalf("speech-then-silence production CLI session error: %v\nprovider errors=%v\ntimeline=%v\nappend lengths=%v\nstdout:\n%s\nstderr:\n%s", err, providerErrors, timeline, audioLengthsFromOutbound(outbound), stdout.String(), stderr.String())
	}

	timeline, outbound, providerErrors, dialCount, serverVADEnabled := server.snapshots()
	if dialCount != 1 {
		t.Fatalf("speech-then-silence provider dial count = %d, want 1; timeline=%v", dialCount, timeline)
	}
	if serverVADEnabled {
		t.Fatalf("scheduled session update left server VAD enabled: %v", timeline)
	}
	if got := server.providerAutoCommitCount(); got != 0 {
		t.Fatalf("client-owned scheduled run unexpectedly auto-committed %d turn(s): %v", got, timeline)
	}
	if len(providerErrors) != 0 {
		t.Fatalf("speech-then-silence provider errors = %v; timeline=%v", providerErrors, timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 2 || countTimeline(timeline, "out:input_audio_buffer.commit") != 2 || countTimeline(timeline, "out:response.create") != 2 || countTimeline(timeline, "in:response.done") != 2 {
		t.Fatalf("speech-then-silence scheduled lifecycle = %v, want two complete client-owned turns", timeline)
	}
	assertScheduledBoundaryOrder(t, timeline, 2)
	firstResponseDone := indexOfTimeline(timeline, "in:response.done", 0)
	secondAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 1)
	if firstResponseDone < 0 || secondAppend <= firstResponseDone {
		t.Fatalf("silence turn crossed the active first response: %v", timeline)
	}
	if countTimeline(timeline, "in:input_audio_buffer.speech_started") != 0 || countTimeline(timeline, "in:input_audio_buffer.speech_stopped") != 0 {
		t.Fatalf("client-owned scheduled run unexpectedly emitted VAD observations = %v", timeline)
	}

	appendAudio := audioPayloadsFromOutbound(outbound)
	if len(appendAudio) != 2 || len(appendAudio[0]) == 0 || len(appendAudio[1]) == 0 {
		t.Fatalf("speech-then-silence append payloads = %v, want two non-empty scheduled payloads", audioLengths(appendAudio))
	}
	if len(appendAudio[0]) != len(appendAudio[1]) {
		t.Fatalf("speech and exact-silence payload lengths = %v, want equal duration after normalization", audioLengths(appendAudio))
	}
	if !hasNonZeroPCM(appendAudio[0]) || hasNonZeroPCM(appendAudio[1]) {
		t.Fatalf("scheduled payloads do not preserve speech then exact silence: non-zero=%t,%t", hasNonZeroPCM(appendAudio[0]), hasNonZeroPCM(appendAudio[1]))
	}
	for _, output := range []string{strings.Join(timeline, "\n"), strings.Join(providerErrors, "\n"), stdout.String(), stderr.String()} {
		if strings.Contains(output, "input_audio_buffer_commit_empty") || strings.Contains(output, "conversation_already_has_active_response") {
			t.Fatalf("speech-then-silence run contains a provider collision: %q", output)
		}
	}
	assertCLILiveRecordingBundle(t, recordDir, 2)
}

// TestSessionCommand_LiveScheduledAudioServerVADCreateResponseFalseNegativeControl
// drives the old server_vad plus create_response:false wire configuration
// through the same production CLI boundary. The provider double auto-commits
// and clears a speech buffer at speech stop, so the later client commit must
// be rejected as input_audio_buffer_commit_empty.
func TestSessionCommand_LiveScheduledAudioServerVADCreateResponseFalseNegativeControl(t *testing.T) {
	speechPath := locateCorpusWAV(t, "truncated_24k")
	silencePath := equalDuration24kSilenceFixture(t, speechPath)
	server := newCLILiveScheduledBoundaryServer(false)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIServerVADBoundaryAgent(t, server)
	recordDir := filepath.Join(t.TempDir(), "server-vad-recording")
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs(scheduledBoundaryArgs(t.TempDir(), recordDir, speechPath, silencePath))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := rootCmd.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("former server-VAD scheduled configuration unexpectedly completed")
	}

	timeline, _, providerErrors, _, serverVADEnabled := server.snapshots()
	if !serverVADEnabled {
		t.Fatalf("negative control did not leave server VAD enabled: %v", timeline)
	}
	if server.providerAutoCommitCount() != 1 {
		t.Fatalf("provider auto-commit count = %d, want one speech-stop auto-commit; timeline=%v", server.providerAutoCommitCount(), timeline)
	}
	if len(providerErrors) != 1 || providerErrors[0] != "input_audio_buffer_commit_empty" {
		t.Fatalf("negative-control provider errors = %v, want one input_audio_buffer_commit_empty; timeline=%v", providerErrors, timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 1 || countTimeline(timeline, "out:input_audio_buffer.commit") != 1 {
		t.Fatalf("negative-control client boundary = %v, want one append and one rejected commit", timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") >= 2 || countTimeline(timeline, "in:response.done") != 0 {
		t.Fatalf("negative-control continued after the provider collision: %v", timeline)
	}
	if countTimeline(timeline, "in:input_audio_buffer.speech_started") != 1 || countTimeline(timeline, "in:input_audio_buffer.speech_stopped") != 1 {
		t.Fatalf("negative-control VAD observations = %v, want one speech start and stop: %v", timeline, timeline)
	}
	assertScheduledServerVADCreateResponseFalse(t, server.sessionUpdateSnapshot())
	if !strings.Contains(stdout.String()+stderr.String(), "input audio buffer is empty") {
		t.Fatalf("negative-control CLI output did not preserve provider error: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func assertScheduledServerVADCreateResponseFalse(t *testing.T, session json.RawMessage) {
	t.Helper()
	var update struct {
		Audio struct {
			Input struct {
				TurnDetection json.RawMessage `json:"turn_detection"`
			} `json:"input"`
		} `json:"audio"`
	}
	if err := json.Unmarshal(session, &update); err != nil {
		t.Fatalf("decode negative-control session.update: %v", err)
	}
	if len(update.Audio.Input.TurnDetection) == 0 {
		t.Fatal("negative-control session.update omitted audio.input.turn_detection")
	}
	var detection struct {
		Type           string `json:"type"`
		CreateResponse *bool  `json:"create_response"`
	}
	if err := json.Unmarshal(update.Audio.Input.TurnDetection, &detection); err != nil {
		t.Fatalf("decode negative-control turn detection: %v", err)
	}
	if detection.Type != "server_vad" || detection.CreateResponse == nil || *detection.CreateResponse {
		t.Fatalf("negative-control turn detection = %+v, want server_vad/create_response:false", detection)
	}
}

func audioPayloadsFromOutbound(outbound []cliLiveOutbound) [][]byte {
	audio := make([][]byte, 0, len(outbound))
	for _, event := range outbound {
		if event.typeName == "input_audio_buffer.append" {
			audio = append(audio, append([]byte(nil), event.audio...))
		}
	}
	return audio
}
