package audio

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const VirtualBackendName = "virtual"

var (
	ErrDeviceLost        = errors.New("device lost")
	ErrVirtualDeviceLost = ErrDeviceLost
	ErrVirtualNoLoopback = errors.New("virtual device has no loopback")
	ErrVirtualEmptyFrame = errors.New("virtual audio frame is empty")
)

// DeviceLostError identifies a device that disappeared after it was opened.
// Direction is retained so callers can report which half of a pair failed.
type DeviceLostError struct {
	ID        DeviceID
	Direction Direction
}

func (e *DeviceLostError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("device %q (%s) was lost", e.ID, e.Direction)
}

func (e *DeviceLostError) Unwrap() error { return ErrDeviceLost }

type VirtualDeviceLostError = DeviceLostError

// VirtualCapability describes one format a virtual endpoint accepts. The
// values are metadata only: the loopback moves the caller's bytes unchanged.
type VirtualCapability struct {
	SampleRate   int
	Channels     int
	BitDepth     int
	SampleFormat string
	Format       string
}

type VirtualAudioCapability = VirtualCapability
type VirtualAudioFormat = VirtualCapability

// VirtualDeviceConfig is the immutable input used to construct one endpoint.
// ID may be a native ID or a complete virtual:<native-id> ID; NativeID is an
// explicit spelling for callers that want to avoid that distinction.
type VirtualDeviceConfig struct {
	ID           string
	NativeID     string
	Name         string
	DisplayName  string
	Direction    Direction
	Capabilities []VirtualCapability
	Default      bool
	Exclusive    bool
	LoopbackID   string
	PairID       string
}

type VirtualDeviceSpec = VirtualDeviceConfig

// VirtualBackendConfig describes a complete isolated backend instance. A
// non-nil Defaults map is authoritative, including when it is empty. With a
// nil map, Default flags are used and otherwise the first endpoint per
// direction becomes the default for convenience.
type VirtualBackendConfig struct {
	Devices       []VirtualDeviceConfig
	Defaults      map[Direction]string
	InputDefault  string
	OutputDefault string
}

type VirtualConfig = VirtualBackendConfig

// VirtualDevice is the capability-bearing view of a listed Device.
type VirtualDevice struct {
	Device
	Capabilities []VirtualCapability
	Exclusive    bool
	LoopbackID   DeviceID
}

func (d VirtualDevice) CapabilitiesCopy() []VirtualCapability {
	return cloneVirtualCapabilities(d.Capabilities)
}

// DefaultVirtualBackendConfig is hardware-independent and suitable for the
// production registration path and registry conformance fixture.
func DefaultVirtualBackendConfig() VirtualBackendConfig {
	capability := VirtualCapability{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, SampleFormat: "pcm16", Format: "pcm16"}
	return VirtualBackendConfig{
		Devices: []VirtualDeviceConfig{
			{ID: "input", Name: "Virtual Input", Direction: DirectionInput, Capabilities: []VirtualCapability{capability}, Default: true, LoopbackID: "output"},
			{ID: "output", Name: "Virtual Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{capability}, Default: true, LoopbackID: "input"},
			{ID: "exclusive", Name: "Virtual Exclusive Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{capability}, Exclusive: true},
		},
	}
}

func normalizeVirtualCapability(cap VirtualCapability) (VirtualCapability, error) {
	if cap.SampleRate == 0 {
		cap.SampleRate = SampleRate
	}
	if cap.Channels == 0 {
		cap.Channels = Channels
	}
	if cap.BitDepth == 0 {
		cap.BitDepth = 16
	}
	if cap.SampleFormat == "" {
		cap.SampleFormat = cap.Format
	}
	if cap.SampleFormat == "" {
		cap.SampleFormat = "pcm16"
	}
	cap.Format = cap.SampleFormat
	if cap.SampleRate <= 0 || cap.Channels <= 0 || cap.BitDepth <= 0 || strings.TrimSpace(cap.SampleFormat) == "" {
		return VirtualCapability{}, fmt.Errorf("invalid virtual audio capability")
	}
	return cap, nil
}

func normalizeVirtualCapabilities(caps []VirtualCapability) ([]VirtualCapability, error) {
	if len(caps) == 0 {
		caps = []VirtualCapability{{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, SampleFormat: "pcm16", Format: "pcm16"}}
	}
	result := make([]VirtualCapability, len(caps))
	seen := make(map[VirtualCapability]struct{}, len(caps))
	for i, cap := range caps {
		cap, err := normalizeVirtualCapability(cap)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[cap]; exists {
			return nil, fmt.Errorf("duplicate virtual audio capability")
		}
		seen[cap] = struct{}{}
		result[i] = cap
	}
	return result, nil
}

func cloneVirtualCapabilities(caps []VirtualCapability) []VirtualCapability {
	result := make([]VirtualCapability, len(caps))
	copy(result, caps)
	return result
}

func virtualReference(value string) (DeviceID, error) {
	if strings.HasPrefix(value, VirtualBackendName+":") {
		backend, nativeID, err := ParseDeviceID(value)
		if err != nil || backend != VirtualBackendName {
			return "", &InvalidDeviceError{Reason: fmt.Sprintf("invalid virtual device reference %q", value)}
		}
		return NewDeviceID(VirtualBackendName, nativeID)
	}
	return NewDeviceID(VirtualBackendName, value)
}

func virtualConfigNativeID(spec VirtualDeviceConfig) (string, error) {
	nativeID := spec.NativeID
	if nativeID == "" {
		nativeID = spec.ID
	}
	if strings.HasPrefix(nativeID, VirtualBackendName+":") {
		_, parsedNativeID, err := ParseDeviceID(nativeID)
		if err != nil {
			return "", err
		}
		nativeID = parsedNativeID
	}
	if spec.ID != "" && spec.NativeID != "" && spec.ID != spec.NativeID && spec.ID != VirtualBackendName+":"+spec.NativeID {
		return "", &InvalidDeviceError{Reason: fmt.Sprintf("ID %q and NativeID %q disagree", spec.ID, spec.NativeID)}
	}
	return nativeID, nil
}

func virtualPairReference(spec VirtualDeviceConfig) string {
	if spec.LoopbackID != "" {
		return spec.LoopbackID
	}
	return spec.PairID
}

func virtualDisplayName(spec VirtualDeviceConfig) (string, error) {
	name := spec.DisplayName
	if name == "" {
		name = spec.Name
	}
	if spec.Name != "" && spec.DisplayName != "" && spec.Name != spec.DisplayName {
		return "", &InvalidDeviceError{Reason: "Name and DisplayName must match"}
	}
	return name, nil
}

func virtualCapabilitiesCompatible(left, right []VirtualCapability) bool {
	for _, a := range left {
		for _, b := range right {
			if a == b {
				return true
			}
		}
	}
	return false
}

type virtualDeviceState struct {
	info    VirtualDevice
	present bool
	opened  int
}

type virtualPair struct {
	mu                    sync.Mutex
	input                 DeviceID
	output                DeviceID
	inputOpen, outputOpen int
	inputSeen, outputSeen bool
	queue                 [][]byte
	changed               chan struct{}
}

func newVirtualPair(input, output DeviceID) *virtualPair {
	return &virtualPair{input: input, output: output, changed: make(chan struct{})}
}

func (p *virtualPair) signal() {
	p.mu.Lock()
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

// VirtualRegistry is a production DeviceRegistry backed only by synchronized
// memory. Each constructor call owns its topology, queues, defaults, and
// fault state.
type VirtualRegistry struct {
	mu           sync.Mutex
	devices      map[DeviceID]*virtualDeviceState
	defaults     map[Direction]DeviceID
	pairs        map[DeviceID]*virtualPair
	observations DeviceRegistryObservations
}

var _ DeviceRegistry = (*VirtualRegistry)(nil)

// NewVirtualRegistry validates and defensively copies cfg before returning a
// usable backend. No host audio APIs or CGO-backed dependencies are touched.
func NewVirtualRegistry(cfg VirtualBackendConfig) (*VirtualRegistry, error) {
	if len(cfg.Devices) == 0 {
		return nil, &InvalidDeviceError{Reason: "virtual backend needs at least one device"}
	}
	r := &VirtualRegistry{devices: make(map[DeviceID]*virtualDeviceState), defaults: make(map[Direction]DeviceID), pairs: make(map[DeviceID]*virtualPair)}
	defaultsByDirection := make(map[Direction][]DeviceID)
	refs := make(map[DeviceID]string)
	for _, spec := range cfg.Devices {
		if err := ValidateDirection(spec.Direction); err != nil {
			return nil, err
		}
		nativeID, err := virtualConfigNativeID(spec)
		if err != nil {
			return nil, err
		}
		name, err := virtualDisplayName(spec)
		if err != nil {
			return nil, err
		}
		device, err := NewDevice(VirtualBackendName, nativeID, name, spec.Direction)
		if err != nil {
			return nil, err
		}
		if _, exists := r.devices[device.ID]; exists {
			return nil, &InvalidDeviceError{ID: device.ID, Reason: "duplicate virtual device ID"}
		}
		caps, err := normalizeVirtualCapabilities(spec.Capabilities)
		if err != nil {
			return nil, &InvalidDeviceError{ID: device.ID, Reason: err.Error()}
		}
		info := VirtualDevice{Device: device, Capabilities: caps, Exclusive: spec.Exclusive}
		r.devices[device.ID] = &virtualDeviceState{info: info, present: true}
		if spec.Default {
			defaultsByDirection[spec.Direction] = append(defaultsByDirection[spec.Direction], device.ID)
		}
		if ref := virtualPairReference(spec); ref != "" {
			refs[device.ID] = ref
		}
	}

	pairIDs := make(map[DeviceID]DeviceID)
	for id, ref := range refs {
		other, err := virtualReference(ref)
		if err != nil {
			return nil, &InvalidDeviceError{ID: id, Reason: fmt.Sprintf("invalid loopback reference: %v", err)}
		}
		otherState, ok := r.devices[other]
		if !ok {
			return nil, &InvalidDeviceError{ID: id, Reason: fmt.Sprintf("loopback device %q is outside the topology", other)}
		}
		if otherState.info.Direction == r.devices[id].info.Direction {
			return nil, &InvalidDeviceError{ID: id, Reason: "loopback devices must have opposite directions"}
		}
		if previous, exists := pairIDs[id]; exists && previous != other {
			return nil, &InvalidDeviceError{ID: id, Reason: "loopback device is paired more than once"}
		}
		if previous, exists := pairIDs[other]; exists && previous != id {
			return nil, &InvalidDeviceError{ID: id, Reason: "loopback device is paired more than once"}
		}
		pairIDs[id] = other
		pairIDs[other] = id
	}
	for id, other := range pairIDs {
		if !virtualCapabilitiesCompatible(r.devices[id].info.Capabilities, r.devices[other].info.Capabilities) {
			return nil, &InvalidDeviceError{ID: id, Reason: fmt.Sprintf("loopback device %q has incompatible capabilities", other)}
		}
	}
	createdPairs := make(map[string]*virtualPair)
	for id, other := range pairIDs {
		left, right := string(id), string(other)
		if left > right {
			left, right = right, left
		}
		key := left + "\x00" + right
		pair := createdPairs[key]
		if pair == nil {
			if r.devices[id].info.Direction == DirectionInput {
				pair = newVirtualPair(id, other)
			} else {
				pair = newVirtualPair(other, id)
			}
			createdPairs[key] = pair
		}
		r.pairs[id] = pair
		r.devices[id].info.LoopbackID = other
	}

	if cfg.Defaults == nil {
		for direction, ids := range defaultsByDirection {
			if len(ids) > 1 {
				return nil, &InvalidDeviceError{Reason: fmt.Sprintf("multiple virtual defaults for %s", direction)}
			}
			if len(ids) == 1 {
				r.defaults[direction] = ids[0]
			}
		}
		for _, direction := range []Direction{DirectionInput, DirectionOutput} {
			if _, exists := r.defaults[direction]; exists {
				continue
			}
			ids := make([]DeviceID, 0, len(r.devices))
			for id, state := range r.devices {
				if state.info.Direction == direction {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				r.defaults[direction] = ids[0]
			}
		}
	} else {
		for direction, ref := range cfg.Defaults {
			if err := r.setVirtualDefault(direction, ref); err != nil {
				return nil, err
			}
		}
	}
	for direction, ref := range map[Direction]string{DirectionInput: cfg.InputDefault, DirectionOutput: cfg.OutputDefault} {
		if ref == "" {
			continue
		}
		if err := r.setVirtualDefault(direction, ref); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *VirtualRegistry) setVirtualDefault(direction Direction, ref string) error {
	if err := ValidateDirection(direction); err != nil {
		return err
	}
	id, err := virtualReference(ref)
	if err != nil {
		return err
	}
	state, ok := r.devices[id]
	if !ok {
		return &InvalidDeviceError{ID: id, Reason: "default is outside the topology"}
	}
	if state.info.Direction != direction {
		return &InvalidDeviceError{ID: id, Reason: fmt.Sprintf("default has direction %s, want %s", state.info.Direction, direction)}
	}
	r.defaults[direction] = id
	return nil
}

func (r *VirtualRegistry) List() ([]Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations.ListCalls++
	devices := make([]Device, 0, len(r.devices))
	for _, state := range r.devices {
		if state.present {
			devices = append(devices, state.info.Device)
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	return devices, nil
}

func (r *VirtualRegistry) ListVirtualDevices() []VirtualDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	devices := make([]VirtualDevice, 0, len(r.devices))
	for _, state := range r.devices {
		if state.present {
			copy := state.info
			copy.Capabilities = cloneVirtualCapabilities(copy.Capabilities)
			devices = append(devices, copy)
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	return devices
}

func (r *VirtualRegistry) Capabilities(id DeviceID) ([]VirtualCapability, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.devices[id]
	if !ok || !state.present {
		return nil, NewDeviceNotFoundError(id)
	}
	return cloneVirtualCapabilities(state.info.Capabilities), nil
}

func (r *VirtualRegistry) Default(direction Direction) (Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations.DefaultCalls++
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	id, ok := r.defaults[direction]
	if !ok {
		return Device{}, NewNoDefaultDeviceError(direction)
	}
	state, ok := r.devices[id]
	if !ok || !state.present || state.info.Direction != direction {
		return Device{}, NewNoDefaultDeviceError(direction)
	}
	return state.info.Device, nil
}

func (r *VirtualRegistry) Open(id DeviceID) (OpenedDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.devices[id]
	if !ok || !state.present {
		return nil, NewDeviceNotFoundError(id)
	}
	if state.info.Exclusive && state.opened != 0 {
		return nil, NewDeviceInUseError(id)
	}
	state.opened++
	r.observations.OpenCount++
	pair := r.pairs[id]
	if pair != nil {
		pair.mu.Lock()
		if state.info.Direction == DirectionInput {
			pair.inputOpen++
			pair.inputSeen = true
		} else {
			pair.outputOpen++
			pair.outputSeen = true
		}
		pair.mu.Unlock()
	}
	return &VirtualStream{registry: r, id: id, direction: state.info.Direction, pair: pair, done: make(chan struct{})}, nil
}

func (r *VirtualRegistry) Observations() DeviceRegistryObservations {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.observations
}

// RemoveDevice makes a configured endpoint disappear and wakes its streams.
func (r *VirtualRegistry) RemoveDevice(id DeviceID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.devices[id]
	if !ok || !state.present {
		return false
	}
	state.present = false
	if pair := r.pairs[id]; pair != nil {
		pair.signal()
	}
	return true
}

func (r *VirtualRegistry) release(id DeviceID, direction Direction, pair *virtualPair) {
	r.mu.Lock()
	if state := r.devices[id]; state != nil && state.opened > 0 {
		state.opened--
		r.observations.ReleaseCount++
	}
	if pair != nil {
		pair.mu.Lock()
		if direction == DirectionInput && pair.inputOpen > 0 {
			pair.inputOpen--
		}
		if direction == DirectionOutput && pair.outputOpen > 0 {
			pair.outputOpen--
		}
		pair.mu.Unlock()
	}
	r.mu.Unlock()
	if pair != nil {
		pair.signal()
	}
}

func (r *VirtualRegistry) streamErrorLocked(id DeviceID, direction Direction, pair *virtualPair, operation string) error {
	state := r.devices[id]
	if state == nil || !state.present {
		return &DeviceLostError{ID: id, Direction: direction}
	}
	if pair == nil {
		return nil
	}
	other := pair.input
	if id == pair.input {
		other = pair.output
	}
	otherState := r.devices[other]
	if otherState == nil || !otherState.present {
		return &DeviceLostError{ID: id, Direction: direction}
	}
	pair.mu.Lock()
	if direction == DirectionInput && pair.outputSeen && pair.outputOpen == 0 {
		pair.mu.Unlock()
		return &ClosedError{Operation: operation, Path: string(other)}
	}
	if direction == DirectionOutput && pair.inputSeen && pair.inputOpen == 0 {
		pair.mu.Unlock()
		return &ClosedError{Operation: operation, Path: string(other)}
	}
	pair.mu.Unlock()
	return nil
}

// VirtualStream is the byte-preserving endpoint returned by Open. Read and
// Write each operate on one caller-defined frame, retaining queue boundaries.
type VirtualStream struct {
	registry  *VirtualRegistry
	id        DeviceID
	direction Direction
	pair      *virtualPair
	done      chan struct{}
	closed    atomic.Bool
}

var _ OpenedDevice = (*VirtualStream)(nil)

func (s *VirtualStream) closedError(operation string) error {
	return &ClosedError{Operation: operation, Path: string(s.id)}
}

func (s *VirtualStream) operationError(operation string, direction Direction) error {
	if s.closed.Load() {
		return s.closedError(operation)
	}
	if s.direction != direction {
		return &InvalidDeviceError{ID: s.id, Reason: fmt.Sprintf("cannot %s %s device", operation, s.direction)}
	}
	return nil
}

func (s *VirtualStream) Write(ctx context.Context, frame []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := s.operationError("write", DirectionOutput); err != nil {
		return err
	}
	if len(frame) == 0 {
		return ErrVirtualEmptyFrame
	}
	if s.pair == nil {
		return ErrVirtualNoLoopback
	}
	s.registry.mu.Lock()
	if err := s.registry.streamErrorLocked(s.id, s.direction, s.pair, "write"); err != nil {
		s.registry.mu.Unlock()
		return err
	}
	s.pair.mu.Lock()
	s.pair.queue = append(s.pair.queue, append([]byte(nil), frame...))
	close(s.pair.changed)
	s.pair.changed = make(chan struct{})
	s.pair.mu.Unlock()
	s.registry.mu.Unlock()
	return nil
}

func (s *VirtualStream) WriteFrame(ctx context.Context, frame []byte) error {
	return s.Write(ctx, frame)
}

func (s *VirtualStream) Read(ctx context.Context) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := s.operationError("read", DirectionInput); err != nil {
		return nil, err
	}
	if s.pair == nil {
		return nil, ErrVirtualNoLoopback
	}
	for {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if s.closed.Load() {
			return nil, s.closedError("read")
		}
		s.registry.mu.Lock()
		if err := s.registry.streamErrorLocked(s.id, s.direction, s.pair, "read"); err != nil {
			s.registry.mu.Unlock()
			return nil, err
		}
		s.pair.mu.Lock()
		if len(s.pair.queue) > 0 {
			frame := append([]byte(nil), s.pair.queue[0]...)
			copy(s.pair.queue, s.pair.queue[1:])
			s.pair.queue[len(s.pair.queue)-1] = nil
			s.pair.queue = s.pair.queue[:len(s.pair.queue)-1]
			s.pair.mu.Unlock()
			s.registry.mu.Unlock()
			return frame, nil
		}
		changed := s.pair.changed
		s.pair.mu.Unlock()
		s.registry.mu.Unlock()
		select {
		case <-ctxDone(ctx):
			return nil, contextError(ctx)
		case <-s.done:
			return nil, s.closedError("read")
		case <-changed:
		}
	}
}

func (s *VirtualStream) ReadFrame(ctx context.Context) ([]byte, error) {
	return s.Read(ctx)
}

func ctxDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

func (s *VirtualStream) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	close(s.done)
	s.registry.release(s.id, s.direction, s.pair)
	return nil
}

// AudioBackendFactory is the production registration shape. Factories receive
// a value configuration and must return a fresh isolated DeviceRegistry.
type AudioBackendFactory func(VirtualBackendConfig) (DeviceRegistry, error)

// AudioBackendRegistry is a catalog of backend factories. New catalogs are
// cheap and carry no device or stream state.
type AudioBackendRegistry struct {
	mu        sync.RWMutex
	factories map[string]AudioBackendFactory
}

func NewAudioBackendRegistry() *AudioBackendRegistry {
	r := &AudioBackendRegistry{factories: make(map[string]AudioBackendFactory)}
	_ = r.Register(VirtualBackendName, func(cfg VirtualBackendConfig) (DeviceRegistry, error) { return NewVirtualRegistry(cfg) })
	return r
}

func NewProductionAudioBackendRegistry() *AudioBackendRegistry { return NewAudioBackendRegistry() }

func (r *AudioBackendRegistry) Register(name string, factory AudioBackendFactory) error {
	if r == nil || factory == nil {
		return fmt.Errorf("audio backend factory is nil")
	}
	if _, err := NewDeviceID(name, "registration"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("audio backend %q is already registered", name)
	}
	r.factories[name] = factory
	return nil
}

func (r *AudioBackendRegistry) New(name string, cfg VirtualBackendConfig) (DeviceRegistry, error) {
	if r == nil {
		return nil, fmt.Errorf("audio backend registry is nil")
	}
	r.mu.RLock()
	factory := r.factories[name]
	r.mu.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("audio backend %q is not registered", name)
	}
	return factory(cfg)
}

func (r *AudioBackendRegistry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func NewRegisteredAudioBackend(name string, cfg VirtualBackendConfig) (DeviceRegistry, error) {
	return NewAudioBackendRegistry().New(name, cfg)
}
