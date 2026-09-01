//go:build linux && cgo && !nomicrophone

package functional

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

func TestLinuxPhysicalAudioDeviceLoop(t *testing.T) {
	if os.Getenv("AGENT_TEST_REAL_AUDIO") != "1" {
		t.Skip("set AGENT_TEST_REAL_AUDIO=1 to exercise physical ALSA/PulseAudio devices")
	}
	registry := audio.NewDeviceRegistry()
	if _, err := registry.Default(audio.DirectionInput); err != nil {
		t.Fatalf("resolve default Linux input: %v", err)
	}
	if _, err := registry.Default(audio.DirectionOutput); err != nil {
		t.Fatalf("resolve default Linux output: %v", err)
	}
	source, err := audio.NewDeviceSource(registry, "")
	if err != nil {
		t.Fatalf("open physical Linux input: %v", err)
	}
	defer source.Close()
	sink, err := audio.NewDeviceSink(registry, "")
	if err != nil {
		t.Fatalf("open physical Linux output: %v", err)
	}
	defer sink.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := source.ReadFrame(ctx, make([]int16, audio.FrameSize)); err != nil {
		t.Fatalf("read physical Linux microphone frame: %v", err)
	}
	frame := make([]int16, audio.FrameSize)
	frame[0] = 1200
	if err := sink.WriteFrame(ctx, frame); err != nil {
		t.Fatalf("write physical Linux render frame: %v", err)
	}
	if err := sink.WaitForPlayback(ctx); err != nil {
		t.Fatalf("physical Linux render callback did not consume frame: %v", err)
	}
}
