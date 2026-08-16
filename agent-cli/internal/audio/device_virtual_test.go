package audio_test

import (
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

func TestVirtualBackendRegisteredAndIsolated(t *testing.T) {
	catalog := audio.NewProductionAudioBackendRegistry()
	if got := catalog.Names(); !reflect.DeepEqual(got, []string{audio.VirtualBackendName}) {
		t.Fatalf("registered backends=%v", got)
	}
	a, err := catalog.New(audio.VirtualBackendName, audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, err := catalog.New(audio.VirtualBackendName, audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	va, vb := a.(*audio.VirtualRegistry), b.(*audio.VirtualRegistry)
	devices, err := va.List()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(devices))
	for i, d := range devices {
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
		ids[i] = d.ID
	}
	if len(ids) != 3 || !sort.StringsAreSorted(ids) {
		t.Fatalf("IDs=%v", ids)
	}
	if len(va.ListVirtualDevices()[0].Capabilities) == 0 {
		t.Fatal("virtual device has no capabilities")
	}
	caps, _ := va.Capabilities(ids[0])
	caps[0].SampleRate = 1
	again, _ := va.Capabilities(ids[0])
	if again[0].SampleRate == 1 {
		t.Fatal("capabilities alias backend state")
	}
	if !va.RemoveDevice("virtual:input") {
		t.Fatal("remove input")
	}
	if got, _ := vb.List(); len(got) != 3 {
		t.Fatalf("isolated list length=%d", len(got))
	}
}

func TestVirtualBackendRunsSharedConformance(t *testing.T) {
	audio.RunDeviceRegistryConformance(t, func() audio.DeviceRegistryConformanceFixture {
		r, err := audio.NewRegisteredAudioBackend(audio.VirtualBackendName, audio.DefaultVirtualBackendConfig())
		if err != nil {
			t.Fatal(err)
		}
		v := r.(*audio.VirtualRegistry)
		return audio.DeviceRegistryConformanceFixture{Registry: r, InputDefault: "virtual:input", OutputDefault: "virtual:output", ExclusiveID: "virtual:exclusive", RemoveDevice: func(id audio.DeviceID) { v.RemoveDevice(id) }, Observations: v.Observations}
	})
}

func TestVirtualLoopbackCloseIsLinearizable(t *testing.T) {
	r := newPair(t)
	out, in := open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	frames := [][]byte{{1, 2}, {7, 8, 9, 10}, {42}}
	wants := make([][]byte, len(frames))
	for i, frame := range frames {
		wants[i] = append([]byte(nil), frame...)
		if err := out.Write(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
		frame[0] = 255
	}
	for i, want := range wants {
		got, err := in.Read(context.Background())
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("frame %d=(%v,%v), want %v", i, got, err, want)
		}
	}
	ready, result := make(chan struct{}, 1), make(chan error, 1)
	go func() { _, err := in.Read(readyContext{Context: context.Background(), ready: ready}); result <- err }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("read did not become pending")
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
			t.Fatalf("pending read=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock read")
	}
	if err := out.Write(context.Background(), []byte{9}); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("write after peer close=%v", err)
	}
	_ = out.Close()

	r = newPair(t)
	out, in = open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	writeResult := make(chan error, 1)
	go func() { writeResult <- out.Write(context.Background(), []byte{3}) }()
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-writeResult; err != nil && !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("racing write=%v", err)
	}
	if err := out.Write(context.Background(), []byte{4}); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("post-close write=%v", err)
	}
	_ = in.Close()
}

func TestVirtualFaultsAndValidation(t *testing.T) {
	dups, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "a", Name: "Same", Direction: audio.DirectionInput}, {ID: "b", Name: "Same", Direction: audio.DirectionInput}}, Defaults: map[audio.Direction]string{}})
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := dups.List()
	amb := audio.NewAmbiguousDeviceNameError("Same", listed)
	if !errors.Is(amb, audio.ErrAmbiguousDeviceName) || len(amb.Candidates) != 2 {
		t.Fatalf("ambiguous=%v", amb)
	}
	r := newPair(t)
	out, in := open(t, r, "virtual:playback"), open(t, r, "virtual:capture")
	if !r.RemoveDevice("virtual:playback") {
		t.Fatal("remove playback")
	}
	if _, err := r.Open("virtual:playback"); !errors.Is(err, audio.ErrDeviceNotFound) {
		t.Fatalf("open disappeared=%v", err)
	}
	if err := out.Write(context.Background(), []byte{1}); !errors.Is(err, audio.ErrDeviceLost) {
		t.Fatalf("lost output=%v", err)
	}
	if _, err := in.Read(context.Background()); !errors.Is(err, audio.ErrDeviceLost) {
		t.Fatalf("lost input=%v", err)
	}
	_ = out.Close()
	_ = in.Close()
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

func TestVirtualS8ConcurrentAccounting(t *testing.T) {
	r, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	out, in := open(t, r, "virtual:output"), open(t, r, "virtual:input")
	const frames, attempts = 32, 24
	start := make(chan struct{})
	results := make(chan error, 2)
	var accepted, delivered, opened, rejected atomic.Int64
	go func() {
		<-start
		for i := 0; i < frames; i++ {
			if err := out.Write(context.Background(), []byte{byte(i), 0xa5}); err != nil {
				results <- err
				return
			}
			accepted.Add(1)
		}
		results <- nil
	}()
	go func() {
		<-start
		for i := 0; i < frames; i++ {
			frame, err := in.Read(context.Background())
			if err != nil {
				results <- err
				return
			}
			if !reflect.DeepEqual(frame, []byte{byte(i), 0xa5}) {
				results <- errors.New("loopback order changed")
				return
			}
			delivered.Add(1)
		}
		results <- nil
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	for worker := 0; worker < 2; worker++ {
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < attempts; i++ {
				h, err := r.Open("virtual:exclusive")
				if err == nil {
					opened.Add(1)
					_ = h.Close()
					_ = h.Close()
				} else if errors.Is(err, audio.ErrDeviceInUse) {
					rejected.Add(1)
				} else {
					t.Errorf("exclusive open: %v", err)
				}
			}
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("S8 worker did not finish")
		}
	}
	wg.Wait()
	if accepted.Load() != frames || delivered.Load() != frames || opened.Load()+rejected.Load() != attempts*2 {
		t.Fatalf("accounting accepted=%d delivered=%d opened=%d rejected=%d", accepted.Load(), delivered.Load(), opened.Load(), rejected.Load())
	}
	_ = in.Close()
	_ = out.Close()
	got := r.Observations()
	if got.OpenCount != int(opened.Load())+2 || got.ReleaseCount != got.OpenCount {
		t.Fatalf("ownership observations=%+v", got)
	}
}

func invalid(t *testing.T, c audio.VirtualBackendConfig) {
	t.Helper()
	if _, err := audio.NewVirtualRegistry(c); !errors.Is(err, audio.ErrInvalidDevice) {
		t.Fatalf("invalid config=%v", err)
	}
}
func newPair(t *testing.T) *audio.VirtualRegistry {
	t.Helper()
	c := audio.VirtualBackendConfig{Devices: []audio.VirtualDeviceConfig{{ID: "capture", Name: "Capture", Direction: audio.DirectionInput, LoopbackID: "playback"}, {ID: "playback", Name: "Playback", Direction: audio.DirectionOutput, LoopbackID: "capture"}}, Defaults: map[audio.Direction]string{audio.DirectionInput: "capture", audio.DirectionOutput: "playback"}}
	r, err := audio.NewVirtualRegistry(c)
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
		t.Fatalf("%T is not VirtualStream", h)
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
