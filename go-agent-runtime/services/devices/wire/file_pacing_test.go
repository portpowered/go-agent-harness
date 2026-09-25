package wire

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// pacingClipSamples is a one-second clip at audio.SampleRate.
const pacingClipSamples = audio.SampleRate

// TestFilePacingDeliversClipOnSchedulerCadence drives the public finite media
// and audio services end to end on a virtual clock. Real time must take the
// clip's encoded duration, a speed multiplier must shrink it by that factor,
// and unpaced delivery must not wait at all. Every mode delivers the whole
// clip.
func TestFilePacingDeliversClipOnSchedulerCadence(t *testing.T) {
	t.Parallel()
	path := writePacingClip(t, pacingClipSamples)
	for _, test := range []struct {
		name    string
		pacing  devices.FilePacing
		elapsed time.Duration
	}{
		{name: "realtime default", elapsed: time.Second},
		{name: "ten times", pacing: devices.FilePacing{Speed: 10}, elapsed: 100 * time.Millisecond},
		{name: "unpaced", pacing: devices.FilePacing{Unpaced: true}, elapsed: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scheduler := clock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
			input := openPacedInput(t, path, scheduler, test.pacing)
			outbound := &countingOutbound{}
			elapsed := pumpOnVirtualClock(t, scheduler, input, outbound)
			if elapsed != test.elapsed {
				t.Fatalf("virtual pacing time = %v, want %v", elapsed, test.elapsed)
			}
			if got := outbound.samples(); got < pacingClipSamples {
				t.Fatalf("delivered %d samples, want at least the %d-sample clip", got, pacingClipSamples)
			}
		})
	}
}

// TestFilePacingDefaultsToWallClockRealTime is the representative real-clock
// check: with the production scheduler and the zero pacing value, a finite
// clip is never delivered faster than its encoded duration.
func TestFilePacingDefaultsToWallClockRealTime(t *testing.T) {
	t.Parallel()
	const clip = 200 * time.Millisecond
	path := writePacingClip(t, int(clip/time.Millisecond)*audio.SampleRate/1000)
	input := openPacedInput(t, path, clock.Real{}, devices.FilePacing{})
	started := time.Now()
	if err := input.Pump(context.Background(), &countingOutbound{}); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if elapsed := time.Since(started); elapsed < clip-10*time.Millisecond {
		t.Fatalf("real-time file input finished in %v, want at least its %v duration", elapsed, clip)
	}
}

func writePacingClip(t *testing.T, samples int) string {
	t.Helper()
	pcm := make([]byte, samples*2)
	for index := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(1000+index%200))
	}
	path := filepath.Join(t.TempDir(), "clip.pcm")
	if err := os.WriteFile(path, pcm, 0o600); err != nil {
		t.Fatalf("write clip: %v", err)
	}
	return path
}

func openPacedInput(t *testing.T, path string, scheduler clock.Scheduler, pacing devices.FilePacing) audioio.Input {
	t.Helper()
	handle, err := NewFileMediaService().OpenFileMedia(devices.FileMediaRequest{
		Input: &devices.FileMediaSource{Path: path, SampleRate: audio.SampleRate}, Scheduler: scheduler, Pacing: pacing,
	})
	if err != nil {
		t.Fatalf("OpenFileMedia: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("close media: %v", err)
		}
	})
	file := handle.Media().Input
	input, err := audioiowire.NewService().OpenInput(context.Background(), audioio.InputRequest{
		Source: file.Source, SourceRate: file.SampleRate, Pace: file.Pace, Scheduler: file.Scheduler,
	})
	if err != nil {
		t.Fatalf("OpenInput: %v", err)
	}
	return input
}

// pumpOnVirtualClock advances the scheduler one millisecond at a time, only
// while the pump has a pacing timer armed, and returns the virtual time the
// clip needed.
func pumpOnVirtualClock(t *testing.T, scheduler *clock.Deterministic, input audioio.Input, outbound audio.OutboundMedia) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pumping, pumpDone := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- input.Pump(ctx, outbound)
		pumpDone()
	}()
	for scheduler.WaitForTimers(pumping, 1) == nil {
		scheduler.AdvanceBy(time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatalf("Pump: %v", err)
	}
	return scheduler.Elapsed()
}

type countingOutbound struct {
	mu    sync.Mutex
	count int
}

func (o *countingOutbound) WriteFrame(_ context.Context, frame audio.PCMFrame) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.count += len(frame.Samples)
	return nil
}

func (o *countingOutbound) Close() error { return nil }

func (o *countingOutbound) samples() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.count
}
