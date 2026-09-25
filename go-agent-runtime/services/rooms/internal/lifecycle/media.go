package lifecycle

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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
	draining  bool
	need      map[string]int
	lastAudio time.Time
	changed   chan struct{}
}

func newFinalTurnDelivery(clock platformclock.TimerSource, manifest rooms.Manifest) *finalTurnDelivery {
	delivery := &finalTurnDelivery{
		clock: clock, agents: make(map[string]struct{}),
		expected: make(map[string]int), audioOpen: make(map[string]bool),
		delivered: make(map[string]map[string]int), changed: make(chan struct{}, 1),
	}
	for _, participant := range manifest.Participants {
		if roommanifest.NormalizeParticipantKind(participant.Kind) == rooms.ParticipantKindAgent {
			delivery.agents[participant.ID] = struct{}{}
		}
	}
	return delivery
}

// attach binds the room media plane. Without a graph there is no peer audio
// to wait for, so a reached bound stops immediately.
func (d *finalTurnDelivery) attach(graph *roomGraph) {
	if d == nil || graph == nil {
		return
	}
	d.mu.Lock()
	d.graph = graph
	d.mu.Unlock()
	graph.delivery.Store(d)
}

// observe counts assistant responses that produced audio. An interrupted
// response's queued audio is discarded by the session, so it no longer
// expects a delivered boundary.
func (d *finalTurnDelivery) observe(participantID string, event session.LiveEvent) {
	if d == nil || event.Message == nil {
		return
	}
	if _, agent := d.agents[participantID]; !agent {
		return
	}
	message := event.Message
	if message.Role != "" && message.Role != messages.RoleAssistant {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if message.Type == messages.StreamTypeMessageEnd {
		if d.audioOpen[participantID] && responseInterrupted(message) {
			d.expected[participantID]--
		}
		d.audioOpen[participantID] = false
		return
	}
	if message.Type != messages.StreamTypeAudioDelta || d.audioOpen[participantID] {
		return
	}
	if value, ok := message.Value.(*messages.AudioDeltaValue); ok && value != nil && len(value.Content) > 0 {
		d.audioOpen[participantID] = true
		d.expected[participantID]++
	}
}

func responseInterrupted(message *messages.StreamMessage) bool {
	end, ok := message.Value.(*messages.MessageEndValue)
	return ok && end != nil && end.TerminalReason == messages.TerminalReasonPartialOutput
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
		select {
		case d.changed <- struct{}{}:
		default:
		}
	}
}

// begin freezes the audio responses produced so far and calls release once
// they have reached every peer, the pending speakers go quiet, or the grace
// expires. Responses started after the bound are not awaited. ctx ends the
// wait when the room stops for another reason.
func (d *finalTurnDelivery) begin(ctx context.Context, release func()) {
	if d == nil || d.clock == nil {
		release()
		return
	}
	d.mu.Lock()
	if d.graph == nil || d.draining {
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
	start := d.clock.Now()
	d.lastAudio = start
	satisfied := d.satisfiedLocked()
	d.mu.Unlock()
	if satisfied {
		release()
		return
	}
	go d.wait(ctx, start.Add(finalTurnDeliveryGrace), release)
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
// every live peer of its speaker.
func (d *finalTurnDelivery) satisfiedLocked() bool {
	for source, need := range d.need {
		for _, target := range d.graph.deliveryTargets(source) {
			if d.delivered[source][target] < need {
				return false
			}
		}
	}
	return true
}

// deliveryEventSink feeds live events to the final-turn ledger before the
// host sink, so a turn boundary is always counted after its audio events.
type deliveryEventSink struct {
	host     rooms.EventSink
	delivery *finalTurnDelivery
}

func (s deliveryEventSink) Publish(ctx context.Context, participantID string, event session.LiveEvent) error {
	s.delivery.observe(participantID, event)
	if s.host == nil {
		return nil
	}
	return s.host.Publish(ctx, participantID, event)
}
