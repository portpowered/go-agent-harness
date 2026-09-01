package services

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

var ErrSessionAudioPCM16Truncated = errors.New("session PCM16 audio has a truncated sample")

// convertSessionAudioPCM converts little-endian mono PCM16 at the provider
// boundary. A zero source rate is the explicit injected/replay contract: the
// bytes already use the resolved provider rate. The identity path validates
// the rate but returns the original bytes without copying.
func convertSessionAudioPCM(pcm []byte, sourceRate, providerRate int) ([]byte, error) {
	if providerRate == 0 {
		if sourceRate > 0 {
			providerRate = sourceRate
		} else {
			providerRate = audio.SampleRate
		}
	}
	if sourceRate == 0 {
		if _, err := wavio.Resample(nil, providerRate, providerRate); err != nil {
			return nil, fmt.Errorf("validate injected session input at provider rate %d Hz: %w", providerRate, err)
		}
		return pcm, nil
	}
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("%w: %d bytes at %d Hz", ErrSessionAudioPCM16Truncated, len(pcm), sourceRate)
	}
	if _, err := wavio.Resample(nil, sourceRate, providerRate); err != nil {
		return nil, fmt.Errorf("convert session input from %d Hz to provider rate %d Hz: %w", sourceRate, providerRate, err)
	}
	if sourceRate == providerRate {
		return pcm, nil
	}

	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	converted, err := wavio.Resample(samples, sourceRate, providerRate)
	if err != nil {
		return nil, fmt.Errorf("convert session input from %d Hz to provider rate %d Hz: %w", sourceRate, providerRate, err)
	}
	encoded := make([]byte, len(converted)*2)
	for index, sample := range converted {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded, nil
}

func convertScheduledAudioInputs(inputs []ScheduledAudioInput, providerRate int) ([]ScheduledAudioInput, error) {
	if inputs == nil {
		return nil, nil
	}
	converted := make([]ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		pcm, err := convertSessionAudioPCM(input.PCM, input.SourceSampleRate, providerRate)
		if err != nil {
			return nil, fmt.Errorf("convert scheduled audio input %d: %w", index+1, err)
		}
		converted[index] = input
		converted[index].PCM = pcm
		converted[index].SourceSampleRate = providerRate
	}
	return converted, nil
}

type sessionProviderInputFramer struct {
	sourceRate   int
	providerRate int
	frameBytes   int
	pending      []byte
}

func newSessionProviderInputFramer(sourceRate, providerRate int) *sessionProviderInputFramer {
	providerFrameSamples := audio.FrameSize * providerRate / audio.SampleRate
	return &sessionProviderInputFramer{
		sourceRate:   sourceRate,
		providerRate: providerRate,
		frameBytes:   providerFrameSamples * 2,
	}
}

func (f *sessionProviderInputFramer) push(pcm []byte) ([][]byte, error) {
	converted, err := convertSessionAudioPCM(pcm, f.sourceRate, f.providerRate)
	if err != nil {
		return nil, err
	}
	f.pending = append(f.pending, converted...)
	return f.takeCompleteFrames(), nil
}

func (f *sessionProviderInputFramer) takeCompleteFrames() [][]byte {
	var frames [][]byte
	for f.frameBytes > 0 && len(f.pending) >= f.frameBytes {
		frames = append(frames, append([]byte(nil), f.pending[:f.frameBytes]...))
		f.pending = f.pending[f.frameBytes:]
	}
	return frames
}

func (f *sessionProviderInputFramer) flush() []byte {
	if len(f.pending) == 0 {
		return nil
	}
	frame := make([]byte, f.frameBytes)
	copy(frame, f.pending)
	f.pending = nil
	return frame
}
