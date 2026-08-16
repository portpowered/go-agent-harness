//go:build linux && cgo && !nomicrophone

package audio

/*
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"errors"
	"fmt"
	"github.com/gen2brain/malgo"
	"sort"
	"strings"
	"sync"
)

const (
	linuxPulseBackend = "pulse"
	linuxAlsaBackend  = "alsa"
)

var linuxBackends = [...]malgo.Backend{malgo.BackendPulseaudio, malgo.BackendAlsa}
var linuxBackendNames = [...]string{linuxPulseBackend, linuxAlsaBackend}

type linuxDeviceRecord struct {
	Device
	nativeID  malgo.DeviceID
	backend   malgo.Backend
	defaulted bool
}

// LinuxDeviceRegistry adapts ALSA and PulseAudio to DeviceRegistry.
type LinuxDeviceRegistry struct {
	mu        sync.Mutex
	enumerate func() ([]linuxDeviceRecord, error)
	inUse     map[DeviceID]struct{}
}

// NewDeviceRegistry creates a lazy Linux ALSA/PulseAudio registry.
func NewDeviceRegistry() *LinuxDeviceRegistry {
	return newLinuxDeviceRegistry(enumerateLinuxDevices)
}
func newLinuxDeviceRegistry(enumerate func() ([]linuxDeviceRecord, error)) *LinuxDeviceRegistry {
	return &LinuxDeviceRegistry{enumerate: enumerate, inUse: make(map[DeviceID]struct{})}
}

func (r *LinuxDeviceRegistry) List() ([]Device, error) {
	records, err := r.snapshot()
	devices := make([]Device, len(records))
	for i := range records {
		devices[i] = records[i].Device
	}
	return devices, err
}
func (r *LinuxDeviceRegistry) Default(direction Direction) (Device, error) {
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	records, err := r.snapshot()
	if err != nil {
		return Device{}, err
	}
	for _, record := range records {
		if record.Direction == direction && record.defaulted {
			return record.Device, nil
		}
	}
	return Device{}, NewNoDefaultDeviceError(direction)
}
func (r *LinuxDeviceRegistry) Open(id DeviceID) (OpenedDevice, error) {
	if _, _, err := ParseDeviceID(id); err != nil {
		return nil, err
	}
	records, err := r.snapshot()
	if err != nil {
		return nil, NewDeviceNotFoundError(id)
	}
	var record *linuxDeviceRecord
	for i := range records {
		if records[i].ID == id {
			record = &records[i]
			break
		}
	}
	if record == nil {
		return nil, NewDeviceNotFoundError(id)
	}
	r.mu.Lock()
	if _, ok := r.inUse[id]; ok {
		r.mu.Unlock()
		return nil, NewDeviceInUseError(id)
	}
	r.inUse[id] = struct{}{}
	r.mu.Unlock()
	opened, err := r.openNative(*record)
	if err != nil || opened == nil {
		r.release(id)
		if err == nil {
			err = errors.New("native opener returned a nil handle")
		}
		return nil, mapLinuxOpenError(id, err)
	}
	if render, ok := opened.(*linuxOpenedDevice); ok {
		render.release = func() { r.release(id) }
		return render, nil
	}
	return &linuxRegistryHandle{inner: opened, registry: r, id: id}, nil
}
func (r *LinuxDeviceRegistry) snapshot() ([]linuxDeviceRecord, error) {
	records, err := r.enumerate()
	return canonicalLinuxRecords(records), err
}
func (r *LinuxDeviceRegistry) release(id DeviceID) { r.mu.Lock(); delete(r.inUse, id); r.mu.Unlock() }
func canonicalLinuxRecords(records []linuxDeviceRecord) []linuxDeviceRecord {
	records = append([]linuxDeviceRecord(nil), records...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].ID != records[j].ID {
			return records[i].ID < records[j].ID
		}
		return records[i].Display() < records[j].Display()
	})
	unique := records[:0]
	for _, record := range records {
		if len(unique) > 0 && unique[len(unique)-1].ID == record.ID {
			unique[len(unique)-1].defaulted = unique[len(unique)-1].defaulted || record.defaulted
			continue
		}
		unique = append(unique, record)
	}
	return unique
}
func enumerateLinuxDevices() ([]linuxDeviceRecord, error) {
	var byBackend [2][]linuxDeviceRecord
	var errs []error
	for i, backend := range linuxBackends {
		var err error
		byBackend[i], err = enumerateLinuxBackend(backend, linuxBackendNames[i])
		if err != nil {
			errs = append(errs, fmt.Errorf("linux %s: %w", linuxBackendNames[i], err))
		}
	}
	selected := append([]linuxDeviceRecord(nil), byBackend[0]...)
	for _, record := range byBackend[1] {
		fallback := true
		for _, pulse := range byBackend[0] {
			fallback = fallback && pulse.Direction != record.Direction
		}
		if fallback {
			selected = append(selected, record)
		}
	}
	if len(selected) > 0 {
		return selected, nil
	}
	return nil, errors.Join(append(errs, errors.New("no usable ALSA or PulseAudio endpoints"))...)
}
func enumerateLinuxBackend(backend malgo.Backend, name string) (records []linuxDeviceRecord, err error) {
	ctx, err := malgo.InitContext([]malgo.Backend{backend}, malgo.ContextConfig{Alsa: malgo.AlsaContextConfig{UseVerboseDeviceEnumeration: 1}}, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cleanupLinuxContext(ctx) }()
	for _, request := range []struct {
		kind      malgo.DeviceType
		direction Direction
	}{{malgo.Capture, DirectionInput}, {malgo.Playback, DirectionOutput}} {
		infos, e := ctx.Devices(request.kind)
		if e != nil {
			return records, fmt.Errorf("%s %s enumeration: %w", name, request.direction, e)
		}
		for _, info := range infos {
			record, e := newLinuxDeviceRecord(backend, name, request.direction, info)
			if e == nil {
				records = append(records, record)
			}
		}
	}
	return records, nil
}
func newLinuxDeviceRecord(backend malgo.Backend, name string, direction Direction, info malgo.DeviceInfo) (linuxDeviceRecord, error) {
	nativeID := info.ID.String()
	if nativeID == "00" {
		return linuxDeviceRecord{}, fmt.Errorf("%s %s device has an empty native ID", name, direction)
	}
	display := strings.TrimSpace(info.Name())
	if display == "" {
		display = nativeID
	}
	device, err := NewDevice(name, direction.String()+":"+nativeID, display, direction)
	if err != nil {
		return linuxDeviceRecord{}, err
	}
	return linuxDeviceRecord{Device: device, nativeID: info.ID, backend: backend, defaulted: info.IsDefault != 0}, nil
}
func (r *LinuxDeviceRegistry) openNative(record linuxDeviceRecord) (OpenedDevice, error) {
	ctx, err := malgo.InitContext([]malgo.Backend{record.backend}, malgo.ContextConfig{Alsa: malgo.AlsaContextConfig{UseVerboseDeviceEnumeration: 1}}, nil)
	if err != nil {
		return nil, err
	}
	kind, config := malgo.Playback, malgo.DefaultDeviceConfig(malgo.Playback)
	if record.Direction == DirectionInput {
		kind, config = malgo.Capture, malgo.DefaultDeviceConfig(malgo.Capture)
	}
	config.SampleRate, config.Alsa.NoMMap = uint32(SampleRate), 1
	nativeID := record.nativeID.Pointer()
	defer C.free(nativeID)
	if kind == malgo.Capture {
		config.Capture.Format, config.Capture.Channels, config.Capture.DeviceID = malgo.FormatS16, uint32(Channels), nativeID
	} else {
		config.Playback.Format, config.Playback.Channels, config.Playback.DeviceID = malgo.FormatS16, uint32(Channels), nativeID
	}
	var opened OpenedDevice
	var handle *linuxOpenedDevice
	var microphone *MicrophoneSource
	callbacks := malgo.DeviceCallbacks{}
	if kind == malgo.Capture {
		microphone = &MicrophoneSource{malgoCtx: ctx, frameCh: make(chan []int16, 64)}
		opened, callbacks.Data = microphone, func(_, input []byte, frames uint32) { microphone.onCapture(input, int(frames)) }
	} else {
		handle = &linuxOpenedDevice{id: record.ID, direction: record.Direction, context: ctx}
		opened, callbacks.Data = handle, handle.onData
	}
	device, err := malgo.InitDevice(ctx.Context, config, callbacks)
	if err != nil {
		return nil, errors.Join(err, cleanupLinuxContext(ctx))
	}
	if handle != nil {
		handle.device = device
	} else {
		microphone.device = device
	}
	if err = device.Start(); err != nil {
		device.Uninit()
		return nil, errors.Join(err, cleanupLinuxContext(ctx))
	}
	return opened, nil
}
func cleanupLinuxContext(ctx *malgo.AllocatedContext) error {
	if ctx == nil {
		return nil
	}
	err := ctx.Uninit()
	ctx.Free()
	return err
}
func mapLinuxOpenError(id DeviceID, err error) error {
	switch {
	case errors.Is(err, malgo.ErrBusy), errors.Is(err, malgo.ErrAlreadyInUse):
		return NewDeviceInUseError(id)
	case errors.Is(err, malgo.ErrNoDevice), errors.Is(err, malgo.ErrDoesNotExist), errors.Is(err, malgo.ErrUnavailable):
		return NewDeviceNotFoundError(id)
	default:
		return fmt.Errorf("open device %q: %w", id, err)
	}
}

type linuxOpenedDevice struct {
	mu        sync.Mutex
	closeOnce sync.Once
	id        DeviceID
	direction Direction
	context   *malgo.AllocatedContext
	device    *malgo.Device
	playback  []int16
	closed    bool
	positive  bool
	closeErr  error
	release   func()
}

func (d *linuxOpenedDevice) onData(output, _ []byte, _ uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	clear(output)
	n := min(len(output)/2, len(d.playback))
	encodePCM16(output[:n*2], d.playback[:n])
	d.positive, d.playback = d.positive || n > 0, d.playback[n:]
}
func (d *linuxOpenedDevice) WriteFrame(ctx context.Context, frame []int16) error {
	if d.direction != DirectionOutput {
		return fmt.Errorf("audio device %q is input-only", d.id)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateFrame("write", frame); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return &ClosedError{Operation: "write", Path: string(d.id)}
	}
	d.playback = append(d.playback, frame...)
	if len(d.playback) > FrameSize*64 {
		d.playback = d.playback[len(d.playback)-FrameSize*64:]
	}
	return nil
}
func (d *linuxOpenedDevice) PositiveAudioEvidence() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.positive
}
func (d *linuxOpenedDevice) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		device, ctx := d.device, d.context
		d.mu.Unlock()
		var stopErr error
		if device != nil {
			stopErr = device.Stop()
			device.Uninit()
		}
		d.closeErr = errors.Join(stopErr, cleanupLinuxContext(ctx))
		if d.release != nil {
			d.release()
		}
	})
	return d.closeErr
}

type linuxRegistryHandle struct {
	inner    OpenedDevice
	registry *LinuxDeviceRegistry
	id       DeviceID
	once     sync.Once
	err      error
}

func (h *linuxRegistryHandle) Close() error {
	h.once.Do(func() { h.err = h.inner.Close(); h.registry.release(h.id) })
	return h.err
}
