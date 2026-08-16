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
	caps       []VirtualCapability
	LoopbackID DeviceID
	Exclusive  bool
	opened     int
}

func bad(id DeviceID, reason string) error { return &InvalidDeviceError{ID: id, Reason: reason} }
func DefaultVirtualBackendConfig() VirtualBackendConfig {
	return VirtualBackendConfig{Devices: []VirtualDeviceConfig{
		{ID: "input", Name: "Virtual Input", Direction: DirectionInput, LoopbackID: "output"},
		{ID: "output", Name: "Virtual Output", Direction: DirectionOutput, LoopbackID: "input"},
		{ID: "exclusive", Name: "Virtual Exclusive Output", Direction: DirectionOutput, Exclusive: true},
	}, Defaults: map[Direction]string{DirectionInput: "input", DirectionOutput: "output"}}
}
func virtualID(ref string) (DeviceID, error) {
	if strings.ContainsRune(ref, ':') && !strings.HasPrefix(ref, VirtualBackendName+":") {
		return "", bad("", "invalid virtual device reference")
	}
	ref = strings.TrimPrefix(ref, VirtualBackendName+":")
	return NewDeviceID(VirtualBackendName, ref)
}
func compatible(a, b []VirtualCapability) bool {
	return slices.ContainsFunc(a, func(x VirtualCapability) bool { return slices.Contains(b, x) })
}
func makeVirtualDevice(s VirtualDeviceConfig) (virtualDevice, error) {
	id, err := virtualID(s.ID)
	if err != nil {
		return virtualDevice{}, err
	}
	d, err := NewDevice(VirtualBackendName, strings.TrimPrefix(string(id), VirtualBackendName+":"), s.Name, s.Direction)
	if err != nil {
		return virtualDevice{}, err
	}
	caps := append([]VirtualCapability(nil), s.Capabilities...)
	if len(caps) == 0 {
		caps = []VirtualCapability{{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, Format: "pcm16"}}
	}
	var loopback DeviceID
	if s.LoopbackID != "" {
		loopback, err = virtualID(s.LoopbackID)
		if err != nil {
			return virtualDevice{}, bad(id, err.Error())
		}
	}
	return virtualDevice{Device: d, caps: caps, LoopbackID: loopback, Exclusive: s.Exclusive}, nil
}

type virtualPair struct {
	ids     [2]DeviceID
	open    [2]int
	seen    [2]bool
	queue   [][]byte
	changed chan struct{}
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
		return nil, bad("", "virtual backend needs at least one device")
	}
	r := &VirtualRegistry{devices: map[DeviceID]*virtualDevice{}, defaults: map[Direction]DeviceID{}, pairs: map[DeviceID]*virtualPair{}}
	for _, spec := range c.Devices {
		v, err := makeVirtualDevice(spec)
		if err != nil {
			return nil, err
		}
		if _, exists := r.devices[v.ID]; exists {
			return nil, bad(v.ID, "duplicate virtual device ID")
		}
		r.devices[v.ID] = &v
	}
	for id, a := range r.devices {
		if a.LoopbackID == "" || r.pairs[id] != nil {
			continue
		}
		b := r.devices[a.LoopbackID]
		if b == nil {
			return nil, bad(id, fmt.Sprintf("loopback device %q is outside the topology", a.LoopbackID))
		}
		if a.Direction == b.Direction || !compatible(a.caps, b.caps) {
			return nil, bad(id, "loopback devices have incompatible directions or capabilities")
		}
		p := &virtualPair{ids: [2]DeviceID{a.LoopbackID, id}, changed: make(chan struct{})}
		if a.Direction == DirectionInput {
			p.ids[0], p.ids[1] = p.ids[1], p.ids[0]
		}
		r.pairs[p.ids[0]], r.pairs[p.ids[1]] = p, p
	}
	for d, ref := range c.Defaults {
		if err := ValidateDirection(d); err != nil {
			return nil, err
		}
		id, err := virtualID(ref)
		if err != nil {
			return nil, err
		}
		if v := r.devices[id]; v == nil || v.Direction != d {
			return nil, bad(id, "default is outside the topology or has the wrong direction")
		}
		r.defaults[d] = id
	}
	return r, nil
}
func (r *VirtualRegistry) device(id DeviceID) *virtualDevice {
	return r.devices[id]
}
func (r *VirtualRegistry) List() ([]Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Device, 0, len(r.devices))
	for _, v := range r.devices {
		out = append(out, v.Device)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (r *VirtualRegistry) Capabilities(id DeviceID) ([]VirtualCapability, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.device(id); v != nil {
		return append([]VirtualCapability(nil), v.caps...), nil
	}
	return nil, NewDeviceNotFoundError(id)
}
func (r *VirtualRegistry) Default(d Direction) (Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ValidateDirection(d); err != nil {
		return Device{}, err
	}
	if v := r.device(r.defaults[d]); v != nil {
		return v.Device, nil
	}
	return Device{}, NewNoDefaultDeviceError(d)
}
func side(d Direction) int { return map[Direction]int{DirectionOutput: 1}[d] }
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
		i := side(v.Direction)
		p.open[i]++
		p.seen[i] = true
	}
	return &VirtualStream{registry: r, device: v, id: id, direction: v.Direction, pair: p}, nil
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
	delete(r.devices, id)
	if p := r.pairs[id]; p != nil {
		p.signal()
	}
	return true
}
func (r *VirtualRegistry) streamError(s *VirtualStream, op string) error {
	if r.device(s.id) == nil {
		return &DeviceLostError{ID: s.id, Direction: s.direction}
	}
	if s.pair == nil {
		return nil
	}
	p, i := s.pair, side(s.direction)
	other := p.ids[1-i]
	if r.device(other) == nil {
		return &DeviceLostError{ID: s.id, Direction: s.direction}
	}
	if p.seen[1-i] && p.open[1-i] == 0 {
		return &ClosedError{Operation: op, Path: string(other)}
	}
	return nil
}

type VirtualStream struct {
	registry  *VirtualRegistry
	device    *virtualDevice
	id        DeviceID
	direction Direction
	pair      *virtualPair
	closed    bool
}

func (s *VirtualStream) lock(op string) (*virtualPair, error) {
	s.registry.mu.Lock()
	if s.closed {
		return s.fail(&ClosedError{Operation: op, Path: string(s.id)})
	}
	if err := s.registry.streamError(s, op); err != nil {
		return s.fail(err)
	}
	if s.pair == nil {
		return s.fail(ErrVirtualNoLoopback)
	}
	return s.pair, nil
}
func (s *VirtualStream) fail(err error) (*virtualPair, error) {
	s.registry.mu.Unlock()
	return nil, err
}
func (s *VirtualStream) Write(ctx context.Context, frame []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	p, err := s.lock("write")
	if err != nil {
		return err
	}
	defer s.registry.mu.Unlock()
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
		p, err := s.lock("read")
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
	s.device.opened--
	r.observations.ReleaseCount++
	if p := s.pair; p != nil {
		i := side(s.direction)
		p.open[i]--
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
