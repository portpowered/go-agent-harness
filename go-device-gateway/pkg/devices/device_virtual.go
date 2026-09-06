package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
)

const VirtualBackendName = "virtual"

var ErrDeviceLost, ErrVirtualNoLoopback = errors.New("device lost"), errors.New("virtual device has no loopback")

type DeviceLostError struct {
	ID        DeviceID
	Direction Direction
}

func lostMessage(id DeviceID, direction Direction) string {
	return fmt.Sprintf("device %q (%s) was lost", id, direction)
}
func (e *DeviceLostError) Error() string { return lostMessage(e.ID, e.Direction) }
func (*DeviceLostError) Unwrap() error   { return ErrDeviceLost }

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
	// LoopbackDelaySamples and LoopbackImpulse model the device-owned acoustic
	// path from an output endpoint to its paired input endpoint. An empty
	// impulse is an identity path. The delay is inserted once per pair and the
	// FIR history is preserved across writes, so tests exercise streaming
	// device behavior rather than pre-transforming file or provider input.
	LoopbackDelaySamples int
	LoopbackImpulse      []float64
}
type VirtualBackendConfig struct {
	Devices  []VirtualDeviceConfig
	Defaults map[Direction]string
	// RecordPCM retains owned copies of typed device writes and reads for
	// deterministic test evidence. Raw byte traffic is intentionally excluded.
	RecordPCM bool
}

type VirtualPCMObservation struct {
	Sequence  int
	DeviceID  DeviceID
	Direction Direction
	Operation string
	Format    audio.DeviceFormat
	Samples   []int16
}
type virtualDevice struct {
	Device
	spec   VirtualDeviceConfig
	pair   *virtualPair
	opened int
}

func bad(id DeviceID, reason string) error { return &InvalidDeviceError{ID: id, Reason: reason} }
func DefaultVirtualBackendConfig() VirtualBackendConfig {
	return VirtualBackendConfig{
		Devices: []VirtualDeviceConfig{
			{ID: "input", Name: "Virtual Input", Direction: DirectionInput, LoopbackID: "output"},
			{ID: "output", Name: "Virtual Output", Direction: DirectionOutput, LoopbackID: "input"},
			{ID: "exclusive", Name: "Virtual Exclusive Output", Direction: DirectionOutput, Exclusive: true},
		},
		Defaults: map[Direction]string{DirectionInput: "input", DirectionOutput: "output"},
	}
}
func virtualID(ref string) (DeviceID, error) {
	if strings.ContainsRune(ref, ':') && !strings.HasPrefix(ref, VirtualBackendName+":") {
		return "", bad("", "invalid virtual device reference")
	}
	return NewDeviceID(VirtualBackendName, strings.TrimPrefix(ref, VirtualBackendName+":"))
}
func compatible(a, b []VirtualCapability) bool {
	return slices.ContainsFunc(a, func(c VirtualCapability) bool { return slices.Contains(b, c) })
}

func virtualDeviceFormats(capabilities []VirtualCapability) []audio.DeviceFormat {
	formats := make([]audio.DeviceFormat, 0, len(capabilities))
	for _, capability := range capabilities {
		encoding := capability.Format
		if encoding == "" {
			encoding = audio.DeviceEncodingPCM16
		}
		formats = append(formats, audio.DeviceFormat{
			SampleRate: capability.SampleRate,
			Channels:   capability.Channels,
			BitDepth:   capability.BitDepth,
			Encoding:   encoding,
		})
	}
	return formats
}

func containsDeviceFormat(formats []audio.DeviceFormat, want audio.DeviceFormat) bool {
	return slices.ContainsFunc(formats, func(format audio.DeviceFormat) bool { return format.Equal(want) })
}
func makeVirtualDevice(s VirtualDeviceConfig) (virtualDevice, error) {
	id, err := virtualID(s.ID)
	if err != nil {
		return virtualDevice{}, err
	}
	d, err := NewDevice(VirtualBackendName, strings.TrimPrefix(s.ID, VirtualBackendName+":"), s.Name, s.Direction)
	if err != nil {
		return virtualDevice{}, err
	}
	caps := append([]VirtualCapability(nil), s.Capabilities...)
	if len(caps) == 0 {
		caps = []VirtualCapability{{SampleRate: audio.SampleRate, Channels: audio.Channels, BitDepth: 16, Format: "pcm16"}}
	}
	var loopback DeviceID
	if s.LoopbackID != "" {
		loopback, err = virtualID(s.LoopbackID)
		if err != nil {
			return virtualDevice{}, bad(id, err.Error())
		}
	}
	s.Capabilities, s.LoopbackID = caps, loopback
	return virtualDevice{Device: d, spec: s}, nil
}

type virtualPair struct {
	open     [2]int
	seen     [2]bool
	queue    [][]byte
	playback *audio.PlaybackQueue
	changed  chan struct{}
	coupling virtualLoopbackCoupling
}

type virtualLoopbackCoupling struct {
	delayLine []int16
	impulse   []float64
	history   []int16
}

func (p *virtualPair) signal() { close(p.changed); p.changed = make(chan struct{}) }

type VirtualRegistry struct {
	mu           sync.Mutex
	devices      map[DeviceID]*virtualDevice
	defaults     map[Direction]DeviceID
	observations DeviceRegistryObservations
	recordPCM    bool
	pcmSequence  int
	pcm          []VirtualPCMObservation
	pcmChanged   chan struct{}
}

func NewVirtualRegistry(c VirtualBackendConfig) (*VirtualRegistry, error) {
	if len(c.Devices) == 0 {
		return nil, bad("", "virtual backend needs at least one device")
	}
	r := &VirtualRegistry{devices: map[DeviceID]*virtualDevice{}, defaults: map[Direction]DeviceID{}, recordPCM: c.RecordPCM, pcmChanged: make(chan struct{})}
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
	for _, a := range r.devices {
		if a.spec.LoopbackID == "" || a.pair != nil {
			continue
		}
		b := r.devices[a.spec.LoopbackID]
		if b == nil {
			return nil, bad(a.ID, fmt.Sprintf("loopback device %q is outside the topology", a.spec.LoopbackID))
		}
		if a.Direction == b.Direction || !compatible(a.spec.Capabilities, b.spec.Capabilities) {
			return nil, bad(a.ID, "loopback devices have incompatible directions or capabilities")
		}
		output := a
		if output.Direction != DirectionOutput {
			output = b
		}
		if output.spec.LoopbackDelaySamples < 0 {
			return nil, bad(output.ID, "loopback delay samples must not be negative")
		}
		impulse := append([]float64(nil), output.spec.LoopbackImpulse...)
		for _, coefficient := range impulse {
			if math.IsNaN(coefficient) || math.IsInf(coefficient, 0) {
				return nil, bad(output.ID, "loopback impulse coefficients must be finite")
			}
		}
		p := &virtualPair{changed: make(chan struct{}), coupling: virtualLoopbackCoupling{
			delayLine: make([]int16, output.spec.LoopbackDelaySamples),
			impulse:   impulse,
		}}
		b.spec.LoopbackID = a.ID
		a.pair, b.pair = p, p
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
	if v := r.devices[id]; v != nil {
		return append([]VirtualCapability(nil), v.spec.Capabilities...), nil
	}
	return nil, NewDeviceNotFoundError(id)
}
func (r *VirtualRegistry) Default(d Direction) (Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ValidateDirection(d); err != nil {
		return Device{}, err
	}
	if v := r.devices[r.defaults[d]]; v != nil {
		return v.Device, nil
	}
	return Device{}, NewNoDefaultDeviceError(d)
}
func side(d Direction) int                                 { return map[Direction]int{DirectionOutput: 1}[d] }
func fail(mu *sync.Mutex, err error) (*virtualPair, error) { mu.Unlock(); return nil, err }
func (r *VirtualRegistry) Open(id DeviceID) (OpenedDevice, error) {
	return r.openWithFormat(id, audio.DefaultDeviceFormat())
}

// OpenWithFormat opens an exact virtual endpoint at the requested PCM
// format. Virtual capabilities are intentionally exact so a loopback can
// prove that no hidden sample-rate conversion occurred.
func (r *VirtualRegistry) OpenWithFormat(id DeviceID, format audio.DeviceFormat) (OpenedDevice, error) {
	return r.openWithFormat(id, format)
}

func (r *VirtualRegistry) openWithFormat(id DeviceID, format audio.DeviceFormat) (OpenedDevice, error) {
	if err := format.Validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.devices[id]
	if v == nil {
		return nil, NewDeviceNotFoundError(id)
	}
	available := virtualDeviceFormats(v.spec.Capabilities)
	if !containsDeviceFormat(available, format) {
		return nil, &DeviceFormatError{ID: id, Direction: v.Direction, Requested: format, Available: available}
	}
	if v.spec.Exclusive && v.opened != 0 {
		return nil, NewDeviceInUseError(id)
	}
	v.opened++
	r.observations.OpenCount++
	if p := v.pair; p != nil {
		i := side(v.Direction)
		p.open[i]++
		p.seen[i] = true
	}
	return &VirtualStream{registry: r, device: v, format: format}, nil
}
func (r *VirtualRegistry) Observations() DeviceRegistryObservations {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.observations
}

// PCMObservations returns deep-copied typed PCM evidence in device operation
// order. Recording is opt-in through VirtualBackendConfig.RecordPCM.
func (r *VirtualRegistry) PCMObservations() []VirtualPCMObservation {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]VirtualPCMObservation, len(r.pcm))
	for index, observation := range r.pcm {
		out[index] = observation
		out[index].Samples = append([]int16(nil), observation.Samples...)
	}
	return out
}

// WaitForPCMObservations waits until the opt-in recorder has retained at
// least count typed operations. It is a deterministic mock-device
// synchronization seam; production backends do not implement it.
func (r *VirtualRegistry) WaitForPCMObservations(ctx context.Context, count int) ([]VirtualPCMObservation, error) {
	if r == nil || count <= 0 {
		return r.PCMObservations(), nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		r.mu.Lock()
		if len(r.pcm) >= count {
			r.mu.Unlock()
			return r.PCMObservations(), nil
		}
		changed := r.pcmChanged
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (r *VirtualRegistry) recordPCMLocked(device *virtualDevice, format audio.DeviceFormat, operation string, samples []int16) {
	if !r.recordPCM || device == nil || len(samples) == 0 {
		return
	}
	r.pcmSequence++
	r.pcm = append(r.pcm, VirtualPCMObservation{
		Sequence: r.pcmSequence, DeviceID: device.ID, Direction: device.Direction,
		Operation: operation, Format: format, Samples: append([]int16(nil), samples...),
	})
	close(r.pcmChanged)
	r.pcmChanged = make(chan struct{})
}
func (r *VirtualRegistry) RemoveDevice(id DeviceID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.devices[id]
	if v == nil {
		return false
	}
	delete(r.devices, id)
	if p := v.pair; p != nil {
		p.signal()
	}
	return true
}

type VirtualStream struct {
	registry *VirtualRegistry
	device   *virtualDevice
	format   audio.DeviceFormat
	closed   bool
}

// DeviceFormat reports the exact virtual capability selected at open time.
func (s *VirtualStream) DeviceFormat() audio.DeviceFormat {
	if s == nil {
		return audio.DeviceFormat{}
	}
	return s.format
}

func (s *VirtualStream) lock(op string) (*virtualPair, error) {
	r := s.registry
	r.mu.Lock()
	if s.closed {
		return fail(&r.mu, &audio.ClosedError{Operation: op, Path: string(s.device.ID)})
	}
	if r.devices[s.device.ID] == nil {
		return fail(&r.mu, &DeviceLostError{ID: s.device.ID, Direction: s.device.Direction})
	}
	p := s.device.pair
	if p == nil {
		return fail(&r.mu, ErrVirtualNoLoopback)
	}
	i, other := side(s.device.Direction), s.device.spec.LoopbackID
	if r.devices[other] == nil {
		return fail(&r.mu, &DeviceLostError{ID: s.device.ID, Direction: s.device.Direction})
	}
	if p.seen[1-i] && p.open[1-i] == 0 {
		return fail(&r.mu, &audio.ClosedError{Operation: op, Path: string(other)})
	}
	return p, nil
}
func (s *VirtualStream) Write(ctx context.Context, frame []byte) error {
	if err := audio.ContextError(ctx); err != nil {
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

// WriteFrame is the typed PCM16 path used by DeviceSink. The raw Write
// method remains available for transport/loopback tests that intentionally
// exercise arbitrary byte payloads.
func (s *VirtualStream) WriteFrame(ctx context.Context, frame []int16) error {
	if err := audio.ContextError(ctx); err != nil {
		return err
	}
	if err := audio.ValidateFrame("write", frame); err != nil {
		return err
	}
	return s.WriteSamples(ctx, frame)
}

// WriteSamples queues an arbitrary non-empty PCM16 chunk for playback.
func (s *VirtualStream) WriteSamples(ctx context.Context, samples []int16) error {
	if err := audio.ContextError(ctx); err != nil {
		return err
	}
	if len(samples) == 0 {
		return nil
	}
	p, err := s.lock("write")
	if err != nil {
		return err
	}
	defer s.registry.mu.Unlock()
	s.registry.recordPCMLocked(s.device, s.format, "write", samples)
	p.playback = ensureVirtualPlaybackQueue(p.playback, s.format)
	p.playback.Enqueue(p.coupling.apply(samples))
	p.signal()
	return nil
}
func (s *VirtualStream) Read(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := audio.ContextError(ctx); err != nil {
			return nil, err
		}
		p, err := s.lock("read")
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
		case <-ctx.Done():
			return nil, audio.ContextError(ctx)
		case <-changed:
		}
	}
}

// ReadFrame is the typed PCM16 path paired with WriteFrame. It waits for a
// complete legacy frame so DeviceSource preserves its existing frame API.
func (s *VirtualStream) ReadFrame(ctx context.Context, frame []int16) error {
	if err := audio.ContextError(ctx); err != nil {
		return err
	}
	if err := audio.ValidateFrame("read", frame); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := audio.ContextError(ctx); err != nil {
			return err
		}
		p, err := s.lock("read")
		if err != nil {
			return err
		}
		p.playback = ensureVirtualPlaybackQueue(p.playback, s.format)
		if p.playback.Snapshot().QueuedSamples >= len(frame) {
			p.playback.ReadInto(frame)
			p.signal()
			s.registry.recordPCMLocked(s.device, s.format, "read", frame)
			s.registry.mu.Unlock()
			return nil
		}
		changed := p.changed
		s.registry.mu.Unlock()
		select {
		case <-ctx.Done():
			return audio.ContextError(ctx)
		case <-changed:
		}
	}
}

// ReadSamples reads an exact arbitrary-sized PCM16 chunk from the paired
// playback queue. Device callbacks and functional tests use it to preserve a
// response-final remainder that is smaller than the legacy FrameSize.
func (s *VirtualStream) ReadSamples(ctx context.Context, samples []int16) error {
	if err := audio.ContextError(ctx); err != nil {
		return err
	}
	if len(samples) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := audio.ContextError(ctx); err != nil {
			return err
		}
		p, err := s.lock("read")
		if err != nil {
			return err
		}
		p.playback = ensureVirtualPlaybackQueue(p.playback, s.format)
		if p.playback.Snapshot().QueuedSamples >= len(samples) {
			p.playback.ReadInto(samples)
			p.signal()
			s.registry.recordPCMLocked(s.device, s.format, "read", samples)
			s.registry.mu.Unlock()
			return nil
		}
		changed := p.changed
		s.registry.mu.Unlock()
		select {
		case <-ctx.Done():
			return audio.ContextError(ctx)
		case <-changed:
		}
	}
}

// WaitForPlaybackCapacity gives the virtual backend the same producer
// backpressure contract as callback-driven native devices. A paired reader is
// the deterministic virtual device clock that advances playback.
func (s *VirtualStream) WaitForPlaybackCapacity(ctx context.Context, samples int) error {
	if s == nil || samples <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	low, high, err := audio.PlaybackQueueWatermarks(s.format)
	if err != nil {
		return err
	}
	if samples > high {
		return fmt.Errorf("%w: incoming playback chunk %d exceeds high watermark %d", audio.ErrInvalidPlaybackQueue, samples, high)
	}
	throttled := false
	for {
		if err := audio.ContextError(ctx); err != nil {
			return err
		}
		p, err := s.lock("wait for playback capacity")
		if err != nil {
			return err
		}
		p.playback = ensureVirtualPlaybackQueue(p.playback, s.format)
		queued := p.playback.Snapshot().QueuedSamples
		if (!throttled && queued+samples <= high) || (throttled && queued <= low && queued+samples <= high) {
			s.registry.mu.Unlock()
			return nil
		}
		throttled = true
		changed := p.changed
		s.registry.mu.Unlock()
		select {
		case <-ctx.Done():
			return audio.ContextError(ctx)
		case <-changed:
		}
	}
}

func (c *virtualLoopbackCoupling) apply(samples []int16) []int16 {
	if len(samples) == 0 {
		return nil
	}
	impulse := c.impulse
	if len(impulse) == 0 {
		impulse = []float64{1}
	}
	transformed := make([]int16, len(samples))
	historyLen := len(impulse) - 1
	for index := range samples {
		value := 0.0
		for tap, coefficient := range impulse {
			sourceIndex := index - tap
			var source int16
			if sourceIndex >= 0 {
				source = samples[sourceIndex]
			} else if historyIndex := len(c.history) + sourceIndex; historyIndex >= 0 {
				source = c.history[historyIndex]
			}
			value += float64(source) * coefficient
		}
		if value > math.MaxInt16 {
			value = math.MaxInt16
		} else if value < math.MinInt16 {
			value = math.MinInt16
		}
		transformed[index] = int16(math.Round(value))
	}
	if historyLen > 0 {
		joined := append(append([]int16(nil), c.history...), samples...)
		if len(joined) > historyLen {
			joined = joined[len(joined)-historyLen:]
		}
		c.history = joined
	}
	if len(c.delayLine) == 0 {
		return transformed
	}
	// A physical delay is state carried between clocked device callbacks, not
	// one oversized first write. Returning exactly one output sample per input
	// sample preserves the configured latency even when it exceeds the bounded
	// playback queue's capacity.
	combined := make([]int16, 0, len(c.delayLine)+len(transformed))
	combined = append(combined, c.delayLine...)
	combined = append(combined, transformed...)
	output := append([]int16(nil), combined[:len(transformed)]...)
	c.delayLine = append(c.delayLine[:0], combined[len(transformed):]...)
	return output
}

func ensureVirtualPlaybackQueue(queue *audio.PlaybackQueue, format audio.DeviceFormat) *audio.PlaybackQueue {
	if queue != nil {
		return queue
	}
	queue, _ = audio.PlaybackQueueForFormat(format)
	return queue
}

// PlaybackStats exposes the typed playback queue used by DeviceSink. Raw
// byte writes intentionally do not participate in this PCM observation.
func (s *VirtualStream) PlaybackStats() audio.PlaybackQueueStats {
	if s == nil || s.registry == nil || s.device == nil {
		return audio.PlaybackQueueStats{}
	}
	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	if s.device.pair == nil {
		return audio.EmptyPlaybackQueueStats(s.format)
	}
	if s.device.pair.playback == nil {
		return audio.EmptyPlaybackQueueStats(s.format)
	}
	return s.device.pair.playback.Snapshot()
}

func (s *VirtualStream) SetPlaybackRenderObserver(observer audio.PlaybackRenderObserver) {
	if s == nil || s.registry == nil || s.device == nil || s.device.Direction != DirectionOutput {
		return
	}
	s.registry.mu.Lock()
	if s.device.pair != nil {
		s.device.pair.playback = ensureVirtualPlaybackQueue(s.device.pair.playback, s.format)
		s.device.pair.playback.SetRenderObserver(observer)
	}
	s.registry.mu.Unlock()
}

// DiscardPlayback removes typed PCM samples waiting for the virtual device's
// paired read callback.
func (s *VirtualStream) DiscardPlayback() int {
	if s == nil || s.registry == nil || s.device == nil {
		return 0
	}
	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	if s.device.pair == nil || s.device.pair.playback == nil {
		return 0
	}
	discarded := s.device.pair.playback.Discard()
	if discarded > 0 {
		s.device.pair.signal()
	}
	return discarded
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
	if p := s.device.pair; p != nil {
		i := side(s.device.Direction)
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
