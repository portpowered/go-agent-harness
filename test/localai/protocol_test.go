package localai

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const (
	openAIRealtimeModel   = "gpt-realtime-2.1-mini"
	localAIRealtimeModel  = "gpt-realtime"
	defaultOpenAIURL      = "wss://api.openai.com/v1/realtime?model=" + openAIRealtimeModel
	defaultLocalAIURL     = "ws://localhost:8080/v1/realtime?model=" + localAIRealtimeModel
	probeTimeout          = 2 * time.Second
	operationTimeout      = 15 * time.Second
	behaviorTimeout       = 90 * time.Second
	audioInputRate        = 16000
	localAudioOutputRate  = 22050
	openAIAudioOutputRate = 24000
	silenceRMSThreshold   = 0.01
)

// conformanceSpeechPCM16Base64 is a checked-in mono 16 kHz speech fixture.
// It exercises transcription and server VAD without invoking a TTS or audio
// binary during the test.
//
//go:embed testdata/utterance.pcm.b64
var conformanceSpeechPCM16Base64 string

type endpointConfig struct {
	name                 string
	url                  string
	model                string
	apiKey               string
	inputRate            int
	outputRate           int
	manualResponseCreate bool
	available            bool
	skipReason           string
}

type sessionSettings struct {
	modalities   []string
	instructions string
	audio        bool
	serverVAD    bool
	tools        []toolDefinition
}

type toolDefinition struct {
	name        string
	description string
	parameters  map[string]toolParameter
	required    []string
}

type toolParameter struct {
	typeName    string
	description string
}

type realtimeEvent struct {
	typeName string
	data     map[string]any
}

type toolCallObservation struct {
	id        string
	name      string
	arguments string
}

type responseObservation struct {
	text                    string
	audio                   []byte
	calls                   []toolCallObservation
	events                  []string
	responseStatus          string
	cancellationObserved    bool
	playbackFlushObserved   bool
	audioDeltasBeforeCancel int
	audioDeltasAfterCancel  int
}

func (e endpointConfig) connect(ctx context.Context, settings sessionSettings) (*websocket.Conn, error) {
	conn, err := dialRealtime(ctx, e)
	if err != nil {
		return nil, err
	}
	if err := waitForEvent(ctx, conn, "session.created"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("wait for session.created: %w", err)
	}
	if err := writeEvent(ctx, conn, sessionUpdateEvent(e, settings)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send session.update: %w", err)
	}
	if err := waitForEvent(ctx, conn, "session.updated"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("wait for session.updated: %w", err)
	}
	return conn, nil
}

func dialRealtime(ctx context.Context, endpoint endpointConfig) (*websocket.Conn, error) {
	requestHeaders := http.Header{}
	if endpoint.apiKey != "" {
		requestHeaders.Set("Authorization", "Bearer "+endpoint.apiKey)
	}
	dialer := websocket.Dialer{HandshakeTimeout: operationTimeout}
	conn, response, err := dialer.DialContext(ctx, endpoint.url, requestHeaders)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("dial websocket %s: %w", safeEndpoint(endpoint.url), err)
	}
	if conn == nil {
		return nil, fmt.Errorf("dial websocket %s returned nil connection", safeEndpoint(endpoint.url))
	}
	return conn, nil
}

func waitForEvent(ctx context.Context, conn *websocket.Conn, want string) error {
	for {
		event, err := readEvent(ctx, conn)
		if err != nil {
			return err
		}
		if event.typeName == "error" {
			return fmt.Errorf("server error: %s", eventErrorMessage(event.data))
		}
		if event.typeName == want {
			return nil
		}
	}
}

func readEvent(ctx context.Context, conn *websocket.Conn) (realtimeEvent, error) {
	deadline := time.Now().Add(operationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return realtimeEvent{}, err
	}
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			return realtimeEvent{}, err
		}
		if messageType != websocket.TextMessage {
			continue
		}
		data := map[string]any{}
		if err := json.Unmarshal(payload, &data); err != nil {
			return realtimeEvent{}, fmt.Errorf("decode realtime event: %w", err)
		}
		typeName, ok := data["type"].(string)
		if !ok || typeName == "" {
			return realtimeEvent{}, fmt.Errorf("realtime event has no type")
		}
		return realtimeEvent{typeName: typeName, data: data}, nil
	}
}

func writeEvent(ctx context.Context, conn *websocket.Conn, event map[string]any) error {
	deadline := time.Now().Add(operationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return conn.WriteJSON(event)
}

func sessionUpdateEvent(endpoint endpointConfig, settings sessionSettings) map[string]any {
	update := map[string]any{
		"type":              "realtime",
		"model":             endpoint.model,
		"output_modalities": settings.modalities,
		"instructions":      settings.instructions,
	}
	if settings.audio {
		input := map[string]any{
			"format": map[string]any{"type": "audio/pcm", "rate": endpoint.inputRate},
		}
		if settings.serverVAD {
			input["turn_detection"] = map[string]any{
				"type":                "server_vad",
				"prefix_padding_ms":   200,
				"silence_duration_ms": 500,
				// VAD owns both the initial response and the interruption for
				// the second speech segment.
				"create_response":    true,
				"interrupt_response": true,
			}
		} else {
			input["turn_detection"] = nil
		}
		update["audio"] = map[string]any{
			"input": input,
			"output": map[string]any{
				"format": map[string]any{"type": "audio/pcm", "rate": endpoint.outputRate},
			},
		}
	}
	if len(settings.tools) > 0 {
		update["tools"] = realtimeTools(settings.tools)
	}
	return map[string]any{"type": "session.update", "session": update}
}

func realtimeTools(tools []toolDefinition) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		properties := map[string]any{}
		for name, parameter := range tool.parameters {
			properties[name] = map[string]any{
				"type":        parameter.typeName,
				"description": parameter.description,
			}
		}
		result = append(result, map[string]any{
			"type":        "function",
			"name":        tool.name,
			"description": tool.description,
			"parameters": map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   tool.required,
			},
		})
	}
	return result
}

func sendTextTurn(ctx context.Context, conn *websocket.Conn, content []map[string]any) (responseObservation, error) {
	if err := writeEvent(ctx, conn, map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type":    "message",
			"role":    "user",
			"content": content,
		},
	}); err != nil {
		return responseObservation{}, fmt.Errorf("send conversation item: %w", err)
	}
	if err := writeEvent(ctx, conn, map[string]any{"type": "response.create"}); err != nil {
		return responseObservation{}, fmt.Errorf("send response.create: %w", err)
	}
	return readResponse(ctx, conn)
}

func readResponse(ctx context.Context, conn *websocket.Conn) (responseObservation, error) {
	var observation responseObservation
	calls := map[string]int{}
	cancelled := false
	for {
		event, err := readEvent(ctx, conn)
		if err != nil {
			return observation, err
		}
		observation.events = append(observation.events, event.typeName)
		switch event.typeName {
		case "error":
			return observation, fmt.Errorf("server error: %s", eventErrorMessage(event.data))
		case "response.output_text.delta", "response.text.delta", "response.output_audio_transcript.delta", "response.audio_transcript.delta":
			observation.text += stringAt(event.data, "delta")
		case "response.output_audio.delta", "response.audio.delta", "response.audio.output.delta":
			encoded := stringAt(event.data, "delta")
			chunk, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return observation, fmt.Errorf("decode audio delta: %w", err)
			}
			if len(chunk) == 0 || len(chunk)%2 != 0 {
				return observation, fmt.Errorf("audio delta has invalid PCM16 byte count %d", len(chunk))
			}
			if cancelled {
				observation.audioDeltasAfterCancel++
			} else {
				observation.audioDeltasBeforeCancel++
			}
			observation.audio = append(observation.audio, chunk...)
		case "response.output_audio.done", "response.audio.done":
			if cancelled {
				observation.playbackFlushObserved = true
			}
		case "response.output_item.added":
			item := mapAt(event.data, "item")
			if stringAt(item, "type") == "function_call" {
				upsertToolCall(&observation, calls, toolCallObservation{
					id:   firstString(item, "call_id", "id"),
					name: stringAt(item, "name"),
				})
			}
		case "response.function_call_arguments.done":
			upsertToolCall(&observation, calls, toolCallObservation{
				id:        firstString(event.data, "call_id", "item.call_id", "item_id"),
				name:      firstString(event.data, "name", "item.name"),
				arguments: stringAt(event.data, "arguments"),
			})
		case "response.cancelled":
			cancelled = true
			observation.cancellationObserved = true
		case "response.done":
			observation.responseStatus = firstString(event.data, "response.status", "status")
			statusReason := firstString(event.data, "response.status_details.reason", "status_details.reason")
			if observation.responseStatus == "cancelled" || statusReason == "turn_detected" || statusReason == "client_cancelled" {
				cancelled = true
				observation.cancellationObserved = true
			}
			if cancelled && !observation.playbackFlushObserved {
				// Some compatible servers use response.done as the only
				// post-cancellation playback boundary.
				if observation.audioDeltasBeforeCancel > 0 && observation.audioDeltasAfterCancel == 0 {
					observation.playbackFlushObserved = true
				}
			}
			return observation, nil
		}
	}
}

func upsertToolCall(observation *responseObservation, indexes map[string]int, call toolCallObservation) {
	for index, existing := range observation.calls {
		if call.id != "" && existing.id == call.id {
			if call.name != "" {
				existing.name = call.name
			}
			if call.arguments != "" {
				existing.arguments = call.arguments
			}
			observation.calls[index] = existing
			return
		}
		if call.id == "" && existing.id == "" && call.name != "" && existing.name == call.name {
			if call.arguments != "" {
				existing.arguments = call.arguments
			}
			observation.calls[index] = existing
			return
		}
	}
	key := call.id
	if key == "" {
		key = fmt.Sprintf("anonymous-%d", len(observation.calls))
	}
	if _, exists := indexes[key]; exists {
		return
	}
	indexes[key] = len(observation.calls)
	observation.calls = append(observation.calls, call)
}

func appendAudio(ctx context.Context, conn *websocket.Conn, audio []byte, sampleRate int) error {
	chunkBytes, err := audioChunkBytes(sampleRate)
	if err != nil {
		return err
	}
	for offset := 0; offset < len(audio); offset += chunkBytes {
		end := offset + chunkBytes
		if end > len(audio) {
			end = len(audio)
		}
		if err := writeEvent(ctx, conn, map[string]any{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(audio[offset:end]),
		}); err != nil {
			return err
		}
	}
	return nil
}

func audioChunkBytes(sampleRate int) (int, error) {
	if sampleRate <= 0 {
		return 0, fmt.Errorf("audio input rate must be positive, got %d", sampleRate)
	}
	chunkBytes := sampleRate / 10 * 2
	if chunkBytes == 0 {
		return 0, fmt.Errorf("audio input rate %d produces no chunk bytes", sampleRate)
	}
	return chunkBytes, nil
}

func TestAudioChunkBytesUsesProviderRate(t *testing.T) {
	tests := []struct {
		name       string
		sampleRate int
		wantBytes  int
	}{
		{name: "localai", sampleRate: audioInputRate, wantBytes: 3200},
		{name: "openai", sampleRate: openAIInputRate(), wantBytes: 4800},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := audioChunkBytes(test.sampleRate)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.wantBytes {
				t.Fatalf("audio chunk bytes for %d Hz = %d, want %d", test.sampleRate, got, test.wantBytes)
			}
		})
	}
}

func speechPCM16(sampleRate int) ([]byte, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("speech fixture sample rate must be positive, got %d", sampleRate)
	}
	encoded := strings.Join(strings.Fields(conformanceSpeechPCM16Base64), "")
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode speech fixture: %w", err)
	}
	if len(audio) == 0 || len(audio)%2 != 0 {
		return nil, fmt.Errorf("speech fixture has invalid PCM16 byte count %d", len(audio))
	}
	if sampleRate == audioInputRate {
		return audio, nil
	}
	return resamplePCM16(audio, audioInputRate, sampleRate)
}

func silencePCM16(sampleRate, durationMs int) ([]byte, error) {
	if sampleRate <= 0 || durationMs <= 0 {
		return nil, fmt.Errorf("silence fixture dimensions must be positive, got %d Hz for %d ms", sampleRate, durationMs)
	}
	samples := sampleRate * durationMs / 1000
	if samples == 0 {
		return nil, fmt.Errorf("silence fixture duration is too short for %d Hz", sampleRate)
	}
	return make([]byte, samples*2), nil
}

func resamplePCM16(audio []byte, inputRate, outputRate int) ([]byte, error) {
	if inputRate <= 0 || outputRate <= 0 {
		return nil, fmt.Errorf("PCM16 resample rates must be positive, got %d to %d", inputRate, outputRate)
	}
	if len(audio) == 0 || len(audio)%2 != 0 {
		return nil, fmt.Errorf("PCM16 resample input has invalid byte count %d", len(audio))
	}
	if inputRate == outputRate {
		return audio, nil
	}
	inputSamples := len(audio) / 2
	outputSamples := int(math.Round(float64(inputSamples) * float64(outputRate) / float64(inputRate)))
	if outputSamples == 0 {
		return nil, fmt.Errorf("PCM16 resample output is empty for %d input samples", inputSamples)
	}
	resampled := make([]byte, outputSamples*2)
	for outputIndex := 0; outputIndex < outputSamples; outputIndex++ {
		sourcePosition := float64(outputIndex) * float64(inputRate) / float64(outputRate)
		leftIndex := int(sourcePosition)
		if leftIndex >= inputSamples-1 {
			leftIndex = inputSamples - 1
		}
		rightIndex := leftIndex
		fraction := 0.0
		if leftIndex < inputSamples-1 {
			rightIndex++
			fraction = sourcePosition - float64(leftIndex)
		}
		left := float64(int16(binary.LittleEndian.Uint16(audio[leftIndex*2:])))
		right := float64(int16(binary.LittleEndian.Uint16(audio[rightIndex*2:])))
		value := int(math.Round(left + (right-left)*fraction))
		if value > math.MaxInt16 {
			value = math.MaxInt16
		} else if value < math.MinInt16 {
			value = math.MinInt16
		}
		binary.LittleEndian.PutUint16(resampled[outputIndex*2:], uint16(int16(value)))
	}
	return resampled, nil
}

func TestSpeechPCM16FixtureSupportsProviderRates(t *testing.T) {
	for _, sampleRate := range []int{audioInputRate, openAIInputRate()} {
		audio, err := speechPCM16(sampleRate)
		if err != nil {
			t.Fatalf("speech fixture at %d Hz: %v", sampleRate, err)
		}
		if len(audio) == 0 || len(audio)%2 != 0 {
			t.Fatalf("speech fixture at %d Hz has invalid PCM16 byte count %d", sampleRate, len(audio))
		}
		rms, err := pcm16RMS(audio)
		if err != nil {
			t.Fatalf("speech fixture RMS at %d Hz: %v", sampleRate, err)
		}
		if rms <= silenceRMSThreshold {
			t.Fatalf("speech fixture RMS at %d Hz = %.6f, want above %.6f", sampleRate, rms, silenceRMSThreshold)
		}
	}
}

func responseCreate(ctx context.Context, conn *websocket.Conn) error {
	return writeEvent(ctx, conn, map[string]any{"type": "response.create"})
}

func pcm16RMS(audio []byte) (float64, error) {
	if len(audio) == 0 || len(audio)%2 != 0 {
		return 0, fmt.Errorf("PCM16 audio has invalid byte count %d", len(audio))
	}
	var sumSquares float64
	for offset := 0; offset < len(audio); offset += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(audio[offset:]))) / math.MaxInt16
		sumSquares += sample * sample
	}
	return math.Sqrt(sumSquares / float64(len(audio)/2)), nil
}

func probeLocalEndpoint(endpoint endpointConfig) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	conn, err := dialRealtime(ctx, endpoint)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	return waitForEvent(ctx, conn, "session.created") == nil
}

func endpointURL(raw, model string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", fmt.Errorf("unsupported websocket scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("websocket endpoint has no host")
	}
	query := parsed.Query()
	if query.Get("model") == "" {
		query.Set("model", model)
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

func safeEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid-endpoint>"
	}
	parsed.User = nil
	query := parsed.Query()
	for _, key := range []string{"key", "api_key", "access_token", "token"} {
		query.Del(key)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func eventErrorMessage(data map[string]any) string {
	message := firstString(data, "error.message", "message")
	kind := firstString(data, "error.type")
	code := firstString(data, "error.code")
	param := firstString(data, "error.param")
	if message == "" {
		message = firstString(data, "error.type", "error.code")
	}
	if message == "" {
		return "unknown realtime error"
	}
	details := make([]string, 0, 3)
	if kind != "" && kind != message {
		details = append(details, kind)
	}
	if code != "" && code != message && code != kind {
		details = append(details, "code="+code)
	}
	if param != "" {
		details = append(details, "param="+param)
	}
	if len(details) > 0 {
		message += " (" + strings.Join(details, ", ") + ")"
	}
	return message
}

func TestEventErrorMessageIncludesProviderDetails(t *testing.T) {
	got := eventErrorMessage(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"code":    "audio_format_not_supported",
			"param":   "session.audio.input.format",
			"message": "input audio format is not supported",
		},
	})
	want := "input audio format is not supported (invalid_request_error, code=audio_format_not_supported, param=session.audio.input.format)"
	if got != want {
		t.Fatalf("event error = %q, want %q", got, want)
	}
}

func mapAt(data map[string]any, path ...string) map[string]any {
	current := data
	for _, part := range path {
		next, ok := current[part].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func stringAt(data map[string]any, path ...string) string {
	if len(path) == 0 {
		return ""
	}
	current := data
	for index, part := range path {
		value, ok := current[part]
		if !ok {
			return ""
		}
		if index == len(path)-1 {
			text, _ := value.(string)
			return text
		}
		next, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

func firstString(data map[string]any, paths ...string) string {
	for _, path := range paths {
		if value := stringAt(data, strings.Split(path, ".")...); value != "" {
			return value
		}
	}
	return ""
}

func envFirst(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func decodeBase64(encoded string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(encoded)
}
