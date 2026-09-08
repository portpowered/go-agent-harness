package deviceprobe

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func captureLiveDeviceProbeInput(ctx context.Context, source *devicegw.DeviceSource, link *liveDeviceProbeMediaLink, runner *participants.ModelRunner) (int, float64, error) {
	pending := make([]int16, 0, audio.FrameSize*2)
	readFrame := make([]int16, audio.FrameSize)
	frameCount := 0
	var maxRMS float64
	for {
		if err := source.ReadFrame(ctx, readFrame); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == context.DeadlineExceeded {
				break
			}
			return frameCount, maxRMS, fmt.Errorf("read selected microphone: %w", err)
		}
		pending = append(pending, readFrame...)
		if rms := liveDeviceProbeRMS(readFrame); rms > maxRMS {
			maxRMS = rms
		}
		var err error
		pending, frameCount, err = forwardProbeFrames(ctx, pending, link, runner, frameCount)
		if err != nil {
			return frameCount, maxRMS, err
		}
	}
	return frameCount, maxRMS, nil
}

func forwardProbeFrames(ctx context.Context, pending []int16, link *liveDeviceProbeMediaLink, runner *participants.ModelRunner, frameCount int) ([]int16, int, error) {
	for len(pending) >= deviceProbeInputFrameSamples {
		inputFrame := append([]int16(nil), pending[:deviceProbeInputFrameSamples]...)
		pending = pending[deviceProbeInputFrameSamples:]
		trackFrame, err := link.RoundTrip(ctx, inputFrame)
		if err != nil {
			return pending, frameCount, fmt.Errorf("round-trip microphone frame over WebRTC: %w", err)
		}
		providerFrame, err := wavio.Resample(trackFrame, deviceProbeInputSampleRate, deviceProbeProviderSampleRate)
		if err != nil {
			return pending, frameCount, fmt.Errorf("resample microphone frame for session: %w", err)
		}
		if err := sendProbeAudio(ctx, runner, liveDeviceProbePCMBytes(providerFrame)); err != nil {
			return pending, frameCount, err
		}
		frameCount++
	}
	return pending, frameCount, nil
}

func sendProbeAudio(ctx context.Context, runner *participants.ModelRunner, pcm []byte) error {
	select {
	case runner.UserAudioInbox <- pcm:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func liveDeviceProbeRMS(samples []int16) float64 {
	return audio.PCM16RMSEnergy(samples)
}

func liveDeviceProbePCMBytes(samples []int16) []byte {
	return codec.EncodePCM16(samples)
}
