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
	"slices"
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

var (
	_ OpenedDevice = (*linuxOpenedDevice)(nil)
	_ AudioSource  = (*linuxOpenedDevice)(nil)
	_ AudioSink    = (*linuxOpenedDevice)(nil)
)

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
	if err != nil {
		return nil, err
	}
	devices := make([]Device, len(records))
	for i := range records {
		devices[i] = records[i].Device
	}
	return devices, nil
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
	return r.openAtFormat(id, DefaultDeviceFormat(), false)
}

// OpenWithFormat opens a Linux endpoint at an explicit PCM format. The
// backend is configured with the requested rate rather than silently using
// the legacy 16 kHz default.
func (r *LinuxDeviceRegistry) OpenWithFormat(id DeviceID, format DeviceFormat) (OpenedDevice, error) {
	return r.openAtFormat(id, format, true)
}

func (r *LinuxDeviceRegistry) openAtFormat(id DeviceID, format DeviceFormat, wrapFormatErrors bool) (OpenedDevice, error) {
	if err := format.Validate(); err != nil {
		return nil, err
	}
	if _, _, err := ParseDeviceID(id); err != nil {
		return nil, err
	}
	records, err := r.snapshot()
	if err != nil {
		return nil, NewDeviceNotFoundError(id)
	}
	i := slices.IndexFunc(records, func(record linuxDeviceRecord) bool { return record.ID == id })
	if i < 0 {
		return nil, NewDeviceNotFoundError(id)
	}
	r.mu.Lock()
	if _, ok := r.inUse[id]; ok {
		r.mu.Unlock()
		return nil, NewDeviceInUseError(id)
	}
	r.inUse[id] = struct{}{}
	r.mu.Unlock()
	opened, err := r.openNative(records[i], format)
	if err != nil {
		r.release(id)
		mapped := mapLinuxOpenError(id, err)
		if wrapFormatErrors {
			return nil, &DeviceFormatError{ID: id, Direction: records[i].Direction, Requested: format, Available: defaultDeviceFormatAvailability(), Err: mapped}
		}
		return nil, mapped
	}
	opened.release = func() { r.release(id) }
	return opened, nil
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
		if !slices.ContainsFunc(byBackend[0], func(pulse linuxDeviceRecord) bool { return pulse.Direction == record.Direction }) {
			selected = append(selected, record)
		}
	}
	if len(selected) > 0 {
		return selected, nil
	}
	if len(errs) == 0 || allLinuxBackendErrorsUnavailable(errs) {
		return []linuxDeviceRecord{}, nil
	}
	return nil, errors.Join(append(errs, errors.New("no usable ALSA or PulseAudio endpoints"))...)
}

func allLinuxBackendErrorsUnavailable(errs []error) bool {
	for _, err := range errs {
		if !isLinuxBackendUnavailable(err) {
			return false
		}
	}
	return true
}

func isLinuxBackendUnavailable(err error) bool {
	return errors.Is(err, malgo.ErrNoBackend) ||
		errors.Is(err, malgo.ErrNoDevice) ||
		errors.Is(err, malgo.ErrDoesNotExist) ||
		errors.Is(err, malgo.ErrUnavailable) ||
		errors.Is(err, malgo.ErrFailedToInitBackend)
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
			if record, e := newLinuxDeviceRecord(backend, name, request.direction, info); e == nil {
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
func (r *LinuxDeviceRegistry) openNative(record linuxDeviceRecord, formats ...DeviceFormat) (*linuxOpenedDevice, error) {
	format := DefaultDeviceFormat()
	if len(formats) > 0 {
		format = formats[0]
	}
	if err := format.Validate(); err != nil {
		return nil, err
	}
	ctx, err := malgo.InitContext([]malgo.Backend{record.backend}, malgo.ContextConfig{Alsa: malgo.AlsaContextConfig{UseVerboseDeviceEnumeration: 1}}, nil)
	if err != nil {
		return nil, err
	}
	config := malgo.DefaultDeviceConfig(malgo.Playback)
	if record.Direction == DirectionInput {
		config = malgo.DefaultDeviceConfig(malgo.Capture)
	}
	config.SampleRate, config.Alsa.NoMMap = uint32(format.SampleRate), 1
	nativeID := record.nativeID.Pointer()
	defer C.free(nativeID)
	if record.Direction == DirectionInput {
		config.Capture.Format, config.Capture.Channels, config.Capture.DeviceID = malgo.FormatS16, uint32(format.Channels), nativeID
	} else {
		config.Playback.Format, config.Playback.Channels, config.Playback.DeviceID = malgo.FormatS16, uint32(format.Channels), nativeID
	}
	handle := &linuxOpenedDevice{id: record.ID, direction: record.Direction, context: ctx, format: format}
	callbacks := malgo.DeviceCallbacks{Data: handle.onData}
	if record.Direction == DirectionInput {
		handle.microphone = &MicrophoneSource{malgoCtx: ctx, frameCh: make(chan []int16, 64)}
		callbacks.Data = func(_, input []byte, frames uint32) { handle.microphone.onCapture(input, int(frames)) }
	}
	device, err := malgo.InitDevice(ctx.Context, config, callbacks)
	if err != nil {
		return nil, errors.Join(err, cleanupLinuxContext(ctx))
	}
	handle.device = device
	if handle.microphone != nil {
		handle.microphone.device = device
	}
	if err = device.Start(); err != nil {
		device.Uninit()
		return nil, errors.Join(err, cleanupLinuxContext(ctx))
	}
	return handle, nil
}
func cleanupLinuxContext(ctx *malgo.AllocatedContext) error {
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
	mu         sync.Mutex
	closeOnce  sync.Once
	id         DeviceID
	direction  Direction
	format     DeviceFormat
	context    *malgo.AllocatedContext
	device     *malgo.Device
	microphone *MicrophoneSource
	playback   []int16
	closed     bool
	positive   bool
	closeErr   error
	release    func()
}

func (d *linuxOpenedDevice) DeviceFormat() DeviceFormat {
	if d == nil {
		return DeviceFormat{}
	}
	return d.format
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
	if !d.positive && slices.ContainsFunc(d.playback[:n], func(sample int16) bool { return sample != 0 }) {
		d.positive = true
	}
	d.playback = d.playback[n:]
}
func (d *linuxOpenedDevice) ReadFrame(ctx context.Context, frame []int16) error {
	if d.direction != DirectionInput {
		return fmt.Errorf("audio device %q is output-only", d.id)
	}
	if err := validateFrame("read", frame); err != nil {
		return err
	}
	if d.microphone == nil {
		return fmt.Errorf("audio device %q has no capture source", d.id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return d.microphone.ReadFrame(ctx, frame)
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
		if d.microphone != nil {
			d.closeErr = d.microphone.Close()
		} else {
			stopErr := device.Stop()
			device.Uninit()
			d.closeErr = errors.Join(stopErr, cleanupLinuxContext(ctx))
		}
		if d.release != nil {
			d.release()
		}
	})
	return d.closeErr
}
