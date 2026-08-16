package audio

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

const VirtualBackendName = "virtual"

var (
	ErrDeviceLost        = errors.New("device lost")
	ErrVirtualNoLoopback = errors.New("virtual device has no loopback")
	ErrVirtualEmptyFrame = errors.New("virtual audio frame is empty")
)

type DeviceLostError struct {
	ID        DeviceID
	Direction Direction
}

func (e *DeviceLostError) Error() string {
	return fmt.Sprintf("device %q (%s) was lost", e.ID, e.Direction)
}
func (*DeviceLostError) Unwrap() error { return ErrDeviceLost }

type VirtualCapability struct {
	SampleRate, Channels, BitDepth int
	Format                         string
}
type VirtualDeviceConfig struct {
	ID, Name     string
	Direction    Direction
	Capabilities []VirtualCapability
	Exclusive    bool
	LoopbackID   string
}
type VirtualBackendConfig struct {
	Devices  []VirtualDeviceConfig
	Defaults map[Direction]string
}
type virtualDevice struct {
	Device
	Capabilities []VirtualCapability
	Exclusive    bool
	LoopbackID   DeviceID
	present      bool
	opened       int
}

func invalidDevice(id DeviceID, reason string) error {
	return &InvalidDeviceError{ID: id, Reason: reason}
}
func DefaultVirtualBackendConfig() VirtualBackendConfig {
	return VirtualBackendConfig{Devices: []VirtualDeviceConfig{{ID: "input", Name: "Virtual Input", Direction: DirectionInput, Capabilities: []VirtualCapability{{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, Format: "pcm16"}}, LoopbackID: "output"}, {ID: "output", Name: "Virtual Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, Format: "pcm16"}}, LoopbackID: "input"}, {ID: "exclusive", Name: "Virtual Exclusive Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, Format: "pcm16"}}, Exclusive: true}}, Defaults: map[Direction]string{DirectionInput: "input", DirectionOutput: "output"}}
}
func virtualID(ref string) (DeviceID, error) {
	if !strings.HasPrefix(ref, VirtualBackendName+":") {
		return NewDeviceID(VirtualBackendName, ref)
	}
	b, n, err := ParseDeviceID(ref)
	if err != nil || b != VirtualBackendName {
		return "", invalidDevice("", fmt.Sprintf("invalid virtual device reference %q", ref))
	}
	return NewDeviceID(b, n)
}
func virtualCaps(in []VirtualCapability) ([]VirtualCapability, error) {
	if len(in) == 0 {
		in = []VirtualCapability{{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, Format: "pcm16"}}
	}
	out := append([]VirtualCapability(nil), in...)
	for _, c := range out {
		if c.SampleRate <= 0 || c.Channels <= 0 || c.BitDepth <= 0 || strings.TrimSpace(c.Format) == "" {
			return nil, errors.New("invalid virtual audio capability")
		}
	}
	return out, nil
}
func compatible(a, b []VirtualCapability) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}
func makeVirtualDevice(s VirtualDeviceConfig) (virtualDevice, error) {
	id, err := virtualID(s.ID)
	if err != nil {
		return virtualDevice{}, err
	}
	_, native, _ := ParseDeviceID(id)
	d, err := NewDevice(VirtualBackendName, native, s.Name, s.Direction)
	if err != nil {
		return virtualDevice{}, err
	}
	caps, err := virtualCaps(s.Capabilities)
	if err != nil {
		return virtualDevice{}, invalidDevice(id, err.Error())
	}
	var loopback DeviceID
	if s.LoopbackID != "" {
		loopback, err = virtualID(s.LoopbackID)
		if err != nil {
			return virtualDevice{}, invalidDevice(id, err.Error())
		}
	}
	return virtualDevice{Device: d, Capabilities: caps, Exclusive: s.Exclusive, LoopbackID: loopback, present: true}, nil
}

type virtualPair struct {
	input, output DeviceID
	open          [2]int
	seen          [2]bool
	queue         [][]byte
	changed       chan struct{}
}

func (p *virtualPair) signal() { close(p.changed); p.changed = make(chan struct{}) }

type VirtualRegistry struct {
	mu           sync.Mutex
	devices      map[DeviceID]*virtualDevice
	defaults     map[Direction]DeviceID
	pairs        map[DeviceID]*virtualPair
	observations DeviceRegistryObservations
}

func NewVirtualRegistry(c VirtualBackendConfig) (*VirtualRegistry, error) {
	if len(c.Devices) == 0 {
		return nil, invalidDevice("", "virtual backend needs at least one device")
	}
	r := &VirtualRegistry{devices: map[DeviceID]*virtualDevice{}, defaults: map[Direction]DeviceID{}, pairs: map[DeviceID]*virtualPair{}}
	for _, spec := range c.Devices {
		v, err := makeVirtualDevice(spec)
		if err != nil {
			return nil, err
		}
		if _, exists := r.devices[v.ID]; exists {
			return nil, invalidDevice(v.ID, "duplicate virtual device ID")
		}
		r.devices[v.ID] = &v
	}
	for id, a := range r.devices {
		other := a.LoopbackID
		if other == "" || r.pairs[id] != nil {
			continue
		}
		b := r.devices[other]
		if b == nil {
			return nil, invalidDevice(id, fmt.Sprintf("loopback device %q is outside the topology", other))
		}
		if a.Direction == b.Direction || !compatible(a.Capabilities, b.Capabilities) {
			return nil, invalidDevice(id, "loopback devices have incompatible directions or capabilities")
		}
		p := &virtualPair{changed: make(chan struct{})}
		if a.Direction == DirectionInput {
			p.input, p.output = id, other
		} else {
			p.input, p.output = other, id
		}
		r.pairs[id], r.pairs[other] = p, p
	}
	for d, id := range c.Defaults {
		if err := r.setDefault(d, id); err != nil {
			return nil, err
		}
	}
	return r, nil
}
func (r *VirtualRegistry) setDefault(d Direction, ref string) error {
	if err := ValidateDirection(d); err != nil {
		return err
	}
	id, err := virtualID(ref)
	if err != nil {
		return err
	}
	v := r.devices[id]
	if v == nil || v.Direction != d {
		return invalidDevice(id, "default is outside the topology or has the wrong direction")
	}
	r.defaults[d] = id
	return nil
}
func (r *VirtualRegistry) device(id DeviceID) *virtualDevice {
	if v := r.devices[id]; v != nil && v.present {
		return v
	}
	return nil
}
func (r *VirtualRegistry) List() ([]Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Device, 0, len(r.devices))
	for _, v := range r.devices {
		if v.present {
			out = append(out, v.Device)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (r *VirtualRegistry) Capabilities(id DeviceID) ([]VirtualCapability, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.device(id)
	if v == nil {
		return nil, NewDeviceNotFoundError(id)
	}
	return append([]VirtualCapability(nil), v.Capabilities...), nil
}
func (r *VirtualRegistry) Default(d Direction) (Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ValidateDirection(d); err != nil {
		return Device{}, err
	}
	v := r.device(r.defaults[d])
	if v == nil {
		return Device{}, NewNoDefaultDeviceError(d)
	}
	return v.Device, nil
}
func (r *VirtualRegistry) Open(id DeviceID) (OpenedDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.device(id)
	if v == nil {
		return nil, NewDeviceNotFoundError(id)
	}
	if v.Exclusive && v.opened != 0 {
		return nil, NewDeviceInUseError(id)
	}
	v.opened++
	r.observations.OpenCount++
	p := r.pairs[id]
	if p != nil {
		i := 0
		if v.Direction == DirectionOutput {
			i = 1
		}
		p.open[i]++
		p.seen[i] = true
	}
	return &VirtualStream{registry: r, id: id, direction: v.Direction, pair: p}, nil
}
func (r *VirtualRegistry) Observations() DeviceRegistryObservations {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.observations
}
func (r *VirtualRegistry) RemoveDevice(id DeviceID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.device(id)
	if v == nil {
		return false
	}
	v.present = false
	if p := r.pairs[id]; p != nil {
		p.signal()
	}
	return true
}
func (r *VirtualRegistry) streamError(s *VirtualStream, op string) error {
	if r.device(s.id) == nil {
		return &DeviceLostError{ID: s.id, Direction: s.direction}
	}
	p := s.pair
	if p == nil {
		return nil
	}
	side, other := 1, p.input
	if s.id == p.input {
		side, other = 0, p.output
	}
	if r.device(other) == nil {
		return &DeviceLostError{ID: s.id, Direction: s.direction}
	}
	if p.seen[1-side] && p.open[1-side] == 0 {
		return &ClosedError{Operation: op, Path: string(other)}
	}
	return nil
}

type VirtualStream struct {
	registry  *VirtualRegistry
	id        DeviceID
	direction Direction
	pair      *virtualPair
	closed    bool
}

func (s *VirtualStream) check(op string, d Direction) error {
	if s.closed {
		return &ClosedError{Operation: op, Path: string(s.id)}
	}
	if s.direction != d {
		return invalidDevice(s.id, fmt.Sprintf("cannot %s %s device", op, s.direction))
	}
	return s.registry.streamError(s, op)
}
func (s *VirtualStream) begin(op string, d Direction) (*virtualPair, error) {
	s.registry.mu.Lock()
	if err := s.check(op, d); err != nil {
		s.registry.mu.Unlock()
		return nil, err
	}
	if s.pair == nil {
		s.registry.mu.Unlock()
		return nil, ErrVirtualNoLoopback
	}
	return s.pair, nil
}
func (s *VirtualStream) Write(ctx context.Context, frame []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	p, err := s.begin("write", DirectionOutput)
	if err != nil {
		return err
	}
	defer s.registry.mu.Unlock()
	if len(frame) == 0 {
		return ErrVirtualEmptyFrame
	}
	p.queue = append(p.queue, append([]byte(nil), frame...))
	p.signal()
	return nil
}
func (s *VirtualStream) Read(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		p, err := s.begin("read", DirectionInput)
		if err != nil {
			return nil, err
		}
		if len(p.queue) != 0 {
			frame := append([]byte(nil), p.queue[0]...)
			p.queue = p.queue[1:]
			s.registry.mu.Unlock()
			return frame, nil
		}
		changed := p.changed
		s.registry.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, contextError(ctx)
		case <-changed:
		}
	}
}
func (s *VirtualStream) Close() error {
	r := s.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if v := r.devices[s.id]; v != nil && v.opened > 0 {
		v.opened--
		r.observations.ReleaseCount++
	}
	if p := s.pair; p != nil {
		i := 0
		if s.direction == DirectionOutput {
			i = 1
		}
		if p.open[i] > 0 {
			p.open[i]--
		}
		p.signal()
	}
	return nil
}

type AudioBackendRegistry struct{}

func NewAudioBackendRegistry() *AudioBackendRegistry { return &AudioBackendRegistry{} }

func NewProductionAudioBackendRegistry() *AudioBackendRegistry { return NewAudioBackendRegistry() }

func (r *AudioBackendRegistry) New(name string, c VirtualBackendConfig) (DeviceRegistry, error) {
	if name == VirtualBackendName {
		return NewVirtualRegistry(c)
	}
	return nil, fmt.Errorf("audio backend %q is not registered", name)
}
func (r *AudioBackendRegistry) Names() []string { return []string{VirtualBackendName} }
