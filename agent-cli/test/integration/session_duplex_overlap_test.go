package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	v8OverlapTick        uint64 = 7
	v8OverlapTickLimit   uint64 = 8
	v8TickDuration              = 10 * time.Millisecond
	v8CommandMaxDuration        = time.Second
	v8RunTimeout                = 2 * time.Second
	v8VADThreshold              = 300.0
	v8TurnBound                 = 2
	v8PCMFrameBytes             = audio.FrameSize * 2
	v8MultiTurnCount            = 3
	v8MultiTurnFinalTick uint64 = 10
)

const (
	v8HarnessAInstruction = "harness-A: answer with the amber profile"
	v8HarnessBInstruction = "harness-B: answer with the cobalt profile"
)

// v8Crossing is the transport-level record. Emitted is what the CLI wrote to
// its raw --audio-out stream; delivered is what the peer consumed from its
// raw --audio-in stream. Keeping both makes the silence mutation observable
// without ever using transcript text as a bridge.
type v8Crossing struct {
	Sequence  int
	Direction string
	TurnKey   string
	Turn      int
	Schedule  int
	Tick      uint64
	Timestamp time.Time
	Emitted   []byte
	Delivered []byte
}

type v8RuntimeOutputSink interface {
	acceptRuntimeOutput(services.SessionRuntimeObservation)
}

type v8RuntimeInputSink interface {
	acceptRuntimeInput(services.SessionRuntimeObservation)
}

type v8CrossingCoordinator struct {
	overlapTick uint64

	mu            sync.Mutex
	nextDirection string
	crossings     []v8Crossing
	aToBReady     chan struct{}
	deliveryReady chan struct{}
	abort         chan struct{}
	abortOnce     sync.Once
	deliveryOnce  sync.Once
}

func newV8CrossingCoordinator() *v8CrossingCoordinator {
	return &v8CrossingCoordinator{
		overlapTick:   v8OverlapTick,
		nextDirection: "A-to-B",
		aToBReady:     make(chan struct{}),
		deliveryReady: make(chan struct{}),
		abort:         make(chan struct{}),
	}
}

func (c *v8CrossingCoordinator) abortRun() {
	c.abortOnce.Do(func() { close(c.abort) })
}

func (c *v8CrossingCoordinator) releaseDelivery() {
	c.deliveryOnce.Do(func() { close(c.deliveryReady) })
}

func (c *v8CrossingCoordinator) record(direction string, tick uint64, timestamp time.Time, emitted, delivered []byte) (v8Crossing, error) {
	if direction == "B-to-A" {
		select {
		case <-c.aToBReady:
		case <-c.abort:
			return v8Crossing{}, context.Canceled
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.abort:
		return v8Crossing{}, context.Canceled
	default:
	}
	if direction != c.nextDirection {
		return v8Crossing{}, fmt.Errorf("crossing order %s arrived while expecting %s", direction, c.nextDirection)
	}
	if tick != c.overlapTick {
		return v8Crossing{}, fmt.Errorf("crossing %s observed at logical tick %d, want overlap tick %d", direction, tick, c.overlapTick)
	}
	if timestamp.IsZero() {
		return v8Crossing{}, fmt.Errorf("crossing %s has no runtime timestamp", direction)
	}

	crossing := v8Crossing{
		Sequence:  len(c.crossings) + 1,
		Direction: direction,
		Tick:      tick,
		Timestamp: timestamp,
		Emitted:   append([]byte(nil), emitted...),
		Delivered: append([]byte(nil), delivered...),
	}
	c.crossings = append(c.crossings, crossing)
	if direction == "A-to-B" {
		c.nextDirection = "B-to-A"
		close(c.aToBReady)
	}
	return crossing, nil
}

func (c *v8CrossingCoordinator) snapshot() []v8Crossing {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]v8Crossing, len(c.crossings))
	for i, crossing := range c.crossings {
		out[i] = crossing
		out[i].Emitted = append([]byte(nil), crossing.Emitted...)
		out[i].Delivered = append([]byte(nil), crossing.Delivered...)
	}
	return out
}

type v8MultiTurnScheduleEntry struct {
	Turn        int
	Tick        uint64
	Direction   string
	Overlapping bool
}

// v8MultiTurnSchedule is the literal logical schedule used by the extended
// proof. The first two turns open both directional output intervals at the
// same tick. Turn three is intentionally serialized A then B so the same
// bridge path proves both multiplexed and ordinary boundaries.
func v8MultiTurnSchedule() []v8MultiTurnScheduleEntry {
	return []v8MultiTurnScheduleEntry{
		{Turn: 1, Tick: 7, Direction: "A-to-B", Overlapping: true},
		{Turn: 1, Tick: 7, Direction: "B-to-A", Overlapping: true},
		{Turn: 2, Tick: 8, Direction: "A-to-B", Overlapping: true},
		{Turn: 2, Tick: 8, Direction: "B-to-A", Overlapping: true},
		{Turn: 3, Tick: 9, Direction: "A-to-B"},
		{Turn: 3, Tick: 10, Direction: "B-to-A"},
	}
}

type v8MultiTurnCoordinator struct {
	clock  *clock.Deterministic
	base   time.Time
	groups []v8MultiTurnScheduleEntry

	mu               sync.Mutex
	current          int
	completionCursor int
	sequence         int
	crossings        []v8Crossing
	completed        []bool
	changed          chan struct{}
	abort            chan struct{}
	abortOnce        sync.Once
}

func newV8MultiTurnCoordinator(logicalClock *clock.Deterministic, base time.Time) *v8MultiTurnCoordinator {
	return &v8MultiTurnCoordinator{
		clock:     logicalClock,
		base:      base,
		groups:    v8MultiTurnSchedule(),
		completed: make([]bool, len(v8MultiTurnSchedule())),
		changed:   make(chan struct{}),
		abort:     make(chan struct{}),
	}
}

func (c *v8MultiTurnCoordinator) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *v8MultiTurnCoordinator) abortRun() {
	c.abortOnce.Do(func() {
		close(c.abort)
		c.mu.Lock()
		c.notifyLocked()
		c.mu.Unlock()
	})
}

func (c *v8MultiTurnCoordinator) record(direction string, tick uint64, timestamp time.Time, emitted, delivered []byte) (v8Crossing, error) {
	for {
		c.mu.Lock()
		select {
		case <-c.abort:
			c.mu.Unlock()
			return v8Crossing{}, context.Canceled
		default:
		}
		if c.current >= len(c.groups) {
			c.mu.Unlock()
			return v8Crossing{}, fmt.Errorf("multi-turn output %s arrived after the complete schedule", direction)
		}
		entry := c.groups[c.current]
		if c.current > 0 && entry.Tick != c.groups[c.current-1].Tick && c.completionCursor < c.current {
			wait := c.changed
			c.mu.Unlock()
			select {
			case <-wait:
			case <-c.abort:
				return v8Crossing{}, context.Canceled
			}
			continue
		}
		if entry.Direction != direction {
			wait := c.changed
			c.mu.Unlock()
			select {
			case <-wait:
			case <-c.abort:
				return v8Crossing{}, context.Canceled
			}
			continue
		}
		if tick != entry.Tick {
			c.mu.Unlock()
			return v8Crossing{}, fmt.Errorf("multi-turn %s turn %d observed at logical tick %d, want %d", direction, entry.Turn, tick, entry.Tick)
		}
		wantTimestamp := c.base.Add(time.Duration(entry.Tick) * v8TickDuration)
		if !timestamp.Equal(wantTimestamp) {
			c.mu.Unlock()
			return v8Crossing{}, fmt.Errorf("multi-turn %s turn %d timestamp=%s, want deterministic timestamp %s", direction, entry.Turn, timestamp.Format(time.RFC3339Nano), wantTimestamp.Format(time.RFC3339Nano))
		}

		c.sequence++
		crossing := v8Crossing{
			Sequence:  c.sequence,
			Direction: direction,
			TurnKey:   v8MultiTurnKey(direction, entry.Turn),
			Turn:      entry.Turn,
			Schedule:  c.current,
			Tick:      tick,
			Timestamp: timestamp,
			Emitted:   append([]byte(nil), emitted...),
			Delivered: append([]byte(nil), delivered...),
		}
		c.crossings = append(c.crossings, crossing)
		c.current++
		c.notifyLocked()
		c.mu.Unlock()
		return crossing, nil
	}
}

func (c *v8MultiTurnCoordinator) complete(crossing v8Crossing) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.abort:
		return context.Canceled
	default:
	}
	if crossing.Schedule < 0 || crossing.Schedule >= len(c.groups) {
		return fmt.Errorf("multi-turn %s turn %d completed after the schedule", crossing.Direction, crossing.Turn)
	}
	entry := c.groups[crossing.Schedule]
	if entry.Direction != crossing.Direction || entry.Turn != crossing.Turn {
		return fmt.Errorf("multi-turn completion %s turn %d does not match scheduled %s turn %d", crossing.Direction, crossing.Turn, entry.Direction, entry.Turn)
	}
	if c.completed[crossing.Schedule] {
		return fmt.Errorf("multi-turn %s turn %d completed more than once", crossing.Direction, crossing.Turn)
	}
	c.completed[crossing.Schedule] = true
	// A schedule entry is a single directional interval. For the first four
	// entries, the adjacent entry has the same tick and direction alternates;
	// completion advances the shared clock only after both intervals have been
	// delivered. This keeps the two overlap pairs at one exact logical tick.
	for c.completionCursor < len(c.groups) && c.completed[c.completionCursor] {
		c.completionCursor++
	}
	if c.completionCursor < len(c.groups) && c.completionCursor > 0 && c.groups[c.completionCursor].Tick != c.groups[c.completionCursor-1].Tick {
		c.clock.AdvanceTo(c.groups[c.completionCursor].Tick)
	}
	c.notifyLocked()
	return nil
}

func (c *v8MultiTurnCoordinator) waitForCompletion(schedule int) error {
	for {
		c.mu.Lock()
		if schedule < 0 || schedule >= len(c.groups) {
			c.mu.Unlock()
			return fmt.Errorf("multi-turn completion wait has invalid schedule %d", schedule)
		}
		if c.completed[schedule] {
			c.mu.Unlock()
			return nil
		}
		wait := c.changed
		c.mu.Unlock()
		select {
		case <-wait:
		case <-c.abort:
			return context.Canceled
		}
	}
}

func (c *v8MultiTurnCoordinator) waitForRecord(schedule int) error {
	for {
		c.mu.Lock()
		if schedule < 0 || schedule >= len(c.groups) {
			c.mu.Unlock()
			return fmt.Errorf("multi-turn record wait has invalid schedule %d", schedule)
		}
		if c.current > schedule {
			c.mu.Unlock()
			return nil
		}
		wait := c.changed
		c.mu.Unlock()
		select {
		case <-wait:
		case <-c.abort:
			return context.Canceled
		}
	}
}

func (c *v8MultiTurnCoordinator) snapshot() []v8Crossing {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]v8Crossing, len(c.crossings))
	for i, crossing := range c.crossings {
		out[i] = crossing
		out[i].Emitted = append([]byte(nil), crossing.Emitted...)
		out[i].Delivered = append([]byte(nil), crossing.Delivered...)
	}
	return out
}

func v8MultiTurnKey(direction string, turn int) string {
	harness := strings.TrimSuffix(direction, "-to-B")
	if direction == "B-to-A" {
		harness = "B"
	}
	return fmt.Sprintf("%s-turn-%d", harness, turn)
}

type v8MultiTurnBridgePacket struct {
	eof      bool
	crossing v8Crossing
	ack      chan struct{}
}

type v8RuntimeInputEvent struct {
	observation services.SessionRuntimeObservation
	// release is closed only after the bridge has recorded the accepted input
	// and completed its scheduled crossing, keeping a subsequent replay event
	// from observing the shared clock too early.
	release chan struct{}
}

type v8MultiTurnBridge struct {
	coordinator *v8MultiTurnCoordinator
	direction   string
	sender      *v8RecordingView
	receiver    *v8RecordingView
	eofReady    <-chan struct{}
	runtimeOut  chan services.SessionRuntimeObservation
	runtimeIn   chan v8RuntimeInputEvent

	packets chan v8MultiTurnBridgePacket
	mu      sync.Mutex
	writes  int
	eofRead bool
	eofSeen chan struct{}
	eofOnce sync.Once
}

func newV8MultiTurnBridge(coordinator *v8MultiTurnCoordinator, direction string, sender, receiver *v8RecordingView, eofReady <-chan struct{}) *v8MultiTurnBridge {
	return &v8MultiTurnBridge{
		coordinator: coordinator,
		direction:   direction,
		sender:      sender,
		receiver:    receiver,
		eofReady:    eofReady,
		runtimeOut:  make(chan services.SessionRuntimeObservation, 1),
		runtimeIn:   make(chan v8RuntimeInputEvent),
		packets:     make(chan v8MultiTurnBridgePacket, 2),
		eofSeen:     make(chan struct{}),
	}
}

func (b *v8MultiTurnBridge) acceptRuntimeOutput(observation services.SessionRuntimeObservation) {
	select {
	case b.runtimeOut <- observation:
	case <-b.coordinator.abort:
	}
}

func (b *v8MultiTurnBridge) nextRuntimeOutput() (services.SessionRuntimeObservation, error) {
	select {
	case observation := <-b.runtimeOut:
		return observation, nil
	case <-b.coordinator.abort:
		return services.SessionRuntimeObservation{}, context.Canceled
	}
}

func (b *v8MultiTurnBridge) acceptRuntimeInput(observation services.SessionRuntimeObservation) {
	event := v8RuntimeInputEvent{
		observation: observation,
		release:     make(chan struct{}),
	}
	select {
	case b.runtimeIn <- event:
		select {
		case <-event.release:
		case <-b.coordinator.abort:
		}
	case <-b.coordinator.abort:
	}
}

func (b *v8MultiTurnBridge) waitForRuntimeInput(crossing v8Crossing) error {
	var event v8RuntimeInputEvent
	select {
	case event = <-b.runtimeIn:
	case <-b.coordinator.abort:
		return context.Canceled
	}
	observation := event.observation
	if observation.Tick != crossing.Tick || !observation.Timestamp.Equal(crossing.Timestamp) || !bytes.Equal(observation.Payload, crossing.Emitted) {
		close(event.release)
		return fmt.Errorf("%s runtime input observation does not match turn %d at tick %d", b.direction, crossing.Turn, crossing.Tick)
	}
	err := b.coordinator.complete(crossing)
	close(event.release)
	return err
}

func (b *v8MultiTurnBridge) write(data []byte) (int, error) {
	if len(data) != v8PCMFrameBytes {
		return 0, fmt.Errorf("%s emitted %d PCM bytes, want one %d-byte frame", b.direction, len(data), v8PCMFrameBytes)
	}
	outputObservation, err := b.nextRuntimeOutput()
	if err != nil {
		return 0, err
	}
	if outputObservation.Kind != services.SessionRuntimeObservationAudioOutput {
		return 0, fmt.Errorf("%s runtime observation kind = %q, want %q", b.direction, outputObservation.Kind, services.SessionRuntimeObservationAudioOutput)
	}
	emitted := append([]byte(nil), data...)
	if !bytes.Equal(outputObservation.Payload, emitted) {
		return 0, fmt.Errorf("%s runtime audio output payload differs from the CLI writer payload: runtime hash=%s writer hash=%s", b.direction, v8PCMHash(outputObservation.Payload), v8PCMHash(emitted))
	}
	crossing, err := b.coordinator.record(b.direction, outputObservation.Tick, outputObservation.Timestamp, emitted, emitted)
	if err != nil {
		return 0, err
	}
	if crossing.Direction == "A-to-B" && crossing.Schedule < len(v8MultiTurnSchedule())-2 && v8MultiTurnSchedule()[crossing.Schedule].Overlapping {
		// Each overlapping replay records both server output intervals before
		// either client sends the peer AUDIO.DELTA. This preserves the strict
		// capture order while retaining the equal-tick overlap.
		if err := b.coordinator.waitForRecord(crossing.Schedule + 1); err != nil {
			return 0, err
		}
	}
	b.sender.record(crossing, emitted)
	ack := make(chan struct{})
	select {
	case b.packets <- v8MultiTurnBridgePacket{crossing: crossing, ack: ack}:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	// Holding the output boundary until the peer has consumed the packet makes
	// the overlap observable: the next user's PCM starts while this response
	// is still in the bridge, without sending RESPONSE.CANCEL. The final
	// response also waits for peer input acceptance; its EOF is released only
	// after the peer has completed its second turn so the raw input commit
	// cannot preempt the queued response events.
	finalSchedule := len(v8MultiTurnSchedule()) - 1
	if crossing.Schedule == finalSchedule {
		select {
		case <-ack:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
		if err := b.waitForRuntimeInput(crossing); err != nil {
			return 0, err
		}
		b.mu.Lock()
		b.writes++
		writes := b.writes
		b.mu.Unlock()
		if writes == v8MultiTurnCount {
			if err := b.waitForEOF(); err != nil {
				return 0, err
			}
			select {
			case b.packets <- v8MultiTurnBridgePacket{eof: true}:
			case <-b.coordinator.abort:
				return 0, context.Canceled
			}
			select {
			case <-b.eofSeen:
			case <-b.coordinator.abort:
				return 0, context.Canceled
			}
		}
		return len(data), nil
	}
	select {
	case <-ack:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	if err := b.waitForRuntimeInput(crossing); err != nil {
		return 0, err
	}
	// Do not let A's third server response take its runtime clock snapshot
	// before B's second directional interval has completed. The replay streams
	// intentionally expose those independent boundaries concurrently, so this
	// release keeps the shared deterministic clock at the scheduled tick rather
	// than asking the bridge to repair a stale observation after the fact.
	switch crossing.Schedule {
	case 0:
		if err := b.coordinator.waitForCompletion(1); err != nil {
			return 0, err
		}
	case 1:
		if err := b.coordinator.waitForCompletion(0); err != nil {
			return 0, err
		}
	case 2:
		if err := b.coordinator.waitForCompletion(3); err != nil {
			return 0, err
		}
	case 3:
		if err := b.coordinator.waitForCompletion(2); err != nil {
			return 0, err
		}
	}
	b.mu.Lock()
	b.writes++
	writes := b.writes
	b.mu.Unlock()
	if writes == v8MultiTurnCount {
		if err := b.waitForEOF(); err != nil {
			return 0, err
		}
		select {
		case b.packets <- v8MultiTurnBridgePacket{eof: true}:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
		select {
		case <-b.eofSeen:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
	}
	return len(data), nil
}

func (b *v8MultiTurnBridge) waitForEOF() error {
	if b.eofReady == nil {
		return nil
	}
	select {
	case <-b.eofReady:
		return nil
	case <-b.coordinator.abort:
		return context.Canceled
	}
}

func (b *v8MultiTurnBridge) read(ctx context.Context, destination []byte) (int, error) {
	if len(destination) < v8PCMFrameBytes {
		return 0, fmt.Errorf("%s receiver requested %d PCM bytes, want at least %d", b.direction, len(destination), v8PCMFrameBytes)
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-b.coordinator.abort:
		return 0, context.Canceled
	case packet := <-b.packets:
		if packet.eof {
			b.mu.Lock()
			b.eofRead = true
			b.mu.Unlock()
			b.eofOnce.Do(func() { close(b.eofSeen) })
			return 0, io.EOF
		}
		copy(destination, packet.crossing.Emitted)
		b.receiver.record(packet.crossing, packet.crossing.Emitted)
		close(packet.ack)
		return len(packet.crossing.Emitted), nil
	}
}

func (b *v8MultiTurnBridge) wroteFrames() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes
}

func (b *v8MultiTurnBridge) observedEOF() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.eofRead
}

type v8MultiTurnPCMWriter struct{ bridge *v8MultiTurnBridge }

func (w v8MultiTurnPCMWriter) Write(data []byte) (int, error) { return w.bridge.write(data) }

type v8MultiTurnPCMReader struct {
	bridge          *v8MultiTurnBridge
	boundaryPending bool
}

func (r *v8MultiTurnPCMReader) Read(data []byte) (int, error) {
	return r.ReadContext(context.Background(), data)
}

func (r *v8MultiTurnPCMReader) ReadContext(ctx context.Context, data []byte) (int, error) {
	if r.boundaryPending {
		r.boundaryPending = false
		return 0, audio.ErrEndOfTurn
	}
	count, err := r.bridge.read(ctx, data)
	if err == nil && count > 0 {
		r.boundaryPending = true
	}
	return count, err
}

type v8BridgePacket struct {
	eof      bool
	crossing v8Crossing
}

type v8PCMBridge struct {
	coordinator *v8CrossingCoordinator
	direction   string
	sender      *v8RecordingView
	receiver    *v8RecordingView
	silence     []byte
	mutateFirst bool
	runtimeOut  chan services.SessionRuntimeObservation

	packets chan v8BridgePacket
	mu      sync.Mutex
	written bool
	eofRead bool
}

func newV8PCMBridge(coordinator *v8CrossingCoordinator, direction string, sender, receiver *v8RecordingView, silence []byte, mutateFirst bool) *v8PCMBridge {
	return &v8PCMBridge{
		coordinator: coordinator,
		direction:   direction,
		sender:      sender,
		receiver:    receiver,
		silence:     append([]byte(nil), silence...),
		mutateFirst: mutateFirst,
		packets:     make(chan v8BridgePacket, 2),
		runtimeOut:  make(chan services.SessionRuntimeObservation, 1),
	}
}

func (b *v8PCMBridge) acceptRuntimeOutput(observation services.SessionRuntimeObservation) {
	select {
	case b.runtimeOut <- observation:
	case <-b.coordinator.abort:
	}
}

func (b *v8PCMBridge) nextRuntimeOutput() (services.SessionRuntimeObservation, error) {
	select {
	case observation := <-b.runtimeOut:
		return observation, nil
	case <-b.coordinator.abort:
		return services.SessionRuntimeObservation{}, context.Canceled
	}
}

func (b *v8PCMBridge) write(data []byte) (int, error) {
	if len(data) != v8PCMFrameBytes {
		return 0, fmt.Errorf("%s emitted %d PCM bytes, want one %d-byte frame", b.direction, len(data), v8PCMFrameBytes)
	}

	b.mu.Lock()
	if b.written {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	b.written = true
	b.mu.Unlock()

	emitted := append([]byte(nil), data...)
	delivered := append([]byte(nil), emitted...)
	if b.mutateFirst {
		if len(b.silence) != len(delivered) {
			return 0, fmt.Errorf("%s silence mutation is %d bytes, want %d", b.direction, len(b.silence), len(delivered))
		}
		delivered = append([]byte(nil), b.silence...)
	}
	outputObservation, err := b.nextRuntimeOutput()
	if err != nil {
		return 0, err
	}
	if outputObservation.Kind != services.SessionRuntimeObservationAudioOutput {
		return 0, fmt.Errorf("%s runtime observation kind = %q, want %q", b.direction, outputObservation.Kind, services.SessionRuntimeObservationAudioOutput)
	}
	if !bytes.Equal(outputObservation.Payload, emitted) {
		return 0, fmt.Errorf("%s runtime audio output payload differs from the CLI writer payload: runtime hash=%s writer hash=%s", b.direction, v8PCMHash(outputObservation.Payload), v8PCMHash(emitted))
	}
	crossing, err := b.coordinator.record(b.direction, outputObservation.Tick, outputObservation.Timestamp, emitted, delivered)
	if err != nil {
		return 0, err
	}
	b.sender.record(crossing, emitted)
	if b.direction == "A-to-B" {
		select {
		case <-b.coordinator.deliveryReady:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
	} else {
		// Do not release A-to-B to B until B has emitted its own response. This
		// preserves the replayed server-output-before-client-input order on
		// both CLIs while retaining one equal-tick overlap window.
		b.coordinator.releaseDelivery()
	}

	select {
	case b.packets <- v8BridgePacket{crossing: crossing}:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	// The one-frame input turn is intentionally closed after the first
	// crossing. That EOF is what makes the peer CLI send its MESSAGE.END and
	// exercises the shipped audio-input commit path.
	select {
	case b.packets <- v8BridgePacket{eof: true}:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	return len(data), nil
}

func (b *v8PCMBridge) read(ctx context.Context, destination []byte) (int, error) {
	if len(destination) < v8PCMFrameBytes {
		return 0, fmt.Errorf("%s receiver requested %d PCM bytes, want at least %d", b.direction, len(destination), v8PCMFrameBytes)
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-b.coordinator.abort:
		return 0, context.Canceled
	case packet := <-b.packets:
		if packet.eof {
			b.mu.Lock()
			b.eofRead = true
			b.mu.Unlock()
			return 0, io.EOF
		}
		copy(destination, packet.crossing.Delivered)
		b.receiver.record(packet.crossing, packet.crossing.Delivered)
		return len(packet.crossing.Delivered), nil
	}
}

func (b *v8PCMBridge) wroteFrame() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written
}

func (b *v8PCMBridge) observedEOF() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.eofRead
}

type v8RuntimeObserver struct {
	outputBridge v8RuntimeOutputSink
	inputBridge  v8RuntimeInputSink
	turnTwoReady chan struct{}

	mu           sync.Mutex
	observations []services.SessionRuntimeObservation
	turnTwoOnce  sync.Once
}

func (o *v8RuntimeObserver) ObserveSessionRuntime(observation services.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	observation.Payload = append([]byte(nil), observation.Payload...)
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
	if observation.Kind == services.SessionRuntimeObservationTurnCompleted && observation.TurnsCompleted == 2 && o.turnTwoReady != nil {
		o.turnTwoOnce.Do(func() { close(o.turnTwoReady) })
	}
	if observation.Kind == services.SessionRuntimeObservationAudioInput && o.inputBridge != nil {
		o.inputBridge.acceptRuntimeInput(observation)
	}
	if observation.Kind == services.SessionRuntimeObservationAudioOutput && o.outputBridge != nil {
		o.outputBridge.acceptRuntimeOutput(observation)
	}
}

func (o *v8RuntimeObserver) snapshot() []services.SessionRuntimeObservation {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	observations := make([]services.SessionRuntimeObservation, len(o.observations))
	for i, observation := range o.observations {
		observations[i] = observation
		observations[i].Payload = append([]byte(nil), observation.Payload...)
	}
	return observations
}

type v8StreamRecorder struct {
	mu      sync.Mutex
	records []v8StreamRecord
}

func (o *v8StreamRecorder) Observe(msg messages.StreamMessage) {
	if o == nil {
		return
	}
	record := v8StreamRecord{Type: string(msg.Type)}
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil {
		record.Text = value.Content
	}
	o.mu.Lock()
	o.records = append(o.records, record)
	o.mu.Unlock()
}

func (o *v8StreamRecorder) snapshot() []v8StreamRecord {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]v8StreamRecord(nil), o.records...)
}

type v8PCMWriter struct{ bridge *v8PCMBridge }

func (w v8PCMWriter) Write(data []byte) (int, error) { return w.bridge.write(data) }

type v8PCMReader struct{ bridge *v8PCMBridge }

func (r v8PCMReader) Read(data []byte) (int, error) {
	return r.bridge.read(context.Background(), data)
}

func (r v8PCMReader) ReadContext(ctx context.Context, data []byte) (int, error) {
	return r.bridge.read(ctx, data)
}

type v8ViewRecord struct {
	Order     int       `json:"order"`
	Direction string    `json:"direction"`
	TurnKey   string    `json:"turn_key,omitempty"`
	Turn      int       `json:"turn,omitempty"`
	Tick      uint64    `json:"tick"`
	Timestamp time.Time `json:"timestamp"`
	Payload   []byte    `json:"payload"`
	SHA256    string    `json:"sha256"`
	RMS       float64   `json:"rms"`
}

type v8RecordingView struct {
	Harness string
	Role    string

	mu      sync.Mutex
	records []v8ViewRecord
}

func (v *v8RecordingView) record(crossing v8Crossing, payload []byte) {
	hash, rms := v8PCMStats(payload)
	v.mu.Lock()
	v.records = append(v.records, v8ViewRecord{
		Order:     crossing.Sequence,
		Direction: crossing.Direction,
		TurnKey:   crossing.TurnKey,
		Turn:      crossing.Turn,
		Tick:      crossing.Tick,
		Timestamp: crossing.Timestamp,
		Payload:   append([]byte(nil), payload...),
		SHA256:    hash,
		RMS:       rms,
	})
	v.mu.Unlock()
}

func (v *v8RecordingView) snapshot() []v8ViewRecord {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]v8ViewRecord, len(v.records))
	for i, record := range v.records {
		out[i] = record
		out[i].Payload = append([]byte(nil), record.Payload...)
	}
	return out
}

type v8TerminalFact struct {
	Clean          bool      `json:"clean"`
	Turns          int       `json:"turns"`
	FinalTick      uint64    `json:"final_tick"`
	FinalTimestamp time.Time `json:"final_timestamp"`
	InputEOF       bool      `json:"input_eof"`
	OutputFrame    bool      `json:"output_frame"`
	Error          string    `json:"error,omitempty"`
}

type v8ViewArtifact struct {
	Harness    string         `json:"harness"`
	Role       string         `json:"role"`
	SampleRate int            `json:"sample_rate_hz"`
	Records    []v8ViewRecord `json:"records"`
	Terminal   v8TerminalFact `json:"terminal"`
}

type v8HarnessResult struct {
	Name        string
	Instruction string
	ReplayPath  string
	Err         error
	Elapsed     time.Duration
	Runtime     []services.SessionRuntimeObservation
	Stream      []v8StreamRecord
}

type v8StreamRecord struct {
	Type string
	Text string
}

type v8DuplexRun struct {
	base       time.Time
	crossings  []v8Crossing
	harnesses  map[string]v8HarnessResult
	views      map[string]*v8RecordingView
	artifacts  map[string]string
	terminal   map[string]v8TerminalFact
	finalTick  uint64
	turnsBound int
}

func v8PCMStats(payload []byte) (string, float64) {
	digest := sha256.Sum256(payload)
	if len(payload) == 0 || len(payload)%2 != 0 {
		return hex.EncodeToString(digest[:]), 0
	}
	var energy float64
	for offset := 0; offset < len(payload); offset += 2 {
		sample := int16(binary.LittleEndian.Uint16(payload[offset:]))
		energy += float64(sample) * float64(sample)
	}
	return hex.EncodeToString(digest[:]), math.Sqrt(energy / float64(len(payload)/2))
}

func v8PCMHash(payload []byte) string {
	hash, _ := v8PCMStats(payload)
	return hash
}

func v8PCM16Bytes(samples []int16) []byte {
	payload := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(payload[i*2:], uint16(sample))
	}
	return payload
}

func v8AudioFixturePath(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve v8 audio fixture path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "go-agent-loop", "testdata", "audio", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("v8 audio fixture %q not found at %q: %v", name, path, err)
	}
	return path
}

func v8LoudFrames(t *testing.T, path string) ([]byte, []byte) {
	frames := v8LoudFrameSet(t, path, 2)
	return frames[0], frames[1]
}

func v8LoudFrameSet(t *testing.T, path string, count int) [][]byte {
	t.Helper()
	if count <= 0 {
		t.Fatalf("v8 loud frame count = %d, want positive", count)
	}
	wav, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read v8 overlap fixture: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wav))
	if err != nil {
		t.Fatalf("parse v8 overlap fixture: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("v8 overlap fixture rate = %d, want %d", rate, audio.SampleRate)
	}
	starts := make([]int, 0, count)
	frames := make([][]byte, 0, count)
	for len(frames) < count {
		bestStart := -1
		bestEnergy := -1.0
		for start := 0; start+audio.FrameSize <= len(samples); start += audio.FrameSize {
			allowed := true
			for _, selectedStart := range starts {
				if absInt(start-selectedStart) < audio.FrameSize*4 {
					allowed = false
					break
				}
			}
			if !allowed {
				continue
			}
			var energy float64
			for _, sample := range samples[start : start+audio.FrameSize] {
				energy += float64(sample) * float64(sample)
			}
			if energy > bestEnergy {
				bestStart, bestEnergy = start, energy
			}
		}
		if bestStart < 0 {
			t.Fatalf("v8 overlap fixture has fewer than %d distinct energetic frames", count)
		}
		starts = append(starts, bestStart)
		frames = append(frames, v8PCM16Bytes(samples[bestStart:bestStart+audio.FrameSize]))
	}
	for _, payload := range frames {
		_, rms := v8PCMStats(payload)
		if rms <= v8VADThreshold {
			t.Fatalf("v8 overlap fixture frame RMS = %.1f, want > %.1f", rms, v8VADThreshold)
		}
	}
	return frames
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func v8CaptureRecord(sequence int, direction gwtesting.SessionEventDirection, msg messages.StreamMessage) gwtesting.CapturedSessionEvent {
	payload, err := gwtesting.MarshalStreamMessage(msg)
	if err != nil {
		panic(fmt.Sprintf("marshal v8 capture event %s: %v", msg.Type, err))
	}
	return gwtesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: int64(sequence - 1),
		Type:        string(msg.Type),
		PayloadType: gwtesting.SessionPayloadTypeStreamMessage,
		Payload:     payload,
	}
}

func writeV8ReplayCapture(t *testing.T, path, sessionID, instruction string, output, expectedInput []byte) {
	t.Helper()
	records := []gwtesting.CapturedSessionEvent{
		v8CaptureRecord(1, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue(sessionID, "audio_inference"),
		}),
		v8CaptureRecord(2, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue(instruction),
		}),
		v8CaptureRecord(3, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(output),
		}),
		v8CaptureRecord(4, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(expectedInput),
		}),
		// The audio source sends a type-only MESSAGE.END after it reads EOF.
		v8CaptureRecord(5, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type: messages.StreamTypeMessageEnd,
		}),
		v8CaptureRecord(6, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeAudioEnd,
			Value: messages.NewAudioEndValue(),
		}),
		v8CaptureRecord(7, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		}),
	}
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "synthetic-t1", Model: "session-replay"},
		Session: gwtesting.SessionMetadata{
			ID:                sessionID,
			StartedAtUTC:      "2026-08-26T00:00:00Z",
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: records,
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal v8 replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write v8 replay capture: %v", err)
	}
}

func writeV8MultiTurnReplayCapture(t *testing.T, path, sessionID, instruction, harness string, outputs, expectedInputs [][]byte) {
	t.Helper()
	if len(outputs) != v8MultiTurnCount || len(expectedInputs) != v8MultiTurnCount {
		t.Fatalf("v8 multi-turn capture %s has outputs=%d inputs=%d, want %d each", harness, len(outputs), len(expectedInputs), v8MultiTurnCount)
	}
	sequence := 1
	records := []gwtesting.CapturedSessionEvent{
		v8CaptureRecord(sequence, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue(sessionID, "audio_inference"),
		}),
	}
	sequence++
	records = append(records, v8CaptureRecord(sequence, gwtesting.DirectionClientToServer, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue(instruction),
	}))
	sequence++
	appendServerTurn := func(turn int) {
		marker := fmt.Sprintf("%s transcript turn %d", harness, turn)
		records = append(records,
			v8CaptureRecord(sequence, gwtesting.DirectionServerToClient, messages.StreamMessage{
				Type:  messages.StreamTypeTextDelta,
				Value: messages.NewTextDeltaValue(marker),
			}),
			v8CaptureRecord(sequence+1, gwtesting.DirectionServerToClient, messages.StreamMessage{
				Type:  messages.StreamTypeAudioDelta,
				Value: messages.NewAudioDeltaValue(outputs[turn-1]),
			}),
		)
		sequence += 2
	}
	appendPeerInput := func(turn int) {
		records = append(records, v8CaptureRecord(sequence, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(expectedInputs[turn-1]),
		}))
		sequence++
	}
	appendResponseEnd := func() {
		records = append(records,
			v8CaptureRecord(sequence, gwtesting.DirectionServerToClient, messages.StreamMessage{
				Type:  messages.StreamTypeAudioEnd,
				Value: messages.NewAudioEndValue(),
			}),
			v8CaptureRecord(sequence+1, gwtesting.DirectionServerToClient, messages.StreamMessage{
				Type:  messages.StreamTypeMessageEnd,
				Value: messages.NewMessageEndValue(messages.TokenUsage{}),
			}),
		)
		sequence += 2
	}
	appendInputEnd := func() {
		records = append(records, v8CaptureRecord(sequence, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type: messages.StreamTypeMessageEnd,
		}))
		sequence++
	}

	for turn := 1; turn <= 2; turn++ {
		appendServerTurn(turn)
		appendPeerInput(turn)
		appendInputEnd()
		appendResponseEnd()
	}
	// Turn three is the ordinary sequential boundary. A's output interval is
	// scheduled first; B receives it and commits its finite input before its
	// own output interval begins. A receives B's final output before its own
	// end-of-input commit, so both raw streams remain coupled until the script
	// is complete and each bridge emits EOF only once.
	if harness == "A" {
		appendServerTurn(3)
		appendPeerInput(3)
		appendInputEnd()
		appendResponseEnd()
	} else {
		appendPeerInput(3)
		appendInputEnd()
		appendServerTurn(3)
		appendResponseEnd()
	}
	records = append(records, v8CaptureRecord(sequence, gwtesting.DirectionServerToClient, messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue(sessionID, "provider_closed"),
	}))
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "synthetic-t1", Model: "session-replay"},
		Session: gwtesting.SessionMetadata{
			ID:                sessionID,
			StartedAtUTC:      "2026-08-26T00:00:00Z",
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: records,
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal v8 multi-turn replay capture %s: %v", harness, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write v8 multi-turn replay capture %s: %v", harness, err)
	}
}

func newV8CLI(t *testing.T, logicalClock *clock.Deterministic, observer *v8RuntimeObserver) *cli.AgentCLI {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortClock, logicalClock),
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, observer),
	)
	if err != nil {
		t.Fatalf("initialize v8 CLI with shared clock and runtime observer: %v", err)
	}
	return agentCLI
}

func runV8Duplex(t *testing.T, aToB, bToA []byte, mutateFirst bool) v8DuplexRun {
	t.Helper()
	base := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	logicalClock := clock.NewDeterministic(base, v8TickDuration)
	logicalClock.AdvanceTo(v8OverlapTick)
	coordinator := newV8CrossingCoordinator()

	silencePath := v8AudioFixturePath(t, "silence_16k.wav")
	silenceWAV, err := os.ReadFile(silencePath)
	if err != nil {
		t.Fatalf("read v8 silence fixture: %v", err)
	}
	_, silenceSamples, err := wavio.Read(bytes.NewReader(silenceWAV))
	if err != nil {
		t.Fatalf("parse v8 silence fixture: %v", err)
	}
	if len(silenceSamples) < audio.FrameSize {
		t.Fatalf("v8 silence fixture has %d samples, want at least %d", len(silenceSamples), audio.FrameSize)
	}
	silenceFrame := v8PCM16Bytes(silenceSamples[:audio.FrameSize])

	views := map[string]*v8RecordingView{
		"A/client": {Harness: "A", Role: "client"},
		"A/agent":  {Harness: "A", Role: "agent"},
		"B/client": {Harness: "B", Role: "client"},
		"B/agent":  {Harness: "B", Role: "agent"},
	}
	aToBBridge := newV8PCMBridge(coordinator, "A-to-B", views["A/client"], views["B/agent"], silenceFrame, mutateFirst)
	bToABridge := newV8PCMBridge(coordinator, "B-to-A", views["B/client"], views["A/agent"], silenceFrame, false)
	aObserver := &v8RuntimeObserver{outputBridge: aToBBridge}
	bObserver := &v8RuntimeObserver{outputBridge: bToABridge}

	runDir := t.TempDir()
	aReplay := filepath.Join(runDir, "harness-a.session.json")
	bReplay := filepath.Join(runDir, "harness-b.session.json")
	writeV8ReplayCapture(t, aReplay, "s2s-v8-harness-a", v8HarnessAInstruction, aToB, bToA)
	replayedAToB := aToB
	if mutateFirst {
		replayedAToB = silenceFrame
	}
	writeV8ReplayCapture(t, bReplay, "s2s-v8-harness-b", v8HarnessBInstruction, bToA, replayedAToB)

	// Construct both generated shipped CLIs before starting either command and
	// pass the same *clock.Deterministic identity and a runtime observer through
	// both composition graphs. The goroutines execute only `agent session`; no
	// loop, provider, or replay helper is the evidence path. The observer is
	// fed by the session runtime itself, including its clock-stamped output.
	aCLI := newV8CLI(t, logicalClock, aObserver)
	bCLI := newV8CLI(t, logicalClock, bObserver)

	ctx, cancel := context.WithTimeout(context.Background(), v8RunTimeout)
	defer cancel()
	results := make(chan v8HarnessResult, 2)
	startGate := make(chan struct{})
	var wg sync.WaitGroup
	start := func(name, instruction, replayPath string, input io.Reader, output io.Writer, commandCLI *cli.AgentCLI, observer *v8RuntimeObserver) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			started := time.Now()
			root := commandCLI.Generate()
			root.SetIn(input)
			root.SetOut(output)
			root.SetErr(io.Discard)
			root.SetArgs([]string{
				"session",
				"--replay", replayPath,
				"--audio-in", "-",
				"--audio-out", "-",
				"--wait-for-close",
				"--max-duration", v8CommandMaxDuration.String(),
				instruction,
			})
			results <- v8HarnessResult{
				Name:        name,
				Instruction: instruction,
				ReplayPath:  replayPath,
				Err:         root.ExecuteContext(ctx),
				Elapsed:     time.Since(started),
				Runtime:     observer.snapshot(),
			}
		}()
	}
	start("A", v8HarnessAInstruction, aReplay, v8PCMReader{bridge: bToABridge}, v8PCMWriter{bridge: aToBBridge}, aCLI, aObserver)
	start("B", v8HarnessBInstruction, bReplay, v8PCMReader{bridge: aToBBridge}, v8PCMWriter{bridge: bToABridge}, bCLI, bObserver)
	close(startGate)

	harnesses := make(map[string]v8HarnessResult, 2)
	contextDone := ctx.Done()
	cleanupTimer := time.NewTimer(v8RunTimeout + time.Second)
	defer cleanupTimer.Stop()
	for len(harnesses) < 2 {
		select {
		case result := <-results:
			harnesses[result.Name] = result
			if result.Err != nil {
				coordinator.abortRun()
				cancel()
			}
		case <-contextDone:
			coordinator.abortRun()
			cancel()
			contextDone = nil
		case <-cleanupTimer.C:
			coordinator.abortRun()
			cancel()
			t.Fatal("v8 CLI harnesses did not return after the bounded cleanup window")
		}
	}
	wg.Wait()

	finalTick := uint64(0)
	run := v8DuplexRun{
		base:       base,
		crossings:  coordinator.snapshot(),
		harnesses:  harnesses,
		views:      views,
		terminal:   map[string]v8TerminalFact{},
		finalTick:  finalTick,
		turnsBound: v8TurnBound,
	}
	for name, result := range harnesses {
		terminalObservation, err := v8RuntimeObservation(result.Runtime, services.SessionRuntimeObservationTerminal)
		if err != nil {
			t.Fatalf("harness %s terminal runtime observation: %v", name, err)
		}
		terminal := v8TerminalFact{
			Clean:          terminalObservation.Clean,
			Turns:          terminalObservation.TurnsCompleted,
			FinalTick:      terminalObservation.Tick,
			FinalTimestamp: terminalObservation.Timestamp,
			Error:          terminalObservation.Error,
		}
		if terminal.FinalTick > finalTick {
			finalTick = terminal.FinalTick
		}
		if name == "A" {
			terminal.InputEOF = bToABridge.observedEOF()
			terminal.OutputFrame = aToBBridge.wroteFrame()
		} else {
			terminal.InputEOF = aToBBridge.observedEOF()
			terminal.OutputFrame = bToABridge.wroteFrame()
		}
		run.terminal[name] = terminal
	}
	run.finalTick = finalTick
	for name, view := range views {
		terminal := run.terminal[view.Harness]
		viewPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+".json")
		wavPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+".wav")
		writeV8ViewArtifacts(t, view, terminal, viewPath, wavPath)
		run.artifacts = appendArtifactPaths(run.artifacts, name, viewPath, wavPath)
	}
	return run
}

func runV8MultiTurnDuplex(t *testing.T, aToB, bToA [][]byte) v8DuplexRun {
	t.Helper()
	if len(aToB) != v8MultiTurnCount || len(bToA) != v8MultiTurnCount {
		t.Fatalf("v8 multi-turn run has A-to-B=%d B-to-A=%d frames, want %d each", len(aToB), len(bToA), v8MultiTurnCount)
	}
	base := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	logicalClock := clock.NewDeterministic(base, v8TickDuration)
	logicalClock.AdvanceTo(v8OverlapTick)
	coordinator := newV8MultiTurnCoordinator(logicalClock, base)
	aTurnTwoReady := make(chan struct{})
	bTurnTwoReady := make(chan struct{})
	views := map[string]*v8RecordingView{
		"A/client": {Harness: "A", Role: "client"},
		"A/agent":  {Harness: "A", Role: "agent"},
		"B/client": {Harness: "B", Role: "client"},
		"B/agent":  {Harness: "B", Role: "agent"},
	}
	aToBBridge := newV8MultiTurnBridge(coordinator, "A-to-B", views["A/client"], views["B/agent"], bTurnTwoReady)
	bToABridge := newV8MultiTurnBridge(coordinator, "B-to-A", views["B/client"], views["A/agent"], aTurnTwoReady)
	aObserver := &v8RuntimeObserver{outputBridge: aToBBridge, inputBridge: bToABridge, turnTwoReady: aTurnTwoReady}
	bObserver := &v8RuntimeObserver{outputBridge: bToABridge, inputBridge: aToBBridge, turnTwoReady: bTurnTwoReady}
	aStream := &v8StreamRecorder{}
	bStream := &v8StreamRecorder{}

	runDir := t.TempDir()
	aReplay := filepath.Join(runDir, "harness-a-multiturn.session.json")
	bReplay := filepath.Join(runDir, "harness-b-multiturn.session.json")
	writeV8MultiTurnReplayCapture(t, aReplay, "s2s-v8-multiturn-harness-a", v8HarnessAInstruction, "A", aToB, bToA)
	writeV8MultiTurnReplayCapture(t, bReplay, "s2s-v8-multiturn-harness-b", v8HarnessBInstruction, "B", bToA, aToB)

	// Both commands are generated from the same composition path and share
	// the exact deterministic clock object. Stream markers are observed only
	// after they cross the command's session-loop boundary.
	aCLI := newV8CLI(t, logicalClock, aObserver)
	bCLI := newV8CLI(t, logicalClock, bObserver)
	aCLI.SetSessionStreamObserver(aStream.Observe)
	bCLI.SetSessionStreamObserver(bStream.Observe)

	ctx, cancel := context.WithTimeout(context.Background(), v8RunTimeout)
	defer cancel()
	results := make(chan v8HarnessResult, 2)
	startGate := make(chan struct{})
	var wg sync.WaitGroup
	start := func(name, instruction, replayPath string, input io.Reader, output io.Writer, commandCLI *cli.AgentCLI, observer *v8RuntimeObserver, stream *v8StreamRecorder) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			started := time.Now()
			root := commandCLI.Generate()
			root.SetIn(input)
			root.SetOut(output)
			root.SetErr(io.Discard)
			root.SetArgs([]string{
				"session",
				"--replay", replayPath,
				"--audio-in", "-",
				"--audio-out", "-",
				"--max-duration", v8CommandMaxDuration.String(),
				instruction,
			})
			results <- v8HarnessResult{
				Name:        name,
				Instruction: instruction,
				ReplayPath:  replayPath,
				Err:         root.ExecuteContext(ctx),
				Elapsed:     time.Since(started),
				Runtime:     observer.snapshot(),
				Stream:      stream.snapshot(),
			}
		}()
	}
	start("A", v8HarnessAInstruction, aReplay, &v8MultiTurnPCMReader{bridge: bToABridge}, v8MultiTurnPCMWriter{bridge: aToBBridge}, aCLI, aObserver, aStream)
	start("B", v8HarnessBInstruction, bReplay, &v8MultiTurnPCMReader{bridge: aToBBridge}, v8MultiTurnPCMWriter{bridge: bToABridge}, bCLI, bObserver, bStream)
	close(startGate)

	harnesses := make(map[string]v8HarnessResult, 2)
	contextDone := ctx.Done()
	cleanupTimer := time.NewTimer(v8RunTimeout + time.Second)
	defer cleanupTimer.Stop()
	for len(harnesses) < 2 {
		select {
		case result := <-results:
			harnesses[result.Name] = result
			if result.Err != nil {
				coordinator.abortRun()
				cancel()
			}
		case <-contextDone:
			coordinator.abortRun()
			cancel()
			contextDone = nil
		case <-cleanupTimer.C:
			coordinator.abortRun()
			cancel()
			t.Fatal("v8 multi-turn CLI harnesses did not return after the bounded cleanup window")
		}
	}
	wg.Wait()

	run := v8DuplexRun{
		base:       base,
		crossings:  coordinator.snapshot(),
		harnesses:  harnesses,
		views:      views,
		terminal:   map[string]v8TerminalFact{},
		turnsBound: v8MultiTurnCount,
	}
	for name, result := range harnesses {
		terminalObservation, err := v8RuntimeObservation(result.Runtime, services.SessionRuntimeObservationTerminal)
		if err != nil {
			t.Fatalf("harness %s terminal runtime observation: %v", name, err)
		}
		terminal := v8TerminalFact{
			Clean:          terminalObservation.Clean,
			Turns:          terminalObservation.TurnsCompleted,
			FinalTick:      terminalObservation.Tick,
			FinalTimestamp: terminalObservation.Timestamp,
			Error:          terminalObservation.Error,
		}
		if terminal.FinalTick > run.finalTick {
			run.finalTick = terminal.FinalTick
		}
		if name == "A" {
			terminal.InputEOF = bToABridge.observedEOF()
			terminal.OutputFrame = aToBBridge.wroteFrames() == v8MultiTurnCount
		} else {
			terminal.InputEOF = aToBBridge.observedEOF()
			terminal.OutputFrame = bToABridge.wroteFrames() == v8MultiTurnCount
		}
		run.terminal[name] = terminal
	}
	for name, view := range views {
		terminal := run.terminal[view.Harness]
		viewPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+"-multiturn.json")
		wavPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+"-multiturn.wav")
		writeV8ViewArtifacts(t, view, terminal, viewPath, wavPath)
		run.artifacts = appendArtifactPaths(run.artifacts, name, viewPath, wavPath)
	}
	return run
}

func v8RuntimeObservation(observations []services.SessionRuntimeObservation, kind services.SessionRuntimeObservationKind) (services.SessionRuntimeObservation, error) {
	var found services.SessionRuntimeObservation
	count := 0
	for _, observation := range observations {
		if observation.Kind != kind {
			continue
		}
		found = observation
		count++
	}
	if count != 1 {
		return services.SessionRuntimeObservation{}, fmt.Errorf("runtime observation %q count = %d, want exactly one", kind, count)
	}
	return found, nil
}

func appendArtifactPaths(artifacts map[string]string, viewName, jsonPath, wavPath string) map[string]string {
	if artifacts == nil {
		artifacts = make(map[string]string)
	}
	artifacts[viewName+".json"] = jsonPath
	artifacts[viewName+".wav"] = wavPath
	return artifacts
}

func writeV8ViewArtifacts(t *testing.T, view *v8RecordingView, terminal v8TerminalFact, jsonPath, wavPath string) {
	t.Helper()
	artifact := v8ViewArtifact{
		Harness:    view.Harness,
		Role:       view.Role,
		SampleRate: audio.SampleRate,
		Records:    view.snapshot(),
		Terminal:   terminal,
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal v8 %s/%s recording artifact: %v", view.Harness, view.Role, err)
	}
	if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
		t.Fatalf("write v8 %s/%s recording artifact: %v", view.Harness, view.Role, err)
	}
	payload := []byte{}
	for _, record := range artifact.Records {
		payload = append(payload, record.Payload...)
	}
	if len(payload) == 0 {
		t.Fatalf("v8 %s/%s recording has no PCM payload", view.Harness, view.Role)
	}
	samples := make([]int16, len(payload)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(payload[i*2:]))
	}
	var wav bytes.Buffer
	if err := wavio.Write(&wav, audio.SampleRate, samples); err != nil {
		t.Fatalf("encode v8 %s/%s WAV artifact: %v", view.Harness, view.Role, err)
	}
	if err := os.WriteFile(wavPath, wav.Bytes(), 0o600); err != nil {
		t.Fatalf("write v8 %s/%s WAV artifact: %v", view.Harness, view.Role, err)
	}
}

func verifyV8Run(run v8DuplexRun, expected map[string][]byte) error {
	if len(run.harnesses) != 2 {
		return fmt.Errorf("expected two CLI harness results, observed %d", len(run.harnesses))
	}
	aHarness, aOK := run.harnesses["A"]
	bHarness, bOK := run.harnesses["B"]
	if !aOK || !bOK {
		return fmt.Errorf("expected harness results for A and B")
	}
	if aHarness.Instruction == bHarness.Instruction {
		return fmt.Errorf("harness instructions are not distinct: %q", aHarness.Instruction)
	}
	if aHarness.Instruction != v8HarnessAInstruction || bHarness.Instruction != v8HarnessBInstruction {
		return fmt.Errorf("harness instructions do not match the two scripted profiles")
	}
	if len(run.crossings) != 2 {
		return fmt.Errorf("expected two retained PCM crossings, observed %d", len(run.crossings))
	}
	wantDirections := []string{"A-to-B", "B-to-A"}
	for i, crossing := range run.crossings {
		if crossing.Sequence != i+1 || crossing.Direction != wantDirections[i] {
			return fmt.Errorf("crossing order mismatch at index %d: got sequence=%d direction=%s", i, crossing.Sequence, crossing.Direction)
		}
		if crossing.Tick != v8OverlapTick {
			return fmt.Errorf("%s crossing recorded at logical tick %d, want %d", crossing.Direction, crossing.Tick, v8OverlapTick)
		}
		wantTime := run.base.Add(time.Duration(crossing.Tick) * v8TickDuration)
		if !crossing.Timestamp.Equal(wantTime) {
			return fmt.Errorf("%s tick %d timestamp=%s, want deterministic timestamp %s", crossing.Direction, crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano), wantTime.Format(time.RFC3339Nano))
		}
		want := expected[crossing.Direction]
		if !bytes.Equal(crossing.Emitted, want) {
			return v8PCMFailure(crossing, want, crossing.Emitted, "CLI output")
		}
		_, deliveredRMS := v8PCMStats(crossing.Delivered)
		if !bytes.Equal(crossing.Delivered, want) || deliveredRMS <= v8VADThreshold {
			return v8PCMFailure(crossing, want, crossing.Delivered, "peer input")
		}

		sender, receiver := "A", "B"
		if crossing.Direction == "B-to-A" {
			sender, receiver = "B", "A"
		}
		outputObservation, err := v8RuntimeObservation(run.harnesses[sender].Runtime, services.SessionRuntimeObservationAudioOutput)
		if err != nil {
			return fmt.Errorf("harness %s output runtime observation: %w", sender, err)
		}
		if outputObservation.Tick != crossing.Tick || !outputObservation.Timestamp.Equal(crossing.Timestamp) {
			return fmt.Errorf("%s runtime output timing differs from crossing: runtime tick=%d timestamp=%s, crossing tick=%d timestamp=%s", crossing.Direction, outputObservation.Tick, outputObservation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
		}
		if !bytes.Equal(outputObservation.Payload, crossing.Emitted) {
			return v8PCMFailure(crossing, crossing.Emitted, outputObservation.Payload, "runtime output")
		}
		inputObservation, err := v8RuntimeObservation(run.harnesses[receiver].Runtime, services.SessionRuntimeObservationAudioInput)
		if err != nil {
			return fmt.Errorf("harness %s input runtime observation: %w", receiver, err)
		}
		if inputObservation.Tick != crossing.Tick || !inputObservation.Timestamp.Equal(crossing.Timestamp) {
			return fmt.Errorf("%s runtime input timing differs from crossing: runtime tick=%d timestamp=%s, crossing tick=%d timestamp=%s", crossing.Direction, inputObservation.Tick, inputObservation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
		}
		if !bytes.Equal(inputObservation.Payload, crossing.Delivered) {
			return v8PCMFailure(crossing, crossing.Delivered, inputObservation.Payload, "runtime input")
		}
	}

	if run.crossings[0].Tick != run.crossings[1].Tick {
		return fmt.Errorf("directional speech windows do not overlap: A-to-B tick %d, B-to-A tick %d", run.crossings[0].Tick, run.crossings[1].Tick)
	}
	parityPairs := [][2]string{{"A/client", "B/agent"}, {"B/client", "A/agent"}}
	for _, pair := range parityPairs {
		left := run.views[pair[0]].snapshot()
		right := run.views[pair[1]].snapshot()
		if len(left) != 1 || len(right) != 1 {
			return fmt.Errorf("recording parity %s vs %s: got %d and %d records, want one each", pair[0], pair[1], len(left), len(right))
		}
		if err := compareV8ViewRecords(pair[0], left[0], pair[1], right[0]); err != nil {
			return err
		}
	}

	for name, terminal := range run.terminal {
		if !terminal.Clean || !terminal.InputEOF || !terminal.OutputFrame {
			return fmt.Errorf("harness %s terminal facts are not clean: %+v", name, terminal)
		}
		if terminal.Turns > run.turnsBound || terminal.FinalTick > v8OverlapTickLimit {
			return fmt.Errorf("harness %s exceeded turn/tick bounds: %+v", name, terminal)
		}
		turnObservation, err := v8RuntimeObservation(run.harnesses[name].Runtime, services.SessionRuntimeObservationTurnCompleted)
		if err != nil {
			return fmt.Errorf("harness %s turn runtime observation: %w", name, err)
		}
		if turnObservation.TurnsCompleted != terminal.Turns {
			return fmt.Errorf("harness %s completed-turn observation = %d, terminal observation = %d", name, turnObservation.TurnsCompleted, terminal.Turns)
		}
		if terminal.Turns == 0 {
			return fmt.Errorf("harness %s terminal observation reported no completed turns", name)
		}
		terminalObservation, err := v8RuntimeObservation(run.harnesses[name].Runtime, services.SessionRuntimeObservationTerminal)
		if err != nil {
			return fmt.Errorf("harness %s terminal runtime observation: %w", name, err)
		}
		if terminalObservation.Tick != terminal.FinalTick || !terminalObservation.Timestamp.Equal(terminal.FinalTimestamp) {
			return fmt.Errorf("harness %s terminal fact differs from runtime observation", name)
		}
		wantTerminalTime := run.base.Add(time.Duration(terminal.FinalTick) * v8TickDuration)
		if !terminal.FinalTimestamp.Equal(wantTerminalTime) {
			return fmt.Errorf("harness %s terminal tick %d timestamp=%s, want deterministic timestamp %s", name, terminal.FinalTick, terminal.FinalTimestamp.Format(time.RFC3339Nano), wantTerminalTime.Format(time.RFC3339Nano))
		}
		if (run.harnesses[name].Err == nil) != terminalObservation.Clean {
			return fmt.Errorf("harness %s runtime clean=%t disagrees with CLI error=%v", name, terminalObservation.Clean, run.harnesses[name].Err)
		}
	}
	aTerminal, aTerminalOK := run.terminal["A"]
	bTerminal, bTerminalOK := run.terminal["B"]
	if !aTerminalOK || !bTerminalOK {
		return fmt.Errorf("terminal facts missing for A or B")
	}
	if aTerminal != bTerminal {
		return fmt.Errorf("terminal parity A vs B differs: A=%+v B=%+v", aTerminal, bTerminal)
	}
	for name, result := range run.harnesses {
		if result.Err != nil {
			return fmt.Errorf("harness %s CLI failed after %s: %w", name, result.Elapsed, result.Err)
		}
		if result.Elapsed > v8CommandMaxDuration+500*time.Millisecond {
			return fmt.Errorf("harness %s exceeded command bound: %s", name, result.Elapsed)
		}
	}
	return verifyV8Artifacts(run)
}

func v8RuntimeObservations(observations []services.SessionRuntimeObservation, kind services.SessionRuntimeObservationKind) []services.SessionRuntimeObservation {
	matched := make([]services.SessionRuntimeObservation, 0)
	for _, observation := range observations {
		if observation.Kind != kind {
			continue
		}
		observation.Payload = append([]byte(nil), observation.Payload...)
		matched = append(matched, observation)
	}
	return matched
}

func v8StreamTextMarkers(records []v8StreamRecord) []string {
	markers := make([]string, 0)
	for _, record := range records {
		if record.Type == string(messages.StreamTypeTextDelta) && record.Text != "" {
			markers = append(markers, record.Text)
		}
	}
	return markers
}

func verifyV8TranscriptMarkers(harness string, records []v8StreamRecord) error {
	wantMarkers := make([]string, v8MultiTurnCount)
	for index := range wantMarkers {
		wantMarkers[index] = fmt.Sprintf("%s transcript turn %d", harness, index+1)
	}
	gotMarkers := v8StreamTextMarkers(records)
	if len(gotMarkers) != len(wantMarkers) {
		return fmt.Errorf("multi-turn harness %s transcript ledger has %d markers, want %d: expected=%v observed=%v", harness, len(gotMarkers), len(wantMarkers), wantMarkers, gotMarkers)
	}
	for index, expected := range wantMarkers {
		if gotMarkers[index] != expected {
			turnKey := v8MultiTurnKey("A-to-B", index+1)
			if harness == "B" {
				turnKey = v8MultiTurnKey("B-to-A", index+1)
			}
			return fmt.Errorf("multi-turn harness %s turn %d (%s) transcript marker mismatch: expected=%q observed=%q", harness, index+1, turnKey, expected, gotMarkers[index])
		}
	}
	return nil
}

func v8InputDirection(harness string) string {
	if harness == "A" {
		return "B-to-A"
	}
	return "A-to-B"
}

func v8InputCrossingIndex(harness string, turn int) int {
	index := (turn - 1) * 2
	if harness == "A" {
		index++
	}
	return index
}

func v8InputCommitFailure(harness string, crossing v8Crossing, expected, observed []byte) error {
	wantHash, wantRMS := v8PCMStats(expected)
	gotHash, gotRMS := v8PCMStats(observed)
	return fmt.Errorf("multi-turn harness %s %s %s turn %d input commit PCM mismatch: expected hash=%s RMS=%.1f (> %.1f); observed hash=%s RMS=%.1f", harness, crossing.Direction, crossing.TurnKey, crossing.Turn, wantHash, wantRMS, v8VADThreshold, gotHash, gotRMS)
}

func verifyV8InputCommitLedger(harness string, result v8HarnessResult, crossings []v8Crossing, completions []services.SessionRuntimeObservation, expected [][]byte, base time.Time) error {
	direction := v8InputDirection(harness)
	commits := v8RuntimeObservations(result.Runtime, services.SessionRuntimeObservationInputCommit)
	markers := v8StreamTextMarkers(result.Stream)
	observedOrdinals := make([]int, 0, len(commits))
	for turnIndex, observation := range commits {
		turn := turnIndex + 1
		observedOrdinals = append(observedOrdinals, observation.InputCommit)
		if observation.InputCommit != turn {
			return fmt.Errorf("multi-turn harness %s direction %s %s turn %d input commit ordinal mismatch: expected=%d observed=%d", harness, direction, v8MultiTurnKey(direction, turn), turn, turn, observation.InputCommit)
		}
	}
	if len(commits) != v8MultiTurnCount {
		missingTurn := 0
		seen := make(map[int]struct{}, len(commits))
		for _, ordinal := range observedOrdinals {
			seen[ordinal] = struct{}{}
		}
		for turn := 1; turn <= v8MultiTurnCount; turn++ {
			if _, ok := seen[turn]; !ok {
				missingTurn = turn
				break
			}
		}
		if missingTurn == 0 {
			return fmt.Errorf("multi-turn harness %s input commit ledger has %d commits, want %d; duplicate or unexpected commit ordinals=%v", harness, len(commits), v8MultiTurnCount, observedOrdinals)
		}
		return fmt.Errorf("multi-turn harness %s input commit ledger has %d commits, want %d; missing stable %s turn %d; observed ordinals=%v", harness, len(commits), v8MultiTurnCount, v8MultiTurnKey(direction, missingTurn), missingTurn, observedOrdinals)
	}
	if len(markers) != v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s input commit ledger cannot bind transcript markers: expected %d, observed %d", harness, v8MultiTurnCount, len(markers))
	}
	for turnIndex, observation := range commits {
		turn := turnIndex + 1
		crossing := crossings[v8InputCrossingIndex(harness, turn)]
		turnKey := v8MultiTurnKey(direction, turn)
		completion := completions[turnIndex]
		wantTimestamp := base.Add(time.Duration(observation.Tick) * v8TickDuration)
		if !observation.Timestamp.Equal(wantTimestamp) {
			return fmt.Errorf("multi-turn harness %s %s turn %d input commit timestamp=%s is not deterministic for tick %d", harness, turnKey, turn, observation.Timestamp.Format(time.RFC3339Nano), observation.Tick)
		}
		if observation.Tick < crossing.Tick || (observation.Tick == crossing.Tick && observation.Timestamp.Before(crossing.Timestamp)) {
			return fmt.Errorf("multi-turn harness %s %s turn %d input commit precedes its audio crossing: commit tick=%d timestamp=%s; crossing tick=%d timestamp=%s", harness, turnKey, turn, observation.Tick, observation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
		}
		if completion.TurnsCompleted != turn || completion.Tick < observation.Tick || (completion.Tick == observation.Tick && completion.Timestamp.Before(observation.Timestamp)) {
			return fmt.Errorf("multi-turn harness %s %s turn %d input commit is not bound to completed turn: commit tick=%d timestamp=%s; completion turns=%d tick=%d timestamp=%s", harness, turnKey, turn, observation.Tick, observation.Timestamp.Format(time.RFC3339Nano), completion.TurnsCompleted, completion.Tick, completion.Timestamp.Format(time.RFC3339Nano))
		}
		if !bytes.Equal(observation.Payload, expected[turnIndex]) || !bytes.Equal(observation.Payload, crossing.Delivered) {
			return v8InputCommitFailure(harness, crossing, expected[turnIndex], observation.Payload)
		}
		_, rms := v8PCMStats(observation.Payload)
		if rms <= v8VADThreshold {
			return v8InputCommitFailure(harness, crossing, expected[turnIndex], observation.Payload)
		}
		expectedMarker := fmt.Sprintf("%s transcript turn %d", harness, turn)
		if markers[turnIndex] != expectedMarker {
			return fmt.Errorf("multi-turn harness %s %s turn %d input commit transcript attribution mismatch: expected=%q observed=%q", harness, turnKey, turn, expectedMarker, markers[turnIndex])
		}
	}
	return nil
}

func verifyV8ViewLedger(viewName string, view *v8RecordingView, crossings []v8Crossing, direction string, expected [][]byte) error {
	if view == nil {
		return fmt.Errorf("multi-turn recording view %s is missing", viewName)
	}
	records := view.snapshot()
	if len(records) != v8MultiTurnCount {
		return fmt.Errorf("multi-turn recording view %s has %d records, want %d", viewName, len(records), v8MultiTurnCount)
	}
	for turnIndex, payload := range expected {
		crossingIndex := turnIndex * 2
		if direction == "B-to-A" {
			crossingIndex++
		}
		if crossingIndex >= len(crossings) {
			return fmt.Errorf("multi-turn recording view %s turn %d has no crossing ledger entry", viewName, turnIndex+1)
		}
		crossing := crossings[crossingIndex]
		record := records[turnIndex]
		if record.Order != crossing.Sequence || record.Direction != crossing.Direction || record.Turn != crossing.Turn || record.TurnKey != crossing.TurnKey || record.Tick != crossing.Tick || !record.Timestamp.Equal(crossing.Timestamp) {
			return fmt.Errorf("multi-turn recording view %s turn %d identity/timing mismatch: expected order=%d direction=%s key=%s tick=%d timestamp=%s; observed order=%d direction=%s key=%s tick=%d timestamp=%s", viewName, turnIndex+1, crossing.Sequence, crossing.Direction, crossing.TurnKey, crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano), record.Order, record.Direction, record.TurnKey, record.Tick, record.Timestamp.Format(time.RFC3339Nano))
		}
		wantHash, wantRMS := v8PCMStats(payload)
		gotHash, gotRMS := v8PCMStats(record.Payload)
		if !bytes.Equal(record.Payload, payload) || record.SHA256 != wantHash || record.RMS != wantRMS {
			return fmt.Errorf("multi-turn recording view %s %s turn %d PCM identity mismatch: expected hash=%s RMS=%.1f; observed hash=%s RMS=%.1f", viewName, crossing.TurnKey, crossing.Turn, wantHash, wantRMS, gotHash, gotRMS)
		}
	}
	return nil
}

func verifyV8MultiTurnRun(run v8DuplexRun, aToB, bToA [][]byte) error {
	if len(aToB) != v8MultiTurnCount || len(bToA) != v8MultiTurnCount {
		return fmt.Errorf("multi-turn verifier expected %d scripted frames per direction, got A-to-B=%d B-to-A=%d", v8MultiTurnCount, len(aToB), len(bToA))
	}
	if len(run.harnesses) != 2 {
		return fmt.Errorf("multi-turn verifier expected two CLI harnesses, observed %d", len(run.harnesses))
	}
	if len(run.crossings) != len(v8MultiTurnSchedule()) {
		return fmt.Errorf("multi-turn verifier expected %d scheduled crossings, observed %d", len(v8MultiTurnSchedule()), len(run.crossings))
	}
	schedule := v8MultiTurnSchedule()
	overlapTurns := make(map[int]struct{})
	for _, entry := range schedule {
		if entry.Overlapping {
			overlapTurns[entry.Turn] = struct{}{}
		}
	}
	if len(overlapTurns) < 2 {
		return fmt.Errorf("multi-turn schedule has %d overlap turns, want at least two distinct overlap boundaries", len(overlapTurns))
	}
	if schedule[4].Overlapping || schedule[5].Overlapping || schedule[4].Tick == schedule[5].Tick {
		return fmt.Errorf("multi-turn schedule lacks the required sequential turn-3 boundary: entries=%+v", schedule[4:])
	}
	for direction, frames := range map[string][][]byte{"A-to-B": aToB, "B-to-A": bToA} {
		seen := make(map[string]int, len(frames))
		for turn, frame := range frames {
			hash := v8PCMHash(frame)
			if previous, ok := seen[hash]; ok && bytes.Equal(frames[previous], frame) {
				return fmt.Errorf("multi-turn %s scripted PCM identity is duplicated between turns %d and %d (hash=%s)", direction, previous+1, turn+1, hash)
			}
			seen[hash] = turn
		}
	}
	for index, entry := range schedule {
		crossing := run.crossings[index]
		if crossing.Sequence != index+1 || crossing.Schedule != index || crossing.Direction != entry.Direction || crossing.Turn != entry.Turn || crossing.TurnKey != v8MultiTurnKey(entry.Direction, entry.Turn) {
			return fmt.Errorf("multi-turn crossing %d identity mismatch: got sequence=%d schedule=%d direction=%s turn=%d key=%s; want direction=%s turn=%d key=%s", index+1, crossing.Sequence, crossing.Schedule, crossing.Direction, crossing.Turn, crossing.TurnKey, entry.Direction, entry.Turn, v8MultiTurnKey(entry.Direction, entry.Turn))
		}
		if crossing.Tick != entry.Tick {
			return fmt.Errorf("multi-turn %s turn %d recorded at logical tick %d, want %d", crossing.Direction, crossing.Turn, crossing.Tick, entry.Tick)
		}
		wantTimestamp := run.base.Add(time.Duration(entry.Tick) * v8TickDuration)
		if !crossing.Timestamp.Equal(wantTimestamp) {
			return fmt.Errorf("multi-turn %s turn %d timestamp=%s, want %s", crossing.Direction, crossing.Turn, crossing.Timestamp.Format(time.RFC3339Nano), wantTimestamp.Format(time.RFC3339Nano))
		}
		want := aToB[entry.Turn-1]
		if entry.Direction == "B-to-A" {
			want = bToA[entry.Turn-1]
		}
		if !bytes.Equal(crossing.Emitted, want) || !bytes.Equal(crossing.Delivered, want) {
			return v8PCMFailure(crossing, want, crossing.Delivered, "multi-turn bridge delivery")
		}
		_, rms := v8PCMStats(crossing.Delivered)
		if rms <= v8VADThreshold {
			return v8PCMFailure(crossing, want, crossing.Delivered, "multi-turn bridge delivery")
		}
	}

	for _, name := range []string{"A", "B"} {
		result := run.harnesses[name]
		if result.Err != nil {
			return fmt.Errorf("multi-turn harness %s CLI failed after %s: %w", name, result.Elapsed, result.Err)
		}
		if result.Instruction != map[string]string{"A": v8HarnessAInstruction, "B": v8HarnessBInstruction}[name] {
			return fmt.Errorf("multi-turn harness %s instruction = %q, want its distinct scripted instruction", name, result.Instruction)
		}
		if result.Elapsed > v8CommandMaxDuration+500*time.Millisecond {
			return fmt.Errorf("multi-turn harness %s exceeded command bound: %s", name, result.Elapsed)
		}
		outputObservations := v8RuntimeObservations(result.Runtime, services.SessionRuntimeObservationAudioOutput)
		inputObservations := v8RuntimeObservations(result.Runtime, services.SessionRuntimeObservationAudioInput)
		turnObservations := v8RuntimeObservations(result.Runtime, services.SessionRuntimeObservationTurnCompleted)
		if len(outputObservations) != v8MultiTurnCount || len(inputObservations) != v8MultiTurnCount || len(turnObservations) != v8MultiTurnCount {
			return fmt.Errorf("multi-turn harness %s runtime counts output=%d input=%d completed=%d, want %d each", name, len(outputObservations), len(inputObservations), len(turnObservations), v8MultiTurnCount)
		}
		for index, observation := range turnObservations {
			if observation.TurnsCompleted != index+1 {
				return fmt.Errorf("multi-turn harness %s completed-turn observation %d reports %d, want %d", name, index+1, observation.TurnsCompleted, index+1)
			}
		}
		if err := verifyV8TranscriptMarkers(name, result.Stream); err != nil {
			return err
		}
		inputExpected := aToB
		if name == "A" {
			inputExpected = bToA
		}
		if err := verifyV8InputCommitLedger(name, result, run.crossings, turnObservations, inputExpected, run.base); err != nil {
			return err
		}
		for index, observation := range outputObservations {
			entryIndex := index * 2
			if name == "B" {
				entryIndex++
			}
			crossing := run.crossings[entryIndex]
			if observation.Tick != crossing.Tick || !observation.Timestamp.Equal(crossing.Timestamp) || !bytes.Equal(observation.Payload, crossing.Emitted) {
				return fmt.Errorf("multi-turn harness %s output observation %d does not match %s turn %d timing or PCM", name, index+1, crossing.TurnKey, crossing.Turn)
			}
		}
		for index, observation := range inputObservations {
			entryIndex := index * 2
			if name == "A" {
				entryIndex++
			}
			crossing := run.crossings[entryIndex]
			if observation.Tick != crossing.Tick || !observation.Timestamp.Equal(crossing.Timestamp) || !bytes.Equal(observation.Payload, crossing.Delivered) {
				return fmt.Errorf("multi-turn harness %s input observation %d does not match %s turn %d timing or PCM", name, index+1, crossing.TurnKey, crossing.Turn)
			}
		}
		terminal, ok := run.terminal[name]
		if !ok || !terminal.Clean || !terminal.InputEOF || !terminal.OutputFrame || terminal.Turns != v8MultiTurnCount || terminal.FinalTick != v8MultiTurnFinalTick {
			return fmt.Errorf("multi-turn harness %s terminal facts are not clean or complete: %+v", name, terminal)
		}
		wantTerminalTime := run.base.Add(time.Duration(terminal.FinalTick) * v8TickDuration)
		if !terminal.FinalTimestamp.Equal(wantTerminalTime) {
			return fmt.Errorf("multi-turn harness %s terminal timestamp=%s, want %s", name, terminal.FinalTimestamp.Format(time.RFC3339Nano), wantTerminalTime.Format(time.RFC3339Nano))
		}
	}

	for _, expectation := range []struct {
		name      string
		direction string
		expected  [][]byte
	}{
		{name: "A/client", direction: "A-to-B", expected: aToB},
		{name: "B/agent", direction: "A-to-B", expected: aToB},
		{name: "B/client", direction: "B-to-A", expected: bToA},
		{name: "A/agent", direction: "B-to-A", expected: bToA},
	} {
		if err := verifyV8ViewLedger(expectation.name, run.views[expectation.name], run.crossings, expectation.direction, expectation.expected); err != nil {
			return err
		}
	}

	for _, pair := range [][2]string{{"A/client", "B/agent"}, {"B/client", "A/agent"}} {
		left := run.views[pair[0]].snapshot()
		right := run.views[pair[1]].snapshot()
		if len(left) != v8MultiTurnCount || len(right) != v8MultiTurnCount {
			return fmt.Errorf("multi-turn recording parity %s vs %s has %d and %d records, want %d each", pair[0], pair[1], len(left), len(right), v8MultiTurnCount)
		}
		for index := range left {
			if err := compareV8ViewRecords(fmt.Sprintf("%s turn %d", pair[0], index+1), left[index], fmt.Sprintf("%s turn %d", pair[1], index+1), right[index]); err != nil {
				return err
			}
		}
	}
	if run.finalTick != v8MultiTurnFinalTick {
		return fmt.Errorf("multi-turn final logical tick = %d, want %d", run.finalTick, v8MultiTurnFinalTick)
	}
	return verifyV8Artifacts(run)
}

func verifyV8Artifacts(run v8DuplexRun) error {
	viewNames := []string{"A/client", "A/agent", "B/client", "B/agent"}
	if len(run.artifacts) != len(viewNames)*2 {
		return fmt.Errorf("expected JSON and WAV artifacts for four views, observed %d paths", len(run.artifacts))
	}
	for _, viewName := range viewNames {
		jsonPath, jsonOK := run.artifacts[viewName+".json"]
		wavPath, wavOK := run.artifacts[viewName+".wav"]
		if !jsonOK || !wavOK {
			return fmt.Errorf("artifacts missing for %s", viewName)
		}
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			return fmt.Errorf("read %s JSON artifact: %w", viewName, err)
		}
		var artifact v8ViewArtifact
		if err := json.Unmarshal(data, &artifact); err != nil {
			return fmt.Errorf("decode %s JSON artifact: %w", viewName, err)
		}
		view := run.views[viewName]
		if view == nil {
			return fmt.Errorf("recording view %s is missing", viewName)
		}
		if artifact.Harness != view.Harness || artifact.Role != view.Role || artifact.SampleRate != audio.SampleRate {
			return fmt.Errorf("%s artifact metadata is invalid: %+v", viewName, artifact)
		}
		liveRecords := view.snapshot()
		if len(artifact.Records) != len(liveRecords) || len(artifact.Records) == 0 {
			return fmt.Errorf("%s artifact has %d records, live view has %d; want the same non-empty per-turn ledger", viewName, len(artifact.Records), len(liveRecords))
		}
		for index := range artifact.Records {
			if err := compareV8ViewRecords(fmt.Sprintf("%s artifact turn %d", viewName, index+1), artifact.Records[index], fmt.Sprintf("%s live turn %d", viewName, index+1), liveRecords[index]); err != nil {
				return err
			}
		}
		if wantTerminal, ok := run.terminal[view.Harness]; !ok || artifact.Terminal != wantTerminal {
			return fmt.Errorf("%s artifact terminal facts do not match the harness terminal facts", viewName)
		}

		wavData, err := os.ReadFile(wavPath)
		if err != nil {
			return fmt.Errorf("read %s WAV artifact: %w", viewName, err)
		}
		rate, samples, err := wavio.Read(bytes.NewReader(wavData))
		if err != nil {
			return fmt.Errorf("decode %s WAV artifact: %w", viewName, err)
		}
		livePayload := []byte{}
		for _, record := range liveRecords {
			livePayload = append(livePayload, record.Payload...)
		}
		if rate != audio.SampleRate || len(samples) != len(livePayload)/2 {
			return fmt.Errorf("%s WAV artifact shape is rate=%d samples=%d, want rate=%d samples=%d", viewName, rate, len(samples), audio.SampleRate, len(livePayload)/2)
		}
		if !bytes.Equal(v8PCM16Bytes(samples), livePayload) {
			return fmt.Errorf("%s WAV artifact payload differs from the recorded PCM", viewName)
		}
	}
	return nil
}

func v8PCMFailure(crossing v8Crossing, expected, observed []byte, view string) error {
	wantHash, wantRMS := v8PCMStats(expected)
	gotHash, gotRMS := v8PCMStats(observed)
	return fmt.Errorf("%s %s turn %d logical tick %d %s PCM mismatch: expected hash=%s RMS=%.1f (> %.1f); observed hash=%s RMS=%.1f", crossing.Direction, crossing.TurnKey, crossing.Turn, crossing.Tick, view, wantHash, wantRMS, v8VADThreshold, gotHash, gotRMS)
}

func compareV8ViewRecords(leftName string, left v8ViewRecord, rightName string, right v8ViewRecord) error {
	if left.Direction != right.Direction {
		return fmt.Errorf("recording parity %s vs %s direction differs: %s != %s", leftName, rightName, left.Direction, right.Direction)
	}
	if left.TurnKey != right.TurnKey || left.Turn != right.Turn {
		return fmt.Errorf("recording parity %s vs %s turn identity differs: left key=%s turn=%d; right key=%s turn=%d", leftName, rightName, left.TurnKey, left.Turn, right.TurnKey, right.Turn)
	}
	if left.Order != right.Order || left.Tick != right.Tick || !left.Timestamp.Equal(right.Timestamp) {
		return fmt.Errorf("recording parity %s vs %s timing/order differs: left order=%d tick=%d timestamp=%s; right order=%d tick=%d timestamp=%s", leftName, rightName, left.Order, left.Tick, left.Timestamp.Format(time.RFC3339Nano), right.Order, right.Tick, right.Timestamp.Format(time.RFC3339Nano))
	}
	if left.SHA256 != right.SHA256 || !bytes.Equal(left.Payload, right.Payload) || left.RMS != right.RMS {
		return fmt.Errorf("recording parity %s vs %s payload differs: hash %s != %s RMS %.1f != %.1f", leftName, rightName, left.SHA256, right.SHA256, left.RMS, right.RMS)
	}
	return nil
}

func assertV8GoroutinesSettled(t *testing.T, baseline int, operation string) {
	t.Helper()
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines after %s = %d, baseline = %d; CLI lifecycle did not settle", operation, runtime.NumGoroutine(), baseline)
}

func mutateV8ViewPayload(run *v8DuplexRun, viewName string, turn int, payload []byte) error {
	if run == nil {
		return fmt.Errorf("cannot mutate a nil multi-turn run")
	}
	view := run.views[viewName]
	if view == nil {
		return fmt.Errorf("multi-turn recording view %s is missing", viewName)
	}
	if turn < 1 || turn > v8MultiTurnCount {
		return fmt.Errorf("multi-turn recording view %s mutation turn %d is outside 1..%d", viewName, turn, v8MultiTurnCount)
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if turn > len(view.records) {
		return fmt.Errorf("multi-turn recording view %s has %d records; cannot mutate turn %d", viewName, len(view.records), turn)
	}
	view.records[turn-1].Payload = append([]byte(nil), payload...)
	view.records[turn-1].SHA256, view.records[turn-1].RMS = v8PCMStats(payload)
	return nil
}

func mutateV8TranscriptMarker(run *v8DuplexRun, harness string, turn int, replacement string) error {
	if run == nil {
		return fmt.Errorf("cannot mutate a nil multi-turn run")
	}
	if turn < 1 || turn > v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s transcript mutation turn %d is outside 1..%d", harness, turn, v8MultiTurnCount)
	}
	result, ok := run.harnesses[harness]
	if !ok {
		return fmt.Errorf("multi-turn harness %s is missing", harness)
	}
	markerIndex := 0
	for index := range result.Stream {
		if result.Stream[index].Type != string(messages.StreamTypeTextDelta) || result.Stream[index].Text == "" {
			continue
		}
		markerIndex++
		if markerIndex == turn {
			result.Stream[index].Text = replacement
			run.harnesses[harness] = result
			return nil
		}
	}
	return fmt.Errorf("multi-turn harness %s has no transcript marker for turn %d", harness, turn)
}

func mutateV8InputCommitPayload(run *v8DuplexRun, harness string, turn int, payload []byte) error {
	if run == nil {
		return fmt.Errorf("cannot mutate a nil multi-turn run")
	}
	if turn < 1 || turn > v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s input commit mutation turn %d is outside 1..%d", harness, turn, v8MultiTurnCount)
	}
	result, ok := run.harnesses[harness]
	if !ok {
		return fmt.Errorf("multi-turn harness %s is missing", harness)
	}
	commitOrdinal := 0
	for index := range result.Runtime {
		if result.Runtime[index].Kind != services.SessionRuntimeObservationInputCommit {
			continue
		}
		commitOrdinal++
		if commitOrdinal == turn {
			result.Runtime[index].Payload = append([]byte(nil), payload...)
			run.harnesses[harness] = result
			return nil
		}
	}
	return fmt.Errorf("multi-turn harness %s has no input commit for turn %d", harness, turn)
}

func dropV8InputCommit(run *v8DuplexRun, harness string, turn int) error {
	if run == nil {
		return fmt.Errorf("cannot mutate a nil multi-turn run")
	}
	if turn < 1 || turn > v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s input commit drop turn %d is outside 1..%d", harness, turn, v8MultiTurnCount)
	}
	result, ok := run.harnesses[harness]
	if !ok {
		return fmt.Errorf("multi-turn harness %s is missing", harness)
	}
	filtered := make([]services.SessionRuntimeObservation, 0, len(result.Runtime))
	commitOrdinal := 0
	dropped := false
	for _, observation := range result.Runtime {
		if observation.Kind == services.SessionRuntimeObservationInputCommit {
			commitOrdinal++
			if commitOrdinal == turn {
				dropped = true
				continue
			}
		}
		filtered = append(filtered, observation)
	}
	if !dropped {
		return fmt.Errorf("multi-turn harness %s has no input commit for turn %d", harness, turn)
	}
	result.Runtime = filtered
	run.harnesses[harness] = result
	return nil
}

func TestSessionCLI_DuplexPCMOverlap(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	aToB, bToA := v8LoudFrames(t, v8AudioFixturePath(t, "overlap_16k.wav"))
	run := runV8Duplex(t, aToB, bToA, false)
	if err := verifyV8Run(run, map[string][]byte{"A-to-B": aToB, "B-to-A": bToA}); err != nil {
		t.Fatalf("positive v8 duplex proof failed: %v", err)
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "positive duplex run")
	t.Logf("v8 positive evidence: shared clock base=%s tick_duration=%s overlap_tick=%d final_tick=%d crossings=%d", run.base.Format(time.RFC3339Nano), v8TickDuration, v8OverlapTick, run.finalTick, len(run.crossings))
}

func TestSessionCLI_DuplexPCMOverlapRejectsSilenceControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	aToB, bToA := v8LoudFrames(t, v8AudioFixturePath(t, "overlap_16k.wav"))
	run := runV8Duplex(t, aToB, bToA, true)
	err := verifyV8Run(run, map[string][]byte{"A-to-B": aToB, "B-to-A": bToA})
	if err == nil {
		t.Fatal("silence negative control passed the positive audio verification")
	}
	diagnostic := err.Error()
	if !strings.Contains(diagnostic, "A-to-B") || !strings.Contains(diagnostic, fmt.Sprintf("logical tick %d", v8OverlapTick)) || !strings.Contains(diagnostic, "RMS") || !strings.Contains(diagnostic, "hash=") {
		t.Fatalf("negative control diagnostic lacks direction/tick/hash/RMS details: %v", err)
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "silence negative control")
	t.Logf("v8 silence negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnSchedule(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := verifyV8MultiTurnRun(run, frames, frames); err != nil {
		t.Fatalf("positive v8 multi-turn duplex proof failed: %v", err)
	}
	t.Logf("v8 multi-turn evidence: final_tick=%d crossings=%d A_runtime=%d B_runtime=%d", run.finalTick, len(run.crossings), len(run.harnesses["A"].Runtime), len(run.harnesses["B"].Runtime))
	assertV8GoroutinesSettled(t, baselineGoroutines, "multi-turn duplex run")
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnAudioControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := mutateV8ViewPayload(&run, "B/agent", 2, frames[0]); err != nil {
		t.Fatalf("mutate later-turn PCM control: %v", err)
	}
	err := verifyV8MultiTurnRun(run, frames, frames)
	if err == nil {
		t.Fatal("later-turn PCM negative control passed the positive multi-turn verifier")
	}
	diagnostic := err.Error()
	for _, part := range []string{"B/agent", "turn 2", "PCM", "expected hash=", "observed hash="} {
		if !strings.Contains(diagnostic, part) {
			t.Fatalf("later-turn PCM diagnostic lacks %q: %v", part, err)
		}
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "later-turn PCM negative control")
	t.Logf("v8 later-turn PCM negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnTranscriptControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := mutateV8TranscriptMarker(&run, "B", 2, "A transcript turn 2"); err != nil {
		t.Fatalf("mutate later-turn transcript control: %v", err)
	}
	err := verifyV8MultiTurnRun(run, frames, frames)
	if err == nil {
		t.Fatal("later-turn transcript negative control passed the positive multi-turn verifier")
	}
	diagnostic := err.Error()
	for _, part := range []string{"harness B", "turn 2", "transcript", "expected=", "observed="} {
		if !strings.Contains(diagnostic, part) {
			t.Fatalf("later-turn transcript diagnostic lacks %q: %v", part, err)
		}
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "later-turn transcript negative control")
	t.Logf("v8 later-turn transcript negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls(t *testing.T) {
	t.Run("missing commit", func(t *testing.T) {
		baselineGoroutines := runtime.NumGoroutine()
		frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
		run := runV8MultiTurnDuplex(t, frames, frames)
		if err := dropV8InputCommit(&run, "A", 2); err != nil {
			t.Fatalf("drop later-turn input commit control: %v", err)
		}
		err := verifyV8MultiTurnRun(run, frames, frames)
		if err == nil {
			t.Fatal("missing later-turn input commit negative control passed the positive multi-turn verifier")
		}
		diagnostic := err.Error()
		for _, part := range []string{"harness A", "B-to-A", "B-turn-2", "input commit", "expected=2", "observed=3"} {
			if !strings.Contains(diagnostic, part) {
				t.Fatalf("missing input commit diagnostic lacks %q: %v", part, err)
			}
		}
		assertV8GoroutinesSettled(t, baselineGoroutines, "missing later-turn input commit negative control")
		t.Logf("v8 missing later-turn input commit negative control rejected as expected: %v", err)
	})

	t.Run("cross-attributed commit", func(t *testing.T) {
		baselineGoroutines := runtime.NumGoroutine()
		frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
		run := runV8MultiTurnDuplex(t, frames, frames)
		if err := mutateV8InputCommitPayload(&run, "A", 2, frames[0]); err != nil {
			t.Fatalf("mutate later-turn input commit control: %v", err)
		}
		err := verifyV8MultiTurnRun(run, frames, frames)
		if err == nil {
			t.Fatal("cross-attributed later-turn input commit negative control passed the positive multi-turn verifier")
		}
		diagnostic := err.Error()
		for _, part := range []string{"harness A", "B-to-A", "B-turn-2", "input commit", "expected hash=", "observed hash="} {
			if !strings.Contains(diagnostic, part) {
				t.Fatalf("cross-attributed input commit diagnostic lacks %q: %v", part, err)
			}
		}
		assertV8GoroutinesSettled(t, baselineGoroutines, "cross-attributed later-turn input commit negative control")
		t.Logf("v8 cross-attributed later-turn input commit negative control rejected as expected: %v", err)
	})
}
