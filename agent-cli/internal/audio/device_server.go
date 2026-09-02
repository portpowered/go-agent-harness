package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const deviceServerAPIPrefix = "/v1/audio-device"

const (
	deviceServerJSONLimit = 1 << 20
	deviceServerPCMLimit  = 8 << 20
)

var ErrRemoteDeviceServerEndpoint = errors.New("invalid remote audio-device server endpoint")

type remoteOpenRequest struct {
	DeviceID DeviceID     `json:"device_id"`
	Format   DeviceFormat `json:"format"`
}

type remoteOpenResponse struct {
	HandleID string       `json:"handle_id"`
	Device   Device       `json:"device"`
	Format   DeviceFormat `json:"format"`
}

type remoteAdvanceRequest struct {
	Callbacks int `json:"callbacks"`
}

type remoteErrorResponse struct {
	Error     string         `json:"error"`
	Kind      string         `json:"kind,omitempty"`
	DeviceID  DeviceID       `json:"device_id,omitempty"`
	Direction Direction      `json:"direction,omitempty"`
	Requested DeviceFormat   `json:"requested,omitempty"`
	Available []DeviceFormat `json:"available,omitempty"`
}

// DeviceServerSnapshot is the process-boundary evidence exposed by the
// deterministic device server. RenderedSamples are samples consumed by the
// simulated callback clock, not merely written or queued by the client.
type DeviceServerSnapshot struct {
	Playback        PlaybackQueueStats `json:"playback"`
	Capture         CaptureQueueStats  `json:"capture"`
	RenderedSamples []int16            `json:"rendered_samples"`
	CapturedSamples []int16            `json:"captured_samples"`
	Trace           []DeviceTraceEvent `json:"trace"`
}

// DeviceServer exposes a DeviceRegistry over a loopback HTTP connection. It
// is deliberately transport-only: all queueing, callback clocks, formats and
// statistics remain owned by the wrapped audio backend.
type DeviceServer struct {
	registry DeviceRegistry

	mu         sync.Mutex
	handles    map[string]OpenedDevice
	devices    map[DeviceID]Device
	nextHandle uint64
}

func NewDeviceServer(registry DeviceRegistry) (*DeviceServer, error) {
	if nilInterface(registry) {
		return nil, ErrNilDeviceRegistry
	}
	devices, err := registry.List()
	if err != nil {
		return nil, fmt.Errorf("list device-server registry: %w", err)
	}
	indexed := make(map[DeviceID]Device, len(devices))
	for _, device := range devices {
		if err := device.Validate(); err != nil {
			return nil, fmt.Errorf("validate device-server device %q: %w", device.ID, err)
		}
		indexed[device.ID] = device
	}
	return &DeviceServer{registry: registry, handles: make(map[string]OpenedDevice), devices: indexed}, nil
}

func (s *DeviceServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(deviceServerAPIPrefix+"/devices", s.handleDevices)
	mux.HandleFunc(deviceServerAPIPrefix+"/default", s.handleDefault)
	mux.HandleFunc(deviceServerAPIPrefix+"/open", s.handleOpen)
	mux.HandleFunc(deviceServerAPIPrefix+"/handles/", s.handleDevice)
	mux.HandleFunc(deviceServerAPIPrefix+"/control/advance", s.handleAdvance)
	mux.HandleFunc(deviceServerAPIPrefix+"/control/inject-capture", s.handleInjectCapture)
	mux.HandleFunc(deviceServerAPIPrefix+"/control/snapshot", s.handleSnapshot)
	return mux
}

func (s *DeviceServer) Close() error {
	s.mu.Lock()
	handles := make([]OpenedDevice, 0, len(s.handles))
	for id, handle := range s.handles {
		handles = append(handles, handle)
		delete(s.handles, id)
	}
	s.mu.Unlock()
	var result error
	for _, handle := range handles {
		result = errors.Join(result, handle.Close())
	}
	return result
}

func (s *DeviceServer) handleDevices(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	devices, err := s.registry.List()
	writeDeviceServerJSON(w, devices, err)
}

func (s *DeviceServer) handleDefault(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	direction := Direction(r.URL.Query().Get("direction"))
	if err := ValidateDirection(direction); err != nil {
		writeDeviceServerError(w, http.StatusBadRequest, err)
		return
	}
	device, err := s.registry.Default(direction)
	writeDeviceServerJSON(w, device, err)
}

func (s *DeviceServer) handleOpen(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var request remoteOpenRequest
	if err := decodeDeviceServerJSON(r.Body, &request); err != nil {
		writeDeviceServerError(w, http.StatusBadRequest, err)
		return
	}
	device, ok := s.devices[request.DeviceID]
	if !ok {
		writeDeviceServerError(w, http.StatusNotFound, NewDeviceNotFoundError(request.DeviceID))
		return
	}
	if err := request.Format.Validate(); err != nil {
		writeDeviceServerError(w, http.StatusBadRequest, err)
		return
	}
	var (
		handle OpenedDevice
		err    error
	)
	if opener, ok := s.registry.(DeviceFormatOpener); ok {
		handle, err = opener.OpenWithFormat(request.DeviceID, request.Format)
	} else if request.Format.equal(DefaultDeviceFormat()) {
		handle, err = s.registry.Open(request.DeviceID)
	} else {
		err = &DeviceFormatError{ID: request.DeviceID, Direction: device.Direction, Requested: request.Format, Available: defaultDeviceFormatAvailability()}
	}
	if err != nil {
		writeDeviceServerError(w, http.StatusConflict, err)
		return
	}
	if nilInterface(handle) {
		writeDeviceServerError(w, http.StatusInternalServerError, ErrNilOpenedDevice)
		return
	}
	s.mu.Lock()
	s.nextHandle++
	handleID := strconv.FormatUint(s.nextHandle, 10)
	s.handles[handleID] = handle
	s.mu.Unlock()
	writeDeviceServerJSON(w, remoteOpenResponse{HandleID: handleID, Device: device, Format: request.Format}, nil)
}

func (s *DeviceServer) handleDevice(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, deviceServerAPIPrefix+"/handles/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeDeviceServerError(w, http.StatusNotFound, errors.New("unknown device-server handle operation"))
		return
	}
	handleID, operation := parts[0], parts[1]
	s.mu.Lock()
	handle := s.handles[handleID]
	s.mu.Unlock()
	if handle == nil {
		writeDeviceServerError(w, http.StatusNotFound, errors.New("audio-device handle is closed or unknown"))
		return
	}
	s.handleDeviceOperation(w, r, handleID, operation, handle)
}

func (s *DeviceServer) handleDeviceOperation(w http.ResponseWriter, r *http.Request, handleID, operation string, handle OpenedDevice) {
	switch operation {
	case "read":
		s.handleRead(w, r, handle)
	case "write":
		s.handleWrite(w, r, handle)
	case "wait":
		s.handleWait(w, r, handle)
	case "capacity":
		s.handleCapacity(w, r, handle)
	case "playback-stats":
		s.handlePlaybackStats(w, r, handle)
	case "capture-stats":
		s.handleCaptureStats(w, r, handle)
	case "discard":
		s.handleDiscard(w, r, handle)
	case "close":
		if !requireMethod(w, r, http.MethodDelete) {
			return
		}
		s.mu.Lock()
		delete(s.handles, handleID)
		s.mu.Unlock()
		writeDeviceServerJSON(w, struct{}{}, handle.Close())
	default:
		writeDeviceServerError(w, http.StatusNotFound, errors.New("unknown device-server handle operation"))
	}
}

func (s *DeviceServer) handleRead(w http.ResponseWriter, r *http.Request, handle OpenedDevice) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	count, err := strconv.Atoi(r.URL.Query().Get("samples"))
	if err != nil || count != FrameSize {
		writeDeviceServerError(w, http.StatusBadRequest, &FrameSizeError{Operation: "remote device read", Got: count, Want: FrameSize})
		return
	}
	reader, ok := handle.(deviceFrameReader)
	if !ok {
		writeDeviceServerError(w, http.StatusBadRequest, ErrDeviceCapabilityMismatch)
		return
	}
	samples := make([]int16, count)
	if err := reader.ReadFrame(r.Context(), samples); err != nil {
		writeDeviceServerError(w, http.StatusConflict, err)
		return
	}
	data := make([]byte, len(samples)*2)
	encodePCM16(data, samples)
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func (s *DeviceServer) handleWrite(w http.ResponseWriter, r *http.Request, handle OpenedDevice) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	data, err := readDeviceServerPCM16(r.Body)
	if err != nil {
		writeDeviceServerError(w, http.StatusBadRequest, fmt.Errorf("remote device write: %w", err))
		return
	}
	samples := make([]int16, len(data)/2)
	decodePCM16(samples, data)
	if len(samples) == FrameSize {
		writer, ok := handle.(deviceFrameWriter)
		if !ok {
			writeDeviceServerError(w, http.StatusBadRequest, ErrDeviceCapabilityMismatch)
			return
		}
		err = writer.WriteFrame(r.Context(), samples)
	} else {
		writer, ok := handle.(deviceSampleWriter)
		if !ok {
			writeDeviceServerError(w, http.StatusBadRequest, ErrDeviceCapabilityMismatch)
			return
		}
		err = writer.WriteSamples(r.Context(), samples)
	}
	writeDeviceServerJSON(w, struct{}{}, err)
}

func (s *DeviceServer) handleWait(w http.ResponseWriter, r *http.Request, handle OpenedDevice) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	waiter, ok := handle.(devicePlaybackWaiter)
	if !ok {
		writeDeviceServerJSON(w, struct{}{}, nil)
		return
	}
	writeDeviceServerJSON(w, struct{}{}, waiter.WaitForPlayback(r.Context()))
}

func (s *DeviceServer) handleCapacity(w http.ResponseWriter, r *http.Request, handle OpenedDevice) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	samples, err := strconv.Atoi(r.URL.Query().Get("samples"))
	if err != nil || samples <= 0 {
		writeDeviceServerError(w, http.StatusBadRequest, errors.New("capacity samples must be positive"))
		return
	}
	waiter, ok := handle.(devicePlaybackCapacityWaiter)
	if !ok {
		writeDeviceServerJSON(w, struct{}{}, nil)
		return
	}
	writeDeviceServerJSON(w, struct{}{}, waiter.WaitForPlaybackCapacity(r.Context(), samples))
}

func (s *DeviceServer) handlePlaybackStats(w http.ResponseWriter, r *http.Request, handle OpenedDevice) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	provider, ok := handle.(PlaybackStatsProvider)
	if !ok {
		writeDeviceServerError(w, http.StatusBadRequest, errors.New("device does not expose playback statistics"))
		return
	}
	writeDeviceServerJSON(w, provider.PlaybackStats(), nil)
}

func (s *DeviceServer) handleCaptureStats(w http.ResponseWriter, r *http.Request, handle OpenedDevice) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	provider, ok := handle.(CaptureStatsProvider)
	if !ok {
		writeDeviceServerError(w, http.StatusBadRequest, errors.New("device does not expose capture statistics"))
		return
	}
	writeDeviceServerJSON(w, provider.CaptureStats(), nil)
}

func (s *DeviceServer) handleDiscard(w http.ResponseWriter, r *http.Request, handle OpenedDevice) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	discarder, ok := handle.(PlaybackDiscarder)
	if !ok {
		writeDeviceServerError(w, http.StatusBadRequest, errors.New("device does not expose playback discard"))
		return
	}
	writeDeviceServerJSON(w, map[string]int{"discarded_samples": discarder.DiscardPlayback()}, nil)
}

func (s *DeviceServer) handleAdvance(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	controller, ok := s.registry.(interface{ Advance(int) error })
	if !ok {
		writeDeviceServerError(w, http.StatusBadRequest, errors.New("device registry has no explicit callback clock"))
		return
	}
	var request remoteAdvanceRequest
	if err := decodeDeviceServerJSON(r.Body, &request); err != nil || request.Callbacks <= 0 {
		writeDeviceServerError(w, http.StatusBadRequest, errors.New("advance callbacks must be positive"))
		return
	}
	writeDeviceServerJSON(w, struct{}{}, controller.Advance(request.Callbacks))
}

func (s *DeviceServer) handleInjectCapture(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	injector, ok := s.registry.(interface{ InjectNearEnd([]int16) })
	if !ok {
		writeDeviceServerError(w, http.StatusBadRequest, errors.New("device registry does not accept injected capture PCM"))
		return
	}
	data, err := readDeviceServerPCM16(r.Body)
	if err != nil {
		writeDeviceServerError(w, http.StatusBadRequest, fmt.Errorf("capture injection: %w", err))
		return
	}
	samples := make([]int16, len(data)/2)
	decodePCM16(samples, data)
	injector.InjectNearEnd(samples)
	writeDeviceServerJSON(w, map[string]int{"injected_samples": len(samples)}, nil)
}

func (s *DeviceServer) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	registry, ok := s.registry.(interface {
		PlaybackStats() PlaybackQueueStats
		CaptureStats() CaptureQueueStats
		RenderedSamples() []int16
		CapturedSamples() []int16
		Trace() []DeviceTraceEvent
	})
	if !ok {
		writeDeviceServerError(w, http.StatusBadRequest, errors.New("device registry does not expose deterministic evidence"))
		return
	}
	writeDeviceServerJSON(w, DeviceServerSnapshot{
		Playback: registry.PlaybackStats(), Capture: registry.CaptureStats(),
		RenderedSamples: registry.RenderedSamples(), CapturedSamples: registry.CapturedSamples(), Trace: registry.Trace(),
	}, nil)
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeDeviceServerError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s is not allowed", r.Method))
	return false
}

func decodeDeviceServerJSON(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, deviceServerJSONLimit+1))
	if err != nil {
		return fmt.Errorf("read audio-device server request: %w", err)
	}
	if len(data) > deviceServerJSONLimit {
		return fmt.Errorf("audio-device server request exceeds %d bytes", deviceServerJSONLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode audio-device server request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decode audio-device server request: trailing JSON value")
	}
	return nil
}

func readDeviceServerPCM16(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, deviceServerPCMLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > deviceServerPCMLimit {
		return nil, fmt.Errorf("PCM payload exceeds %d bytes", deviceServerPCMLimit)
	}
	if len(data) == 0 || len(data)%2 != 0 {
		return nil, errors.New("PCM16 payload must contain a non-empty even number of bytes")
	}
	return data, nil
}

func writeDeviceServerJSON(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeDeviceServerError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if encodeErr := json.NewEncoder(w).Encode(value); encodeErr != nil {
		return
	}
}

func writeDeviceServerError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	payload := remoteErrorResponse{Error: err.Error()}
	var formatErr *DeviceFormatError
	if errors.As(err, &formatErr) {
		payload.Kind = "device_format"
		payload.DeviceID = formatErr.ID
		payload.Direction = formatErr.Direction
		payload.Requested = formatErr.Requested
		payload.Available = append([]DeviceFormat(nil), formatErr.Available...)
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// RemoteDeviceRegistry connects the production audio adapters to a
// loopback-only DeviceServer. It intentionally implements the same optional
// format capability as native registries, so all ordinary input/output flags
// continue to work unchanged.
type RemoteDeviceRegistry struct {
	baseURL string
	client  *http.Client
}

func NewRemoteDeviceRegistry(endpoint string) (*RemoteDeviceRegistry, error) {
	baseURL, err := validateRemoteDeviceServerEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	return &RemoteDeviceRegistry{baseURL: baseURL, client: &http.Client{}}, nil
}

func validateRemoteDeviceServerEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || strings.Contains(endpoint, "://") {
		return "", fmt.Errorf("%w: want loopback host:port", ErrRemoteDeviceServerEndpoint)
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || port == "" {
		return "", fmt.Errorf("%w: want loopback host:port, got %q", ErrRemoteDeviceServerEndpoint, endpoint)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber <= 0 || portNumber > 65535 {
		return "", fmt.Errorf("%w: port %q is invalid", ErrRemoteDeviceServerEndpoint, port)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("%w: host %q is not loopback", ErrRemoteDeviceServerEndpoint, host)
		}
	}
	return "http://" + endpoint + deviceServerAPIPrefix, nil
}

func (r *RemoteDeviceRegistry) List() ([]Device, error) {
	var devices []Device
	if err := r.doMetadata(http.MethodGet, "/devices", nil, &devices); err != nil {
		return nil, err
	}
	for _, device := range devices {
		if err := device.Validate(); err != nil {
			return nil, fmt.Errorf("remote device %q: %w", device.ID, err)
		}
	}
	return devices, nil
}

func (r *RemoteDeviceRegistry) Default(direction Direction) (Device, error) {
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	var device Device
	err := r.doMetadata(http.MethodGet, "/default?direction="+url.QueryEscape(string(direction)), nil, &device)
	return device, err
}

func (r *RemoteDeviceRegistry) Open(id DeviceID) (OpenedDevice, error) {
	return r.OpenWithFormat(id, DefaultDeviceFormat())
}

func (r *RemoteDeviceRegistry) OpenWithFormat(id DeviceID, format DeviceFormat) (OpenedDevice, error) {
	if err := format.Validate(); err != nil {
		return nil, err
	}
	var response remoteOpenResponse
	if err := r.doMetadata(http.MethodPost, "/open", remoteOpenRequest{DeviceID: id, Format: format}, &response); err != nil {
		return nil, err
	}
	if response.HandleID == "" || response.Device.ID != id || !response.Device.Direction.IsValid() || !response.Format.equal(format) {
		return nil, errors.New("remote audio-device server returned an invalid open response")
	}
	return &remoteOpenedDevice{registry: r, id: response.HandleID, direction: response.Device.Direction, format: response.Format}, nil
}

func (r *RemoteDeviceRegistry) doMetadata(method, path string, request, response any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var body io.Reader
	if request != nil {
		data, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, body)
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return r.do(req, response)
}

func (r *RemoteDeviceRegistry) do(req *http.Request, response any) error {
	result, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("audio-device server %s: %w", req.URL.Host, err)
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		var payload remoteErrorResponse
		_ = json.NewDecoder(io.LimitReader(result.Body, 1<<20)).Decode(&payload)
		if payload.Error == "" {
			payload.Error = result.Status
		}
		if payload.Kind == "device_format" {
			return &DeviceFormatError{
				ID: payload.DeviceID, Direction: payload.Direction, Requested: payload.Requested,
				Available: payload.Available, Err: errors.New(payload.Error),
			}
		}
		return fmt.Errorf("audio-device server %s: %s", req.URL.Host, payload.Error)
	}
	if response == nil {
		_, _ = io.Copy(io.Discard, result.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(result.Body, 16<<20)).Decode(response); err != nil {
		return fmt.Errorf("decode audio-device server response: %w", err)
	}
	return nil
}

type remoteOpenedDevice struct {
	registry  *RemoteDeviceRegistry
	id        string
	direction Direction
	format    DeviceFormat
	closeOnce sync.Once
	closeErr  error
}

func (d *remoteOpenedDevice) DeviceDirection() Direction { return d.direction }
func (d *remoteOpenedDevice) DeviceFormat() DeviceFormat { return d.format }

func (d *remoteOpenedDevice) ReadFrame(ctx context.Context, frame []int16) error {
	if err := validateFrame("read", frame); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, d.path("read")+"?samples="+strconv.Itoa(len(frame)), nil)
	if err != nil {
		return err
	}
	result, err := d.registry.client.Do(req)
	if err != nil {
		return err
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return decodeRemoteDeviceError(result)
	}
	data, err := io.ReadAll(io.LimitReader(result.Body, int64(len(frame)*2+1)))
	if err != nil {
		return err
	}
	if len(data) != len(frame)*2 {
		return &FrameSizeError{Operation: "remote device read", Got: len(data) / 2, Want: len(frame)}
	}
	decodePCM16(frame, data)
	return nil
}

func (d *remoteOpenedDevice) WriteFrame(ctx context.Context, frame []int16) error {
	if err := validateFrame("write", frame); err != nil {
		return err
	}
	return d.writeSamples(ctx, frame)
}

func (d *remoteOpenedDevice) WriteSamples(ctx context.Context, samples []int16) error {
	if len(samples) == 0 {
		return nil
	}
	return d.writeSamples(ctx, samples)
}

func (d *remoteOpenedDevice) writeSamples(ctx context.Context, samples []int16) error {
	data := make([]byte, len(samples)*2)
	encodePCM16(data, samples)
	req, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, d.path("write"), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	return d.registry.do(req, nil)
}

func (d *remoteOpenedDevice) WaitForPlayback(ctx context.Context) error {
	return d.postContext(ctx, "wait", "")
}

func (d *remoteOpenedDevice) WaitForPlaybackCapacity(ctx context.Context, samples int) error {
	return d.postContext(ctx, "capacity", "?samples="+strconv.Itoa(samples))
}

func (d *remoteOpenedDevice) PlaybackStats() PlaybackQueueStats {
	var stats PlaybackQueueStats
	if err := d.registry.doMetadata(http.MethodGet, strings.TrimPrefix(d.path("playback-stats"), d.registry.baseURL), nil, &stats); err != nil {
		return emptyPlaybackQueueStats(d.format)
	}
	return stats
}

func (d *remoteOpenedDevice) CaptureStats() CaptureQueueStats {
	var stats CaptureQueueStats
	if err := d.registry.doMetadata(http.MethodGet, strings.TrimPrefix(d.path("capture-stats"), d.registry.baseURL), nil, &stats); err != nil {
		return CaptureQueueStats{}
	}
	return stats
}

func (d *remoteOpenedDevice) DiscardPlayback() int {
	var response map[string]int
	if err := d.registry.doMetadata(http.MethodPost, strings.TrimPrefix(d.path("discard"), d.registry.baseURL), struct{}{}, &response); err != nil {
		return 0
	}
	return response["discarded_samples"]
}

func (d *remoteOpenedDevice) Close() error {
	d.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, d.path("close"), nil)
		if err != nil {
			d.closeErr = err
			return
		}
		d.closeErr = d.registry.do(req, nil)
	})
	return d.closeErr
}

func (d *remoteOpenedDevice) postContext(ctx context.Context, operation, query string) error {
	req, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, d.path(operation)+query, nil)
	if err != nil {
		return err
	}
	return d.registry.do(req, nil)
}

func (d *remoteOpenedDevice) path(operation string) string {
	return d.registry.baseURL + "/handles/" + url.PathEscape(d.id) + "/" + operation
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func decodeRemoteDeviceError(response *http.Response) error {
	var payload struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
	if payload.Error == "" {
		payload.Error = response.Status
	}
	return errors.New(payload.Error)
}

// AdvanceRemoteDeviceServer advances an explicitly-clocked server. It is a
// harness operation, not part of DeviceRegistry, and therefore cannot affect
// native device implementations.
func AdvanceRemoteDeviceServer(ctx context.Context, endpoint string, callbacks int) error {
	registry, err := NewRemoteDeviceRegistry(endpoint)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(remoteAdvanceRequest{Callbacks: callbacks})
	req, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, registry.baseURL+"/control/advance", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return registry.do(req, nil)
}

func ReadRemoteDeviceServerSnapshot(ctx context.Context, endpoint string) (DeviceServerSnapshot, error) {
	registry, err := NewRemoteDeviceRegistry(endpoint)
	if err != nil {
		return DeviceServerSnapshot{}, err
	}
	req, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodGet, registry.baseURL+"/control/snapshot", nil)
	if err != nil {
		return DeviceServerSnapshot{}, err
	}
	var snapshot DeviceServerSnapshot
	err = registry.do(req, &snapshot)
	return snapshot, err
}

// InjectRemoteDeviceServerCapture appends microphone PCM to a deterministic
// server. The samples become visible only when the harness advances capture
// callbacks, preserving an explicit device clock.
func InjectRemoteDeviceServerCapture(ctx context.Context, endpoint string, samples []int16) error {
	if len(samples) == 0 {
		return nil
	}
	registry, err := NewRemoteDeviceRegistry(endpoint)
	if err != nil {
		return err
	}
	data := make([]byte, len(samples)*2)
	encodePCM16(data, samples)
	req, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, registry.baseURL+"/control/inject-capture", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	return registry.do(req, nil)
}
