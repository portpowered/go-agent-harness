package audio_test

import (
	"bytes"
	"context"
	"errors"
	audio "github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

func TestVirtualConformance(t *testing.T) { audio.RunDeviceRegistryConformance(t, virtualFixture) }
func virtualFixture() audio.DeviceRegistryConformanceFixture {
	r := registry(audio.DefaultVirtualBackendConfig())
	return audio.DeviceRegistryConformanceFixture{Registry: r, InputDefault: "virtual:input", OutputDefault: "virtual:output", ExclusiveID: "virtual:exclusive", RemoveDevice: func(id audio.DeviceID) { r.RemoveDevice(id) }, Observations: r.Observations}
}
func TestVirtualLoopbackLifecycle(t *testing.T) {
	r := pair()
	out, in := open(r, "virtual:output"), open(r, "virtual:input")
	wantFrames := [][]byte{{1, 2}, {7, 8, 9}, {42}}
	for _, wantFrame := range wantFrames {
		write := append([]byte(nil), wantFrame...)
		require.NoError(t, out.Write(context.Background(), write))
		write[0] = 255
	}
	for _, wantFrame := range wantFrames {
		got, err := in.Read(context.Background())
		require.NoError(t, err)
		require.True(t, bytes.Equal(got, wantFrame), "got=%v want=%v", got, wantFrame)
	}
	pendingReadCloses(t, in)
	require.ErrorIs(t, out.Write(context.Background(), []byte{9}), audio.ErrClosed)
	require.NoError(t, out.Close())
	require.NoError(t, out.Close())
	r = pair()
	out, in = open(r, "virtual:output"), open(r, "virtual:input")
	result := make(chan error, 1)
	go func() { result <- out.Write(context.Background(), []byte{3}) }()
	require.NoError(t, out.Close())
	err := <-result
	require.True(t, err == nil || errors.Is(err, audio.ErrClosed), err)
	require.ErrorIs(t, out.Write(context.Background(), []byte{4}), audio.ErrClosed)
	require.NoError(t, in.Close())
}
func TestVirtualFaults(t *testing.T) {
	r := registry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "Same", Direction: audio.DirectionInput}, {ID: "b", Name: "Same", Direction: audio.DirectionInput}}})
	list, _ := r.List()
	amb := audio.NewAmbiguousDeviceNameError("Same", list)
	require.True(t, errors.Is(amb, audio.ErrAmbiguousDeviceName) && len(amb.Candidates) == 2 && amb.Candidates[0].ID == "virtual:a" && amb.Candidates[1].ID == "virtual:b", amb)
	bad := audio.DefaultVirtualBackendConfig()
	bad.Devices[1].ID = bad.Devices[0].ID
	_, err := audio.NewVirtualRegistry(bad)
	require.ErrorIs(t, err, audio.ErrInvalidDevice)
	r = pair()
	out, in := open(r, "virtual:output"), open(r, "virtual:input")
	require.True(t, r.RemoveDevice("virtual:output"))
	_, err = r.Open("virtual:output")
	require.ErrorIs(t, err, audio.ErrDeviceNotFound)
	_, err = in.Read(context.Background())
	require.ErrorIs(t, err, audio.ErrDeviceLost)
	err = out.Write(context.Background(), []byte{1})
	var lost *audio.DeviceLostError
	require.ErrorAs(t, err, &lost)
	require.Equal(t, audio.DeviceID("virtual:output"), lost.ID)
	require.NoError(t, in.Close())
	require.NoError(t, out.Close())
}
func TestVirtualS8Accounting(t *testing.T) {
	r := registry(audio.DefaultVirtualBackendConfig())
	out, in := open(r, "virtual:output"), open(r, "virtual:input")
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
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("S8 workers did not finish")
		}
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
	ready, result := make(chan struct{}, 1), make(chan error, 1)
	go func() { _, err := in.Read(readyContext{Context: context.Background(), ready: ready}); result <- err }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("read did not block")
	}
	require.NoError(t, in.Close())
	require.NoError(t, in.Close())
	select {
	case err := <-result:
		require.ErrorIs(t, err, audio.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("close did not unblock read")
	}
}
func registry(c audio.VirtualBackendConfig) *audio.VirtualRegistry {
	r, _ := audio.NewVirtualRegistry(c)
	return r
}
func pair() *audio.VirtualRegistry { return registry(audio.DefaultVirtualBackendConfig()) }
func open(r audio.DeviceRegistry, id audio.DeviceID) *audio.VirtualStream {
	h, _ := r.Open(id)
	return h.(*audio.VirtualStream)
}

type readyContext struct {
	context.Context
	ready chan<- struct{}
}

func (c readyContext) Done() <-chan struct{} { c.ready <- struct{}{}; return c.Context.Done() }
