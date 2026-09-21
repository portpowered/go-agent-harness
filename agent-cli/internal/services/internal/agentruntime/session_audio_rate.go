package agentruntime

import (
	"errors"
	"fmt"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
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

	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, fmt.Errorf("decode session input PCM16: %w", err)
	}
	converted, err := wavio.Resample(samples, sourceRate, providerRate)
	if err != nil {
		return nil, fmt.Errorf("convert session input from %d Hz to provider rate %d Hz: %w", sourceRate, providerRate, err)
	}
	return codec.EncodePCM16(converted), nil
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
