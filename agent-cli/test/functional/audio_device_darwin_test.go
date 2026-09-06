//go:build darwin && cgo && !nomicrophone

package functional

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// TestCoreAudioVoiceProcessingDeviceLoop exercises the physical CoreAudio
// callback graph. It deliberately uses device endpoints, never file input,
// so sample rate, callback sizing, render-reference timing, and AUVoiceIO EAC
// are all part of the assertion boundary.
func TestCoreAudioVoiceProcessingDeviceLoop(t *testing.T) {
	if os.Getenv("AGENT_TEST_REAL_AUDIO") != "1" {
		t.Skip("set AGENT_TEST_REAL_AUDIO=1 to exercise physical AUVoiceIO devices")
	}
	registry := devicegw.NewCoreAudioDeviceRegistry()
	input, err := registry.Default(devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("resolve default CoreAudio input: %v", err)
	}
	output, err := registry.Default(devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("resolve default CoreAudio output: %v", err)
	}
	format := audio.PCM16DeviceFormat(24000)
	inputHandle, outputHandle, err := registry.OpenDuplexWithFormat(input.ID, format, output.ID, format)
	if err != nil {
		t.Fatalf("open AUVoiceIO duplex graph: %v", err)
	}
	t.Cleanup(func() {
		_ = inputHandle.Close()
		_ = outputHandle.Close()
	})
	for name, handle := range map[string]devicegw.OpenedDevice{"input": inputHandle, "output": outputHandle} {
		provider, ok := handle.(devicegw.VoiceProcessingProvider)
		if !ok || !provider.VoiceProcessingActive() {
			t.Fatalf("%s endpoint %T is not backed by native voice processing", name, handle)
		}
	}

	sink, ok := outputHandle.(audio.AudioSink)
	if !ok {
		t.Fatalf("AUVoiceIO output %T has no typed PCM sink", outputHandle)
	}
	source, ok := inputHandle.(audio.AudioSource)
	if !ok {
		t.Fatalf("AUVoiceIO input %T has no typed PCM source", inputHandle)
	}
	frame := make([]int16, audio.FrameSize)
	for index := range frame {
		frame[index] = int16((index%32 - 16) * 20)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sink.WriteFrame(ctx, frame); err != nil {
		t.Fatalf("write physical render frame: %v", err)
	}
	if waiter, ok := outputHandle.(interface{ WaitForPlayback(context.Context) error }); ok {
		if err := waiter.WaitForPlayback(ctx); err != nil {
			t.Fatalf("physical render callback did not consume frame: %v", err)
		}
	}
	if err := source.ReadFrame(ctx, make([]int16, audio.FrameSize)); err != nil {
		t.Fatalf("read voice-processed physical microphone frame: %v", err)
	}

	if err := inputHandle.Close(); err != nil {
		t.Fatalf("close input endpoint: %v", err)
	}
	if err := source.ReadFrame(context.Background(), make([]int16, audio.FrameSize)); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("read after endpoint close = %v, want closed", err)
	}
	if err := outputHandle.Close(); err != nil {
		t.Fatalf("close output endpoint: %v", err)
	}
	if err := outputHandle.Close(); err != nil {
		t.Fatalf("idempotent output close: %v", err)
	}
}
