package audio_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/stretchr/testify/require"
)

func TestVirtualConformance(t *testing.T) { audio.RunDeviceRegistryConformance(t, virtualFixture) }
func virtualFixture() audio.DeviceRegistryConformanceFixture {
	r := registry(audio.DefaultVirtualBackendConfig())
	return audio.DeviceRegistryConformanceFixture{Registry: r, InputDefault: "virtual:input", OutputDefault: "virtual:output", ExclusiveID: "virtual:exclusive", RemoveDevice: func(id audio.DeviceID) { r.RemoveDevice(id) }, Observations: r.Observations}
}
func TestVirtualLoopbackLifecycle(t *testing.T) {
	_, out, in := openPair()
	wants := [][]byte{{1, 2}, {7, 8, 9}, {42}}
	for _, want := range wants {
		write := append([]byte(nil), want...)
		require.NoError(t, out.Write(context.Background(), write))
		write[0] = 255
	}
	for _, want := range wants {
		got, err := in.Read(context.Background())
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	pendingReadCloses(t, in)
	require.ErrorIs(t, out.Write(context.Background(), []byte{9}), audio.ErrClosed)
	require.NoError(t, out.Close())
}
func TestVirtualFaults(t *testing.T) {
	r := registry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "Same", Direction: audio.DirectionInput}, {ID: "b", Name: "Same", Direction: audio.DirectionInput}}})
	list, _ := r.List()
	amb := audio.NewAmbiguousDeviceNameError("Same", list)
	require.ErrorIs(t, amb, audio.ErrAmbiguousDeviceName)
	require.Equal(t, []audio.DeviceID{"virtual:a", "virtual:b"}, []audio.DeviceID{amb.Candidates[0].ID, amb.Candidates[1].ID})
	r, out, _ := openPair()
	r.RemoveDevice("virtual:output")
	_, err := r.Open("virtual:output")
	require.ErrorIs(t, err, audio.ErrDeviceNotFound)
	err = out.Write(context.Background(), []byte{1})
	var lost *audio.DeviceLostError
	require.ErrorAs(t, err, &lost)
	require.Equal(t, audio.DeviceID("virtual:output"), lost.ID)
	require.Equal(t, audio.DirectionOutput, lost.Direction)
	require.ErrorIs(t, err, audio.ErrDeviceLost)
}
func TestVirtualProductionConfiguration(t *testing.T) {
	capability := audio.VirtualCapability{SampleRate: 16000, Channels: 1, BitDepth: 16, Format: "pcm16"}
	caps := []audio.VirtualCapability{capability}
	c := audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{
		{ID: "input", Name: "Input", Direction: audio.DirectionInput, Capabilities: caps, LoopbackID: "output"},
		{ID: "output", Name: "Output", Direction: audio.DirectionOutput, Capabilities: caps, LoopbackID: "input"},
	}, Defaults: map[audio.Direction]string{audio.DirectionInput: "input", audio.DirectionOutput: "output"}}
	production := audio.NewProductionAudioBackendRegistry()
	require.Equal(t, []string{audio.VirtualBackendName}, production.Names())
	registry, err := production.New(audio.VirtualBackendName, c)
	require.NoError(t, err)
	virtual := registry.(*audio.VirtualRegistry)
	caps[0].Channels = 9
	got, err := virtual.Capabilities("virtual:input")
	require.NoError(t, err)
	require.Equal(t, []audio.VirtualCapability{capability}, got)
	got[0].Channels = 8
	got, err = virtual.Capabilities("virtual:input")
	require.NoError(t, err)
	require.Equal(t, capability, got[0])
	_, err = virtual.Capabilities("virtual:missing")
	require.ErrorIs(t, err, audio.ErrDeviceNotFound)
	_, err = production.New("missing", c)
	require.Error(t, err)
}

func TestVirtualExplicitPCM16RatePreservesDeviceContract(t *testing.T) {
	const providerRate = 24000
	capability := audio.VirtualCapability{SampleRate: providerRate, Channels: 1, BitDepth: 16, Format: audio.DeviceEncodingPCM16}
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "input", Name: "Input", Direction: audio.DirectionInput, Capabilities: []audio.VirtualCapability{capability}, LoopbackID: "output"},
			{ID: "output", Name: "Output", Direction: audio.DirectionOutput, Capabilities: []audio.VirtualCapability{capability}, LoopbackID: "input"},
		},
		Defaults: map[audio.Direction]string{audio.DirectionInput: "input", audio.DirectionOutput: "output"},
	})
	require.NoError(t, err)

	sink, err := audio.NewDeviceSinkAtRate(registry, "virtual:output", providerRate)
	require.NoError(t, err)
	source, err := audio.NewDeviceSourceAtRate(registry, "virtual:input", providerRate)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, source.Close())
		require.NoError(t, sink.Close())
	})

	want := make([]int16, audio.FrameSize)
	for index := range want {
		want[index] = int16(index*13 - 2000)
	}
	require.Equal(t, audio.PCM16DeviceFormat(providerRate), sink.DeviceFormat())
	require.Equal(t, providerRate, sink.SampleRate())
	require.Equal(t, audio.PCM16DeviceFormat(providerRate), source.DeviceFormat())
	require.NoError(t, sink.WriteFrame(context.Background(), want))
	got := make([]int16, audio.FrameSize)
	require.NoError(t, source.ReadFrame(context.Background(), got))
	require.Equal(t, want, got)
}

func TestVirtualUnsupportedExplicitRateNamesAvailableCapability(t *testing.T) {
	registry := registry(audio.DefaultVirtualBackendConfig())
	_, err := audio.NewDeviceSinkAtRate(registry, "virtual:output", 24000)
	var formatErr *audio.DeviceFormatError
	require.ErrorAs(t, err, &formatErr)
	require.ErrorIs(t, err, audio.ErrUnsupportedDeviceFormat)
	require.Contains(t, err.Error(), "24000 Hz")
	require.Contains(t, err.Error(), "16000 Hz")
}

func TestVirtualValidationAndDefaults(t *testing.T) {
	base := audio.DefaultVirtualBackendConfig()
	badPair := base
	badPair.Devices = []audio.VirtualDeviceConfig{
		{ID: "input", Name: "Input", Direction: audio.DirectionInput, Capabilities: []audio.VirtualCapability{{Format: "a"}}, LoopbackID: "output"},
		{ID: "output", Name: "Output", Direction: audio.DirectionOutput, Capabilities: []audio.VirtualCapability{{Format: "b"}}, LoopbackID: "input"},
	}
	invalid := []audio.VirtualBackendConfig{
		{},
		{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "A", Direction: audio.DirectionInput}, {ID: "a", Name: "A", Direction: audio.DirectionInput}}},
		{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "A", Direction: audio.DirectionInput, LoopbackID: "missing"}}},
		badPair,
		{Devices: []audio.VirtualDeviceConfig{{ID: "other:a", Name: "A", Direction: audio.DirectionInput}}},
		{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "A", Direction: audio.DirectionInput}}, Defaults: map[audio.Direction]string{audio.Direction("bad"): "a"}},
		{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "A", Direction: audio.DirectionInput}}, Defaults: map[audio.Direction]string{audio.DirectionOutput: "a"}},
	}
	for _, c := range invalid {
		_, err := audio.NewVirtualRegistry(c)
		require.Error(t, err)
	}
	for _, missing := range []audio.Direction{audio.DirectionInput, audio.DirectionOutput} {
		c := audio.DefaultVirtualBackendConfig()
		delete(c.Defaults, missing)
		r, err := audio.NewVirtualRegistry(c)
		require.NoError(t, err)
		_, err = r.Default(missing)
		var noDefault *audio.NoDefaultDeviceError
		require.ErrorAs(t, err, &noDefault)
		require.Equal(t, missing, noDefault.Direction)
	}
}
func TestVirtualS8Accounting(t *testing.T) {
	r, out, in := openPair()
	const frames, attempts = 24, 20
	start, done := make(chan struct{}), make(chan struct{}, 4)
	var accepted, delivered, opened, rejected atomic.Int64
	runS8(start, done, frames, func(i int) {
		if out.Write(context.Background(), []byte{byte(i), 0xa5}) == nil {
			accepted.Add(1)
		}
	})
	runS8(start, done, frames, func(_ int) {
		if _, err := in.Read(context.Background()); err == nil {
			delivered.Add(1)
		}
	})
	for range 2 {
		runS8(start, done, attempts, func(_ int) {
			if h, err := r.Open("virtual:exclusive"); err == nil {
				opened.Add(1)
				_ = h.Close()
			} else if errors.Is(err, audio.ErrDeviceInUse) {
				rejected.Add(1)
			}
		})
	}
	close(start)
	for range 4 {
		<-done
	}
	require.Equal(t, int64(frames), accepted.Load())
	require.Equal(t, int64(frames), delivered.Load())
	require.Equal(t, int64(attempts*2), opened.Load()+rejected.Load())
	require.NoError(t, in.Close())
	require.NoError(t, out.Close())
	got := r.Observations()
	require.Equal(t, int(opened.Load())+2, got.OpenCount)
	require.Equal(t, got.OpenCount, got.ReleaseCount)
}
func runS8(start <-chan struct{}, done chan<- struct{}, n int, fn func(int)) {
	go func() {
		<-start
		for i := range n {
			fn(i)
		}
		done <- struct{}{}
	}()
}
func pendingReadCloses(t *testing.T, in *audio.VirtualStream) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ready, result := make(chan struct{}, 1), make(chan error, 1)
	go func() { _, err := in.Read(readyContext{Context: ctx, ready: ready}); result <- err }()
	<-ready
	require.NoError(t, in.Close())
	require.NoError(t, in.Close())
	require.ErrorIs(t, <-result, audio.ErrClosed)
}
func registry(c audio.VirtualBackendConfig) *audio.VirtualRegistry {
	r, _ := audio.NewVirtualRegistry(c)
	return r
}
func openPair() (*audio.VirtualRegistry, *audio.VirtualStream, *audio.VirtualStream) {
	r := registry(audio.DefaultVirtualBackendConfig())
	out, _ := r.Open("virtual:output")
	in, _ := r.Open("virtual:input")
	return r, out.(*audio.VirtualStream), in.(*audio.VirtualStream)
}

type readyContext struct {
	context.Context
	ready chan<- struct{}
}

func (c readyContext) Done() <-chan struct{} { c.ready <- struct{}{}; return c.Context.Done() }
