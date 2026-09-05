//go:build linux && cgo && !nomicrophone

package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gen2brain/malgo"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestLinuxBackendUnavailableClassification(t *testing.T) {
	known := []error{
		fmt.Errorf("pulse init: %w", malgo.ErrNoBackend),
		fmt.Errorf("alsa init: %w", malgo.ErrFailedToInitBackend),
	}
	if !allLinuxBackendErrorsUnavailable(known) {
		t.Fatalf("known unavailable backend errors were not classified as unavailable: %v", known)
	}
	if allLinuxBackendErrorsUnavailable([]error{errors.New("permission denied while enumerating devices")}) {
		t.Fatal("genuine enumeration error was classified as a no-device condition")
	}
}

func TestLinuxRegistryPreservesGenuineEnumerationError(t *testing.T) {
	want := errors.New("enumeration permission denied")
	registry := newLinuxDeviceRegistry(func() ([]linuxDeviceRecord, error) {
		return nil, want
	})
	devices, err := registry.List()
	if !errors.Is(err, want) {
		t.Fatalf("List error = %v, want wrapped enumeration error %v", err, want)
	}
	if devices != nil {
		t.Fatalf("List devices = %#v, want nil on genuine enumeration failure", devices)
	}
}

func TestLinuxStableDirectionalIDs(t *testing.T) {
	alsaIn := mustLinuxRecord(linuxAlsaBackend, "hw:0,0", "ALSA input", DirectionInput, true)
	alsaOut := mustLinuxRecord(linuxAlsaBackend, "hw:0,0", "ALSA output", DirectionOutput, true)
	duplicate := mustLinuxRecord(linuxAlsaBackend, "hw:0,0", "duplicate", DirectionOutput, true)
	call := 0
	registry := newLinuxDeviceRegistry(func() ([]linuxDeviceRecord, error) {
		call++
		items := []linuxDeviceRecord{alsaIn, alsaOut, duplicate}
		if call%2 == 0 {
			items = []linuxDeviceRecord{duplicate, alsaOut, alsaIn}
		}
		return items, nil
	})
	first, firstErr := registry.List()
	second, secondErr := registry.List()
	if firstErr != nil || secondErr != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("reordered snapshots first=%#v second=%#v errors=%v,%v", first, second, firstErr, secondErr)
	}
	if len(first) != 2 || first[0].ID != "alsa:input:hw:0,0" || first[1].ID != "alsa:output:hw:0,0" {
		t.Fatalf("stable directional IDs=%#v", first)
	}
}
func TestLinuxPositiveAudioEvidence(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		frame []int16
		want  bool
	}{
		{name: "silence", frame: make([]int16, audio.FrameSize)},
		{name: "signal", frame: append([]int16{1}, make([]int16, audio.FrameSize-1)...), want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			writer := &linuxOpenedDevice{direction: DirectionOutput}
			if err := writer.WriteFrame(context.Background(), testCase.frame); err != nil {
				t.Fatal(err)
			}
			writer.onData(make([]byte, audio.FrameSize*2), nil, audio.FrameSize)
			if got := writer.PositiveAudioEvidence(); got != testCase.want {
				t.Fatalf("PositiveAudioEvidence()=%v, want %v", got, testCase.want)
			}
		})
	}
}

func TestLinuxPlaybackQueueUsesResolvedRateAndCountsOverflow(t *testing.T) {
	const providerRate = 24000
	writer := &linuxOpenedDevice{direction: DirectionOutput, format: audio.PCM16DeviceFormat(providerRate)}
	for frameIndex := 0; frameIndex < 16; frameIndex++ {
		frame := make([]int16, audio.FrameSize)
		for sampleIndex := range frame {
			frame[sampleIndex] = int16(frameIndex*audio.FrameSize + sampleIndex)
		}
		if err := writer.WriteFrame(context.Background(), frame); err != nil {
			t.Fatalf("WriteFrame(%d): %v", frameIndex, err)
		}
	}

	stats := writer.PlaybackStats()
	if stats.Format != audio.PCM16DeviceFormat(providerRate) || stats.CapacitySamples != 6000 || stats.QueuedSamples != 6000 {
		t.Fatalf("Linux playback stats before callback = %+v, want 24 kHz/6000 samples", stats)
	}
	if stats.DroppedSamples != 1680 || stats.OverflowEvents != 4 {
		t.Fatalf("Linux overflow stats = %+v, want 1680 samples across 4 events", stats)
	}

	output := make([]byte, audio.FrameSize*2)
	writer.onData(output, nil, audio.FrameSize)
	decoded := make([]int16, audio.FrameSize)
	if err := codec.DecodePCM16Into(decoded, output); err != nil {
		t.Fatal(err)
	}
	if decoded[0] != 1680 || decoded[len(decoded)-1] != 2159 {
		t.Fatalf("Linux callback output starts at %d and ends at %d, want 1680..2159", decoded[0], decoded[len(decoded)-1])
	}
	if got := writer.PlaybackStats().QueuedSamples; got != 5520 {
		t.Fatalf("Linux queued samples after callback = %d, want 5520", got)
	}
}

func TestLinuxPlaybackCapacityWaitResumesAtLowWatermark(t *testing.T) {
	writer := &linuxOpenedDevice{direction: DirectionOutput, format: audio.DefaultDeviceFormat(), playbackWake: make(chan struct{})}
	low, high, err := audio.PlaybackQueueWatermarks(writer.format)
	if err != nil {
		t.Fatalf("resolve playback watermarks: %v", err)
	}
	for queued := 0; queued < high; queued += audio.FrameSize {
		if err := writer.WriteFrame(context.Background(), make([]int16, audio.FrameSize)); err != nil {
			t.Fatalf("prime playback queue: %v", err)
		}
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- writer.WaitForPlaybackCapacity(context.Background(), audio.FrameSize) }()
	select {
	case err := <-waitDone:
		t.Fatalf("capacity wait returned above low watermark: %v", err)
	default:
	}

	callback := make([]byte, audio.FrameSize*2)
	for writer.PlaybackStats().QueuedSamples > low {
		writer.onData(callback, nil, audio.FrameSize)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("capacity wait after callback drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("capacity wait did not resume at low watermark")
	}
	stats := writer.PlaybackStats()
	if stats.QueuedSamples != low || stats.DroppedSamples != 0 {
		t.Fatalf("paced Linux queue stats = %+v, want low watermark and no drops", stats)
	}
}

func TestLinuxPlaybackBurstPreservesFIFO(t *testing.T) {
	writer := &linuxOpenedDevice{direction: DirectionOutput, format: audio.PCM16DeviceFormat(24000), playbackWake: make(chan struct{})}
	testPacedPlaybackBackend(t, writer, func(raw []byte) {
		writer.onData(raw, nil, audio.FrameSize)
	})
}

func mustLinuxRecord(backend, nativeID, name string, direction Direction, defaulted bool) linuxDeviceRecord {
	device, _ := NewDevice(backend, direction.String()+":"+nativeID, name, direction)
	return linuxDeviceRecord{Device: device, defaulted: defaulted}
}
