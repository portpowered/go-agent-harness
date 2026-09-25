package lifecycle

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

// mediaBridge connects a local capture/playback pair to one provider media
// memory with provider or device speed.
type mediaBridge struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.Mutex
	err error
}

func newMediaBridge(parent context.Context, endpoints audio.MediaEndpoints, local rooms.MediaPorts, onError func(error)) *mediaBridge {
	ctx, cancel := context.WithCancel(parent)
	b := &mediaBridge{cancel: cancel, done: make(chan struct{})}
	var workers sync.WaitGroup
	start := func(run func(context.Context) error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				b.setError(err)
				if onError != nil {
					onError(err)
				}
				b.cancel()
			}
		}()
	}
	if local.Capture != nil && endpoints.Outbound != nil {
		start(func(ctx context.Context) error { return local.Capture.Pump(ctx, endpoints.Outbound) })
	}
	if local.Playback != nil && endpoints.Inbound != nil {
		start(func(ctx context.Context) error { return local.Playback.Pump(ctx, endpoints.Inbound) })
	}
	go func() {
		workers.Wait()
		close(b.done)
	}()
	return b
}

func (b *mediaBridge) setError(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	b.err = errors.Join(b.err, err)
	b.mu.Unlock()
}

func (b *mediaBridge) Wait() error {
	if b == nil {
		return nil
	}
	<-b.done
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func (b *mediaBridge) Stop() error {
	if b == nil {
		return nil
	}
	b.cancel()
	return nil
}

type frameFanout struct {
	graph    *roomGraph
	sourceID string
	recorder audioRecorder
	targets  []*mixer.Input
}

func (f frameFanout) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	targets := f.routeTargets()
	if f.recorder != nil {
		f.recorder.RecordSource(f.sourceID, frame)
	}
	if observer, ok := f.recorder.(latencyRecorder); ok {
		observer.ObserveSpeakerAudio(f.sourceID, f.targetIDs(targets), frame)
	}
	for _, target := range targets {
		if target == nil {
			continue
		}
		if err := target.WriteFrame(ctx, frame); err != nil {
			if f.graph != nil && f.graph.inputRetired(target) {
				continue
			}
			return err
		}
	}
	return nil
}

func (f frameFanout) targetIDs(targets []*mixer.Input) []string {
	if f.graph == nil {
		return nil
	}
	return f.graph.routeTargetIDs(targets)
}

func (f frameFanout) routeTargets() []*mixer.Input {
	if f.graph == nil {
		return f.targets
	}
	if f.graph.isRetired(f.sourceID) {
		return nil
	}
	return f.graph.sourceInputs(f.sourceID)
}

func (frameFanout) Close() error { return nil }

type bufferedInbound struct{ consumer audio.FrameConsumer }

func (b bufferedInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	return b.consumer.Receive(ctx)
}

func (bufferedInbound) Close() error { return nil }

func (b bufferedInbound) pump(ctx context.Context, playback rooms.MediaPlayback) error {
	return playback.Pump(ctx, b)
}

func isGraphNormalStop(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF)
}

const (
	// finalTurnDeliveryGrace bounds how long a reached max_turns stop waits for
	// the final responses' audio to reach their peers.
	finalTurnDeliveryGrace = 30 * time.Second
	// finalTurnDeliveryQuiet ends the wait early when no pending speaker's
	// audio has been handed to a peer for this long. It covers a response whose
	// boundary cannot be observed (for example, one mixed with an overlapping
	// speaker) without holding the room for the full grace.
	finalTurnDeliveryQuiet = time.Second
)

// finalTurnDelivery holds a reached max_turns stop until every agent's final
// response audio has been handed to its peers. A provider's MESSAGE.END
// (response.done) normally precedes playback of the response's queued audio,
// so stopping at the turn boundary would truncate the final response for the
// peer. The ledger counts, per agent, the assistant responses that produced
// audio (from the live event stream) and, per peer, the response boundaries
// the room graph has handed off. The stop is released once each agent's
// delivered boundaries cover the audio responses it had produced when the
// bound was reached, when pending speakers go quiet, or at the bounded grace.
// All waits use the room clock.
type finalTurnDelivery struct {
	clock  platformclock.TimerSource
	agents map[string]struct{}

	mu        sync.Mutex
	expected  map[string]int
	audioOpen map[string]bool
	delivered map[string]map[string]int
	graph     *roomGraph
	attached  bool
	ended     map[string]struct{}
	draining  bool
	need      map[string]int
	start     time.Time
	lastAudio time.Time
	changed   chan struct{}
	// pending is a reached bound whose wait starts once the media plane is
	// attached: agents stream (and may finish their turns) while the room
	// graph is still being built.
	pending *finalTurnWait
}

type finalTurnWait struct {
	ctx     context.Context
	release func()
}

func newFinalTurnDelivery(clock platformclock.TimerSource, manifest rooms.Manifest) *finalTurnDelivery {
	delivery := &finalTurnDelivery{
		clock: clock, agents: make(map[string]struct{}),
		expected: make(map[string]int), audioOpen: make(map[string]bool),
		delivered: make(map[string]map[string]int), ended: make(map[string]struct{}),
		changed: make(chan struct{}, 1),
	}
	for _, participant := range manifest.Participants {
		if roommanifest.NormalizeParticipantKind(participant.Kind) == rooms.ParticipantKindAgent {
			delivery.agents[participant.ID] = struct{}{}
		}
	}
	return delivery
}

// attach binds the room media plane once it is built and starts a wait
// whose bound was reached while the graph was still being built. Without a
// graph there is no peer audio to wait for, so a reached bound stops
// immediately. The graph reports hand-offs to the ledger from the moment its
// workers start (newRoomGraph), so no boundary is missed before attach.
func (d *finalTurnDelivery) attach(graph *roomGraph) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.graph, d.attached = graph, true
	pending := d.pending
	d.pending = nil
	d.mu.Unlock()
	if pending != nil {
		d.startWait(pending.ctx, pending.release)
	}
}

// participantEnded stops awaiting delivery to a participant whose session
// ended: it can no longer receive audio. Audio already queued for the
// remaining peers keeps draining through the graph until they have it.
func (d *finalTurnDelivery) participantEnded(participantID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.ended[participantID] = struct{}{}
	d.mu.Unlock()
	d.notify()
}

func (d *finalTurnDelivery) notify() {
	select {
	case d.changed <- struct{}{}:
	default:
	}
}

// handedOff records one mixed frame delivered to target. A response boundary
// is attributable only when the frame has a single contributing source.
func (d *finalTurnDelivery) handedOff(targetID string, sources []string, boundary bool) {
	if d == nil || len(sources) == 0 {
		return
	}
	d.mu.Lock()
	if boundary && len(sources) == 1 {
		byTarget := d.delivered[sources[0]]
		if byTarget == nil {
			byTarget = make(map[string]int)
			d.delivered[sources[0]] = byTarget
		}
		byTarget[targetID]++
	}
	draining := d.draining
	if draining {
		for _, source := range sources {
			if d.need[source] > 0 {
				d.lastAudio = d.clock.Now()
				break
			}
		}
	}
	d.mu.Unlock()
	if draining {
		d.notify()
	}
}

// begin freezes the audio responses produced so far and calls release once
// they have reached every peer, the pending speakers go quiet, or the grace
// expires. Responses started after the bound are not awaited. ctx ends the
// wait when the room stops for another reason. A bound reached before the
// media plane is attached waits for attach instead of stopping unheld.
func (d *finalTurnDelivery) begin(ctx context.Context, release func()) {
	if d == nil || d.clock == nil {
		release()
		return
	}
	d.mu.Lock()
	if d.draining {
		d.mu.Unlock()
		release()
		return
	}
	d.draining = true
	d.need = make(map[string]int, len(d.expected))
	for id, count := range d.expected {
		if count > 0 {
			d.need[id] = count
		}
	}
	d.start = d.clock.Now()
	d.lastAudio = d.start
	if !d.attached {
		d.pending = &finalTurnWait{ctx: ctx, release: release}
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.startWait(ctx, release)
}

func (d *finalTurnDelivery) startWait(ctx context.Context, release func()) {
	d.mu.Lock()
	if d.graph == nil {
		d.mu.Unlock()
		release()
		return
	}
	satisfied := d.satisfiedLocked()
	deadline := d.start.Add(finalTurnDeliveryGrace)
	d.mu.Unlock()
	if satisfied {
		release()
		return
	}
	go d.wait(ctx, deadline, release)
}

func (d *finalTurnDelivery) wait(ctx context.Context, deadline time.Time, release func()) {
	for {
		d.mu.Lock()
		satisfied := d.satisfiedLocked()
		wake := d.lastAudio.Add(finalTurnDeliveryQuiet)
		d.mu.Unlock()
		now := d.clock.Now()
		if satisfied || !now.Before(deadline) || !now.Before(wake) {
			release()
			return
		}
		if deadline.Before(wake) {
			wake = deadline
		}
		timer := d.clock.NewTimer(wake.Sub(now))
		if timer == nil {
			release()
			return
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-d.changed:
		case <-timer.C():
		}
		timer.Stop()
	}
}

// satisfiedLocked reports whether every awaited response boundary reached
// every live peer of its speaker. A peer whose session ended is not awaited.
func (d *finalTurnDelivery) satisfiedLocked() bool {
	for source, need := range d.need {
		for _, target := range d.graph.deliveryTargets(source) {
			if _, ended := d.ended[target]; ended {
				continue
			}
			if d.delivered[source][target] < need {
				return false
			}
		}
	}
	return true
}
