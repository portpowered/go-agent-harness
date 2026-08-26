//go:build linux && cgo && !nomicrophone

package audio

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gen2brain/malgo"
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
func TestLinuxHardwareAndPositiveAudio(t *testing.T) {
	assertRenderEvidence := func(frame []int16, want bool) {
		writer := &linuxOpenedDevice{direction: DirectionOutput}
		if err := writer.WriteFrame(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
		writer.onData(make([]byte, FrameSize*2), nil, FrameSize)
		if got := writer.PositiveAudioEvidence(); got != want {
			t.Fatalf("PositiveAudioEvidence()=%v for frame with non-zero=%v", got, want)
		}
	}
	assertRenderEvidence(make([]int16, FrameSize), false)
	nonZero := make([]int16, FrameSize)
	nonZero[0] = 1
	assertRenderEvidence(nonZero, true)

	records, err := enumerateLinuxDevices()
	if len(records) == 0 {
		t.Skipf("linux: no usable ALSA endpoint or reachable PulseAudio server: %v", err)
	}
	input, output := linuxRecord(records, DirectionInput), linuxRecord(records, DirectionOutput)
	if input == nil {
		t.Skip("linux: configured capture source/default is unavailable")
	}
	if output == nil {
		t.Skip("linux: configured render sink/default is unavailable")
	}
	registry := newLinuxDeviceRegistry(func() ([]linuxDeviceRecord, error) { return append([]linuxDeviceRecord(nil), records...), nil })
	opened, err := registry.Open(input.ID)
	if err != nil {
		t.Skipf("linux: capture source cannot be opened: %v", err)
	}
	source, ok := opened.(AudioSource)
	if !ok {
		t.Fatal("Linux capture handle does not implement AudioSource")
	}
	positive, readErr := readPositiveCapture(source)
	closeErr := opened.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("capture read=%v close=%v", readErr, closeErr)
	}
	if !positive {
		t.Skip("linux: capture source produced no positive PCM energy; runtime capture signal is unavailable")
	}
	opened, err = registry.Open(output.ID)
	if err != nil {
		t.Skipf("linux: render sink cannot be opened: %v", err)
	}
	writer := opened.(*linuxOpenedDevice)
	t.Cleanup(func() { _ = opened.Close() })
	frame := make([]int16, FrameSize)
	frame[0] = 1200
	if err := writer.WriteFrame(context.Background(), frame); err != nil {
		t.Fatalf("render WriteFrame: %v", err)
	}
	if !waitForPositive(writer) {
		t.Fatal("render sink consumed no positive PCM energy")
	}
}
func mustLinuxRecord(backend, nativeID, name string, direction Direction, defaulted bool) linuxDeviceRecord {
	device, _ := NewDevice(backend, direction.String()+":"+nativeID, name, direction)
	return linuxDeviceRecord{Device: device, defaulted: defaulted}
}
func linuxRecord(records []linuxDeviceRecord, direction Direction) *linuxDeviceRecord {
	i := slices.IndexFunc(records, func(record linuxDeviceRecord) bool { return record.Direction == direction && record.defaulted })
	if i >= 0 {
		return &records[i]
	}
	return nil
}
func readPositiveCapture(reader AudioSource) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := 0; i < 10; i++ {
		frame := make([]int16, FrameSize)
		if err := reader.ReadFrame(ctx, frame); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return false, nil
			}
			return false, err
		}
		if slices.ContainsFunc(frame, func(sample int16) bool { return sample != 0 }) {
			return true, nil
		}
	}
	return false, nil
}
func waitForPositive(writer interface{ PositiveAudioEvidence() bool }) bool {
	for i := 0; i < 100 && !writer.PositiveAudioEvidence(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	return writer.PositiveAudioEvidence()
}
