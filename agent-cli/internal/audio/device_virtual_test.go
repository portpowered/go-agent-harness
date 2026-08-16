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
