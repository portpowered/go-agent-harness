package localai

import (
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
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

type toolCallObservation struct {
	name string
}

// playbackConsumer models the client-side audio queue that is observable by
// the user. Realtime output events only deliver chunks; response.done and
// response.output_audio.done do not prove that a client has stopped playing
// already-delivered chunks. A cancellation must explicitly flush this queue.
type playbackConsumer struct {
	pending      [][]byte
	flushedBytes int
	flushCount   int
}

func (p *playbackConsumer) enqueue(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	p.pending = append(p.pending, append([]byte(nil), chunk...))
}

func (p *playbackConsumer) pendingBytes() int {
	var total int
	for _, chunk := range p.pending {
		total += len(chunk)
	}
	return total
}

func (p *playbackConsumer) flush() int {
	flushed := p.pendingBytes()
	p.pending = nil
	p.flushedBytes += flushed
	p.flushCount++
	return flushed
}

func requirePlaybackFlushed(playback *playbackConsumer) error {
	if playback.flushCount != 1 {
		return fmt.Errorf("playback flush count=%d, want exactly one", playback.flushCount)
	}
	if playback.flushedBytes == 0 {
		return fmt.Errorf("playback flush discarded zero bytes, want pending audio")
	}
	if pending := playback.pendingBytes(); pending != 0 {
		return fmt.Errorf("playback queue retained %d bytes after cancellation", pending)
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
	for outputIndex := range outputSamples {
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
		"type": serverEventError,
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

func stringAt(data map[string]any, path ...string) string {
	current := data
	for index, part := range path {
		value, ok := current[part]
		if !ok {
			return ""
		}
		if index == len(path)-1 {
			if text, isText := value.(string); isText {
				return text
			}
			return ""
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
