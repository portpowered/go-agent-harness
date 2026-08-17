//go:build darwin && cgo && !nomicrophone

package audio

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gen2brain/malgo"
)

const coreAudioBackend = "coreaudio"

// CoreAudioDeviceRegistry exposes macOS's current CoreAudio endpoints. A
// context is created per operation so enumeration never keeps native state
// alive; an opened handle owns its context until Close.
type CoreAudioDeviceRegistry struct{}

var _ DeviceRegistry = (*CoreAudioDeviceRegistry)(nil)

var (
	_ OpenedDevice = (*coreAudioHandle)(nil)
	_ AudioSource  = (*coreAudioHandle)(nil)
	_ AudioSink    = (*coreAudioHandle)(nil)
)

// NewCoreAudioDeviceRegistry constructs the Darwin CoreAudio registry.
func NewCoreAudioDeviceRegistry() *CoreAudioDeviceRegistry {
	return &CoreAudioDeviceRegistry{}
}

type coreAudioEndpoint struct {
	device        Device
	native        malgo.DeviceID
	defaultDevice bool
}

func (r *CoreAudioDeviceRegistry) List() ([]Device, error) {
	endpoints, err := r.endpoints()
	if err != nil {
		return nil, err
	}
	devices := make([]Device, len(endpoints))
	for i := range endpoints {
		devices[i] = endpoints[i].device
	}
	return devices, nil
}

func (r *CoreAudioDeviceRegistry) Default(direction Direction) (Device, error) {
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	endpoints, err := r.endpoints()
	if err != nil {
		if isCoreAudioUnavailable(err) {
			return Device{}, NewNoDefaultDeviceError(direction)
		}
		return Device{}, err
	}
	for _, endpoint := range endpoints {
		if endpoint.device.Direction == direction && endpoint.defaultDevice {
			return endpoint.device, nil
		}
	}
	return Device{}, NewNoDefaultDeviceError(direction)
}

func (r *CoreAudioDeviceRegistry) Open(id DeviceID) (OpenedDevice, error) {
	backend, nativeID, err := ParseDeviceID(id)
	if err != nil {
		return nil, err
	}
	if backend != coreAudioBackend {
		return nil, NewDeviceNotFoundError(id)
	}
	if !strings.HasSuffix(nativeID, ":input") && !strings.HasSuffix(nativeID, ":output") {
		return nil, NewDeviceNotFoundError(id)
	}

	ctx, err := newCoreAudioContext()
	if err != nil {
		if isCoreAudioUnavailable(err) {
			return nil, NewDeviceNotFoundError(id)
		}
		return nil, fmt.Errorf("initialize CoreAudio for %q: %w", id, err)
	}
	endpoints, err := enumerateCoreAudioEndpoints(ctx)
	if err != nil {
		releaseCoreAudioContext(ctx)
		if isCoreAudioUnavailable(err) {
			return nil, NewDeviceNotFoundError(id)
		}
		return nil, err
	}
	for _, endpoint := range endpoints {
		if endpoint.device.ID != id {
			continue
		}
		handle, openErr := openCoreAudioEndpoint(ctx, endpoint)
		if openErr == nil {
			return handle, nil
		}
		releaseCoreAudioContext(ctx)
		return nil, mapCoreAudioOpenError(id, openErr)
	}
	releaseCoreAudioContext(ctx)
	return nil, NewDeviceNotFoundError(id)
}

func (r *CoreAudioDeviceRegistry) endpoints() ([]coreAudioEndpoint, error) {
	ctx, err := newCoreAudioContext()
	if err != nil {
		if isCoreAudioUnavailable(err) {
			return []coreAudioEndpoint{}, nil
		}
		return nil, fmt.Errorf("initialize CoreAudio: %w", err)
	}
	defer releaseCoreAudioContext(ctx)
	return enumerateCoreAudioEndpoints(ctx)
}

func newCoreAudioContext() (*malgo.AllocatedContext, error) {
	return malgo.InitContext([]malgo.Backend{malgo.BackendCoreaudio}, malgo.ContextConfig{}, nil)
}

func releaseCoreAudioContext(ctx *malgo.AllocatedContext) error {
	if ctx == nil {
		return nil
	}
	err := ctx.Uninit()
	ctx.Free()
	return err
}

func enumerateCoreAudioEndpoints(ctx *malgo.AllocatedContext) ([]coreAudioEndpoint, error) {
	var endpoints []coreAudioEndpoint
	seen := make(map[DeviceID]struct{})
	for _, direction := range []struct {
		kind  malgo.DeviceType
		value Direction
	}{
		{kind: malgo.Playback, value: DirectionOutput},
		{kind: malgo.Capture, value: DirectionInput},
	} {
		infos, err := ctx.Devices(direction.kind)
		if err != nil {
			if isCoreAudioUnavailable(err) {
				continue
			}
			return nil, fmt.Errorf("enumerate CoreAudio %s devices: %w", direction.value, err)
		}
		for _, raw := range infos {
			info := raw
			// malgo's combined enumeration can carry the playback default bit
			// into a duplex capture entry. Querying by direction fixes that.
			if detailed, detailErr := ctx.DeviceInfo(direction.kind, raw.ID, malgo.Shared); detailErr == nil {
				info = detailed
			} else {
				// The playback bit is directionally reliable in the raw
				// playback snapshot; the combined snapshot is not reliable
				// for capture because it reuses that bit for duplex devices.
				if direction.value == DirectionInput {
					info.IsDefault = 0
				}
			}
			uid := coreAudioUID(info.ID)
			if uid == "" || strings.TrimSpace(info.Name()) == "" {
				continue
			}
			device, deviceErr := NewDevice(coreAudioBackend, coreAudioNativeID(uid, direction.value), info.Name(), direction.value)
			if deviceErr != nil {
				return nil, fmt.Errorf("map CoreAudio %s device %q: %w", direction.value, uid, deviceErr)
			}
			if _, exists := seen[device.ID]; exists {
				return nil, fmt.Errorf("CoreAudio returned duplicate endpoint %q", device.ID)
			}
			seen[device.ID] = struct{}{}
			endpoints = append(endpoints, coreAudioEndpoint{device: device, native: info.ID, defaultDevice: info.IsDefault != 0})
		}
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].device.ID < endpoints[j].device.ID })
	return endpoints, nil
}

func coreAudioUID(id malgo.DeviceID) string { return strings.TrimRight(string(id[:]), "\x00") }

func coreAudioNativeID(uid string, direction Direction) string {
	return url.PathEscape(uid) + ":" + direction.String()
}

func openCoreAudioEndpoint(ctx *malgo.AllocatedContext, endpoint coreAudioEndpoint) (*coreAudioHandle, error) {
	direction := endpoint.device.Direction
	kind := malgo.Capture
	if direction == DirectionOutput {
		kind = malgo.Playback
	}
	config := malgo.DefaultDeviceConfig(kind)
	config.SampleRate = uint32(SampleRate)
	config.PerformanceProfile = malgo.LowLatency
	if direction == DirectionInput {
		config.Capture.Format = malgo.FormatS16
		config.Capture.Channels = uint32(Channels)
	} else {
		config.Playback.Format = malgo.FormatS16
		config.Playback.Channels = uint32(Channels)
	}
	nativeID := endpoint.native
	nativeIDPtr := nativeID.Pointer()
	defer C.free(nativeIDPtr)
	if direction == DirectionInput {
		config.Capture.DeviceID = nativeIDPtr
	} else {
		config.Playback.DeviceID = nativeIDPtr
	}
	handle := &coreAudioHandle{id: endpoint.device.ID, context: ctx, direction: direction}
	if direction == DirectionInput {
		handle.capture = &MicrophoneSource{malgoCtx: ctx, frameCh: make(chan []int16, 64)}
	}
	device, err := malgo.InitDevice(ctx.Context, config, malgo.DeviceCallbacks{Data: handle.onData})
	if err != nil {
		return nil, err
	}
	handle.device = device
	if handle.capture != nil {
		handle.capture.device = device
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		return nil, err
	}
	return handle, nil
}

func mapCoreAudioOpenError(id DeviceID, err error) error {
	if isCoreAudioUnavailable(err) {
		return NewDeviceNotFoundError(id)
	}
	if errors.Is(err, malgo.ErrBusy) || errors.Is(err, malgo.ErrAlreadyInUse) || errors.Is(err, malgo.ErrAccessDenied) || errors.Is(err, malgo.ErrInvalidOperation) {
		return NewDeviceInUseError(id)
	}
	return fmt.Errorf("open CoreAudio device %q: %w", id, err)
}

func isCoreAudioUnavailable(err error) bool {
	return errors.Is(err, malgo.ErrNoDevice) || errors.Is(err, malgo.ErrDoesNotExist) || errors.Is(err, malgo.ErrUnavailable)
}

type coreAudioHandle struct {
	id        DeviceID
	context   *malgo.AllocatedContext
	device    *malgo.Device
	direction Direction
	capture   *MicrophoneSource
	mu        sync.Mutex
	closeOnce sync.Once
	closeErr  error
	closed    bool
	playback  []int16
	callbacks atomic.Uint64
	samples   atomic.Uint64
	energy    atomic.Uint64
	nonZero   atomic.Uint64
}

func (h *coreAudioHandle) onData(output, input []byte, _ uint32) {
	h.callbacks.Add(1)
	if h.direction == DirectionInput {
		h.mu.Lock()
		closed := h.closed
		h.mu.Unlock()
		if closed {
			return
		}
		for i := 0; i+1 < len(input); i += 2 {
			sample := int64(int16(binary.LittleEndian.Uint16(input[i : i+2])))
			if sample < 0 {
				sample = -sample
			}
			h.samples.Add(1)
			h.energy.Add(uint64(sample))
		}
		if h.capture != nil {
			h.capture.onCapture(input, len(input)/2)
		}
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	clear(output)
	n := len(output) / 2
	if n > len(h.playback) {
		n = len(h.playback)
	}
	encodePCM16(output[:n*2], h.playback[:n])
	for _, sample := range h.playback[:n] {
		if sample != 0 {
			h.nonZero.Add(1)
		}
	}
	h.playback = h.playback[n:]
}

func (h *coreAudioHandle) ReadFrame(ctx context.Context, frame []int16) error {
	if h.direction != DirectionInput {
		return fmt.Errorf("audio device %q is output-only", h.id)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateFrame("read", frame); err != nil {
		return err
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return &ClosedError{Operation: "read", Path: string(h.id)}
	}
	if h.capture == nil {
		return fmt.Errorf("audio device %q has no capture source", h.id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return h.capture.ReadFrame(ctx, frame)
}

func (h *coreAudioHandle) WriteFrame(ctx context.Context, frame []int16) error {
	if h.direction != DirectionOutput {
		return fmt.Errorf("audio device %q is input-only", h.id)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateFrame("write", frame); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return &ClosedError{Operation: "write", Path: string(h.id)}
	}
	h.playback = append(h.playback, frame...)
	if len(h.playback) > FrameSize*64 {
		h.playback = h.playback[len(h.playback)-FrameSize*64:]
	}
	return nil
}

func (h *coreAudioHandle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
		if h.capture != nil {
			h.closeErr = h.capture.Close()
			return
		}
		if h.device != nil {
			if err := h.device.Stop(); err != nil && !errors.Is(err, malgo.ErrDeviceNotStarted) {
				h.closeErr = err
			}
			h.device.Uninit()
		}
		if err := releaseCoreAudioContext(h.context); err != nil {
			h.closeErr = errors.Join(h.closeErr, err)
		}
	})
	return h.closeErr
}
