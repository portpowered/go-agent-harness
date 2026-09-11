package consumer_test

import (
	"context"
	"io"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

const simulatedOutputID = "simulated-duplex:output"

type boundaryCase struct {
	name          string
	frames        []audio.PCMFrame
	wantSamples   []int16
	wantReadCount int
}

type scriptedInbound struct {
	mu         sync.Mutex
	frames     []audio.PCMFrame
	reads      []audio.PCMFrame
	closeOnce  sync.Once
	closeCount int
}

func (s *scriptedInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	if err := ctx.Err(); err != nil {
		return audio.PCMFrame{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.frames) == 0 {
		return audio.PCMFrame{}, io.EOF
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	frame.Samples = append([]int16(nil), frame.Samples...)
	s.reads = append(s.reads, frame)
	return frame, nil
}

func (s *scriptedInbound) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closeCount++
		s.mu.Unlock()
	})
	return nil
}

func (s *scriptedInbound) snapshotReads() ([]audio.PCMFrame, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reads := append([]audio.PCMFrame(nil), s.reads...)
	return reads, s.closeCount
}

func c51Samples() []int16 {
	pattern := []int16{1200, -800, 2500, -1600, 0, 327}
	samples := make([]int16, audio.FrameSize)
	for i := range samples {
		samples[i] = pattern[i%len(pattern)]
	}
	return samples
}

func cases() []boundaryCase {
	samples := c51Samples()
	format := audio.PCM16DeviceFormat(audio.SampleRate)
	return []boundaryCase{
		{
			name: "one response frame",
			frames: []audio.PCMFrame{{
				Samples: samples, Format: format, StreamID: "c51-stream",
				Epoch: 4, Sequence: 11, StartSample: 5280, EndOfResponse: true,
			}},
			wantSamples:   append([]int16(nil), samples...),
			wantReadCount: 1,
		},
		{
			name: "explicit empty response boundary",
			frames: []audio.PCMFrame{
				{Samples: samples, Format: format, StreamID: "c51-stream", Epoch: 5, Sequence: 21, StartSample: 10080},
				{Format: format, StreamID: "c51-stream", Epoch: 5, Sequence: 22, StartSample: 10560, EndOfResponse: true},
			},
			wantSamples:   append([]int16(nil), samples...),
			wantReadCount: 2,
		},
	}
}

func newRegistry(t *testing.T) *devicegw.SimulatedDuplexRegistry {
	t.Helper()
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Seed:    51,
		Render:  devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
		PlaybackQueue: devicegw.QueueSpec{
			LatencyNanos: int64(50 * time.Millisecond),
			DropPolicy:   "drop_oldest",
		},
		CaptureQueue: devicegw.QueueSpec{
			LatencyNanos: int64(50 * time.Millisecond),
			DropPolicy:   "drop_oldest",
		},
		Acoustic: devicegw.AcousticSpec{GainQ15: 32768},
	})
	if err != nil {
		t.Fatalf("NewSimulatedDuplexRegistry: %v", err)
	}
	return registry
}

func waitForAdmission(t *testing.T, registry *devicegw.SimulatedDuplexRegistry, want int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if stats := registry.PlaybackStats(); stats.QueuedSamples == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("playback admission did not reach %d samples; stats=%+v", want, registry.PlaybackStats())
		case <-ticker.C:
		}
	}
}

func TestPublicDeviceBoundary(t *testing.T) {
	for _, testCase := range cases() {
		t.Run(testCase.name, func(t *testing.T) {
			registry := newRegistry(t)
			service := devicewire.NewService(registry)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			handle, err := service.Open(ctx, runtimeDevices.Request{
				OutputDevice:    simulatedOutputID,
				PlaybackEnabled: true,
				SampleRate:      audio.SampleRate,
				Channels:        1,
			})
			if err != nil {
				t.Fatalf("public device service Open: %v", err)
			}
			defer func() { _ = handle.Close() }()
			ports := handle.Media()
			if ports.Capture != nil {
				t.Fatal("playback-only request unexpectedly admitted capture")
			}
			if ports.Playback == nil {
				t.Fatal("playback-only request returned nil playback")
			}

			inbound := &scriptedInbound{frames: append([]audio.PCMFrame(nil), testCase.frames...)}
			defer func() { _ = inbound.Close() }()
			pumpDone := make(chan error, 1)
			go func() { pumpDone <- ports.Playback.Pump(ctx, inbound) }()

			waitForAdmission(t, registry, len(testCase.wantSamples))
			admitted := registry.PlaybackStats()
			if admitted.QueuedSamples != len(testCase.wantSamples) || admitted.RenderedSamples != 0 {
				t.Fatalf("queue admission boundary = %+v; want queued=%d rendered=0 before callback", admitted, len(testCase.wantSamples))
			}
			if got := registry.RenderedSamples(); len(got) != 0 {
				t.Fatalf("rendered samples before callback = %d, want 0", len(got))
			}
			if os.Getenv("C51_WRONG_ORACLE") == "1" {
				t.Fatalf("wrong consumption oracle: expected queue admission to equal callback consumption before Advance; admitted=%d rendered=%d", admitted.QueuedSamples, admitted.RenderedSamples)
			}

			if err := registry.Advance(1); err != nil {
				t.Fatalf("Advance(1): %v", err)
			}
			if err := <-pumpDone; err != nil {
				t.Fatalf("playback Pump: %v", err)
			}

			reads, _ := inbound.snapshotReads()
			if len(reads) != testCase.wantReadCount {
				t.Fatalf("inbound reads = %d, want %d", len(reads), testCase.wantReadCount)
			}
			for i, want := range testCase.frames {
				if !reflect.DeepEqual(reads[i], want) {
					t.Fatalf("inbound frame %d = %#v, want %#v", i, reads[i], want)
				}
			}

			if got := registry.RenderedSamples(); !reflect.DeepEqual(got, testCase.wantSamples) {
				t.Fatalf("callback-consumed PCM = %v, want exact order %v", got[:min(len(got), 8)], testCase.wantSamples[:min(len(testCase.wantSamples), 8)])
			}
			stats := registry.PlaybackStats()
			if stats.QueuedSamples != 0 || stats.RenderedSamples != uint64(len(testCase.wantSamples)) || stats.DroppedSamples != 0 || stats.OverflowEvents != 0 {
				t.Fatalf("final playback boundary = %+v; want drained, rendered=%d, no loss", stats, len(testCase.wantSamples))
			}
			trace := registry.Trace()
			if len(trace) < 2 || trace[0].Tap != "render" || trace[0].QueueBefore != len(testCase.wantSamples) || trace[0].QueueAfter != 0 || trace[0].SampleCount != audio.FrameSize || trace[1].Tap != "capture" {
				t.Fatalf("device callback trace = %#v; want render admission/consumption followed by capture", trace)
			}

			if err := handle.Close(); err != nil {
				t.Fatalf("first handle Close: %v", err)
			}
			if err := handle.Close(); err != nil {
				t.Fatalf("idempotent handle Close: %v", err)
			}
			if err := inbound.Close(); err != nil {
				t.Fatalf("inbound Close: %v", err)
			}
			_, closeCount := inbound.snapshotReads()
			if closeCount != 1 {
				t.Fatalf("inbound Close count = %d, want exactly one idempotent close", closeCount)
			}
		})
	}
}
