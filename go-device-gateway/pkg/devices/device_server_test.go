package devices_test

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestRemoteDeviceServerRoundTripUsesExplicitCallbackClock(t *testing.T) {
	nearEnd := make([]int16, audio.FrameSize*2)
	for index := range nearEnd {
		nearEnd[index] = int16(index + 1) //nolint:gosec // bounded deterministic fixture
	}
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Seed:    37,
		Render:  devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatalf("new simulated registry: %v", err)
	}
	server, err := devicegw.NewDeviceServer(registry)
	if err != nil {
		t.Fatalf("new device server: %v", err)
	}
	defer func() { _ = server.Close() }()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	endpoint := strings.TrimPrefix(httpServer.URL, "http://")

	remote, err := devicegw.NewRemoteDeviceRegistry(endpoint)
	if err != nil {
		t.Fatalf("new remote registry: %v", err)
	}
	devices, err := remote.List()
	if err != nil || len(devices) != 2 {
		t.Fatalf("remote devices = %+v, err=%v", devices, err)
	}
	input, err := remote.Default(devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("remote input default: %v", err)
	}
	output, err := remote.Default(devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("remote output default: %v", err)
	}
	source, err := devicegw.NewDeviceSourceAtRate(remote, input.ID, 16000)
	if err != nil {
		t.Fatalf("open remote source: %v", err)
	}
	defer func() { _ = source.Close() }()
	sink, err := devicegw.NewDeviceSinkAtRate(remote, output.ID, 16000)
	if err != nil {
		t.Fatalf("open remote sink: %v", err)
	}
	defer func() { _ = sink.Close() }()

	want := make([]int16, audio.FrameSize)
	for index := range want {
		want[index] = int16(1000 + index) //nolint:gosec // bounded deterministic fixture
	}
	if err := sink.WriteFrame(context.Background(), want); err != nil {
		t.Fatalf("write remote playback: %v", err)
	}
	if got := sink.PlaybackStats().QueuedSamples; got != len(want) {
		t.Fatalf("queued samples before callback = %d, want %d", got, len(want))
	}
	if err := devicegw.InjectRemoteDeviceServerCapture(context.Background(), endpoint, nearEnd); err != nil {
		t.Fatalf("inject remote microphone PCM: %v", err)
	}
	if err := devicegw.AdvanceRemoteDeviceServer(context.Background(), endpoint, 1); err != nil {
		t.Fatalf("advance remote callback clock: %v", err)
	}
	gotCapture := make([]int16, audio.FrameSize)
	if err := source.ReadFrame(context.Background(), gotCapture); err != nil {
		t.Fatalf("read remote capture: %v", err)
	}
	wantCapture := make([]int16, audio.FrameSize)
	for index := range wantCapture {
		wantCapture[index] = want[index] + nearEnd[index]
	}
	if !reflect.DeepEqual(gotCapture, wantCapture) {
		t.Fatal("remote capture samples differ from callback-owned acoustic plus near-end PCM")
	}
	if stats := source.CaptureStats(); stats.CompletedFrames != 1 || stats.DroppedSamples != 0 {
		t.Fatalf("remote capture stats = %+v", stats)
	}
	tail := []int16{7, 11, 13, 17, 19}
	if err := sink.WriteSamples(context.Background(), tail); err != nil {
		t.Fatalf("write arbitrary remote playback samples: %v", err)
	}
	if err := devicegw.AdvanceRemoteDeviceServer(context.Background(), endpoint, 1); err != nil {
		t.Fatalf("advance remote callback for arbitrary samples: %v", err)
	}
	if err := sink.WaitForPlayback(context.Background()); err != nil {
		t.Fatalf("wait for remote playback: %v", err)
	}
	snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("read remote snapshot: %v", err)
	}
	wantRendered := append(append([]int16(nil), want...), tail...)
	if len(snapshot.RenderedSamples) != audio.FrameSize*2 || !reflect.DeepEqual(snapshot.RenderedSamples[:len(wantRendered)], wantRendered) {
		t.Fatalf("rendered device PCM prefix differs from callback consumption: got_len=%d want_prefix_len=%d", len(snapshot.RenderedSamples), len(wantRendered))
	}
	for index, sample := range snapshot.RenderedSamples[len(wantRendered):] {
		if sample != 0 {
			t.Fatalf("rendered underflow sample %d = %d, want zero", index, sample)
		}
	}
	if snapshot.Playback.QueuedSamples != 0 || snapshot.Playback.DroppedSamples != 0 || snapshot.Playback.CallbackCount != 2 {
		t.Fatalf("remote playback evidence = %+v", snapshot.Playback)
	}
	opened, err := remote.Open(output.ID)
	if err != nil {
		t.Fatalf("open remote default-format device: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("close remote default-format device: %v", err)
	}
	if _, err := remote.Default(devicegw.Direction("sideways")); err == nil {
		t.Fatal("invalid remote default direction succeeded")
	}
}

func TestRemoteDeviceServerBackpressureUnblocksOnAdvanceAndDiscard(t *testing.T) {
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:  devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := devicegw.NewDeviceServer(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	endpoint := strings.TrimPrefix(httpServer.URL, "http://")
	remote, err := devicegw.NewRemoteDeviceRegistry(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := devicegw.NewDeviceSinkAtRate(remote, "simulated-duplex:output", 16000)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()

	frame := make([]int16, audio.FrameSize)
	for sink.PlaybackStats().QueuedSamples < sink.PlaybackStats().CapacitySamples-audio.FrameSize {
		if err := sink.WriteFrame(context.Background(), frame); err != nil {
			t.Fatalf("fill remote playback queue: %v", err)
		}
	}
	waiting := make(chan error, 1)
	go func() { waiting <- sink.WaitForPlaybackCapacity(context.Background(), audio.FrameSize) }()
	select {
	case err := <-waiting:
		t.Fatalf("capacity wait returned before callback progress: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := devicegw.AdvanceRemoteDeviceServer(context.Background(), endpoint, 3); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if err != nil {
			t.Fatalf("capacity wait after callback: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote callback did not release capacity waiter")
	}
	if discarded := sink.DiscardPlayback(); discarded <= 0 {
		t.Fatalf("remote discard removed %d samples, want positive queued backlog", discarded)
	}
	if got := sink.PlaybackStats().QueuedSamples; got != 0 {
		t.Fatalf("remote queue after discard = %d, want zero", got)
	}
}

func TestRemoteDeviceServerRejectsNonLoopbackAndMalformedEndpoints(t *testing.T) {
	for _, endpoint := range []string{"", "http://127.0.0.1:1234", "0.0.0.0:1234", "192.0.2.4:1234", "localhost", "localhost:bad port"} {
		if _, err := devicegw.NewRemoteDeviceRegistry(endpoint); !errors.Is(err, devicegw.ErrRemoteDeviceServerEndpoint) {
			t.Errorf("NewRemoteDeviceRegistry(%q) error = %v, want ErrRemoteDeviceServerEndpoint", endpoint, err)
		}
	}
}

func TestRemoteDeviceServerPreservesTypedFormatErrors(t *testing.T) {
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:  devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := devicegw.NewDeviceServer(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	remote, err := devicegw.NewRemoteDeviceRegistry(strings.TrimPrefix(httpServer.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = remote.OpenWithFormat("simulated-duplex:output", audio.PCM16DeviceFormat(24000))
	var formatErr *devicegw.DeviceFormatError
	if !errors.As(err, &formatErr) || formatErr.Requested.SampleRate != 24000 || len(formatErr.Available) != 1 || formatErr.Available[0].SampleRate != 16000 {
		t.Fatalf("remote format error = %#v, want typed 24 kHz to 16 kHz mismatch", err)
	}
}

func TestRemoteDeviceServerRejectsAmbiguousAndOversizedRequests(t *testing.T) {
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:  devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := devicegw.NewDeviceServer(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	tests := []struct {
		name   string
		method string
		path   string
		body   []byte
		status int
	}{
		{name: "wrong method", method: http.MethodPost, path: "/v1/audio-device/devices", status: http.StatusMethodNotAllowed},
		{name: "trailing JSON", method: http.MethodPost, path: "/v1/audio-device/control/advance", body: []byte(`{"callbacks":1} {}`), status: http.StatusBadRequest},
		{name: "unknown JSON field", method: http.MethodPost, path: "/v1/audio-device/control/advance", body: []byte(`{"callbacks":1,"surprise":true}`), status: http.StatusBadRequest},
		{name: "oversized PCM", method: http.MethodPost, path: "/v1/audio-device/control/inject-capture", body: bytes.Repeat([]byte{0}, (8<<20)+2), status: http.StatusBadRequest},
		{name: "odd PCM", method: http.MethodPost, path: "/v1/audio-device/control/inject-capture", body: []byte{1}, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.method, httpServer.URL+test.path, bytes.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
		})
	}
	if got := registry.CaptureStats().QueuedSamples; got != 0 {
		t.Fatalf("rejected capture requests queued %d samples", got)
	}
}

func TestRemoteDeviceServerHTTPContractRejectsInvalidHandleOperations(t *testing.T) {
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:  devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := devicegw.NewDeviceServer(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	base := httpServer.URL + "/v1/audio-device"

	openHandle := func(t *testing.T, deviceID string) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{"device_id": deviceID, "format": audio.PCM16DeviceFormat(16000)})
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(base+"/open", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("open %s status=%d body=%q", deviceID, response.StatusCode, data)
		}
		var result struct {
			HandleID string `json:"handle_id"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.HandleID == "" {
			t.Fatalf("decode open handle: id=%q err=%v", result.HandleID, err)
		}
		return result.HandleID
	}
	inputHandle := openHandle(t, "simulated-duplex:input")
	outputHandle := openHandle(t, "simulated-duplex:output")

	format, _ := json.Marshal(audio.PCM16DeviceFormat(16000))
	tests := []struct {
		name   string
		method string
		path   string
		body   []byte
		status int
	}{
		{name: "invalid default direction", method: http.MethodGet, path: "/default?direction=sideways", status: http.StatusBadRequest},
		{name: "malformed open", method: http.MethodPost, path: "/open", body: []byte(`{`), status: http.StatusBadRequest},
		{name: "unknown open device", method: http.MethodPost, path: "/open", body: []byte(`{"device_id":"simulated-duplex:missing","format":` + string(format) + `}`), status: http.StatusNotFound},
		{name: "invalid open format", method: http.MethodPost, path: "/open", body: []byte(`{"device_id":"simulated-duplex:output","format":{}}`), status: http.StatusBadRequest},
		{name: "bad handle path", method: http.MethodGet, path: "/handles/", status: http.StatusNotFound},
		{name: "unknown handle", method: http.MethodGet, path: "/handles/missing/playback-stats", status: http.StatusNotFound},
		{name: "unknown operation", method: http.MethodGet, path: "/handles/" + outputHandle + "/mystery", status: http.StatusNotFound},
		{name: "wrong read size", method: http.MethodPost, path: "/handles/" + inputHandle + "/read?samples=1", status: http.StatusBadRequest},
		{name: "empty write", method: http.MethodPost, path: "/handles/" + outputHandle + "/write", status: http.StatusBadRequest},
		{name: "invalid capacity", method: http.MethodPost, path: "/handles/" + outputHandle + "/capacity?samples=0", status: http.StatusBadRequest},
		{name: "malformed advance", method: http.MethodPost, path: "/control/advance", body: []byte(`{}`), status: http.StatusBadRequest},
		{name: "empty injection", method: http.MethodPost, path: "/control/inject-capture", status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.method, base+test.path, bytes.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				data, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d, want %d; body=%q", response.StatusCode, test.status, data)
			}
		})
	}
}

func TestRemoteDeviceHandleReportsServerSideClose(t *testing.T) {
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:  devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: 16000, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := devicegw.NewDeviceServer(registry)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	remote, err := devicegw.NewRemoteDeviceRegistry(strings.TrimPrefix(httpServer.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := devicegw.NewDeviceSourceAtRate(remote, "simulated-duplex:input", 16000)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.ReadFrame(context.Background(), make([]int16, audio.FrameSize)); err == nil || !strings.Contains(err.Error(), "closed or unknown") {
		t.Fatalf("read after server-side close error = %v", err)
	}
	if err := source.Close(); err == nil || !strings.Contains(err.Error(), "closed or unknown") {
		t.Fatalf("client close after server-side close error = %v", err)
	}
}
