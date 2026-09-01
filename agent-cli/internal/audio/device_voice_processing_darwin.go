//go:build darwin && cgo && !nomicrophone

package audio

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// voiceProcessingIO is Apple's duplex AUVoiceIO endpoint. Unlike two
// independent capture/playback handles, this audio unit owns both hardware
// directions and therefore has the exact render reference required by its
// acoustic echo canceller.
type voiceProcessingIO struct {
	api      *voiceProcessingAPI
	unit     uintptr
	inputID  DeviceID
	outputID DeviceID

	inputFormat  DeviceFormat
	outputFormat DeviceFormat
	capture      *MicrophoneSource
	playback     *PlaybackQueue

	mu           sync.Mutex
	playbackWake chan struct{}
	closed       atomic.Bool
	closeOnce    sync.Once
	closeErr     error
	refs         atomic.Int32

	// PureGo callback trampolines are process-lifetime allocations. Retaining
	// the function values here also pins their captured engine until the audio
	// unit has synchronously stopped and been disposed.
	renderCallback func(unsafe.Pointer, *uint32, unsafe.Pointer, uint32, uint32, *audioBufferList1) int32
	inputCallback  func(uintptr, uintptr, uintptr, uint32, uint32, uintptr) int32
}

type voiceProcessingEndpoint struct {
	engine    *voiceProcessingIO
	id        DeviceID
	direction Direction
	format    DeviceFormat
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

var (
	_ OpenedDevice         = (*voiceProcessingEndpoint)(nil)
	_ DeviceFormatProvider = (*voiceProcessingEndpoint)(nil)
	_ AudioSource          = (*voiceProcessingEndpoint)(nil)
	_ AudioSink            = (*voiceProcessingEndpoint)(nil)
)

type audioComponentDescription struct {
	ComponentType         uint32
	ComponentSubType      uint32
	ComponentManufacturer uint32
	ComponentFlags        uint32
	ComponentFlagsMask    uint32
}

type audioStreamBasicDescription struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

type auRenderCallbackStruct struct {
	InputProc       uintptr
	InputProcRefCon uintptr
}

type audioBuffer struct {
	NumberChannels uint32
	DataByteSize   uint32
	Data           unsafe.Pointer
}

// AudioBufferList has a trailing variable-sized buffer array. VoiceIO is
// configured as mono/interleaved, so its concrete representation has one
// AudioBuffer and this fixed layout (including the pointer-alignment pad).
type audioBufferList1 struct {
	NumberBuffers uint32
	_             uint32
	Buffers       [1]audioBuffer
}

type voiceProcessingAPI struct {
	audioComponentFindNext        func(uintptr, *audioComponentDescription) uintptr
	audioComponentInstanceNew     func(uintptr, *uintptr) int32
	audioComponentInstanceDispose func(uintptr) int32
	audioUnitSetProperty          func(uintptr, uint32, uint32, uint32, unsafe.Pointer, uint32) int32
	audioUnitInitialize           func(uintptr) int32
	audioUnitUninitialize         func(uintptr) int32
	audioOutputUnitStart          func(uintptr) int32
	audioOutputUnitStop           func(uintptr) int32
	audioUnitRender               func(uintptr, uintptr, uintptr, uint32, uint32, *audioBufferList1) int32
}

var (
	voiceProcessingAPIOnce sync.Once
	voiceProcessingAPIOne  *voiceProcessingAPI
	voiceProcessingAPIErr  error
)

const (
	auScopeGlobal = 0
	auScopeInput  = 1
	auScopeOutput = 2

	auPropertyStreamFormat      = 8
	auPropertyMaximumFrames     = 14
	auPropertySetRenderCallback = 23
	auPropertyEnableIO          = 2003
	auPropertySetInputCallback  = 2005
	auPropertyBypassVoice       = 2100
	auPropertyEnableAGC         = 2101

	voiceProcessingMaxFrames = 8192
)

func fourCC(value string) uint32 {
	return uint32(value[0])<<24 | uint32(value[1])<<16 | uint32(value[2])<<8 | uint32(value[3])
}

func loadVoiceProcessingAPI() (*voiceProcessingAPI, error) {
	voiceProcessingAPIOnce.Do(func() {
		handle, err := purego.Dlopen("/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			voiceProcessingAPIErr = fmt.Errorf("load AudioToolbox: %w", err)
			return
		}
		api := &voiceProcessingAPI{}
		registrations := []struct {
			name string
			dst  any
		}{
			{"AudioComponentFindNext", &api.audioComponentFindNext},
			{"AudioComponentInstanceNew", &api.audioComponentInstanceNew},
			{"AudioComponentInstanceDispose", &api.audioComponentInstanceDispose},
			{"AudioUnitSetProperty", &api.audioUnitSetProperty},
			{"AudioUnitInitialize", &api.audioUnitInitialize},
			{"AudioUnitUninitialize", &api.audioUnitUninitialize},
			{"AudioOutputUnitStart", &api.audioOutputUnitStart},
			{"AudioOutputUnitStop", &api.audioOutputUnitStop},
			{"AudioUnitRender", &api.audioUnitRender},
		}
		for _, registration := range registrations {
			symbol, symbolErr := purego.Dlsym(handle, registration.name)
			if symbolErr != nil {
				voiceProcessingAPIErr = fmt.Errorf("resolve AudioToolbox %s: %w", registration.name, symbolErr)
				return
			}
			purego.RegisterFunc(registration.dst, symbol)
		}
		voiceProcessingAPIOne = api
	})
	return voiceProcessingAPIOne, voiceProcessingAPIErr
}

func newVoiceProcessingIO(inputID, outputID DeviceID, inputFormat, outputFormat DeviceFormat) (*voiceProcessingEndpoint, *voiceProcessingEndpoint, error) {
	if err := inputFormat.Validate(); err != nil {
		return nil, nil, err
	}
	if err := outputFormat.Validate(); err != nil {
		return nil, nil, err
	}
	api, err := loadVoiceProcessingAPI()
	if err != nil {
		return nil, nil, err
	}
	description := audioComponentDescription{
		ComponentType:         fourCC("auou"),
		ComponentSubType:      fourCC("vpio"),
		ComponentManufacturer: fourCC("appl"),
	}
	component := api.audioComponentFindNext(0, &description)
	if component == 0 {
		return nil, nil, errors.New("AudioToolbox AUVoiceIO component is unavailable")
	}
	var unit uintptr
	if status := api.audioComponentInstanceNew(component, &unit); status != 0 {
		return nil, nil, voiceProcessingStatusError("create AUVoiceIO", status)
	}
	engine := &voiceProcessingIO{
		api: api, unit: unit, inputID: inputID, outputID: outputID,
		inputFormat: inputFormat, outputFormat: outputFormat,
		capture:      &MicrophoneSource{frameCh: make(chan []int16, 64)},
		playbackWake: make(chan struct{}),
	}
	engine.playback, err = NewPlaybackQueue(outputFormat)
	if err != nil {
		_ = api.audioComponentInstanceDispose(unit)
		return nil, nil, err
	}
	if err := engine.configureAndStart(); err != nil {
		_ = api.audioComponentInstanceDispose(unit)
		return nil, nil, err
	}
	engine.refs.Store(2)
	return &voiceProcessingEndpoint{engine: engine, id: inputID, direction: DirectionInput, format: inputFormat},
		&voiceProcessingEndpoint{engine: engine, id: outputID, direction: DirectionOutput, format: outputFormat}, nil
}

func (e *voiceProcessingIO) configureAndStart() error {
	one, zero := uint32(1), uint32(0)
	set := func(operation string, property, scope, element uint32, value unsafe.Pointer, size uint32) error {
		if status := e.api.audioUnitSetProperty(e.unit, property, scope, element, value, size); status != 0 {
			return voiceProcessingStatusError(operation, status)
		}
		return nil
	}
	if err := set("enable AUVoiceIO microphone", auPropertyEnableIO, auScopeInput, 1, unsafe.Pointer(&one), 4); err != nil {
		return err
	}
	if err := set("enable AUVoiceIO speaker", auPropertyEnableIO, auScopeOutput, 0, unsafe.Pointer(&one), 4); err != nil {
		return err
	}
	maxFrames := uint32(voiceProcessingMaxFrames)
	if err := set("set AUVoiceIO maximum callback frames", auPropertyMaximumFrames, auScopeGlobal, 0, unsafe.Pointer(&maxFrames), 4); err != nil {
		return err
	}
	inputASBD := voiceProcessingASBD(e.inputFormat)
	if err := set("set AUVoiceIO microphone format", auPropertyStreamFormat, auScopeOutput, 1, unsafe.Pointer(&inputASBD), uint32(unsafe.Sizeof(inputASBD))); err != nil {
		return err
	}
	outputASBD := voiceProcessingASBD(e.outputFormat)
	if err := set("set AUVoiceIO speaker format", auPropertyStreamFormat, auScopeInput, 0, unsafe.Pointer(&outputASBD), uint32(unsafe.Sizeof(outputASBD))); err != nil {
		return err
	}

	e.renderCallback = e.render
	e.inputCallback = e.captureInput
	render := auRenderCallbackStruct{InputProc: purego.NewCallback(e.renderCallback)}
	if err := set("install AUVoiceIO speaker callback", auPropertySetRenderCallback, auScopeInput, 0, unsafe.Pointer(&render), uint32(unsafe.Sizeof(render))); err != nil {
		return err
	}
	input := auRenderCallbackStruct{InputProc: purego.NewCallback(e.inputCallback)}
	if err := set("install AUVoiceIO microphone callback", auPropertySetInputCallback, auScopeGlobal, 1, unsafe.Pointer(&input), uint32(unsafe.Sizeof(input))); err != nil {
		return err
	}
	if err := set("enable AUVoiceIO voice processing", auPropertyBypassVoice, auScopeGlobal, 0, unsafe.Pointer(&zero), 4); err != nil {
		return err
	}
	if err := set("enable AUVoiceIO automatic gain control", auPropertyEnableAGC, auScopeGlobal, 0, unsafe.Pointer(&one), 4); err != nil {
		return err
	}
	if status := e.api.audioUnitInitialize(e.unit); status != 0 {
		return voiceProcessingStatusError("initialize AUVoiceIO", status)
	}
	if status := e.api.audioOutputUnitStart(e.unit); status != 0 {
		_ = e.api.audioUnitUninitialize(e.unit)
		return voiceProcessingStatusError("start AUVoiceIO", status)
	}
	return nil
}

func voiceProcessingASBD(format DeviceFormat) audioStreamBasicDescription {
	bytesPerFrame := uint32(format.Channels * format.BitDepth / 8)
	return audioStreamBasicDescription{
		SampleRate: float64(format.SampleRate), FormatID: fourCC("lpcm"), FormatFlags: 0x4 | 0x8,
		BytesPerPacket: bytesPerFrame, FramesPerPacket: 1, BytesPerFrame: bytesPerFrame,
		ChannelsPerFrame: uint32(format.Channels), BitsPerChannel: uint32(format.BitDepth),
	}
}

func voiceProcessingStatusError(operation string, status int32) error {
	return fmt.Errorf("%s failed with Core Audio OSStatus %d (0x%08x)", operation, status, uint32(status))
}

func (e *voiceProcessingIO) render(_ unsafe.Pointer, actionFlags *uint32, _ unsafe.Pointer, _ uint32, frames uint32, dataPointer *audioBufferList1) int32 {
	if dataPointer == nil {
		return -50 // paramErr
	}
	return e.renderBuffers(actionFlags, frames, dataPointer)
}

func (e *voiceProcessingIO) renderBuffers(actionFlags *uint32, frames uint32, list *audioBufferList1) int32 {
	if list.NumberBuffers == 0 || list.Buffers[0].Data == nil {
		return -50
	}
	buffer := &list.Buffers[0]
	available := int(buffer.DataByteSize)
	want := int(frames) * e.outputFormat.Channels * 2
	if want < available {
		available = want
	}
	raw := unsafe.Slice((*byte)(buffer.Data), available)
	clear(raw)

	read := 0
	e.mu.Lock()
	if !e.closed.Load() {
		read = e.playback.readPCM16(raw)
		if e.playback.Snapshot().QueuedSamples == 0 {
			e.signalPlaybackLocked()
		}
	}
	e.mu.Unlock()
	if actionFlags != nil && read == 0 {
		*actionFlags |= 1 << 4
	}
	return 0
}

func (e *voiceProcessingIO) captureInput(_ uintptr, actionFlags, timestamp uintptr, _ uint32, frames uint32, _ uintptr) int32 {
	if e.closed.Load() || frames == 0 {
		return 0
	}
	if frames > voiceProcessingMaxFrames {
		return -50
	}
	samples := make([]int16, int(frames)*e.inputFormat.Channels)
	list := audioBufferList1{NumberBuffers: 1}
	list.Buffers[0] = audioBuffer{
		NumberChannels: uint32(e.inputFormat.Channels), DataByteSize: uint32(len(samples) * 2), Data: unsafe.Pointer(&samples[0]),
	}
	if status := e.api.audioUnitRender(e.unit, actionFlags, timestamp, 1, frames, &list); status != 0 {
		return status
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&samples[0])), len(samples)*2)
	e.capture.onCapture(raw, len(samples))
	return 0
}

func (e *voiceProcessingIO) signalPlaybackLocked() {
	close(e.playbackWake)
	e.playbackWake = make(chan struct{})
}

func (e *voiceProcessingIO) release() error {
	if e.refs.Add(-1) != 0 {
		return nil
	}
	e.closeOnce.Do(func() {
		e.closed.Store(true)
		e.closeErr = errors.Join(
			voiceProcessingIgnoreStopped(e.api.audioOutputUnitStop(e.unit)),
			voiceProcessingIgnoreStopped(e.api.audioUnitUninitialize(e.unit)),
			voiceProcessingIgnoreStopped(e.api.audioComponentInstanceDispose(e.unit)),
		)
		e.mu.Lock()
		e.signalPlaybackLocked()
		e.mu.Unlock()
		e.closeCapture()
	})
	return e.closeErr
}

func (e *voiceProcessingIO) closeCapture() {
	e.capture.mu.Lock()
	defer e.capture.mu.Unlock()
	if !e.capture.closed {
		e.capture.closed = true
		close(e.capture.frameCh)
	}
}

func voiceProcessingIgnoreStopped(status int32) error {
	if status == 0 {
		return nil
	}
	return voiceProcessingStatusError("close AUVoiceIO", status)
}

func (h *voiceProcessingEndpoint) DeviceDirection() Direction  { return h.direction }
func (h *voiceProcessingEndpoint) DeviceFormat() DeviceFormat  { return h.format }
func (h *voiceProcessingEndpoint) VoiceProcessingActive() bool { return true }

func (h *voiceProcessingEndpoint) ReadFrame(ctx context.Context, frame []int16) error {
	if h.direction != DirectionInput {
		return fmt.Errorf("audio device %q is output-only", h.id)
	}
	if h.closed.Load() {
		return &ClosedError{Operation: "read", Path: string(h.id)}
	}
	return h.engine.capture.ReadFrame(ctx, frame)
}

func (h *voiceProcessingEndpoint) WriteFrame(ctx context.Context, frame []int16) error {
	if err := validateFrame("write", frame); err != nil {
		return err
	}
	return h.WriteSamples(ctx, frame)
}

func (h *voiceProcessingEndpoint) WriteSamples(ctx context.Context, samples []int16) error {
	if h.direction != DirectionOutput {
		return fmt.Errorf("audio device %q is input-only", h.id)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if h.closed.Load() {
		return &ClosedError{Operation: "write", Path: string(h.id)}
	}
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	if h.engine.closed.Load() {
		return &ClosedError{Operation: "write", Path: string(h.id)}
	}
	h.engine.playback.Enqueue(samples)
	return nil
}

func (h *voiceProcessingEndpoint) PlaybackStats() PlaybackQueueStats {
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	return h.engine.playback.Snapshot()
}

func (h *voiceProcessingEndpoint) DiscardPlayback() int {
	if h.closed.Load() {
		return 0
	}
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	return h.engine.playback.Discard()
}

func (h *voiceProcessingEndpoint) WaitForPlayback(ctx context.Context) error {
	for {
		if h.closed.Load() {
			return &ClosedError{Operation: "wait for playback", Path: string(h.id)}
		}
		h.engine.mu.Lock()
		if h.engine.closed.Load() {
			h.engine.mu.Unlock()
			return &ClosedError{Operation: "wait for playback", Path: string(h.id)}
		}
		if h.engine.playback.Snapshot().QueuedSamples == 0 {
			h.engine.mu.Unlock()
			return nil
		}
		wake := h.engine.playbackWake
		h.engine.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *voiceProcessingEndpoint) Close() error {
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		if h.direction == DirectionInput {
			h.engine.closeCapture()
		}
		h.closeErr = h.engine.release()
	})
	return h.closeErr
}
