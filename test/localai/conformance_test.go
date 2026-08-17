package localai

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type behaviorObservation struct {
	latency  time.Duration
	evidence string
}

type behaviorCase struct {
	name string
	run  func(*testing.T, context.Context, endpointConfig) (behaviorObservation, error)
}

var behaviorCases = []behaviorCase{
	{name: "audio-round-trip", run: runAudioRoundTrip},
	{name: "three-turn-context", run: runThreeTurnContext},
	{name: "vad-barge-in", run: runVADBargeIn},
	{name: "model-chosen-function-call", run: runFunctionCall},
	{name: "image-input", run: runImageInput},
}

func TestLiveRealtimeTierConformance(t *testing.T) {
	endpoints := configuredEndpoints(t)
	for _, behavior := range behaviorCases {
		behavior := behavior
		t.Run(behavior.name, func(t *testing.T) {
			for _, endpoint := range endpoints {
				endpoint := endpoint
				t.Run(endpoint.name, func(t *testing.T) {
					if !endpoint.available {
						t.Skip(endpoint.skipReason)
					}
					ctx, cancel := context.WithTimeout(context.Background(), behaviorTimeout)
					defer cancel()
					behaviorStarted := time.Now()
					observation, err := behavior.run(t, ctx, endpoint)
					if observation.latency == 0 {
						observation.latency = time.Since(behaviorStarted)
					}
					if err != nil {
						t.Fatalf("%s endpoint=%s latency=%s evidence=%s: %v", behavior.name, endpoint.name, observation.latency, observation.evidence, err)
					}
					t.Logf("behavior=%s provider=%s model=%s latency=%s evidence=%s", behavior.name, endpoint.name, endpoint.model, observation.latency, observation.evidence)
				})
			}
		})
	}
}

func configuredEndpoints(t *testing.T) []endpointConfig {
	t.Helper()

	localRaw := envFirst("LOCALAI_REALTIME_URL", "AGENT_MODEL__LOCALAI__BASE_URL")
	if localRaw == "" {
		localRaw = defaultLocalAIURL
	}
	localURL, localErr := endpointURL(localRaw, localAIRealtimeModel)
	local := endpointConfig{
		name:                 "localai",
		url:                  localURL,
		model:                localAIRealtimeModel,
		inputRate:            audioInputRate,
		outputRate:           localAudioOutputRate,
		manualResponseCreate: false,
	}
	if localErr != nil {
		local.skipReason = "localai-endpoint-unavailable: invalid endpoint configuration"
	} else if !probeLocalEndpoint(local) {
		local.skipReason = fmt.Sprintf("localai-endpoint-unavailable: %s (start deploy/localai with docker compose)", safeEndpoint(local.url))
	} else {
		local.available = true
	}

	openAIRaw := envFirst("AGENT_MODEL__OPENAI__BASE_URL")
	if openAIRaw == "" {
		openAIRaw = defaultOpenAIURL
	}
	openAIURL, openAIErr := endpointURL(openAIRaw, openAIRealtimeModel)
	openAI := endpointConfig{
		name:                 "openai",
		url:                  openAIURL,
		model:                openAIRealtimeModel,
		apiKey:               envFirst("AGENT_MODEL__OPENAI__API_KEY"),
		inputRate:            openAIInputRate(),
		outputRate:           openAIAudioOutputRate,
		manualResponseCreate: true,
	}
	switch {
	case openAIErr != nil:
		openAI.skipReason = "openai-endpoint-unavailable: invalid endpoint configuration"
	case openAI.apiKey == "":
		openAI.skipReason = "openai-credential-missing: set AGENT_MODEL__OPENAI__API_KEY"
	default:
		openAI.available = true
	}
	return []endpointConfig{local, openAI}
}

func openAIInputRate() int {
	// Keep the behavior body shared while allowing endpoint-specific audio
	// encoding details required by the two realtime services.
	return 24000
}

func runAudioRoundTrip(t *testing.T, ctx context.Context, endpoint endpointConfig) (behaviorObservation, error) {
	started := time.Now()
	conn, err := endpoint.connect(ctx, sessionSettings{
		modalities:   []string{"audio"},
		instructions: "Reply with one short spoken sentence.",
		audio:        true,
	})
	if err != nil {
		return behaviorObservation{}, err
	}
	defer func() { _ = conn.Close() }()
	audio, err := speechPCM16(endpoint.inputRate)
	if err != nil {
		return behaviorObservation{}, err
	}
	if err := appendAudio(ctx, conn, audio, endpoint.inputRate); err != nil {
		return behaviorObservation{}, fmt.Errorf("append input audio: %w", err)
	}
	if err := writeEvent(ctx, conn, map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		return behaviorObservation{}, fmt.Errorf("commit input audio: %w", err)
	}
	if endpoint.manualResponseCreate {
		if err := responseCreate(ctx, conn); err != nil {
			return behaviorObservation{}, err
		}
	}
	response, err := readResponse(ctx, conn)
	if err != nil {
		return behaviorObservation{}, fmt.Errorf("read audio response: %w", err)
	}
	if err := requireNonSilentAudio(response.audio); err != nil {
		return behaviorObservation{}, err
	}
	return behaviorObservation{
		latency:  time.Since(started),
		evidence: fmt.Sprintf("audio_deltas=%d pcm_bytes=%d rms=%.6f", countAudioDeltas(response.events), len(response.audio), mustRMS(response.audio)),
	}, nil
}

const contextFact = "cobalt-17"

func runThreeTurnContext(t *testing.T, ctx context.Context, endpoint endpointConfig) (behaviorObservation, error) {
	started := time.Now()
	instructions := "You are a strict conformance subject. If a fact is absent from this conversation, say UNKNOWN and never guess. Keep replies short."
	conn, err := endpoint.connect(ctx, sessionSettings{modalities: []string{"text"}, instructions: instructions})
	if err != nil {
		return behaviorObservation{}, err
	}
	turns := []string{
		"Remember this private fact for later: the launch code is " + contextFact + ". Do not repeat it yet; reply READY.",
		"Acknowledge the previous instruction with the single word READY.",
		"What is the exact launch code from turn one? Reply with the code.",
	}
	var final responseObservation
	for turnIndex, turn := range turns {
		final, err = sendTextTurn(ctx, conn, []map[string]any{{"type": "input_text", "text": turn}})
		if err != nil {
			_ = conn.Close()
			return behaviorObservation{}, fmt.Errorf("context turn: %w", err)
		}
		t.Logf("context turn=%d reply=%q", turnIndex+1, final.text)
	}
	_ = conn.Close()
	positiveErr := requireContextFact(final.text, contextFact)

	// The same assertion is deliberately exercised without the first two
	// conversation items. A model that guesses the answer would make the
	// positive context proof degenerate, so this control must reject it.
	controlConn, err := endpoint.connect(ctx, sessionSettings{modalities: []string{"text"}, instructions: instructions})
	if err != nil {
		return behaviorObservation{}, fmt.Errorf("withheld-history control connect: %w", err)
	}
	control, err := sendTextTurn(ctx, controlConn, []map[string]any{{"type": "input_text", "text": turns[2]}})
	_ = controlConn.Close()
	if err != nil {
		return behaviorObservation{}, fmt.Errorf("withheld-history control response: %w", err)
	}
	withheldHistoryErr := requireContextFact(control.text, contextFact)
	if withheldHistoryErr == nil {
		return behaviorObservation{}, fmt.Errorf("withheld-history negative control unexpectedly passed: reply=%q", control.text)
	} else {
		t.Logf("negative control withheld-history rejected: %v", withheldHistoryErr)
	}
	observation := behaviorObservation{
		latency:  time.Since(started),
		evidence: fmt.Sprintf("turn3=%q withheld_history=%q", final.text, control.text),
	}
	if positiveErr != nil {
		return observation, positiveErr
	}
	return observation, nil
}

func runVADBargeIn(t *testing.T, ctx context.Context, endpoint endpointConfig) (behaviorObservation, error) {
	started := time.Now()
	conn, err := endpoint.connect(ctx, sessionSettings{
		modalities:   []string{"audio"},
		instructions: "Count slowly from one to one hundred in a continuous spoken response. Say every number clearly and do not stop early, so playback remains in flight long enough for an interruption.",
		audio:        true,
		serverVAD:    true,
	})
	if err != nil {
		return behaviorObservation{}, err
	}
	defer func() { _ = conn.Close() }()
	audio, err := speechPCM16(endpoint.inputRate)
	if err != nil {
		return behaviorObservation{}, err
	}
	if err := appendAudio(ctx, conn, audio, endpoint.inputRate); err != nil {
		return behaviorObservation{}, fmt.Errorf("append initial VAD audio: %w", err)
	}
	silence, err := silencePCM16(endpoint.inputRate, 700)
	if err != nil {
		return behaviorObservation{}, err
	}
	if err := appendAudio(ctx, conn, silence, endpoint.inputRate); err != nil {
		return behaviorObservation{}, fmt.Errorf("append initial VAD silence: %w", err)
	}

	var observation responseObservation
	playback := &playbackConsumer{}
	bargeSent := false
	vadStarted := false
	cancelled := false
	flushPlayback := func() {
		if cancelled {
			return
		}
		cancelled = true
		playback.flush()
	}
	sendBargeAudio := func() error {
		if bargeSent {
			return nil
		}
		if err := appendAudio(ctx, conn, audio, endpoint.inputRate); err != nil {
			return fmt.Errorf("append barge-in audio: %w", err)
		}
		if err := appendAudio(ctx, conn, silence, endpoint.inputRate); err != nil {
			return fmt.Errorf("append barge-in silence: %w", err)
		}
		bargeSent = true
		return nil
	}
	for {
		event, err := readEvent(ctx, conn)
		if err != nil {
			return behaviorObservation{}, fmt.Errorf("read barge-in event: %w", err)
		}
		observation.events = append(observation.events, fmt.Sprintf("%s@%s", event.typeName, time.Since(started).Round(time.Millisecond)))
		switch event.typeName {
		case "error":
			return behaviorObservation{}, fmt.Errorf("server error during barge-in: %s", eventErrorMessage(event.data))
		case "input_audio_buffer.speech_started":
			if bargeSent {
				vadStarted = true
			}
		case "response.output_audio.delta", "response.audio.delta", "response.audio.output.delta":
			encoded := stringAt(event.data, "delta")
			chunk, decodeErr := decodePCMDelta(encoded)
			if decodeErr != nil {
				return behaviorObservation{}, decodeErr
			}
			if cancelled {
				observation.audioDeltasAfterCancel++
			} else {
				observation.audioDeltasBeforeCancel++
			}
			observation.audio = append(observation.audio, chunk...)
			playback.enqueue(chunk)
			if err := sendBargeAudio(); err != nil {
				return behaviorObservation{}, err
			}
		case "response.cancelled":
			flushPlayback()
		case "response.done":
			status := firstString(event.data, "response.status", "status")
			reason := firstString(event.data, "response.status_details.reason", "status_details.reason")
			if status == "cancelled" || reason == "turn_detected" || reason == "client_cancelled" {
				flushPlayback()
			}
			playbackErr := requirePlaybackFlushed(playback)
			if !bargeSent || !vadStarted || !cancelled || playbackErr != nil || observation.audioDeltasBeforeCancel == 0 || observation.audioDeltasAfterCancel != 0 {
				return behaviorObservation{}, fmt.Errorf("barge-in assertion failed: barge_audio=%t vad_started=%t cancelled=%t playback=%v audio_before_cancel=%d audio_after_cancel=%d events=%v", bargeSent, vadStarted, cancelled, playbackErr, observation.audioDeltasBeforeCancel, observation.audioDeltasAfterCancel, observation.events)
			}
			return behaviorObservation{
				latency:  time.Since(started),
				evidence: fmt.Sprintf("vad_speech_started=true cancellation=true playback_flush_bytes=%d playback_pending_bytes=%d audio_before_cancel=%d", playback.flushedBytes, playback.pendingBytes(), observation.audioDeltasBeforeCancel),
			}, nil
		}
	}
}

var lookupWeatherTool = toolDefinition{
	name:        "lookup_weather",
	description: "Look up the weather for one city.",
	parameters: map[string]toolParameter{
		"city": {typeName: "string", description: "City name."},
	},
	required: []string{"city"},
}

const functionCallPrompt = "Use the lookup_weather tool exactly once for Seattle. Do not answer in text before choosing the tool."

func runFunctionCall(t *testing.T, ctx context.Context, endpoint endpointConfig) (behaviorObservation, error) {
	started := time.Now()
	conn, err := endpoint.connect(ctx, sessionSettings{
		modalities:   []string{"text"},
		instructions: "When a user asks for weather, choose the named tool rather than inventing an answer.",
		tools:        []toolDefinition{lookupWeatherTool},
	})
	if err != nil {
		return behaviorObservation{}, err
	}
	positive, err := sendTextTurn(ctx, conn, []map[string]any{{"type": "input_text", "text": functionCallPrompt}})
	_ = conn.Close()
	if err != nil {
		return behaviorObservation{}, fmt.Errorf("tool-enabled response: %w", err)
	}
	if err := requireExactlyOneToolCall(positive.calls, lookupWeatherTool.name); err != nil {
		return behaviorObservation{}, err
	}

	controlConn, err := endpoint.connect(ctx, sessionSettings{
		modalities:   []string{"text"},
		instructions: "There are no tools available. Never claim that a tool was invoked.",
	})
	if err != nil {
		return behaviorObservation{}, fmt.Errorf("no-tools control connect: %w", err)
	}
	control, err := sendTextTurn(ctx, controlConn, []map[string]any{{"type": "input_text", "text": functionCallPrompt}})
	_ = controlConn.Close()
	if err != nil {
		return behaviorObservation{}, fmt.Errorf("no-tools control response: %w", err)
	}
	if err := requireExactlyOneToolCall(control.calls, lookupWeatherTool.name); err == nil {
		return behaviorObservation{}, fmt.Errorf("no-tools negative control unexpectedly passed: calls=%v", control.calls)
	} else {
		t.Logf("negative control no-tools rejected: %v (observed calls=%d)", err, len(control.calls))
	}
	return behaviorObservation{
		latency:  time.Since(started),
		evidence: fmt.Sprintf("tool=%s calls=%d no_tools_calls=%d", lookupWeatherTool.name, len(positive.calls), len(control.calls)),
	}, nil
}

const imageFact = "ORBIT"

func runImageInput(t *testing.T, ctx context.Context, endpoint endpointConfig) (behaviorObservation, error) {
	started := time.Now()
	imageURI, err := fixtureImageDataURI()
	if err != nil {
		return behaviorObservation{}, err
	}
	instructions := "Answer only from content supplied in the current user item. If the image is absent, reply UNKNOWN and never guess."
	question := "What exact private word is printed in the image? Reply with only the word."
	conn, err := endpoint.connect(ctx, sessionSettings{modalities: []string{"text"}, instructions: instructions})
	if err != nil {
		return behaviorObservation{}, err
	}
	positive, err := sendTextTurn(ctx, conn, []map[string]any{
		{"type": "input_text", "text": question},
		{"type": "input_image", "image_url": imageURI},
	})
	_ = conn.Close()
	positiveErr := err

	controlConn, err := endpoint.connect(ctx, sessionSettings{modalities: []string{"text"}, instructions: instructions})
	if err != nil {
		return behaviorObservation{}, fmt.Errorf("no-image control connect: %w", err)
	}
	control, err := sendTextTurn(ctx, controlConn, []map[string]any{{"type": "input_text", "text": question}})
	_ = controlConn.Close()
	if err != nil {
		return behaviorObservation{}, fmt.Errorf("no-image control response: %w", err)
	}
	noImageErr := requireImageFact(control.text, imageFact)
	if noImageErr == nil {
		return behaviorObservation{}, fmt.Errorf("no-image negative control unexpectedly passed: reply=%q", control.text)
	} else {
		t.Logf("negative control no-image rejected: %v", noImageErr)
	}
	observation := behaviorObservation{
		latency:  time.Since(started),
		evidence: fmt.Sprintf("image_fact=%q reply=%q no_image_reply=%q", imageFact, positive.text, control.text),
	}
	if positiveErr != nil {
		observation.evidence = fmt.Sprintf("positive_error=%v %s", positiveErr, observation.evidence)
		return observation, fmt.Errorf("image response: %w", positiveErr)
	}
	if err := requireImageFact(positive.text, imageFact); err != nil {
		return observation, err
	}
	return observation, nil
}

func countAudioDeltas(events []string) int {
	count := 0
	for _, event := range events {
		if strings.Contains(event, "audio.delta") {
			count++
		}
	}
	return count
}

func mustRMS(audio []byte) float64 {
	rms, _ := pcm16RMS(audio)
	return rms
}

func decodePCMDelta(encoded string) ([]byte, error) {
	chunk, err := decodeBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode audio delta: %w", err)
	}
	if len(chunk) == 0 || len(chunk)%2 != 0 {
		return nil, fmt.Errorf("audio delta has invalid PCM16 byte count %d", len(chunk))
	}
	return chunk, nil
}
