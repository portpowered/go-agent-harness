//go:build linux && cgo && !nomicrophone

package audio

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gen2brain/malgo"
)

func TestLinuxDeviceRegistryConformance(t *testing.T) {
	records := linuxTestRecords()
	RunDeviceRegistryConformance(t, func() DeviceRegistryConformanceFixture {
		active := make(map[DeviceID]linuxDeviceRecord, len(records))
		for _, record := range records {
			active[record.device.ID] = record
		}
		var mu sync.Mutex
		var observations DeviceRegistryObservations
		registry := newLinuxDeviceRegistry(func() ([]linuxDeviceRecord, error) {
			mu.Lock()
			defer mu.Unlock()
			out := make([]linuxDeviceRecord, 0, len(active))
			for _, record := range active {
				out = append(out, record)
			}
			return out, nil
		}, func(linuxDeviceRecord) (OpenedDevice, error) {
			observations.OpenCount++
			return &linuxTestHandle{release: func() { observations.ReleaseCount++ }}, nil
		})
		remove := func(id DeviceID) {
			mu.Lock()
			defer mu.Unlock()
			delete(active, id)
		}
		return DeviceRegistryConformanceFixture{
			Registry: registry, InputDefault: records[0].device.ID,
			OutputDefault: records[1].device.ID, ExclusiveID: records[2].device.ID,
			RemoveDevice: remove, Observations: func() DeviceRegistryObservations { return observations },
		}
	})
}

func TestLinuxDeviceRegistryStableDirectionalIDs(t *testing.T) {
	pulseInput := mustLinuxTestRecord(linuxPulseBackend, "hw:0,0", "Pulse input", DirectionInput, true)
	pulseOutput := mustLinuxTestRecord(linuxPulseBackend, "hw:0,0", "Pulse output", DirectionOutput, true)
	alsaInput := mustLinuxTestRecord(linuxAlsaBackend, "hw:0,0", "ALSA input", DirectionInput, true)
	alsaOutput := mustLinuxTestRecord(linuxAlsaBackend, "hw:0,0", "ALSA output", DirectionOutput, true)
	selected := selectLinuxBackendRecords(map[string][]linuxDeviceRecord{
		linuxPulseBackend: {pulseInput, pulseOutput}, linuxAlsaBackend: {alsaInput, alsaOutput},
	})
	if len(selected) != 2 || selected[0].device.Backend != linuxPulseBackend || selected[1].device.Backend != linuxPulseBackend {
		t.Fatalf("backend merge=%#v, want two PulseAudio endpoints", selected)
	}

	duplicate := mustLinuxTestRecord(linuxAlsaBackend, "hw:0,0", "A stable name", DirectionOutput, false)
	input, output := alsaInput, alsaOutput
	call := 0
	registry := newLinuxDeviceRegistry(func() ([]linuxDeviceRecord, error) {
		call++
		records := []linuxDeviceRecord{input, output, duplicate}
		if call%2 == 0 {
			sort.Slice(records, func(i, j int) bool { return records[i].device.ID > records[j].device.ID })
		}
		return records, nil
	}, func(linuxDeviceRecord) (OpenedDevice, error) { return &linuxTestHandle{}, nil })
	first, err := registry.List()
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.List()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first) != 2 {
		t.Fatalf("stable snapshot first=%#v second=%#v", first, second)
	}
	ids := map[DeviceID]bool{}
	for _, device := range first {
		ids[device.ID] = true
		if !strings.Contains(device.NativeID, device.Direction.String()+":") {
			t.Fatalf("device %#v lacks directional native ID", device)
		}
	}
	if !ids["alsa:input:hw:0,0"] || !ids["alsa:output:hw:0,0"] {
		t.Fatalf("IDs=%v, want stable directional IDs", ids)
	}
}

func TestLinuxDeviceRegistryHardwareConformance(t *testing.T) {
	records, err := enumerateLinuxDevices()
	if err != nil && len(records) == 0 {
		t.Skipf("linux: ALSA/PulseAudio enumeration unavailable: %v", err)
	}
	input, output := linuxDefaultRecords(records)
	if input == nil {
		t.Skip("linux: configured capture source/default is unavailable")
	}
	if output == nil {
		t.Skip("linux: configured render sink/default is unavailable")
	}
	RunDeviceRegistryConformance(t, func() DeviceRegistryConformanceFixture {
		return newLinuxHardwareFixture(records)
	})
}

func TestLinuxDeviceRegistryPositiveAudioEvidence(t *testing.T) {
	records, err := enumerateLinuxDevices()
	if err != nil && len(records) == 0 {
		t.Skipf("linux: no usable ALSA endpoint or reachable PulseAudio server: %v", err)
	}
	input, output := linuxFirstRecords(records)
	if input == nil {
		t.Skip("linux: usable capture source is unavailable")
	}
	if output == nil {
		t.Skip("linux: usable render sink is unavailable")
	}
	registry := newLinuxHardwareRegistry(records)
	opened, err := registry.Open(input.device.ID)
	if err != nil {
		t.Skipf("linux: capture source cannot be opened: %v", err)
	}
	reader, ok := opened.(interface {
		ReadFrame(context.Context, []int16) error
	})
	if !ok {
		_ = opened.Close()
		t.Fatal("Linux capture handle lacks ReadFrame")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	positive := false
	for !positive {
		frame := make([]int16, FrameSize)
		err := reader.ReadFrame(ctx, frame)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
			cancel()
			_ = opened.Close()
			t.Fatalf("capture ReadFrame: %v", err)
		}
		for _, sample := range frame {
			positive = positive || sample != 0
		}
	}
	cancel()
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if !positive {
		t.Skip("linux: capture source produced no positive PCM energy; runtime capture signal is unavailable")
	}

	opened, err = registry.Open(output.device.ID)
	if err != nil {
		t.Skipf("linux: render sink cannot be opened: %v", err)
	}
	writer, ok := opened.(interface {
		WriteFrame(context.Context, []int16) error
		PositiveAudioEvidence() bool
	})
	if !ok {
		_ = opened.Close()
		t.Fatal("Linux render handle lacks WriteFrame")
	}
	frame := make([]int16, FrameSize)
	for i := range frame {
		frame[i] = 1200
		if i%2 != 0 {
			frame[i] = -1200
		}
	}
	if err := writer.WriteFrame(context.Background(), frame); err != nil {
		_ = opened.Close()
		t.Fatalf("render WriteFrame: %v", err)
	}
	deadline := time.NewTimer(2 * time.Second)
	for !writer.PositiveAudioEvidence() {
		select {
		case <-deadline.C:
			_ = opened.Close()
			t.Fatal("render sink consumed no positive PCM energy")
		case <-time.After(20 * time.Millisecond):
		}
	}
	deadline.Stop()
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
}

func linuxTestRecords() []linuxDeviceRecord {
	return []linuxDeviceRecord{
		mustLinuxTestRecord(linuxAlsaBackend, "capture", "Capture", DirectionInput, true),
		mustLinuxTestRecord(linuxAlsaBackend, "render", "Render", DirectionOutput, true),
		mustLinuxTestRecord(linuxAlsaBackend, "exclusive", "Exclusive Render", DirectionOutput, false),
	}
}

func mustLinuxTestRecord(backend, nativeID, name string, direction Direction, isDefault bool) linuxDeviceRecord {
	device, err := NewDevice(backend, direction.String()+":"+nativeID, name, direction)
	if err != nil {
		panic(err)
	}
	spec := linuxBackendSpec{name: backend, id: malgo.BackendAlsa}
	if backend == linuxPulseBackend {
		spec.id = malgo.BackendPulseaudio
	}
	var id malgo.DeviceID
	copy(id[:], nativeID)
	return linuxDeviceRecord{device: device, nativeID: id, backend: spec, isDefault: isDefault}
}

func newLinuxHardwareFixture(records []linuxDeviceRecord) DeviceRegistryConformanceFixture {
	active := make(map[DeviceID]linuxDeviceRecord, len(records))
	for _, record := range records {
		active[record.device.ID] = record
	}
	var mu sync.Mutex
	registry := newLinuxDeviceRegistry(func() ([]linuxDeviceRecord, error) {
		mu.Lock()
		defer mu.Unlock()
		out := make([]linuxDeviceRecord, 0, len(active))
		for _, record := range active {
			out = append(out, record)
		}
		return out, nil
	}, nil)
	var observations DeviceRegistryObservations
	registry.open = func(record linuxDeviceRecord) (OpenedDevice, error) {
		opened, err := registry.openNative(record)
		if err != nil {
			return nil, err
		}
		observations.OpenCount++
		return &linuxCountingHandle{inner: opened, release: func() { observations.ReleaseCount++ }}, nil
	}
	input, output := linuxDefaultRecords(records)
	remove := func(id DeviceID) { mu.Lock(); defer mu.Unlock(); delete(active, id) }
	return DeviceRegistryConformanceFixture{
		Registry: registry, InputDefault: input.device.ID, OutputDefault: output.device.ID,
		ExclusiveID: output.device.ID, RemoveDevice: remove,
		Observations: func() DeviceRegistryObservations { return observations },
	}
}

func newLinuxHardwareRegistry(records []linuxDeviceRecord) *LinuxDeviceRegistry {
	r := newLinuxDeviceRegistry(func() ([]linuxDeviceRecord, error) {
		return append([]linuxDeviceRecord(nil), records...), nil
	}, nil)
	r.open = r.openNative
	return r
}

func linuxDefaultRecords(records []linuxDeviceRecord) (*linuxDeviceRecord, *linuxDeviceRecord) {
	var input, output *linuxDeviceRecord
	for i := range records {
		if !records[i].isDefault {
			continue
		}
		if records[i].device.Direction == DirectionInput && input == nil {
			input = &records[i]
		}
		if records[i].device.Direction == DirectionOutput && output == nil {
			output = &records[i]
		}
	}
	return input, output
}

func linuxFirstRecords(records []linuxDeviceRecord) (*linuxDeviceRecord, *linuxDeviceRecord) {
	var input, output *linuxDeviceRecord
	for i := range records {
		if records[i].device.Direction == DirectionInput && input == nil {
			input = &records[i]
		}
		if records[i].device.Direction == DirectionOutput && output == nil {
			output = &records[i]
		}
	}
	return input, output
}

type linuxTestHandle struct {
	once    sync.Once
	release func()
}

type linuxCountingHandle struct {
	once    sync.Once
	inner   OpenedDevice
	release func()
	err     error
}

func (h *linuxCountingHandle) Close() error {
	h.once.Do(func() {
		h.err = h.inner.Close()
		if h.release != nil {
			h.release()
		}
	})
	return h.err
}

func (h *linuxTestHandle) Close() error {
	h.once.Do(func() {
		if h.release != nil {
			h.release()
		}
	})
	return nil
}
