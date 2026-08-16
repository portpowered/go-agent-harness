//go:build linux && cgo && !nomicrophone

package audio

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/gen2brain/malgo"
)

const (
	linuxPulseBackend = "pulse"
	linuxAlsaBackend  = "alsa"
)

type linuxBackendSpec struct {
	name string
	id   malgo.Backend
}

func linuxBackendSpecs() []linuxBackendSpec {
	return []linuxBackendSpec{
		{name: linuxPulseBackend, id: malgo.BackendPulseaudio},
		{name: linuxAlsaBackend, id: malgo.BackendAlsa},
	}
}

type linuxDeviceRecord struct {
	device    Device
	nativeID  malgo.DeviceID
	backend   linuxBackendSpec
	isDefault bool
}

type linuxEnumerateFunc func() ([]linuxDeviceRecord, error)
type linuxOpenFunc func(linuxDeviceRecord) (OpenedDevice, error)

// LinuxDeviceRegistry is the Linux ALSA/PulseAudio adapter for DeviceRegistry.
// Each operation takes a fresh native snapshot, so Open revalidates IDs.
type LinuxDeviceRegistry struct {
	mu        sync.Mutex
	enumerate linuxEnumerateFunc
	open      linuxOpenFunc
	inUse     map[DeviceID]bool
}

// NewDeviceRegistry creates a lazy Linux ALSA/PulseAudio registry.
func NewDeviceRegistry() *LinuxDeviceRegistry {
	r := newLinuxDeviceRegistry(enumerateLinuxDevices, nil)
	r.open = r.openNative
	return r
}

// NewLinuxDeviceRegistry is the explicit platform-named constructor.
func NewLinuxDeviceRegistry() *LinuxDeviceRegistry { return NewDeviceRegistry() }

func newLinuxDeviceRegistry(enumerate linuxEnumerateFunc, open linuxOpenFunc) *LinuxDeviceRegistry {
	r := &LinuxDeviceRegistry{enumerate: enumerate, open: open, inUse: make(map[DeviceID]bool)}
	return r
}

var _ DeviceRegistry = (*LinuxDeviceRegistry)(nil)

func (r *LinuxDeviceRegistry) List() ([]Device, error) {
	if r == nil {
		return nil, errors.New("linux device registry is nil")
	}
	records, err := r.snapshot()
	devices := make([]Device, len(records))
	for i, record := range records {
		devices[i] = record.device
	}
	return devices, err
}

func (r *LinuxDeviceRegistry) Default(direction Direction) (Device, error) {
	if r == nil {
		return Device{}, errors.New("linux device registry is nil")
	}
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	records, err := r.snapshot()
	if err != nil {
		return Device{}, err
	}
	for _, record := range records {
		if record.device.Direction == direction && record.isDefault {
			return record.device, nil
		}
	}
	return Device{}, NewNoDefaultDeviceError(direction)
}

func (r *LinuxDeviceRegistry) Open(id DeviceID) (OpenedDevice, error) {
	if r == nil {
		return nil, errors.New("linux device registry is nil")
	}
	if _, _, err := ParseDeviceID(id); err != nil {
		return nil, err
	}
	records, err := r.snapshot()
	if err != nil {
		return nil, NewDeviceNotFoundError(id)
	}
	var record linuxDeviceRecord
	for _, candidate := range records {
		if candidate.device.ID == id {
			record = candidate
			break
		}
	}
	if record.device.ID == "" {
		return nil, NewDeviceNotFoundError(id)
	}

	r.mu.Lock()
	if r.inUse == nil {
		r.inUse = make(map[DeviceID]bool)
	}
	if r.inUse[id] {
		r.mu.Unlock()
		return nil, NewDeviceInUseError(id)
	}
	r.inUse[id] = true
	open := r.open
	r.mu.Unlock()
	if open == nil {
		r.release(id)
		return nil, errors.New("linux device registry opener is nil")
	}
	opened, err := open(record)
	if err != nil || opened == nil {
		r.release(id)
		if err == nil {
			err = errors.New("native opener returned a nil handle")
		}
		return nil, mapLinuxOpenError(id, err)
	}
	if native, ok := opened.(*LinuxOpenedDevice); ok {
		native.release = func() { r.release(id) }
		return native, nil
	}
	return &linuxRegistryHandle{inner: opened, registry: r, id: id}, nil
}

func (r *LinuxDeviceRegistry) snapshot() ([]linuxDeviceRecord, error) {
	r.mu.Lock()
	enumerate := r.enumerate
	r.mu.Unlock()
	if enumerate == nil {
		return nil, errors.New("linux device registry enumerator is nil")
	}
	records, err := enumerate()
	return canonicalLinuxRecords(records), err
}

func (r *LinuxDeviceRegistry) release(id DeviceID) {
	r.mu.Lock()
	delete(r.inUse, id)
	r.mu.Unlock()
}

func canonicalLinuxRecords(records []linuxDeviceRecord) []linuxDeviceRecord {
	records = append([]linuxDeviceRecord(nil), records...)
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].device.ID != records[j].device.ID {
			return records[i].device.ID < records[j].device.ID
		}
		if records[i].device.Display() != records[j].device.Display() {
			return records[i].device.Display() < records[j].device.Display()
		}
		return records[i].nativeID.String() < records[j].nativeID.String()
	})
	unique := records[:0]
	for _, record := range records {
		if len(unique) > 0 && unique[len(unique)-1].device.ID == record.device.ID {
			unique[len(unique)-1].isDefault = unique[len(unique)-1].isDefault || record.isDefault
			continue
		}
		unique = append(unique, record)
	}
	return unique
}

func enumerateLinuxDevices() ([]linuxDeviceRecord, error) {
	results := make(map[string][]linuxDeviceRecord)
	var errs []error
	for _, spec := range linuxBackendSpecs() {
		records, err := enumerateLinuxBackend(spec)
		results[spec.name] = records
		if err != nil {
			errs = append(errs, fmt.Errorf("linux %s: %w", spec.name, err))
		}
	}
	selected := selectLinuxBackendRecords(results)
	if len(selected) > 0 {
		return selected, nil
	}
	return selected, errors.Join(errs...)
}

func selectLinuxBackendRecords(results map[string][]linuxDeviceRecord) []linuxDeviceRecord {
	var selected []linuxDeviceRecord
	for _, direction := range []Direction{DirectionInput, DirectionOutput} {
		pulse := recordsForDirection(results[linuxPulseBackend], direction)
		if len(pulse) > 0 {
			selected = append(selected, pulse...)
		} else {
			selected = append(selected, recordsForDirection(results[linuxAlsaBackend], direction)...)
		}
	}
	return canonicalLinuxRecords(selected)
}

func recordsForDirection(records []linuxDeviceRecord, direction Direction) []linuxDeviceRecord {
	filtered := make([]linuxDeviceRecord, 0, len(records))
	for _, record := range records {
		if record.device.Direction == direction {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func enumerateLinuxBackend(spec linuxBackendSpec) ([]linuxDeviceRecord, error) {
	ctx, err := malgo.InitContext([]malgo.Backend{spec.id}, malgo.ContextConfig{
		Alsa: malgo.AlsaContextConfig{UseVerboseDeviceEnumeration: 1},
	}, nil)
	if err != nil {
		return nil, err
	}
	var records []linuxDeviceRecord
	var errs []error
	for _, request := range []struct {
		kind malgo.DeviceType
		dir  Direction
	}{{malgo.Capture, DirectionInput}, {malgo.Playback, DirectionOutput}} {
		infos, err := ctx.Devices(request.kind)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s enumeration: %w", request.dir, err))
			continue
		}
		for _, info := range infos {
			record, err := newLinuxDeviceRecord(spec, request.dir, info)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			records = append(records, record)
		}
	}
	if err := cleanupLinuxContext(ctx); err != nil {
		errs = append(errs, err)
	}
	return canonicalLinuxRecords(records), errors.Join(errs...)
}

func newLinuxDeviceRecord(spec linuxBackendSpec, direction Direction, info malgo.DeviceInfo) (linuxDeviceRecord, error) {
	nativeID, err := linuxNativeIdentifier(info.ID)
	if err != nil {
		return linuxDeviceRecord{}, fmt.Errorf("%s %s device: %w", spec.name, direction, err)
	}
	name := strings.TrimSpace(info.Name())
	if name == "" {
		name = nativeID
	}
	device, err := NewDevice(spec.name, direction.String()+":"+nativeID, name, direction)
	if err != nil {
		return linuxDeviceRecord{}, err
	}
	return linuxDeviceRecord{device: device, nativeID: info.ID, backend: spec, isDefault: info.IsDefault != 0}, nil
}

func linuxNativeIdentifier(id malgo.DeviceID) (string, error) {
	encoded := id.String()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	name := strings.TrimRight(string(decoded), "\x00")
	if name != "" && strings.TrimSpace(name) == name && strings.IndexFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) < 0 {
		return name, nil
	}
	if encoded == "" || encoded == "00" {
		return "", errors.New("native ID is empty")
	}
	return encoded, nil
}

func (r *LinuxDeviceRegistry) openNative(record linuxDeviceRecord) (OpenedDevice, error) {
	ctx, err := malgo.InitContext([]malgo.Backend{record.backend.id}, malgo.ContextConfig{
		Alsa: malgo.AlsaContextConfig{UseVerboseDeviceEnumeration: 1},
	}, nil)
	if err != nil {
		return nil, err
	}
	kind := malgo.Playback
	config := malgo.DefaultDeviceConfig(kind)
	if record.device.Direction == DirectionInput {
		kind = malgo.Capture
		config = malgo.DefaultDeviceConfig(kind)
	}
	config.SampleRate = uint32(SampleRate)
	config.Alsa.NoMMap = 1
	if kind == malgo.Capture {
		config.Capture.Format, config.Capture.Channels = malgo.FormatS16, uint32(Channels)
	} else {
		config.Playback.Format, config.Playback.Channels = malgo.FormatS16, uint32(Channels)
	}
	nativeID := record.nativeID
	nativeIDPointer := nativeID.Pointer()
	defer C.free(nativeIDPointer)
	if kind == malgo.Capture {
		config.Capture.DeviceID = nativeIDPointer
	} else {
		config.Playback.DeviceID = nativeIDPointer
	}
	handle := &LinuxOpenedDevice{id: record.device.ID, direction: record.device.Direction, context: ctx}
	device, err := malgo.InitDevice(ctx.Context, config, malgo.DeviceCallbacks{Data: handle.onData})
	if err != nil {
		return nil, errors.Join(err, cleanupLinuxContext(ctx))
	}
	handle.device, handle.frames = device, make(chan []int16, 64)
	if err := device.Start(); err != nil {
		device.Uninit()
		return nil, errors.Join(err, cleanupLinuxContext(ctx))
	}
	return handle, nil
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
	if errors.Is(err, malgo.ErrBusy) || errors.Is(err, malgo.ErrAlreadyInUse) {
		return NewDeviceInUseError(id)
	}
	if errors.Is(err, malgo.ErrNoDevice) || errors.Is(err, malgo.ErrDoesNotExist) || errors.Is(err, malgo.ErrUnavailable) {
		return NewDeviceNotFoundError(id)
	}
	return fmt.Errorf("open device %q: %w", id, err)
}

// LinuxOpenedDevice is the concrete capture or render handle returned by Open.
type LinuxOpenedDevice struct {
	mu        sync.Mutex
	closeOnce sync.Once
	id        DeviceID
	direction Direction
	context   *malgo.AllocatedContext
	device    *malgo.Device
	frames    chan []int16
	capture   []int16
	playback  []int16
	closed    bool
	closeErr  error
	positive  bool
	release   func()
}

var (
	_ OpenedDevice = (*LinuxOpenedDevice)(nil)
	_ AudioSource  = (*LinuxOpenedDevice)(nil)
	_ AudioSink    = (*LinuxOpenedDevice)(nil)
)

func (d *LinuxOpenedDevice) onData(output, input []byte, _ uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if d.direction == DirectionInput {
		for i := 0; i+1 < len(input); i += 2 {
			sample := int16(binary.LittleEndian.Uint16(input[i:]))
			d.positive = d.positive || sample != 0
			d.capture = append(d.capture, sample)
		}
		for len(d.capture) >= FrameSize {
			frame := append([]int16(nil), d.capture[:FrameSize]...)
			d.capture = d.capture[FrameSize:]
			select {
			case d.frames <- frame:
			default:
				select {
				case <-d.frames:
				default:
				}
				select {
				case d.frames <- frame:
				default:
				}
			}
		}
		return
	}
	clear(output)
	n := min(len(output)/2, len(d.playback))
	for i, sample := range d.playback[:n] {
		binary.LittleEndian.PutUint16(output[i*2:], uint16(sample))
		d.positive = d.positive || sample != 0
	}
	d.playback = d.playback[n:]
}

func (d *LinuxOpenedDevice) ReadFrame(ctx context.Context, buf []int16) error {
	if d.direction != DirectionInput {
		return fmt.Errorf("audio device %q is output-only", d.id)
	}
	if err := validateFrame("read", buf); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case frame, ok := <-d.frames:
		if !ok {
			return io.EOF
		}
		copy(buf, frame)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *LinuxOpenedDevice) WriteFrame(ctx context.Context, frame []int16) error {
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

func (d *LinuxOpenedDevice) PositiveAudioEvidence() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.positive
}

func (d *LinuxOpenedDevice) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		device, ctx, frames, release := d.device, d.context, d.frames, d.release
		d.mu.Unlock()
		d.closeErr = errors.Join(deviceStop(device), cleanupLinuxContext(ctx))
		if frames != nil {
			close(frames)
		}
		if release != nil {
			release()
		}
	})
	return d.closeErr
}

func deviceStop(device *malgo.Device) error {
	if device == nil {
		return nil
	}
	err := device.Stop()
	device.Uninit()
	return err
}

type linuxRegistryHandle struct {
	inner    OpenedDevice
	registry *LinuxDeviceRegistry
	id       DeviceID
	once     sync.Once
	err      error
}

func (h *linuxRegistryHandle) Close() error {
	h.once.Do(func() {
		h.err = h.inner.Close()
		h.registry.release(h.id)
	})
	return h.err
}
