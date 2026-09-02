package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type deviceServerStubRegistry struct {
	devices    []Device
	listErr    error
	defaultErr error
	handle     OpenedDevice
	openErr    error
}

func (r *deviceServerStubRegistry) List() ([]Device, error) { return r.devices, r.listErr }

func (r *deviceServerStubRegistry) Default(direction Direction) (Device, error) {
	if r.defaultErr != nil {
		return Device{}, r.defaultErr
	}
	for _, device := range r.devices {
		if device.Direction == direction {
			return device, nil
		}
	}
	return Device{}, NewNoDefaultDeviceError(direction)
}

func (r *deviceServerStubRegistry) Open(DeviceID) (OpenedDevice, error) {
	return r.handle, r.openErr
}

type deviceServerCloseOnlyHandle struct{ err error }

func (h *deviceServerCloseOnlyHandle) Close() error { return h.err }

func TestDeviceServerDefensiveRegistryAndCapabilityBranches(t *testing.T) {
	if _, err := NewDeviceServer(nil); !errors.Is(err, ErrNilDeviceRegistry) {
		t.Fatalf("nil registry error = %v", err)
	}
	if _, err := NewDeviceServer(&deviceServerStubRegistry{listErr: errors.New("list failed")}); err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("list failure = %v", err)
	}
	if _, err := NewDeviceServer(&deviceServerStubRegistry{devices: []Device{{ID: "invalid"}}}); err == nil || !strings.Contains(err.Error(), "validate") {
		t.Fatalf("invalid enumerated device error = %v", err)
	}

	output, err := NewDevice("stub", "output", "Stub Output", DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	closeFailure := errors.New("close failed")
	registry := &deviceServerStubRegistry{devices: []Device{output}, handle: &deviceServerCloseOnlyHandle{err: closeFailure}}
	server, err := NewDeviceServer(registry)
	if err != nil {
		t.Fatal(err)
	}

	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(method, path, bytes.NewReader(body))
		server.Handler().ServeHTTP(recorder, httpRequest)
		return recorder
	}
	openBody, _ := json.Marshal(remoteOpenRequest{DeviceID: output.ID, Format: DefaultDeviceFormat()})
	opened := request(http.MethodPost, deviceServerAPIPrefix+"/open", openBody)
	if opened.Code != http.StatusOK {
		t.Fatalf("fallback open status = %d body=%q", opened.Code, opened.Body.String())
	}
	var response remoteOpenResponse
	if err := json.Unmarshal(opened.Body.Bytes(), &response); err != nil || response.HandleID == "" {
		t.Fatalf("fallback open response = %+v, err=%v", response, err)
	}
	handlePath := deviceServerAPIPrefix + "/handles/" + response.HandleID
	tests := []struct {
		name   string
		method string
		path   string
		body   []byte
		status int
	}{
		{name: "read capability", method: http.MethodPost, path: handlePath + "/read?samples=480", status: http.StatusBadRequest},
		{name: "write capability", method: http.MethodPost, path: handlePath + "/write", body: []byte{0, 0}, status: http.StatusBadRequest},
		{name: "wait fallback", method: http.MethodPost, path: handlePath + "/wait", status: http.StatusOK},
		{name: "capacity fallback", method: http.MethodPost, path: handlePath + "/capacity?samples=1", status: http.StatusOK},
		{name: "playback stats capability", method: http.MethodGet, path: handlePath + "/playback-stats", status: http.StatusBadRequest},
		{name: "capture stats capability", method: http.MethodGet, path: handlePath + "/capture-stats", status: http.StatusBadRequest},
		{name: "discard capability", method: http.MethodPost, path: handlePath + "/discard", status: http.StatusBadRequest},
		{name: "advance capability", method: http.MethodPost, path: deviceServerAPIPrefix + "/control/advance", body: []byte(`{"callbacks":1}`), status: http.StatusBadRequest},
		{name: "inject capability", method: http.MethodPost, path: deviceServerAPIPrefix + "/control/inject-capture", body: []byte{0, 0}, status: http.StatusBadRequest},
		{name: "snapshot capability", method: http.MethodGet, path: deviceServerAPIPrefix + "/control/snapshot", status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := request(test.method, test.path, test.body).Code; got != test.status {
				t.Fatalf("status = %d, want %d", got, test.status)
			}
		})
	}
	nondefault, _ := json.Marshal(remoteOpenRequest{DeviceID: output.ID, Format: PCM16DeviceFormat(24000)})
	if got := request(http.MethodPost, deviceServerAPIPrefix+"/open", nondefault).Code; got != http.StatusConflict {
		t.Fatalf("non-default fallback open status = %d", got)
	}
	if got := request(http.MethodDelete, handlePath+"/close", nil).Code; got != http.StatusInternalServerError {
		t.Fatalf("close failure status = %d", got)
	}

	registry.handle = nil
	if got := request(http.MethodPost, deviceServerAPIPrefix+"/open", openBody).Code; got != http.StatusInternalServerError {
		t.Fatalf("nil opened device status = %d", got)
	}
	registry.openErr = errors.New("open failed")
	if got := request(http.MethodPost, deviceServerAPIPrefix+"/open", openBody).Code; got != http.StatusConflict {
		t.Fatalf("registry open failure status = %d", got)
	}
}

func TestRemoteDeviceRegistryRejectsInvalidServerResponses(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case deviceServerAPIPrefix + "/devices":
			_ = json.NewEncoder(w).Encode([]Device{{ID: "invalid"}})
		case deviceServerAPIPrefix + "/open":
			_ = json.NewEncoder(w).Encode(remoteOpenResponse{HandleID: "", Format: DefaultDeviceFormat()})
		case deviceServerAPIPrefix + "/default":
			_, _ = w.Write([]byte("not-json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer httpServer.Close()
	remote, err := NewRemoteDeviceRegistry(strings.TrimPrefix(httpServer.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.List(); err == nil || !strings.Contains(err.Error(), "remote device") {
		t.Fatalf("invalid list response error = %v", err)
	}
	if _, err := remote.OpenWithFormat("stub:output", DeviceFormat{}); !errors.Is(err, ErrInvalidDeviceFormat) {
		t.Fatalf("invalid requested format error = %v", err)
	}
	if _, err := remote.Open("stub:output"); err == nil || !strings.Contains(err.Error(), "invalid open response") {
		t.Fatalf("invalid open response error = %v", err)
	}
	if _, err := remote.Default(DirectionOutput); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("invalid JSON response error = %v", err)
	}
	if err := AdvanceRemoteDeviceServer(context.Background(), "not-an-endpoint", 1); !errors.Is(err, ErrRemoteDeviceServerEndpoint) {
		t.Fatalf("invalid advance endpoint error = %v", err)
	}
	if _, err := ReadRemoteDeviceServerSnapshot(context.Background(), "not-an-endpoint"); !errors.Is(err, ErrRemoteDeviceServerEndpoint) {
		t.Fatalf("invalid snapshot endpoint error = %v", err)
	}
	if err := InjectRemoteDeviceServerCapture(context.Background(), "not-an-endpoint", []int16{1}); !errors.Is(err, ErrRemoteDeviceServerEndpoint) {
		t.Fatalf("invalid inject endpoint error = %v", err)
	}
	if err := InjectRemoteDeviceServerCapture(context.Background(), "not-an-endpoint", nil); err != nil {
		t.Fatalf("empty capture injection error = %v", err)
	}
}
