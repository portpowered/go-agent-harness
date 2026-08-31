package services

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunSessionWithAudioOutNormalizesAssistantAudioBeforeSinkAndRuntime(t *testing.T) {
	levels := []float64{220, 4200}
	outputs := make([][]byte, len(levels))
	runtimeOutputs := make([][]byte, len(levels))
	for index, level := range levels {
		input := sessionNormalizerSpeech(level, 2*audio.SampleRate/100)
		inferencer := &scriptedSessionInferencer{events: []messages.StreamMessage{
			{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
			{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(input))},
			{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		}}
		runtimeObserver := &recordingSessionRuntimeObserver{}
		var stdout bytes.Buffer
		if err := RunSessionWithAudioOut(context.Background(), &stdout, SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: inferencer,
			RuntimeObserver:   runtimeObserver,
		}, "-"); err != nil {
			t.Fatalf("level %.0f session: %v", level, err)
		}
		outputs[index] = append([]byte(nil), stdout.Bytes()...)
		for _, observation := range runtimeObserver.observations {
			if observation.Kind == SessionRuntimeObservationAudioOutput {
				runtimeOutputs[index] = append(runtimeOutputs[index], observation.Payload...)
			}
		}
		if !bytes.Equal(outputs[index], runtimeOutputs[index]) {
			t.Fatalf("level %.0f runtime audio differs from sink output", level)
		}
		assertSessionNormalizedPCM(t, outputs[index], len(input), level)
	}
	if got := sessionPCM16DBDelta(outputs[0], outputs[1]); got > 3 {
		t.Fatalf("normalized voice RMS delta = %.3f dB, want <= 3 dB", got)
	}
	if bytes.Equal(outputs[0], outputs[1]) {
		t.Fatal("different voice fixtures unexpectedly produced identical PCM")
	}
}

func TestRunSessionWithRecordingDirectoryStoresNormalizedAssistantAudio(t *testing.T) {
	input := sessionNormalizerSpeech(280, 2*audio.SampleRate/100)
	var observedMu sync.Mutex
	var observed []byte
	inferencer := &scriptedSessionInferencer{events: []messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(input))},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}}
	destination := filepath.Join(t.TempDir(), "recording")
	audioOutPath := filepath.Join(t.TempDir(), "response.raw")
	err := runSessionWithRecordingDirectory(context.Background(), &bytes.Buffer{}, SessionRunOptions{
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "test-key",
		ConfigDir:         t.TempDir(),
		ReplayPath:        "synthetic.json",
		SessionInferencer: inferencer,
		StreamObserver: func(msg messages.StreamMessage) {
			if msg.Type != messages.StreamTypeAudioDelta || !assistantAudioDelta(msg) {
				return
			}
			value, ok := msg.Value.(*messages.AudioDeltaValue)
			if !ok || value == nil {
				return
			}
			observedMu.Lock()
			observed = append(observed, value.Content...)
			observedMu.Unlock()
		},
	}, destination, audioOutPath, 0, SessionTextSeed{}, "", false, nil)
	if err != nil {
		t.Fatalf("recording session: %v", err)
	}

	var recorded []byte
	for index := 0; ; index++ {
		path := filepath.Join(destination, "audio", "out-"+threeRecordingDigits(index)+".pcm")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			break
		}
		recorded = append(recorded, data...)
	}
	if len(recorded) == 0 {
		t.Fatal("recording contains no assistant audio")
	}
	observedMu.Lock()
	observedCopy := append([]byte(nil), observed...)
	observedMu.Unlock()
	if !bytes.Equal(recorded, observedCopy) {
		t.Fatalf("recorded audio and stream-observed audio differ: recorded=%d observed=%d", len(recorded), len(observedCopy))
	}
	audioOut, err := os.ReadFile(audioOutPath)
	if err != nil {
		t.Fatalf("read standalone audio output: %v", err)
	}
	if !bytes.Equal(audioOut, recorded) {
		t.Fatalf("standalone sink and recording differ: sink=%d recording=%d", len(audioOut), len(recorded))
	}
	assertSessionNormalizedPCM(t, recorded, len(input), 280)
}

func sessionNormalizerSpeech(amplitude float64, sampleCount int) []int16 {
	samples := make([]int16, sampleCount)
	for index := range samples {
		phase := 2 * math.Pi * float64(index) / 79
		value := amplitude * (math.Sin(phase) + 0.23*math.Sin(phase*2.07))
		samples[index] = int16(math.Round(value))
	}
	return samples
}

func assertSessionNormalizedPCM(t *testing.T, pcm []byte, wantSamples int, level float64) {
	t.Helper()
	if len(pcm) != wantSamples*2 {
		t.Fatalf("level %.0f output has %d samples, want %d", level, len(pcm)/2, wantSamples)
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(uint16(pcm[index*2]) | uint16(pcm[index*2+1])<<8)
	}
	rms := sessionAudioRMS(samples)
	dbfs := 20 * math.Log10(rms/float64(1<<15))
	if dbfs < audio.PCM16NormalizerTargetRMSDBFS-1.5 || dbfs > audio.PCM16NormalizerTargetRMSDBFS+1.5 {
		t.Fatalf("level %.0f output RMS = %.3f dBFS, want %.1f..%.1f dBFS", level, dbfs, audio.PCM16NormalizerTargetRMSDBFS-1.5, audio.PCM16NormalizerTargetRMSDBFS+1.5)
	}
	var sum int64
	for index, sample := range samples {
		abs := int(sample)
		if abs < 0 {
			abs = -abs
		}
		if abs >= audio.PCM16NormalizerClipSampleThreshold {
			t.Fatalf("level %.0f output sample %d = %d reaches clipping guard", level, index, sample)
		}
		sum += int64(sample)
	}
	if mean := math.Abs(float64(sum) / float64(len(samples))); mean > 100 {
		t.Fatalf("level %.0f output DC mean = %.3f, want <= 100", level, mean)
	}
}

func sessionPCM16DBDelta(first, second []byte) float64 {
	decode := func(pcm []byte) float64 {
		samples := make([]int16, len(pcm)/2)
		for index := range samples {
			samples[index] = int16(uint16(pcm[index*2]) | uint16(pcm[index*2+1])<<8)
		}
		return 20 * math.Log10(sessionAudioRMS(samples))
	}
	return math.Abs(decode(first) - decode(second))
}

func normalizedSessionPCM16(t *testing.T, pcm []byte) []byte {
	t.Helper()
	if len(pcm)%2 != 0 {
		t.Fatalf("normalized test PCM has odd byte length %d", len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(uint16(pcm[index*2]) | uint16(pcm[index*2+1])<<8)
	}
	return pcm16Bytes(normalizedSessionSamples(t, samples))
}
