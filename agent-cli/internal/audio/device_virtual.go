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
	ErrVirtualDeviceLost = ErrDeviceLost
	ErrVirtualNoLoopback = errors.New("virtual device has no loopback")
	ErrVirtualEmptyFrame = errors.New("virtual audio frame is empty")
)

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
func (*DeviceLostError) Unwrap() error { return ErrDeviceLost }

type VirtualDeviceLostError = DeviceLostError

type VirtualCapability struct {
	SampleRate, Channels, BitDepth int
	SampleFormat, Format           string
}
type VirtualAudioCapability = VirtualCapability
type VirtualAudioFormat = VirtualCapability
type VirtualDeviceConfig struct {
	ID, NativeID, Name, DisplayName string
	Direction                       Direction
	Capabilities                    []VirtualCapability
	Default, Exclusive              bool
	LoopbackID, PairID              string
}
type VirtualDeviceSpec = VirtualDeviceConfig
type VirtualBackendConfig struct {
	Devices                     []VirtualDeviceConfig
	Defaults                    map[Direction]string
	InputDefault, OutputDefault string
}
type VirtualConfig = VirtualBackendConfig
type VirtualDevice struct {
	Device
	Capabilities []VirtualCapability
	Exclusive    bool
	LoopbackID   DeviceID
	present      bool
	opened       int
}

func (d VirtualDevice) CapabilitiesCopy() []VirtualCapability {
	return append([]VirtualCapability(nil), d.Capabilities...)
}

func DefaultVirtualBackendConfig() VirtualBackendConfig {
	c := VirtualCapability{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, SampleFormat: "pcm16", Format: "pcm16"}
	return VirtualBackendConfig{Devices: []VirtualDeviceConfig{{ID: "input", Name: "Virtual Input", Direction: DirectionInput, Capabilities: []VirtualCapability{c}, Default: true, LoopbackID: "output"}, {ID: "output", Name: "Virtual Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{c}, Default: true, LoopbackID: "input"}, {ID: "exclusive", Name: "Virtual Exclusive Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{c}, Exclusive: true}}}
}
func virtualRef(v string) (DeviceID, error) {
	if strings.HasPrefix(v, VirtualBackendName+":") {
		b, n, err := ParseDeviceID(v)
		if err != nil || b != VirtualBackendName {
			return "", &InvalidDeviceError{Reason: fmt.Sprintf("invalid virtual device reference %q", v)}
		}
		return NewDeviceID(b, n)
	}
	return NewDeviceID(VirtualBackendName, v)
}
func virtualCaps(in []VirtualCapability) ([]VirtualCapability, error) {
	if len(in) == 0 {
		in = []VirtualCapability{{SampleRate: SampleRate, Channels: Channels, BitDepth: 16, SampleFormat: "pcm16", Format: "pcm16"}}
	}
	out := make([]VirtualCapability, len(in))
	for i, c := range in {
		if c.SampleFormat == "" {
			c.SampleFormat = c.Format
		}
		if c.SampleFormat == "" {
			c.SampleFormat = "pcm16"
		}
		c.Format = c.SampleFormat
		if c.SampleRate <= 0 || c.Channels <= 0 || c.BitDepth <= 0 || strings.TrimSpace(c.SampleFormat) == "" {
			return nil, fmt.Errorf("invalid virtual audio capability")
		}
		out[i] = c
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

type virtualPair struct {
	input, output         DeviceID
	inputOpen, outputOpen int
	inputSeen, outputSeen bool
	queue                 [][]byte
	changed               chan struct{}
}

func signal(p *virtualPair) { close(p.changed); p.changed = make(chan struct{}) }

type VirtualRegistry struct {
	mu           sync.Mutex
	devices      map[DeviceID]*VirtualDevice
	defaults     map[Direction]DeviceID
	pairs        map[DeviceID]*virtualPair
	observations DeviceRegistryObservations
}

var _ DeviceRegistry = (*VirtualRegistry)(nil)

func makeVirtualDevice(s VirtualDeviceConfig) (VirtualDevice, error) {
	if err := ValidateDirection(s.Direction); err != nil {
		return VirtualDevice{}, err
	}
	n := s.ID
	if n == "" {
		n = s.NativeID
	}
	if strings.HasPrefix(n, VirtualBackendName+":") {
		var err error
		_, n, err = ParseDeviceID(n)
		if err != nil {
			return VirtualDevice{}, err
		}
	}
	name := s.Name
	if s.DisplayName != "" {
		name = s.DisplayName
	}
	d, err := NewDevice(VirtualBackendName, n, name, s.Direction)
	if err != nil {
		return VirtualDevice{}, err
	}
	caps, err := virtualCaps(s.Capabilities)
	if err != nil {
		return VirtualDevice{}, &InvalidDeviceError{ID: d.ID, Reason: err.Error()}
	}
	return VirtualDevice{Device: d, Capabilities: caps, Exclusive: s.Exclusive, present: true}, nil
}
func NewVirtualRegistry(c VirtualBackendConfig) (*VirtualRegistry, error) {
	if len(c.Devices) == 0 {
		return nil, &InvalidDeviceError{Reason: "virtual backend needs at least one device"}
	}
	r := &VirtualRegistry{devices: map[DeviceID]*VirtualDevice{}, defaults: map[Direction]DeviceID{}, pairs: map[DeviceID]*virtualPair{}}
	refs := map[DeviceID]string{}
	for _, spec := range c.Devices {
		v, err := makeVirtualDevice(spec)
		if err != nil {
			return nil, err
		}
		if _, ok := r.devices[v.ID]; ok {
			return nil, &InvalidDeviceError{ID: v.ID, Reason: "duplicate virtual device ID"}
		}
		r.devices[v.ID] = &v
		if c.Defaults == nil && spec.Default {
			if r.defaults[v.Direction] != "" {
				return nil, &InvalidDeviceError{Reason: fmt.Sprintf("multiple virtual defaults for %s", v.Direction)}
			}
			r.defaults[v.Direction] = v.ID
		}
		if spec.LoopbackID != "" {
			refs[v.ID] = spec.LoopbackID
		} else if spec.PairID != "" {
			refs[v.ID] = spec.PairID
		}
	}
	for id, ref := range refs {
		other, err := virtualRef(ref)
		if err != nil {
			return nil, &InvalidDeviceError{ID: id, Reason: err.Error()}
		}
		a, b := r.devices[id], r.devices[other]
		if b == nil {
			return nil, &InvalidDeviceError{ID: id, Reason: fmt.Sprintf("loopback device %q is outside the topology", other)}
		}
		if a.Direction == b.Direction {
			return nil, &InvalidDeviceError{ID: id, Reason: "loopback devices must have opposite directions"}
		}
		if !compatible(a.Capabilities, b.Capabilities) {
			return nil, &InvalidDeviceError{ID: id, Reason: "loopback devices have incompatible capabilities"}
		}
		p := r.pairs[other]
		if p == nil {
			p = &virtualPair{changed: make(chan struct{})}
			if a.Direction == DirectionInput {
				p.input, p.output = id, other
			} else {
				p.input, p.output = other, id
			}
		}
		r.pairs[id], r.pairs[other] = p, p
		a.LoopbackID, b.LoopbackID = other, id
	}
	for d, id := range c.Defaults {
		if err := r.setDefault(d, id); err != nil {
			return nil, err
		}
	}
	for d, id := range map[Direction]string{DirectionInput: c.InputDefault, DirectionOutput: c.OutputDefault} {
		if id != "" {
			if err := r.setDefault(d, id); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}
func (r *VirtualRegistry) setDefault(d Direction, ref string) error {
	if err := ValidateDirection(d); err != nil {
		return err
	}
	id, err := virtualRef(ref)
	if err != nil {
		return err
	}
	v := r.devices[id]
	if v == nil || v.Direction != d {
		return &InvalidDeviceError{ID: id, Reason: "default is outside the topology or has the wrong direction"}
	}
	r.defaults[d] = id
	return nil
}
func (r *VirtualRegistry) snapshot() []VirtualDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]VirtualDevice, 0, len(r.devices))
	for _, v := range r.devices {
		if v.present {
			c := *v
			c.Capabilities = append([]VirtualCapability(nil), v.Capabilities...)
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (r *VirtualRegistry) List() ([]Device, error) {
	in := r.snapshot()
	out := make([]Device, len(in))
	for i := range in {
		out[i] = in[i].Device
	}
	return out, nil
}
func (r *VirtualRegistry) ListVirtualDevices() []VirtualDevice { return r.snapshot() }
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
	r.observations.DefaultCalls++
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
	if v.Exclusive && v.opened > 0 {
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
		signal(p)
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
	if (s.direction == DirectionInput && p.outputSeen && p.outputOpen == 0) || (s.direction == DirectionOutput && p.inputSeen && p.inputOpen == 0) {
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
		return &InvalidDeviceError{ID: s.id, Reason: fmt.Sprintf("cannot %s %s device", op, s.direction)}
	}
	return s.registry.streamError(s, op)
}
func (s *VirtualStream) begin(op string, d Direction) (*virtualPair, error) {
	s.registry.mu.Lock()
	if err := s.check(op, d); err != nil {
		s.registry.mu.Unlock()
		return nil, err
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
	if p == nil {
		return ErrVirtualNoLoopback
	}
	p.queue = append(p.queue, append([]byte(nil), frame...))
	signal(p)
	return nil
}
func (s *VirtualStream) WriteFrame(ctx context.Context, frame []byte) error {
	return s.Write(ctx, frame)
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
		if p == nil {
			s.registry.mu.Unlock()
			return nil, ErrVirtualNoLoopback
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
func (s *VirtualStream) ReadFrame(ctx context.Context) ([]byte, error) { return s.Read(ctx) }
func ctxDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}
func (s *VirtualStream) Close() error {
	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.done)
	if v := s.registry.devices[s.id]; v != nil && v.opened > 0 {
		v.opened--
		s.registry.observations.ReleaseCount++
	}
	if p := s.pair; p != nil {
		if s.direction == DirectionInput && p.inputOpen > 0 {
			p.inputOpen--
		}
		if s.direction == DirectionOutput && p.outputOpen > 0 {
			p.outputOpen--
		}
		signal(p)
	}
	return nil
}

type AudioBackendFactory func(VirtualBackendConfig) (DeviceRegistry, error)
type AudioBackendRegistry struct {
	factories map[string]AudioBackendFactory
}

func NewAudioBackendRegistry() *AudioBackendRegistry {
	r := &AudioBackendRegistry{factories: map[string]AudioBackendFactory{}}
	_ = r.Register(VirtualBackendName, func(c VirtualBackendConfig) (DeviceRegistry, error) { return NewVirtualRegistry(c) })
	return r
}
func NewProductionAudioBackendRegistry() *AudioBackendRegistry { return NewAudioBackendRegistry() }
func (r *AudioBackendRegistry) Register(name string, f AudioBackendFactory) error {
	if f == nil {
		return fmt.Errorf("audio backend factory is nil")
	}
	if _, err := NewDeviceID(name, "registration"); err != nil {
		return err
	}
	if _, ok := r.factories[name]; ok {
		return fmt.Errorf("audio backend %q is already registered", name)
	}
	r.factories[name] = f
	return nil
}
func (r *AudioBackendRegistry) New(name string, c VirtualBackendConfig) (DeviceRegistry, error) {
	f := r.factories[name]
	if f == nil {
		return nil, fmt.Errorf("audio backend %q is not registered", name)
	}
	return f(c)
}
func (r *AudioBackendRegistry) Names() []string {
	out := make([]string, 0, len(r.factories))
	for n := range r.factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
func NewRegisteredAudioBackend(name string, c VirtualBackendConfig) (DeviceRegistry, error) {
	return NewAudioBackendRegistry().New(name, c)
}
