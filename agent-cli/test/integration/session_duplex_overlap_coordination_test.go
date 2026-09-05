package integration

import runtimecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"

import (
	"context"
	"fmt"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"strings"
	"sync"
	"time"
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
	acceptRuntimeOutput(runtimecontract.SessionRuntimeObservation)
}

type v8RuntimeInputSink interface {
	acceptRuntimeInput(runtimecontract.SessionRuntimeObservation)
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
