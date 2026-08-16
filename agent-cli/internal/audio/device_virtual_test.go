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

func TestVirtualBackendIsRegisteredAndIsolated(t *testing.T) {
	catalog := audio.NewProductionAudioBackendRegistry()
	if got := catalog.Names(); !reflect.DeepEqual(got, []string{audio.VirtualBackendName}) {
		t.Fatalf("registered backends=%v, want virtual", got)
	}
	first, err := catalog.New(audio.VirtualBackendName, audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.New(audio.VirtualBackendName, audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	left, ok := first.(*audio.VirtualRegistry)
	if !ok {
		t.Fatalf("registered backend type=%T, want *VirtualRegistry", first)
	}
	right := second.(*audio.VirtualRegistry)
	devices, err := left.List()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(devices))
	for i, device := range devices {
		if err := device.Validate(); err != nil {
			t.Fatalf("device %#v: %v", device, err)
		}
		ids[i] = device.ID
	}
	if !sort.StringsAreSorted(ids) || len(ids) != 3 {
		t.Fatalf("listed IDs=%v, want three sorted virtual IDs", ids)
	}
	if caps := left.ListVirtualDevices()[0].Capabilities; len(caps) == 0 {
		t.Fatal("listed virtual device has no declared capabilities")
	}
	input := mustDevice(t, devices, audio.DirectionInput)
	if !left.RemoveDevice(input.ID) {
		t.Fatal("RemoveDevice returned false for listed input")
	}
	if got, _ := right.List(); len(got) != 3 {
		t.Fatalf("isolated registry list length=%d after first removal, want 3", len(got))
	}
}

func TestVirtualBackendRunsSharedConformance(t *testing.T) {
	audio.RunDeviceRegistryConformance(t, func() audio.DeviceRegistryConformanceFixture {
		registry, err := audio.NewRegisteredAudioBackend(audio.VirtualBackendName, audio.DefaultVirtualBackendConfig())
		if err != nil {
			t.Fatalf("create registered virtual backend: %v", err)
		}
		devices, err := registry.List()
		if err != nil {
			t.Fatalf("list fixture: %v", err)
		}
		return audio.DeviceRegistryConformanceFixture{
			Registry:      registry,
			InputDefault:  findDevice(t, devices, "virtual:input").ID,
			OutputDefault: findDevice(t, devices, "virtual:output").ID,
			ExclusiveID:   findDevice(t, devices, "virtual:exclusive").ID,
			RemoveDevice:  func(id audio.DeviceID) { registry.(*audio.VirtualRegistry).RemoveDevice(id) },
			Observations:  func() audio.DeviceRegistryObservations { return registry.(*audio.VirtualRegistry).Observations() },
		}
	})
}

func TestVirtualLoopbackPreservesBytesBoundariesAndLifecycle(t *testing.T) {
	registry := mustPairRegistry(t)
	input := mustDevice(t, mustList(t, registry), audio.DirectionInput)
	output := mustDevice(t, mustList(t, registry), audio.DirectionOutput)
	out := mustStream(t, registry, output.ID)
	in := mustStream(t, registry, input.ID)
	frames := [][]byte{{1, 2}, {7, 8, 9, 10}, {42}}
	wants := make([][]byte, len(frames))
	for i, frame := range frames {
		wants[i] = append([]byte(nil), frame...)
		if err := out.Write(context.Background(), frame); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
		frame[0] = 255
	}
	for i, want := range wants {
		got, err := in.Read(context.Background())
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("read frame %d=%v, want %v", i, got, want)
		}
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Write(context.Background(), []byte{1}); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("write after close=%v, want ErrClosed", err)
	}
	blocked := make(chan error, 1)
	go func() { _, err := in.Read(context.Background()); blocked <- err }()
	select {
	case err := <-blocked:
		if !errors.Is(err, audio.ErrClosed) {
			t.Fatalf("read after close=%v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closed read did not complete")
	}
}

func TestVirtualFaultsAndValidationAreTyped(t *testing.T) {
	noDefaults := audio.DefaultVirtualBackendConfig()
	noDefaults.Defaults = map[audio.Direction]string{}
	noDefaults.InputDefault, noDefaults.OutputDefault = "", ""
	registry, err := audio.NewVirtualRegistry(noDefaults)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Default(audio.DirectionInput); !errors.Is(err, audio.ErrNoDefaultDevice) {
		t.Fatalf("missing input default=%v, want ErrNoDefaultDevice", err)
	}

	devices := []audio.VirtualDeviceConfig{
		{ID: "same-a", Name: "Same", Direction: audio.DirectionInput},
		{ID: "same-b", Name: "Same", Direction: audio.DirectionInput},
	}
	duplicateNames, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{Devices: devices, Defaults: map[audio.Direction]string{}})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := duplicateNames.List()
	if err != nil || len(listed) != 2 {
		t.Fatalf("duplicate-name topology list=(%v, %d), want two devices", err, len(listed))
	}
	ambiguous := audio.NewAmbiguousDeviceNameError("same", listed)
	if !errors.Is(ambiguous, audio.ErrAmbiguousDeviceName) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous candidates=%#v, want both stable IDs", ambiguous)
	}

	paired := mustPairRegistry(t)
	input := mustDevice(t, mustList(t, paired), audio.DirectionInput)
	output := mustDevice(t, mustList(t, paired), audio.DirectionOutput)
	out := mustStream(t, paired, output.ID)
	in := mustStream(t, paired, input.ID)
	if !paired.RemoveDevice(output.ID) {
		t.Fatal("failed to remove output")
	}
	if _, err := paired.Open(output.ID); !errors.Is(err, audio.ErrDeviceNotFound) {
		t.Fatalf("open disappeared output=%v, want ErrDeviceNotFound", err)
	}
	if err := out.Write(context.Background(), []byte{1}); !errors.Is(err, audio.ErrDeviceLost) {
		t.Fatalf("write after disappearance=%v, want ErrDeviceLost", err)
	}
	if _, err := in.Read(context.Background()); !errors.Is(err, audio.ErrDeviceLost) {
		t.Fatalf("paired input after output disappearance=%v, want ErrDeviceLost", err)
	}
	_ = out.Close()
	_ = in.Close()

	exclusive := audio.DefaultVirtualBackendConfig()
	exclusive.Defaults = map[audio.Direction]string{}
	exclusive.InputDefault = ""
	exclusive.OutputDefault = ""
	exclusiveRegistry, err := audio.NewVirtualRegistry(exclusive)
	if err != nil {
		t.Fatal(err)
	}
	first := mustStream(t, exclusiveRegistry, "virtual:exclusive")
	if _, err := exclusiveRegistry.Open("virtual:exclusive"); !errors.Is(err, audio.ErrDeviceInUse) {
		t.Fatalf("second exclusive open=%v, want ErrDeviceInUse", err)
	}
	_ = first.Close()
	second := mustStream(t, exclusiveRegistry, "virtual:exclusive")
	_ = second.Close()

	bad := audio.DefaultVirtualBackendConfig()
	bad.Devices[1].ID = bad.Devices[0].ID
	if _, err := audio.NewVirtualRegistry(bad); !errors.Is(err, audio.ErrInvalidDevice) {
		t.Fatalf("duplicate ID config=%v, want ErrInvalidDevice", err)
	}
	bad = audio.DefaultVirtualBackendConfig()
	bad.Defaults = map[audio.Direction]string{audio.DirectionInput: "output"}
	if _, err := audio.NewVirtualRegistry(bad); !errors.Is(err, audio.ErrInvalidDevice) {
		t.Fatalf("wrong-direction default=%v, want ErrInvalidDevice", err)
	}
	bad = audio.DefaultVirtualBackendConfig()
	bad.Devices[0].Capabilities = []audio.VirtualCapability{{SampleRate: 8000}}
	bad.Devices[1].Capabilities = []audio.VirtualCapability{{SampleRate: 16000}}
	if _, err := audio.NewVirtualRegistry(bad); !errors.Is(err, audio.ErrInvalidDevice) {
		t.Fatalf("incompatible pair=%v, want ErrInvalidDevice", err)
	}
}

func TestVirtualConcurrentOpenCloseReadWrite(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	devices := mustList(t, registry)
	input := findDevice(t, devices, "virtual:input")
	output := findDevice(t, devices, "virtual:output")
	out := mustStream(t, registry, output.ID)
	in := mustStream(t, registry, input.ID)
	const frameCount = 64
	start := make(chan struct{})
	results := make(chan error, 2)
	var accepted atomic.Int64
	go func() {
		<-start
		for i := 0; i < frameCount; i++ {
			frame := []byte{byte(i), byte(i >> 8), 0xa5}
			if err := out.Write(context.Background(), frame); err != nil {
				results <- err
				return
			}
			accepted.Add(1)
		}
		results <- nil
	}()
	go func() {
		<-start
		for i := 0; i < frameCount; i++ {
			frame, err := in.Read(context.Background())
			if err != nil {
				results <- err
				return
			}
			want := []byte{byte(i), byte(i >> 8), 0xa5}
			if !reflect.DeepEqual(frame, want) {
				results <- errors.New("loopback order changed")
				return
			}
		}
		results <- nil
	}()
	const attempts = 48
	var openCount atomic.Int64
	var inUseCount atomic.Int64
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < attempts; i++ {
			opened, err := registry.Open("virtual:exclusive")
			if err == nil {
				openCount.Add(1)
				_ = opened.Close()
			} else if errors.Is(err, audio.ErrDeviceInUse) {
				inUseCount.Add(1)
			} else {
				t.Errorf("exclusive open %d: %v", i, err)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < attempts; i++ {
			opened, err := registry.Open("virtual:exclusive")
			if err == nil {
				openCount.Add(1)
				_ = opened.Close()
			} else if errors.Is(err, audio.ErrDeviceInUse) {
				inUseCount.Add(1)
			} else {
				t.Errorf("exclusive contender %d: %v", i, err)
			}
		}
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	workers.Wait()
	if accepted.Load() != frameCount {
		t.Fatalf("accepted writes=%d, want %d", accepted.Load(), frameCount)
	}
	if openCount.Load()+inUseCount.Load() != attempts*2 {
		t.Fatalf("exclusive accounting open=%d in-use=%d, want %d attempts", openCount.Load(), inUseCount.Load(), attempts*2)
	}
	_ = in.Close()
	_ = out.Close()
	if got := registry.Observations(); got.OpenCount != int(openCount.Load())+2 || got.ReleaseCount != got.OpenCount {
		t.Fatalf("observations=%+v, want successful opens plus pair handles and exact releases", got)
	}
}

func mustPairRegistry(t *testing.T) *audio.VirtualRegistry {
	t.Helper()
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "capture", Name: "Capture", Direction: audio.DirectionInput, Capabilities: []audio.VirtualCapability{{SampleRate: 16000, Channels: 1, BitDepth: 16}}, LoopbackID: "playback"},
			{ID: "playback", Name: "Playback", Direction: audio.DirectionOutput, Capabilities: []audio.VirtualCapability{{SampleRate: 16000, Channels: 1, BitDepth: 16}}, LoopbackID: "capture"},
		},
		Defaults: map[audio.Direction]string{audio.DirectionInput: "capture", audio.DirectionOutput: "playback"},
	})
	if err != nil {
		t.Fatalf("create pair registry: %v", err)
	}
	return registry
}

func mustList(t *testing.T, registry audio.DeviceRegistry) []audio.Device {
	t.Helper()
	devices, err := registry.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return devices
}

func mustDevice(t *testing.T, devices []audio.Device, direction audio.Direction) audio.Device {
	t.Helper()
	for _, device := range devices {
		if device.Direction == direction {
			return device
		}
	}
	t.Fatalf("no %s device in %#v", direction, devices)
	return audio.Device{}
}

func findDevice(t *testing.T, devices []audio.Device, id audio.DeviceID) audio.Device {
	t.Helper()
	for _, device := range devices {
		if device.ID == id {
			return device
		}
	}
	t.Fatalf("device %q not in %#v", id, devices)
	return audio.Device{}
}

func mustStream(t *testing.T, registry audio.DeviceRegistry, id audio.DeviceID) *audio.VirtualStream {
	t.Helper()
	opened, err := registry.Open(id)
	if err != nil {
		t.Fatalf("open %q: %v", id, err)
	}
	stream, ok := opened.(*audio.VirtualStream)
	if !ok {
		t.Fatalf("open %q type=%T, want *VirtualStream", id, opened)
	}
	return stream
}
