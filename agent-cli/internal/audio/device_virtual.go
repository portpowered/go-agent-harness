package audio

import (
	"context"
	"errors"
	"fmt"
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
	c := VirtualCapability{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, Format: "pcm16"}
	return VirtualBackendConfig{Devices: []VirtualDeviceConfig{
		{ID: "input", Name: "Virtual Input", Direction: DirectionInput, Capabilities: []VirtualCapability{c}, LoopbackID: "output"},
		{ID: "output", Name: "Virtual Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{c}, LoopbackID: "input"},
		{ID: "exclusive", Name: "Virtual Exclusive Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{c}, Exclusive: true},
	}, Defaults: map[Direction]string{DirectionInput: "input", DirectionOutput: "output"}}
}

func virtualID(ref string) (DeviceID, error) {
	if strings.HasPrefix(ref, VirtualBackendName+":") {
		backend, native, err := ParseDeviceID(ref)
		if err != nil || backend != VirtualBackendName {
			return "", invalidDevice("", fmt.Sprintf("invalid virtual device reference %q", ref))
		}
		return NewDeviceID(backend, native)
	}
	return NewDeviceID(VirtualBackendName, ref)
}
func virtualCaps(in []VirtualCapability) ([]VirtualCapability, error) {
	if len(in) == 0 {
		in = []VirtualCapability{{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, Format: "pcm16"}}
	}
	out := append([]VirtualCapability(nil), in...)
	for i := range out {
		if out[i].SampleRate <= 0 || out[i].Channels <= 0 || out[i].BitDepth <= 0 || strings.TrimSpace(out[i].Format) == "" {
			return nil, errors.New("invalid virtual audio capability")
		}
	}
	return out, nil
}
func compatible(a, b []VirtualCapability) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
func makeVirtualDevice(s VirtualDeviceConfig) (virtualDevice, error) {
	id := s.ID
	if strings.HasPrefix(id, VirtualBackendName+":") {
		var err error
		_, id, err = ParseDeviceID(id)
		if err != nil {
			return virtualDevice{}, err
		}
	}
	d, err := NewDevice(VirtualBackendName, id, s.Name, s.Direction)
	if err != nil {
		return virtualDevice{}, err
	}
	caps, err := virtualCaps(s.Capabilities)
	if err != nil {
		return virtualDevice{}, invalidDevice(d.ID, err.Error())
	}
	var loopback DeviceID
	if s.LoopbackID != "" {
		loopback, err = virtualID(s.LoopbackID)
		if err != nil {
			return virtualDevice{}, invalidDevice(d.ID, err.Error())
		}
	}
	return virtualDevice{Device: d, Capabilities: caps, Exclusive: s.Exclusive, LoopbackID: loopback, present: true}, nil
}

type virtualPair struct {
	input, output         DeviceID
	inputOpen, outputOpen int
	inputSeen, outputSeen bool
	queue                 [][]byte
	changed               chan struct{}
}

func (p *virtualPair) signal() { close(p.changed); p.changed = make(chan struct{}) }

type VirtualRegistry struct {
	mu           sync.Mutex
	devices      map[DeviceID]*virtualDevice
	defaults     map[Direction]DeviceID
	pairs        map[DeviceID]*virtualPair
	observations DeviceRegistryObservations
}

var _ DeviceRegistry = (*VirtualRegistry)(nil)

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
		a.LoopbackID, b.LoopbackID = other, id
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
	v := r.devices[id]
	if v == nil || !v.present {
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
	v := r.devices[r.defaults[d]]
	if v == nil || !v.present {
		return Device{}, NewNoDefaultDeviceError(d)
	}
	return v.Device, nil
}
func (r *VirtualRegistry) Open(id DeviceID) (OpenedDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.devices[id]
	if v == nil || !v.present {
		return nil, NewDeviceNotFoundError(id)
	}
	if v.Exclusive && v.opened != 0 {
		return nil, NewDeviceInUseError(id)
	}
	v.opened++
	r.observations.OpenCount++
	p := r.pairs[id]
	if p != nil {
		if v.Direction == DirectionInput {
			p.inputOpen++
			p.inputSeen = true
		} else {
			p.outputOpen++
			p.outputSeen = true
		}
	}
	return &VirtualStream{registry: r, id: id, direction: v.Direction, pair: p, done: make(chan struct{})}, nil
}
func (r *VirtualRegistry) Observations() DeviceRegistryObservations {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.observations
}
func (r *VirtualRegistry) RemoveDevice(id DeviceID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.devices[id]
	if v == nil || !v.present {
		return false
	}
	v.present = false
	if p := r.pairs[id]; p != nil {
		p.signal()
	}
	return true
}
func (r *VirtualRegistry) streamError(s *VirtualStream, op string) error {
	v := r.devices[s.id]
	if v == nil || !v.present {
		return &DeviceLostError{ID: s.id, Direction: s.direction}
	}
	p := s.pair
	if p == nil {
		return nil
	}
	other := p.input
	if other == s.id {
		other = p.output
	}
	peer := r.devices[other]
	if peer == nil || !peer.present {
		return &DeviceLostError{ID: s.id, Direction: s.direction}
	}
	if s.direction == DirectionInput && p.outputSeen && p.outputOpen == 0 || s.direction == DirectionOutput && p.inputSeen && p.inputOpen == 0 {
		return &ClosedError{Operation: op, Path: string(other)}
	}
	return nil
}

type VirtualStream struct {
	registry  *VirtualRegistry
	id        DeviceID
	direction Direction
	pair      *virtualPair
	done      chan struct{}
	closed    bool
}

var _ OpenedDevice = (*VirtualStream)(nil)

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
	r := s.registry
	r.mu.Lock()
	if err := s.check(op, d); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if s.pair == nil {
		r.mu.Unlock()
		return nil, ErrVirtualNoLoopback
	}
	return s.pair, nil
}
func (s *VirtualStream) Write(ctx context.Context, frame []byte) error {
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
	for {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		p, err := s.begin("read", DirectionInput)
		if err != nil {
			return nil, err
		}
		if len(p.queue) > 0 {
			frame := append([]byte(nil), p.queue[0]...)
			p.queue = p.queue[1:]
			s.registry.mu.Unlock()
			return frame, nil
		}
		changed := p.changed
		s.registry.mu.Unlock()
		select {
		case <-ctxDone(ctx):
			return nil, contextError(ctx)
		case <-s.done:
			return nil, &ClosedError{Operation: "read", Path: string(s.id)}
		case <-changed:
		}
	}
}
func ctxDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}
func (s *VirtualStream) Close() error {
	r := s.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.done)
	if v := r.devices[s.id]; v != nil && v.opened > 0 {
		v.opened--
		r.observations.ReleaseCount++
	}
	if p := s.pair; p != nil {
		if s.direction == DirectionInput && p.inputOpen > 0 {
			p.inputOpen--
		}
		if s.direction == DirectionOutput && p.outputOpen > 0 {
			p.outputOpen--
		}
		p.signal()
	}
	return nil
}

type AudioBackendRegistry struct{}

func NewAudioBackendRegistry() *AudioBackendRegistry           { return &AudioBackendRegistry{} }
func NewProductionAudioBackendRegistry() *AudioBackendRegistry { return NewAudioBackendRegistry() }
func (r *AudioBackendRegistry) New(name string, c VirtualBackendConfig) (DeviceRegistry, error) {
	if name == VirtualBackendName {
		return NewVirtualRegistry(c)
	}
	return nil, fmt.Errorf("audio backend %q is not registered", name)
}
func (r *AudioBackendRegistry) Names() []string { return []string{VirtualBackendName} }
