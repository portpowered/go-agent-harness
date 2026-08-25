//go:build darwin && cgo && !nomicrophone

package audio

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
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

var coreAudioBackends = []malgo.Backend{malgo.BackendCoreaudio}

// CoreAudioDeviceRegistry exposes macOS's current CoreAudio endpoints.
type CoreAudioDeviceRegistry struct {
	enumerate func() ([]coreAudioEndpoint, error)
	open      func(coreAudioEndpoint) (OpenedDevice, error)
}

var _ DeviceRegistry = (*CoreAudioDeviceRegistry)(nil)
var (
	_ OpenedDevice = (*coreAudioHandle)(nil)
	_ AudioSource  = (*coreAudioHandle)(nil)
	_ AudioSink    = (*coreAudioHandle)(nil)
)

func NewCoreAudioDeviceRegistry() *CoreAudioDeviceRegistry {
	return &CoreAudioDeviceRegistry{enumerate: enumerateCoreAudioDevices, open: openCoreAudioDevice}
}

type coreAudioEndpoint struct {
	device        Device
	native        malgo.DeviceID
	defaultDevice bool
}

func (r *CoreAudioDeviceRegistry) List() ([]Device, error) {
	endpoints, err := r.enumerate()
	if err != nil {
		return nil, err
	}
	devices := make([]Device, len(endpoints))
	for i, endpoint := range endpoints {
		devices[i] = endpoint.device
	}
	return devices, nil
}
func (r *CoreAudioDeviceRegistry) Default(direction Direction) (Device, error) {
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	endpoints, err := r.enumerate()
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
	backend, _, err := ParseDeviceID(id)
	if err != nil {
		return nil, err
	}
	if backend != coreAudioBackend {
		return nil, NewDeviceNotFoundError(id)
	}
	endpoints, err := r.enumerate()
	if err != nil {
		if isCoreAudioUnavailable(err) {
			return nil, NewDeviceNotFoundError(id)
		}
		return nil, fmt.Errorf("enumerate CoreAudio for %q: %w", id, err)
	}
	for _, endpoint := range endpoints {
		if endpoint.device.ID != id {
			continue
		}
		opened, err := r.open(endpoint)
		if err != nil {
			return nil, mapCoreAudioOpenError(id, err)
		}
		return opened, nil
	}
	return nil, NewDeviceNotFoundError(id)
}
func releaseCoreAudioContext(ctx *malgo.AllocatedContext) error {
	defer ctx.Free()
	return ctx.Uninit()
}
func enumerateCoreAudioDevices() ([]coreAudioEndpoint, error) {
	ctx, err := malgo.InitContext(coreAudioBackends, malgo.ContextConfig{}, nil)
	if err != nil {
		if isCoreAudioUnavailable(err) {
			return []coreAudioEndpoint{}, nil
		}
		return nil, fmt.Errorf("initialize CoreAudio: %w", err)
	}
	defer releaseCoreAudioContext(ctx)
	return enumerateCoreAudioEndpoints(ctx)
}
func openCoreAudioDevice(endpoint coreAudioEndpoint) (OpenedDevice, error) {
	ctx, err := malgo.InitContext(coreAudioBackends, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, err
	}
	handle, err := openCoreAudioEndpoint(ctx, endpoint)
	if err != nil {
		return nil, errors.Join(err, releaseCoreAudioContext(ctx))
	}
	return handle, nil
}
func enumerateCoreAudioEndpoints(ctx *malgo.AllocatedContext) ([]coreAudioEndpoint, error) {
	var endpoints []coreAudioEndpoint
	seen := make(map[DeviceID]struct{})
	for _, request := range []struct {
		kind      malgo.DeviceType
		direction Direction
	}{{malgo.Playback, DirectionOutput}, {malgo.Capture, DirectionInput}} {
		infos, err := ctx.Devices(request.kind)
		if err != nil {
			if isCoreAudioUnavailable(err) {
				continue
			}
			return nil, fmt.Errorf("enumerate CoreAudio %s devices: %w", request.direction, err)
		}
		for _, raw := range infos {
			info := raw
			if detailed, detailErr := ctx.DeviceInfo(request.kind, raw.ID, malgo.Shared); detailErr == nil {
				info = detailed
			} else {
				info.IsDefault = 0
			}
			uid, name := coreAudioUID(info.ID), strings.TrimSpace(info.Name())
			if uid == "" || name == "" {
				continue
			}
			device, err := NewDevice(coreAudioBackend, coreAudioNativeID(uid, request.direction), name, request.direction)
			if err != nil {
				return nil, fmt.Errorf("map CoreAudio %s device %q: %w", request.direction, uid, err)
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
	config.SampleRate, config.PerformanceProfile = uint32(SampleRate), malgo.LowLatency
	if direction == DirectionInput {
		config.Capture.Format, config.Capture.Channels = malgo.FormatS16, uint32(Channels)
	} else {
		config.Playback.Format, config.Playback.Channels = malgo.FormatS16, uint32(Channels)
	}
	nativeID := endpoint.native.Pointer()
	defer C.free(nativeID)
	if direction == DirectionInput {
		config.Capture.DeviceID = nativeID
	} else {
		config.Playback.DeviceID = nativeID
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
	switch {
	case isCoreAudioUnavailable(err):
		return NewDeviceNotFoundError(id)
	case errors.Is(err, malgo.ErrBusy), errors.Is(err, malgo.ErrAlreadyInUse), errors.Is(err, malgo.ErrAccessDenied), errors.Is(err, malgo.ErrInvalidOperation):
		return NewDeviceInUseError(id)
	default:
		return fmt.Errorf("open CoreAudio device %q: %w", id, err)
	}
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
	closed    atomic.Bool
	playback  []int16
	nonZero   atomic.Uint64
	release   func()
}

func (h *coreAudioHandle) onData(output, input []byte, _ uint32) {
	if h.direction == DirectionInput {
		if h.capture != nil {
			h.capture.onCapture(input, len(input)/2)
		}
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed.Load() {
		return
	}
	clear(output)
	n := min(len(output)/2, len(h.playback))
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
	if h.closed.Load() {
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
	if h.closed.Load() {
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
		h.closed.Store(true)
		switch {
		case h.capture != nil:
			h.closeErr = h.capture.Close()
		case h.release != nil:
			h.release()
		case h.device != nil:
			if err := h.device.Stop(); err != nil && !errors.Is(err, malgo.ErrDeviceNotStarted) {
				h.closeErr = err
			}
			h.device.Uninit()
			h.closeErr = errors.Join(h.closeErr, releaseCoreAudioContext(h.context))
		}
	})
	return h.closeErr
}
