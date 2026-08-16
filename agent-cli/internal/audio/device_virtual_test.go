package audio_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

func TestVirtualRegistryRegistrationAndIsolation(t *testing.T) {
	catalog := audio.NewProductionAudioBackendRegistry()
	if got := catalog.Names(); !reflect.DeepEqual(got, []string{"virtual"}) {
		t.Fatal(got)
	}
	a, err := catalog.New("virtual", audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, err := catalog.New("virtual", audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	va, vb := a.(*audio.VirtualRegistry), b.(*audio.VirtualRegistry)
	list, err := va.List()
	if err != nil || len(list) != 3 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	ids := make([]string, len(list))
	for i, d := range list {
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
		ids[i] = d.ID
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal(ids)
	}
	caps, err := va.Capabilities(ids[0])
	if err != nil || len(caps) == 0 {
		t.Fatalf("caps=%v err=%v", caps, err)
	}
	caps[0].SampleRate = 1
	again, _ := va.Capabilities(ids[0])
	if again[0].SampleRate == 1 {
		t.Fatal("capability alias")
	}
	if !va.RemoveDevice("virtual:input") {
		t.Fatal("remove")
	}
	other, _ := vb.List()
	if len(other) != 3 {
		t.Fatal("registry state leaked")
	}
}

func TestVirtualRegistryConformance(t *testing.T) {
	audio.RunDeviceRegistryConformance(t, func() audio.DeviceRegistryConformanceFixture {
		r, err := audio.NewProductionAudioBackendRegistry().New("virtual", audio.DefaultVirtualBackendConfig())
		if err != nil {
			t.Fatal(err)
		}
		v := r.(*audio.VirtualRegistry)
		return audio.DeviceRegistryConformanceFixture{Registry: r, InputDefault: "virtual:input", OutputDefault: "virtual:output", ExclusiveID: "virtual:exclusive", RemoveDevice: func(id string) { v.RemoveDevice(id) }, Observations: v.Observations}
	})
}

func TestVirtualLoopbackLifecycle(t *testing.T) {
	r := pair(t)
	out, in := open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	want := [][]byte{{1, 2}, {7, 8, 9}, {42}}
	for _, expected := range want {
		frame := append([]byte(nil), expected...)
		if err := out.Write(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
		frame[0] = 255
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
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, audio.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock read")
	}
	if err := out.Write(context.Background(), []byte{9}); !errors.Is(err, audio.ErrClosed) {
		t.Fatal(err)
	}
	_ = out.Close()
	r = pair(t)
	out, in = open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	written := make(chan error, 1)
	go func() { written <- out.Write(context.Background(), []byte{3}) }()
	_ = out.Close()
	if err := <-written; err != nil && !errors.Is(err, audio.ErrClosed) {
		t.Fatal(err)
	}
	if err := out.Write(context.Background(), []byte{4}); !errors.Is(err, audio.ErrClosed) {
		t.Fatal(err)
	}
	_ = in.Close()
}

func TestVirtualFaultsAndValidation(t *testing.T) {
	dups, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "Same", Direction: audio.DirectionInput}, {ID: "b", Name: "Same", Direction: audio.DirectionInput}}, Defaults: map[audio.Direction]string{}})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := dups.List()
	amb := audio.NewAmbiguousDeviceNameError("Same", list)
	if !errors.Is(amb, audio.ErrAmbiguousDeviceName) || len(amb.Candidates) != 2 {
		t.Fatal(amb)
	}
	noInput := audio.DefaultVirtualBackendConfig()
	noInput.Defaults = map[audio.Direction]string{audio.DirectionOutput: "output"}
	r, err := audio.NewVirtualRegistry(noInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Default(audio.DirectionInput)
	var noDefault *audio.NoDefaultDeviceError
	if !errors.As(err, &noDefault) || noDefault.Direction != audio.DirectionInput {
		t.Fatal(err)
	}
	r = pair(t)
	out, in := open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	if _, err := r.Capabilities("virtual:missing"); !errors.Is(err, audio.ErrDeviceNotFound) {
		t.Fatal(err)
	}
	if err := in.Write(context.Background(), []byte{1}); !errors.Is(err, audio.ErrInvalidDevice) {
		t.Fatal(err)
	}
	if err := out.Write(context.Background(), nil); !errors.Is(err, audio.ErrVirtualEmptyFrame) {
		t.Fatal(err)
	}
	if !r.RemoveDevice("virtual:playback") {
		t.Fatal("remove")
	}
	if r.RemoveDevice("virtual:playback") {
		t.Fatal("removed device twice")
	}
	if _, err := r.Open("virtual:playback"); !errors.Is(err, audio.ErrDeviceNotFound) {
		t.Fatal(err)
	}
	if _, err := in.Read(context.Background()); !errors.Is(err, audio.ErrDeviceLost) {
		t.Fatal(err)
	}
	err = out.Write(context.Background(), []byte{1})
	var lost *audio.DeviceLostError
	if !errors.As(err, &lost) || !errors.Is(err, audio.ErrDeviceLost) {
		t.Fatal(err)
	}
	if lost.Error() == "" {
		t.Fatal("lost error has no context")
	}
	_ = in.Close()
	_ = out.Close()
	noLoopback, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "solo", Name: "Solo", Direction: audio.DirectionOutput}}})
	if err != nil {
		t.Fatal(err)
	}
	solo := open(t, noLoopback, "virtual:solo")
	if err := solo.Write(context.Background(), []byte{1}); !errors.Is(err, audio.ErrVirtualNoLoopback) {
		t.Fatal(err)
	}
	_ = solo.Close()
	bad := audio.DefaultVirtualBackendConfig()
	bad.Devices[1].ID = bad.Devices[0].ID
	invalid(t, bad)
	bad = audio.DefaultVirtualBackendConfig()
	bad.Defaults = map[audio.Direction]string{audio.DirectionInput: "output"}
	invalid(t, bad)
	bad = audio.DefaultVirtualBackendConfig()
	bad.Defaults = map[audio.Direction]string{audio.Direction("bad"): "input"}
	if _, err := audio.NewVirtualRegistry(bad); !errors.Is(err, audio.ErrInvalidDirection) {
		t.Fatalf("invalid default direction=%v", err)
	}
	bad = audio.DefaultVirtualBackendConfig()
	bad.Devices[0].Capabilities = []audio.VirtualCapability{{SampleRate: 8000}}
	bad.Devices[1].Capabilities = []audio.VirtualCapability{{SampleRate: 16000}}
	invalid(t, bad)
	bad = audio.DefaultVirtualBackendConfig()
	bad.Devices[0].LoopbackID = "virtual:"
	invalid(t, bad)
}

func TestVirtualS8Accounting(t *testing.T) {
	r, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	out, in := open(t, r, "virtual:output"), open(t, r, "virtual:input")
	const frames, attempts = 24, 20
	start := make(chan struct{})
	errs := make(chan error, 2)
	var accepted, delivered, opened, rejected atomic.Int64
	go func() {
		<-start
		for i := 0; i < frames; i++ {
			if err := out.Write(context.Background(), []byte{byte(i), 0xa5}); err != nil {
				errs <- err
				return
			}
			accepted.Add(1)
		}
		errs <- nil
	}()
	go func() {
		<-start
		for i := 0; i < frames; i++ {
			frame, err := in.Read(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(frame, []byte{byte(i), 0xa5}) {
				errs <- errors.New("loopback order changed")
				return
			}
			delivered.Add(1)
		}
		errs <- nil
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < attempts; j++ {
				h, err := r.Open("virtual:exclusive")
				if err == nil {
					opened.Add(1)
					_ = h.Close()
					_ = h.Close()
				} else if errors.Is(err, audio.ErrDeviceInUse) {
					rejected.Add(1)
				} else {
					errs <- err
					return
				}
			}
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("S8 open workers did not finish")
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if accepted.Load() != frames || delivered.Load() != frames || opened.Load()+rejected.Load() != attempts*2 {
		t.Fatalf("accepted=%d delivered=%d opened=%d rejected=%d", accepted.Load(), delivered.Load(), opened.Load(), rejected.Load())
	}
	_ = in.Close()
	_ = out.Close()
	got := r.Observations()
	if got.OpenCount != int(opened.Load())+2 || got.ReleaseCount != got.OpenCount {
		t.Fatalf("observations=%+v", got)
	}
}

func invalid(t *testing.T, c audio.VirtualBackendConfig) {
	t.Helper()
	if _, err := audio.NewVirtualRegistry(c); !errors.Is(err, audio.ErrInvalidDevice) {
		t.Fatalf("invalid config=%v", err)
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
