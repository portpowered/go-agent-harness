//go:build darwin && cgo && !nomicrophone

package audio

import (
	"context"
	"errors"
	"testing"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"
)

func TestVoiceProcessingCoreAudioABILayout(t *testing.T) {
	if unsafe.Sizeof(audioStreamBasicDescription{}) != 40 || unsafe.Sizeof(audioBufferList1{}) != 24 || unsafe.Sizeof(auRenderCallbackStruct{}) != 16 {
		t.Fatalf("unexpected CoreAudio ABI sizes: ASBD=%d ABL1=%d callback=%d", unsafe.Sizeof(audioStreamBasicDescription{}), unsafe.Sizeof(audioBufferList1{}), unsafe.Sizeof(auRenderCallbackStruct{}))
	}
}

func TestVoiceProcessingRenderMarksQueueUnderflowAsSilence(t *testing.T) {
	queue, err := NewPlaybackQueue(DefaultDeviceFormat())
	if err != nil {
		t.Fatal(err)
	}
	engine := &voiceProcessingIO{outputFormat: DefaultDeviceFormat(), playback: queue, playbackWake: make(chan struct{})}
	raw := make([]byte, FrameSize*2)
	list := audioBufferList1{NumberBuffers: 1, Buffers: [1]audioBuffer{{NumberChannels: 1, DataByteSize: uint32(len(raw)), Data: unsafe.Pointer(&raw[0])}}}
	var flags uint32
	if status := engine.renderBuffers(&flags, FrameSize, &list); status != 0 {
		t.Fatalf("render status=%d", status)
	}
	if flags&(1<<4) == 0 {
		t.Fatalf("render flags=%#x, want kAudioUnitRenderAction_OutputIsSilence", flags)
	}
}

func TestVoiceProcessingPlaybackCapacityWaitResumesAtLowWatermark(t *testing.T) {
	format := PCM16DeviceFormat(24000)
	queue, err := NewPlaybackQueue(format)
	if err != nil {
		t.Fatal(err)
	}
	engine := &voiceProcessingIO{outputFormat: format, playback: queue, playbackWake: make(chan struct{})}
	endpoint := &voiceProcessingEndpoint{engine: engine, id: "coreaudio:voice-processing:output", direction: DirectionOutput, format: format}
	low, high, err := PlaybackQueueWatermarks(format)
	if err != nil {
		t.Fatal(err)
	}
	for queued := 0; queued < high; queued += FrameSize {
		if err := endpoint.WriteFrame(context.Background(), make([]int16, FrameSize)); err != nil {
			t.Fatalf("prime AUVoiceIO playback queue: %v", err)
		}
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- endpoint.WaitForPlaybackCapacity(context.Background(), FrameSize) }()
	select {
	case err := <-waitDone:
		t.Fatalf("AUVoiceIO capacity wait returned above low watermark: %v", err)
	default:
	}

	for endpoint.PlaybackStats().QueuedSamples > low {
		renderVoiceProcessingTestFrame(t, engine)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("AUVoiceIO capacity wait after callback drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AUVoiceIO capacity wait did not resume at low watermark")
	}
	stats := endpoint.PlaybackStats()
	if stats.QueuedSamples != low || stats.DroppedSamples != 0 {
		t.Fatalf("paced AUVoiceIO queue stats = %+v, want low watermark and no drops", stats)
	}
}

func TestVoiceProcessingPlaybackBurstPreservesFIFO(t *testing.T) {
	format := PCM16DeviceFormat(24000)
	queue, err := NewPlaybackQueue(format)
	if err != nil {
		t.Fatal(err)
	}
	engine := &voiceProcessingIO{outputFormat: format, playback: queue, playbackWake: make(chan struct{})}
	endpoint := &voiceProcessingEndpoint{engine: engine, id: "coreaudio:voice-processing:output", direction: DirectionOutput, format: format}
	testPacedPlaybackBackend(t, endpoint, func(raw []byte) {
		list := audioBufferList1{NumberBuffers: 1, Buffers: [1]audioBuffer{{NumberChannels: 1, DataByteSize: uint32(len(raw)), Data: unsafe.Pointer(&raw[0])}}}
		if status := engine.renderBuffers(nil, FrameSize, &list); status != 0 {
			t.Fatalf("AUVoiceIO render status=%d", status)
		}
	})
}

func TestVoiceProcessingPlaybackCapacityWaitLifecycle(t *testing.T) {
	newBlockedWait := func(t *testing.T) (*voiceProcessingIO, *voiceProcessingEndpoint, <-chan error) {
		t.Helper()
		format := PCM16DeviceFormat(24000)
		queue, err := NewPlaybackQueue(format)
		if err != nil {
			t.Fatal(err)
		}
		engine := &voiceProcessingIO{outputFormat: format, playback: queue, playbackWake: make(chan struct{})}
		engine.refs.Store(2)
		endpoint := &voiceProcessingEndpoint{engine: engine, id: "coreaudio:voice-processing:output", direction: DirectionOutput, format: format}
		_, high, err := PlaybackQueueWatermarks(format)
		if err != nil {
			t.Fatal(err)
		}
		for queued := 0; queued < high; queued += FrameSize {
			if err := endpoint.WriteFrame(context.Background(), make([]int16, FrameSize)); err != nil {
				t.Fatal(err)
			}
		}
		wait := make(chan error, 1)
		go func() { wait <- endpoint.WaitForPlaybackCapacity(context.Background(), FrameSize) }()
		assertCapacityWaitBlocked(t, wait)
		return engine, endpoint, wait
	}

	t.Run("discard wakes producer", func(t *testing.T) {
		_, endpoint, wait := newBlockedWait(t)
		if discarded := endpoint.DiscardPlayback(); discarded == 0 {
			t.Fatal("AUVoiceIO discard removed no queued samples")
		}
		if err := awaitCapacityWait(t, wait); err != nil {
			t.Fatalf("capacity wait after discard: %v", err)
		}
	})

	t.Run("output close wakes producer while input remains open", func(t *testing.T) {
		_, endpoint, wait := newBlockedWait(t)
		if err := endpoint.Close(); err != nil {
			t.Fatal(err)
		}
		if err := awaitCapacityWait(t, wait); !errors.Is(err, ErrClosed) {
			t.Fatalf("capacity wait after output close = %v, want ErrClosed", err)
		}
	})
}

func renderVoiceProcessingTestFrame(t *testing.T, engine *voiceProcessingIO) {
	t.Helper()
	raw := make([]byte, FrameSize*2)
	list := audioBufferList1{NumberBuffers: 1, Buffers: [1]audioBuffer{{NumberChannels: 1, DataByteSize: uint32(len(raw)), Data: unsafe.Pointer(&raw[0])}}}
	if status := engine.renderBuffers(nil, FrameSize, &list); status != 0 {
		t.Fatalf("AUVoiceIO render status=%d", status)
	}
}

func TestCoreAudioDeviceRegistryConformance(t *testing.T) {
	t.Log("platform-independent CoreAudio conformance: 7 groups, 0 capability skips")
	RunDeviceRegistryConformance(t, coreAudioPortableFixture)
}
func coreAudioPortableFixture() DeviceRegistryConformanceFixture {
	state := &coreAudioPortableState{}
	input := coreAudioTestEndpoint("persistent-input", "Test microphone", DirectionInput, true)
	output := coreAudioTestEndpoint("persistent-output", "Test speaker", DirectionOutput, true)
	state.endpoints = []coreAudioEndpoint{input, output}
	return DeviceRegistryConformanceFixture{
		Registry: &CoreAudioDeviceRegistry{enumerate: state.list, open: state.open}, InputDefault: input.device.ID,
		OutputDefault: output.device.ID, ExclusiveID: output.device.ID, RemoveDevice: state.remove,
		Observations: state.observations,
	}
}

func TestCoreAudioPlaybackQueueUsesResolvedRateAndCountsOverflow(t *testing.T) {
	const providerRate = 24000
	handle := &coreAudioHandle{direction: DirectionOutput, format: PCM16DeviceFormat(providerRate)}
	for frameIndex := 0; frameIndex < 16; frameIndex++ {
		frame := make([]int16, FrameSize)
		for sampleIndex := range frame {
			frame[sampleIndex] = int16(frameIndex*FrameSize + sampleIndex)
		}
		if err := handle.WriteFrame(context.Background(), frame); err != nil {
			t.Fatalf("WriteFrame(%d): %v", frameIndex, err)
		}
	}

	stats := handle.PlaybackStats()
	if stats.Format != PCM16DeviceFormat(providerRate) || stats.CapacitySamples != 6000 || stats.QueuedSamples != 6000 {
		t.Fatalf("CoreAudio playback stats before callback = %+v, want 24 kHz/6000 samples", stats)
	}
	if stats.DroppedSamples != 1680 || stats.OverflowEvents != 4 {
		t.Fatalf("CoreAudio overflow stats = %+v, want 1680 samples across 4 events", stats)
	}

	output := make([]byte, FrameSize*2)
	handle.onData(output, nil, FrameSize)
	decoded := make([]int16, FrameSize)
	decodePCM16(decoded, output)
	if decoded[0] != 1680 || decoded[len(decoded)-1] != 2159 {
		t.Fatalf("CoreAudio callback output starts at %d and ends at %d, want 1680..2159", decoded[0], decoded[len(decoded)-1])
	}
	if got := handle.PlaybackStats().QueuedSamples; got != 5520 {
		t.Fatalf("CoreAudio queued samples after callback = %d, want 5520", got)
	}
}

func TestCoreAudioPlaybackCapacityWaitResumesAtLowWatermark(t *testing.T) {
	handle := &coreAudioHandle{direction: DirectionOutput, format: DefaultDeviceFormat(), playbackWake: make(chan struct{})}
	low, high, err := PlaybackQueueWatermarks(handle.format)
	if err != nil {
		t.Fatalf("resolve playback watermarks: %v", err)
	}
	for queued := 0; queued < high; queued += FrameSize {
		if err := handle.WriteFrame(context.Background(), make([]int16, FrameSize)); err != nil {
			t.Fatalf("prime playback queue: %v", err)
		}
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- handle.WaitForPlaybackCapacity(context.Background(), FrameSize) }()
	select {
	case err := <-waitDone:
		t.Fatalf("capacity wait returned above low watermark: %v", err)
	default:
	}

	callback := make([]byte, FrameSize*2)
	for handle.PlaybackStats().QueuedSamples > low {
		handle.onData(callback, nil, FrameSize)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("capacity wait after callback drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("capacity wait did not resume at low watermark")
	}
	stats := handle.PlaybackStats()
	if stats.QueuedSamples != low || stats.DroppedSamples != 0 {
		t.Fatalf("paced CoreAudio queue stats = %+v, want low watermark and no drops", stats)
	}
}

func TestCoreAudioPlaybackBurstPreservesFIFO(t *testing.T) {
	handle := &coreAudioHandle{direction: DirectionOutput, format: PCM16DeviceFormat(24000), playbackWake: make(chan struct{})}
	testPacedPlaybackBackend(t, handle, func(raw []byte) {
		handle.onData(raw, nil, FrameSize)
	})
}

func coreAudioTestEndpoint(uid, name string, direction Direction, defaulted bool) coreAudioEndpoint {
	device, _ := NewDevice(coreAudioBackend, coreAudioNativeID(uid, direction), name, direction)
	return coreAudioEndpoint{device: device, defaultDevice: defaulted}
}

type coreAudioPortableState struct {
	endpoints []coreAudioEndpoint
	inUse     bool
	opens     int
	releases  int
}

func (s *coreAudioPortableState) list() ([]coreAudioEndpoint, error) {
	return s.endpoints, nil
}
func (s *coreAudioPortableState) open(_ coreAudioEndpoint) (OpenedDevice, error) {
	if s.inUse {
		return nil, malgo.ErrBusy
	}
	s.inUse = true
	s.opens++
	return &coreAudioHandle{release: func() { s.inUse = false; s.releases++ }}, nil
}
func (s *coreAudioPortableState) remove(id DeviceID) {
	for index, endpoint := range s.endpoints {
		if endpoint.device.ID == id {
			s.endpoints = append(s.endpoints[:index], s.endpoints[index+1:]...)
			return
		}
	}
}
func (s *coreAudioPortableState) observations() DeviceRegistryObservations {
	return DeviceRegistryObservations{OpenCount: s.opens, ReleaseCount: s.releases}
}
