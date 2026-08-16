package audio_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

func TestVirtualRegistryConformance(t *testing.T) {
	audio.RunDeviceRegistryConformance(t, func() audio.DeviceRegistryConformanceFixture {
		r, err := audio.NewProductionAudioBackendRegistry().New("virtual", audio.DefaultVirtualBackendConfig())
		if err != nil {
			t.Fatal(err)
		}
		v := r.(*audio.VirtualRegistry)
		return audio.DeviceRegistryConformanceFixture{Registry: r, InputDefault: "virtual:input", OutputDefault: "virtual:output", ExclusiveID: "virtual:exclusive", RemoveDevice: func(id audio.DeviceID) { v.RemoveDevice(id) }, Observations: v.Observations}
	})
}
func TestVirtualLoopbackLifecycle(t *testing.T) {
	r := pair(t)
	out, in := open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	want := [][]byte{{1, 2}, {7, 8, 9}, {42}}
	for _, frame := range want {
		copyOfWrite := append([]byte(nil), frame...)
		if err := out.Write(context.Background(), copyOfWrite); err != nil {
			t.Fatal(err)
		}
		copyOfWrite[0] = 255
	}
	for _, frame := range want {
		got, err := in.Read(context.Background())
		if err != nil || !bytes.Equal(got, frame) {
			t.Fatalf("got=%v err=%v want=%v", got, err, frame)
		}
	}
	ready, result := make(chan struct{}, 1), make(chan error, 1)
	go func() { _, err := in.Read(readyContext{Context: context.Background(), ready: ready}); result <- err }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("read did not block")
	}
	assertErr(t, in.Close(), nil)
	assertErr(t, in.Close(), nil)
	select {
	case err := <-result:
		assertErr(t, err, audio.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("close did not unblock read")
	}
	assertErr(t, out.Write(context.Background(), []byte{9}), audio.ErrClosed)
	_ = out.Close()
	r = pair(t)
	out, in = open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	written := make(chan error, 1)
	go func() { written <- out.Write(context.Background(), []byte{3}) }()
	_ = out.Close()
	if err := <-written; err != nil && !errors.Is(err, audio.ErrClosed) {
		t.Fatal(err)
	}
	assertErr(t, out.Write(context.Background(), []byte{4}), audio.ErrClosed)
	_ = in.Close()
}
func TestVirtualFaults(t *testing.T) {
	dups, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "Same", Direction: audio.DirectionInput}, {ID: "b", Name: "Same", Direction: audio.DirectionInput}}})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := dups.List()
	amb := audio.NewAmbiguousDeviceNameError("Same", list)
	if !errors.Is(amb, audio.ErrAmbiguousDeviceName) || len(amb.Candidates) != 2 || amb.Candidates[0].ID != "virtual:a" {
		t.Fatal(amb)
	}
	c := audio.DefaultVirtualBackendConfig()
	c.Defaults = map[audio.Direction]string{audio.DirectionOutput: "output"}
	r, err := audio.NewVirtualRegistry(c)
	if err != nil {
		t.Fatal(err)
	}
	var noDefault *audio.NoDefaultDeviceError
	if _, err := r.Default(audio.DirectionInput); !errors.As(err, &noDefault) || noDefault.Direction != audio.DirectionInput {
		t.Fatal(err)
	}
	r = pair(t)
	out, in := open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	if !r.RemoveDevice("virtual:playback") || r.RemoveDevice("virtual:playback") {
		t.Fatal("remove state")
	}
	_, err = r.Open("virtual:playback")
	assertErr(t, err, audio.ErrDeviceNotFound)
	_, err = in.Read(context.Background())
	assertErr(t, err, audio.ErrDeviceLost)
	err = out.Write(context.Background(), []byte{1})
	var lost *audio.DeviceLostError
	if !errors.As(err, &lost) || lost.ID != "virtual:playback" {
		t.Fatal(err)
	}
	_ = in.Close()
	_ = out.Close()
	bad := audio.DefaultVirtualBackendConfig()
	bad.Devices[1].ID = bad.Devices[0].ID
	invalid(t, bad)
	bad = audio.DefaultVirtualBackendConfig()
	bad.Defaults = map[audio.Direction]string{audio.DirectionInput: "output"}
	invalid(t, bad)
	bad = audio.DefaultVirtualBackendConfig()
	bad.Devices[0].Capabilities = []audio.VirtualCapability{{SampleRate: 8000}}
	bad.Devices[1].Capabilities = []audio.VirtualCapability{{SampleRate: 16000}}
	invalid(t, bad)
}

type s8Result struct {
	accepted, delivered, opened, rejected int
	err                                   error
}

func TestVirtualS8Accounting(t *testing.T) {
	r, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	out, in := open(t, r, "virtual:output"), open(t, r, "virtual:input")
	const frames, attempts = 24, 20
	start, results := make(chan struct{}), make(chan s8Result, 4)
	go func() {
		<-start
		result := s8Result{}
		for i := 0; i < frames; i++ {
			if result.err = out.Write(context.Background(), []byte{byte(i), 0xa5}); result.err != nil {
				break
			}
			result.accepted++
		}
		results <- result
	}()
	go func() {
		<-start
		result := s8Result{}
		for range frames {
			_, result.err = in.Read(context.Background())
			if result.err != nil {
				break
			}
			result.delivered++
		}
		results <- result
	}()
	for range 2 {
		go func() {
			<-start
			result := s8Result{}
			for i := 0; i < attempts; i++ {
				h, err := r.Open("virtual:exclusive")
				if err == nil {
					result.opened++
					_ = h.Close()
				} else if errors.Is(err, audio.ErrDeviceInUse) {
					result.rejected++
				} else {
					result.err = err
					break
				}
			}
			results <- result
		}()
	}
	close(start)
	total, timeout := s8Result{}, time.After(time.Second)
	for i := 0; i < 4; i++ {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			total.accepted += result.accepted
			total.delivered += result.delivered
			total.opened += result.opened
			total.rejected += result.rejected
		case <-timeout:
			t.Fatal("S8 workers did not finish")
		}
	}
	if total.accepted != frames || total.delivered != frames || total.opened+total.rejected != attempts*2 {
		t.Fatalf("S8 totals=%+v", total)
	}
	_ = in.Close()
	_ = out.Close()
	got := r.Observations()
	if got.OpenCount != total.opened+2 || got.ReleaseCount != got.OpenCount {
		t.Fatalf("observations=%+v", got)
	}
}
func invalid(t *testing.T, c audio.VirtualBackendConfig) {
	t.Helper()
	if _, err := audio.NewVirtualRegistry(c); !errors.Is(err, audio.ErrInvalidDevice) {
		t.Fatalf("invalid config=%v", err)
	}
}
func assertErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err=%v want=%v", err, target)
	}
}
func pair(t *testing.T) *audio.VirtualRegistry {
	t.Helper()
	r, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "capture", Name: "Capture", Direction: audio.DirectionInput, LoopbackID: "playback"}, {ID: "playback", Name: "Playback", Direction: audio.DirectionOutput, LoopbackID: "capture"}}, Defaults: map[audio.Direction]string{audio.DirectionInput: "capture", audio.DirectionOutput: "playback"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func open(t *testing.T, r audio.DeviceRegistry, id audio.DeviceID) *audio.VirtualStream {
	t.Helper()
	h, err := r.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := h.(*audio.VirtualStream)
	if !ok {
		t.Fatalf("%T", h)
	}
	return s
}

type readyContext struct {
	context.Context
	ready chan<- struct{}
}

func (c readyContext) Done() <-chan struct{} {
	select {
	case c.ready <- struct{}{}:
	default:
	}
	return c.Context.Done()
}
