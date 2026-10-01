//go:build live

package localai

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// TestLiveRealtimeAudio runs only with the live build tag and proves decoded
// non-silent audio from a running LocalAI fixture.
func TestLiveRealtimeAudio(t *testing.T) {
	provider := New()
	endpoint, err := provider.endpoint()
	if err != nil {
		t.Fatalf("resolve LocalAI endpoint: %v", err)
	}
	connectCtx, cancel := context.WithTimeout(context.Background(), 1750*time.Millisecond)
	session, err := provider.ConnectSession(connectCtx, models.SessionConfig{
		Model: ModelID, Modalities: []models.SessionModality{models.SessionModalityAudio},
		Instructions: "Reply with one short spoken sentence.", InputAudioFormat: models.AudioFormatPCM16,
		InputAudioSampleRate: models.SampleRate16000, OutputAudioFormat: models.AudioFormatPCM16,
		OutputAudioSampleRate: models.SampleRate24000,
	})
	cancel()
	if err != nil {
		var connectionErr *ConnectionError
		if errors.As(err, &connectionErr) {
			t.Fatalf("endpoint-unreachable: %s: %v", endpoint, err)
		}
		t.Fatalf("connect to reachable LocalAI endpoint %s: %v", endpoint, err)
	}
	defer closeForTest(t, session)

	sendCtx, sendCancel := context.WithTimeout(context.Background(), time.Second)
	defer sendCancel()
	if outcome := messages.SendSessionWithOutcome(sendCtx, session, messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(livePCM16Tone()),
	}); !outcome.OK() {
		t.Fatalf("send audio = %+v", outcome)
	}
	if outcome := messages.SendSessionWithOutcome(sendCtx, session, messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}); !outcome.OK() {
		t.Fatalf("commit audio = %+v", outcome)
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer readCancel()
	assertLiveAudioResponse(t, readCtx, session, endpoint)
}

// assertLiveAudioResponse reads the session until the response ends and
// requires decoded, non-silent audio.
func assertLiveAudioResponse(t *testing.T, readCtx context.Context, session messages.Session, endpoint string) {
	t.Helper()
	var decoded []byte
	for {
		msg, ok := session.Receive().ReadBlockingContext(readCtx)
		if !ok {
			t.Fatal("timed out waiting for LocalAI audio response")
		}
		if msg.Type == messages.StreamTypeAudioDelta {
			if value, ok := msg.Value.(*messages.AudioDeltaValue); ok {
				decoded = append(decoded, value.Content...)
			}
		}
		if msg.Type == messages.StreamTypeError {
			t.Fatalf("LocalAI returned session error: %v", msg.Value)
		}
		if msg.Type == messages.StreamTypeMessageEnd {
			if len(decoded) == 0 {
				t.Fatal("LocalAI completed without decoded audio")
			}
			rms := pcm16RMS(decoded)
			if rms <= 0.01 {
				t.Fatalf("decoded LocalAI audio RMS = %.6f, want above silence", rms)
			}
			t.Logf("localai model=%s endpoint=%s audio_bytes=%d rms=%.6f", ModelID, endpoint, len(decoded), rms)
			return
		}
	}
}

func livePCM16Tone() []byte {
	const sampleRate, sampleCount, frequency = 16000, 16000 / 2, 440.0
	samples := make([]int16, sampleCount)
	for i := range samples {
		samples[i] = int16(math.Sin(2*math.Pi*frequency*float64(i)/sampleRate) * 0.25 * math.MaxInt16)
	}
	return codec.EncodePCM16(samples)
}

func pcm16RMS(audio []byte) float64 {
	return codec.PCM16RMS(audio) / codec.PCM16FullScale
}
