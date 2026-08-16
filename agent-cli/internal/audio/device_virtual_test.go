package audio_test

import (
	"bytes"
	"context"
	"errors"
	audio "github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"testing"
	"time"
)

func TestVirtualRegistryConformance(t *testing.T) {
	audio.RunDeviceRegistryConformance(t, func() audio.DeviceRegistryConformanceFixture {
		r := registry(audio.DefaultVirtualBackendConfig())
		return audio.DeviceRegistryConformanceFixture{Registry: r, InputDefault: "virtual:input", OutputDefault: "virtual:output", ExclusiveID: "virtual:exclusive", RemoveDevice: func(id audio.DeviceID) { r.RemoveDevice(id) }, Observations: r.Observations}
	})
}
func TestVirtualLoopbackLifecycle(t *testing.T) {
	r := pair()
	out, in := open(r, "virtual:playback"), open(r, "virtual:capture")
	framesWant := [][]byte{{1, 2}, {7, 8, 9}, {42}}
	for _, frame := range framesWant {
		copyOfWrite := append([]byte(nil), frame...)
		want(out.Write(context.Background(), copyOfWrite))
		copyOfWrite[0] = 255
	}
	for _, frame := range framesWant {
		got, err := in.Read(context.Background())
		fatal(t, err != nil || !bytes.Equal(got, frame), "got=%v err=%v want=%v", got, err, frame)
	}
	pendingReadCloses(t, in)
	want(out.Write(context.Background(), []byte{9}), audio.ErrClosed)
	want(out.Close())
	r = pair()
	out, in = open(r, "virtual:playback"), open(r, "virtual:capture")
	written := make(chan error, 1)
	go func() { written <- out.Write(context.Background(), []byte{3}) }()
	want(out.Close())
	err := <-written
	fatal(t, err != nil && !errors.Is(err, audio.ErrClosed), "%v", err)
	want(out.Write(context.Background(), []byte{4}), audio.ErrClosed)
	want(in.Close())
}
func TestVirtualFaults(t *testing.T) {
	dups := registry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "Same", Direction: audio.DirectionInput}, {ID: "b", Name: "Same", Direction: audio.DirectionInput}}})
	list, _ := dups.List()
	amb := audio.NewAmbiguousDeviceNameError("Same", list)
	fatal(t, !errors.Is(amb, audio.ErrAmbiguousDeviceName) || len(amb.Candidates) != 2 || amb.Candidates[0].ID != "virtual:a" || amb.Candidates[1].ID != "virtual:b", "%v", amb)
	for _, d := range []audio.Direction{audio.DirectionInput, audio.DirectionOutput} {
		c := audio.DefaultVirtualBackendConfig()
		delete(c.Defaults, d)
		r := registry(c)
		var e *audio.NoDefaultDeviceError
		_, err := r.Default(d)
		fatal(t, !errors.As(err, &e) || e.Direction != d, "%v", err)
	}
	r := pair()
	out, in := open(r, "virtual:playback"), open(r, "virtual:capture")
	fatal(t, !r.RemoveDevice("virtual:playback"), "remove failed")
	_, err := r.Open("virtual:playback")
	want(err, audio.ErrDeviceNotFound)
	_, err = in.Read(context.Background())
	want(err, audio.ErrDeviceLost)
	err = out.Write(context.Background(), []byte{1})
	var lost *audio.DeviceLostError
	fatal(t, !errors.As(err, &lost) || lost.ID != "virtual:playback", "%v", err)
	want(in.Close())
	want(out.Close())
	bad := audio.DefaultVirtualBackendConfig()
	bad.Devices[1].ID = bad.Devices[0].ID
	invalid(bad)
}

type s8Result struct {
	accepted, delivered, opened, rejected int
	err                                   error
}

func TestVirtualS8Accounting(t *testing.T) {
	r := registry(audio.DefaultVirtualBackendConfig())
	out, in := open(r, "virtual:output"), open(r, "virtual:input")
	const frames, attempts = 24, 20
	start, results := make(chan struct{}), make(chan s8Result, 4)
	go writeWorker(start, results, out, frames)
	go readWorker(start, results, in, frames)
	go exclusiveWorker(start, results, r, attempts)
	go exclusiveWorker(start, results, r, attempts)
	close(start)
	total, timeout := s8Result{}, time.After(time.Second)
	for range 4 {
		select {
		case x := <-results:
			fatal(t, x.err != nil, "%v", x.err)
			total.accepted += x.accepted
			total.delivered += x.delivered
			total.opened += x.opened
			total.rejected += x.rejected
		case <-timeout:
			t.Fatal("S8 workers did not finish")
		}
	}
	fatal(t, total.accepted != frames || total.delivered != frames || total.opened+total.rejected != attempts*2, "S8 totals=%+v", total)
	want(in.Close())
	want(out.Close())
	got := r.Observations()
	fatal(t, got.OpenCount != total.opened+2 || got.ReleaseCount != got.OpenCount, "observations=%+v", got)
}
func writeWorker(start <-chan struct{}, results chan<- s8Result, out *audio.VirtualStream, n int) {
	<-start
	x := s8Result{}
	for i := 0; i < n; i++ {
		x.err = out.Write(context.Background(), []byte{byte(i), 0xa5})
		if x.err != nil {
			break
		}
		x.accepted++
	}
	results <- x
}
func readWorker(start <-chan struct{}, results chan<- s8Result, in *audio.VirtualStream, n int) {
	<-start
	x := s8Result{}
	for range n {
		_, x.err = in.Read(context.Background())
		if x.err != nil {
			break
		}
		x.delivered++
	}
	results <- x
}
func exclusiveWorker(start <-chan struct{}, results chan<- s8Result, r *audio.VirtualRegistry, n int) {
	<-start
	x := s8Result{}
	for range n {
		h, err := r.Open("virtual:exclusive")
		if err == nil {
			x.opened++
			_ = h.Close()
		} else if errors.Is(err, audio.ErrDeviceInUse) {
			x.rejected++
		} else {
			x.err = err
			break
		}
	}
	results <- x
}
func pendingReadCloses(t *testing.T, in *audio.VirtualStream) {
	ready, result := make(chan struct{}, 1), make(chan error, 1)
	go func() { _, err := in.Read(readyContext{Context: context.Background(), ready: ready}); result <- err }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("read did not block")
	}
	want(in.Close())
	want(in.Close())
	select {
	case err := <-result:
		want(err, audio.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("close did not unblock read")
	}
}
func invalid(c audio.VirtualBackendConfig) {
	_, err := audio.NewVirtualRegistry(c)
	want(err, audio.ErrInvalidDevice)
}
func want(err error, target ...error) {
	if len(target) == 0 {
		if err != nil {
			panic(err)
		}
		return
	}
	if !errors.Is(err, target[0]) {
		panic(err)
	}
}
func fatal(t *testing.T, bad bool, format string, args ...any) {
	if bad {
		t.Fatalf(format, args...)
	}
}
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
func registry(c audio.VirtualBackendConfig) *audio.VirtualRegistry {
	return must(audio.NewVirtualRegistry(c))
}
func pair() *audio.VirtualRegistry {
	return registry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "capture", Name: "Capture", Direction: audio.DirectionInput, LoopbackID: "playback"}, {ID: "playback", Name: "Playback", Direction: audio.DirectionOutput, LoopbackID: "capture"}}, Defaults: map[audio.Direction]string{audio.DirectionInput: "capture", audio.DirectionOutput: "playback"}})
}
func open(r audio.DeviceRegistry, id audio.DeviceID) *audio.VirtualStream {
	return must(r.Open(id)).(*audio.VirtualStream)
}

type readyContext struct {
	context.Context
	ready chan<- struct{}
}

func (c readyContext) Done() <-chan struct{} {
	c.ready <- struct{}{}
	return c.Context.Done()
}
