package embedding_test

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type embeddedFileSink struct {
	closes  int
	samples []int16
}

func (*embeddedFileSink) WriteFrame(context.Context, []int16) error { return nil }
func (sink *embeddedFileSink) WriteSamples(_ context.Context, samples []int16) error {
	sink.samples = append(sink.samples, samples...)
	return nil
}
func (sink *embeddedFileSink) Close() error {
	sink.closes++
	return nil
}

type embeddedFrameInput struct{ frames []audio.PCMFrame }

func (*embeddedFrameInput) Close() error { return nil }
func (input *embeddedFrameInput) ReadFrame(context.Context) (audio.PCMFrame, error) {
	if len(input.frames) == 0 {
		return audio.PCMFrame{}, io.EOF
	}
	frame := input.frames[0]
	input.frames = input.frames[1:]
	return frame, nil
}

func TestExternalFilePlaybackPreservesMultipleResponseTails(t *testing.T) {
	sink := &embeddedFileSink{}
	handle, err := devicewire.NewFileService(wire.NewService()).Open(context.Background(), devices.Request{
		PlaybackEnabled: true, SampleRate: 24000,
		FileOutput: &devices.FileOutput{Sink: sink, SampleRate: 24000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	input := &embeddedFrameInput{frames: []audio.PCMFrame{
		{Samples: []int16{1, -2, 3}, Format: audio.PCM16DeviceFormat(24000), StreamID: "first", EndOfResponse: true},
		{Samples: []int16{4, -5}, Format: audio.PCM16DeviceFormat(24000), StreamID: "second", EndOfResponse: true},
	}}
	if err := handle.Media().Playback.Pump(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if want := []int16{1, -2, 3, 4, -5}; !reflect.DeepEqual(sink.samples, want) {
		t.Fatalf("file samples=%v, want exact response tails %v", sink.samples, want)
	}
}

func TestExternalFilePlaybackDoesNotAdmitCapture(t *testing.T) {
	sink := &embeddedFileSink{}
	host := devicewire.NewFileService(wire.NewService())
	handle, err := host.Open(context.Background(), devices.Request{
		PlaybackEnabled: true,
		FileOutput:      &devices.FileOutput{Sink: sink, SampleRate: 24000},
		SampleRate:      24000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	ports := handle.Media()
	if ports.Capture != nil {
		t.Fatal("playback-only admission returned a capture interface")
	}
	if ports.Playback == nil {
		t.Fatal("playback-only admission omitted playback")
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.closes != 1 {
		t.Fatalf("sink close count=%d, want 1", sink.closes)
	}
}

// TestExternalHostPacesFileInputOnItsScheduler drives the public finite
// media and file device services end to end on a virtual clock, selecting the
// policy from its option spelling as a host would. Real time must take the
// clip's encoded duration, a speed multiplier must shrink it by that factor,
// and unpaced delivery must not wait at all. Every mode delivers the clip.
func TestExternalHostPacesFileInputOnItsScheduler(t *testing.T) {
	path := writeEmbeddedPacingClip(t, audio.SampleRate)
	for _, test := range []struct {
		option  string
		elapsed time.Duration
	}{{option: "realtime", elapsed: time.Second}, {option: "10x", elapsed: 100 * time.Millisecond}, {option: "unpaced"}} {
		t.Run(test.option, func(t *testing.T) {
			var pacing devices.FilePacing
			if err := pacing.UnmarshalText([]byte(test.option)); err != nil {
				t.Fatalf("UnmarshalText(%q): %v", test.option, err)
			}
			scheduler := clock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
			capture := openEmbeddedPacedCapture(t, path, scheduler, pacing)
			outbound := &embeddedCountingOutbound{}
			if elapsed := pumpEmbeddedOnVirtualClock(t, scheduler, capture, outbound); elapsed != test.elapsed {
				t.Fatalf("virtual pacing time = %v, want %v", elapsed, test.elapsed)
			}
			if got := outbound.samples(); got < audio.SampleRate {
				t.Fatalf("delivered %d samples, want the whole %d-sample clip", got, audio.SampleRate)
			}
		})
	}
}

// TestExternalHostFileInputDefaultsToWallClockRealTime is the representative
// real-clock check: with the production scheduler and the zero pacing value a
// finite clip is never delivered faster than its encoded duration.
func TestExternalHostFileInputDefaultsToWallClockRealTime(t *testing.T) {
	const clip = 200 * time.Millisecond
	path := writeEmbeddedPacingClip(t, int(clip/time.Millisecond)*audio.SampleRate/1000)
	capture := openEmbeddedPacedCapture(t, path, clock.Real{}, devices.FilePacing{})
	started := time.Now()
	if err := capture.Pump(context.Background(), &embeddedCountingOutbound{}); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if elapsed := time.Since(started); elapsed < clip-10*time.Millisecond {
		t.Fatalf("real-time file input finished in %v, want at least its %v duration", elapsed, clip)
	}
}

func writeEmbeddedPacingClip(t *testing.T, samples int) string {
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

func openEmbeddedPacedCapture(t *testing.T, path string, scheduler clock.Scheduler, pacing devices.FilePacing) devices.Capture {
	t.Helper()
	media, err := devicewire.NewFileMediaService().OpenFileMedia(devices.FileMediaRequest{
		Input: &devices.FileMediaSource{Path: path, SampleRate: audio.SampleRate}, Scheduler: scheduler, Pacing: pacing,
	})
	if err != nil {
		t.Fatalf("OpenFileMedia: %v", err)
	}
	t.Cleanup(func() { closeForTest(t, media) })
	handle, err := devicewire.NewFileService(wire.NewService()).Open(context.Background(), devices.Request{
		CaptureEnabled: true, SampleRate: audio.SampleRate, FileInput: media.Media().Input,
	})
	if err != nil {
		t.Fatalf("Open file capture: %v", err)
	}
	t.Cleanup(func() { closeForTest(t, handle) })
	return handle.Media().Capture
}

// pumpEmbeddedOnVirtualClock advances the scheduler one millisecond at a time
// only while the capture has a pacing timer armed, and returns the virtual
// time the clip needed.
func pumpEmbeddedOnVirtualClock(t *testing.T, scheduler *clock.Deterministic, capture devices.Capture, outbound audio.OutboundMedia) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pumping, pumpDone := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- capture.Pump(ctx, outbound)
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

type embeddedCountingOutbound struct {
	mu    sync.Mutex
	count int
}

func (o *embeddedCountingOutbound) WriteFrame(_ context.Context, frame audio.PCMFrame) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.count += len(frame.Samples)
	return nil
}

func (*embeddedCountingOutbound) Close() error { return nil }

func (o *embeddedCountingOutbound) samples() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.count
}
