package localai

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// TestLiveRealtimeAudio is optional: it skips quickly when the LocalAI
// endpoint is not running, but proves decoded non-silent audio when available.
func TestLiveRealtimeAudio(t *testing.T) {
	provider := New()
	endpoint, err := provider.endpoint()
	if err != nil {
		t.Fatalf("resolve LocalAI endpoint: %v", err)
	}

	connectCtx, cancel := context.WithTimeout(context.Background(), defaultHandshakeTimeout+250*time.Millisecond)
	session, err := provider.ConnectSession(connectCtx, models.SessionConfig{
		Model:                 ModelID,
		Modalities:            []models.SessionModality{models.SessionModalityAudio},
		Instructions:          "Reply with one short spoken sentence.",
		InputAudioFormat:      models.AudioFormatPCM16,
		InputAudioSampleRate:  models.SampleRate16000,
		OutputAudioFormat:     models.AudioFormatPCM16,
		OutputAudioSampleRate: models.SampleRate24000,
	})
	cancel()
	if err != nil {
		var connectionErr *ConnectionError
		if errors.As(err, &connectionErr) {
			t.Skipf("endpoint-unreachable: %s: %v", endpoint, err)
		}
		t.Fatalf("connect to reachable LocalAI endpoint %s: %v", endpoint, err)
	}
	defer func() { _ = session.Close() }()

	audio := livePCM16Tone()
	sendCtx, sendCancel := context.WithTimeout(context.Background(), time.Second)
	defer sendCancel()
	if outcome := messages.SendSessionWithOutcome(sendCtx, session, messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(audio),
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
	var decoded []byte
	for {
		msg, ok := session.Receive().ReadBlockingContext(readCtx)
		if !ok {
			t.Fatal("timed out waiting for LocalAI audio response")
		}
		switch msg.Type {
		case messages.StreamTypeAudioDelta:
			if value, ok := msg.Value.(*messages.AudioDeltaValue); ok {
				decoded = append(decoded, value.Content...)
			}
		case messages.StreamTypeError:
			t.Fatalf("LocalAI returned session error: %v", msg.Value)
		case messages.StreamTypeMessageEnd:
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
	const (
		sampleRate = 16000
		samples    = sampleRate / 2
		frequency  = 440.0
	)
	audio := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		value := int16(math.Sin(2*math.Pi*frequency*float64(i)/sampleRate) * 0.25 * math.MaxInt16)
		binary.LittleEndian.PutUint16(audio[i*2:], uint16(value))
	}
	return audio
}

func pcm16RMS(audio []byte) float64 {
	if len(audio) < 2 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(audio); i += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(audio[i:]))) / math.MaxInt16
		sum += sample * sample
	}
	return math.Sqrt(sum / float64(len(audio)/2))
}
