package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

const playbackQueueFrames = 8

// roomGraph is the room's peer media plane. Each target owns one mixer whose
// inputs are every other admitted participant. Source workers read a provider
// inbound track or local capture once and fan the frame to those target
// inputs; no source is routed back to itself.
type roomGraph struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	mixers   map[string]*mixer.Mixer
	inputs   map[string][]*mixer.Input
	routeTo  map[*mixer.Input]string
	retired  map[string]struct{}
	outputs  []*graphOutput
	err      error
	recorder audioRecorder

	workers sync.WaitGroup
	close   sync.Once
}

type graphOutput struct {
	target       *activeParticipant
	mixer        *mixer.Mixer
	provider     audio.OutboundMedia
	playback     rooms.MediaPlayback
	queue        audio.FrameProducer
	consumer     audio.FrameConsumer
	queueControl audio.BufferControl
	// epoch belongs to this target's mixed output timeline. It is independent
	// from every source interruption epoch and is initialized into the
	// bounded playback queue before the first frame is admitted.
	epoch uint64
}

type audioRecorder interface {
	RecordSource(string, audio.PCMFrame)
	RecordReceived(string, audio.PCMFrame)
}

// latencyRecorder is an optional evidence capability. Keeping it separate
// from audioRecorder preserves the small recorder probe used by media tests
// while allowing production evidence to retain source attribution from the
// canonical mixer output.
type latencyRecorder interface {
	ObserveSpeakerAudio(string, []string, audio.PCMFrame)
	ObservePeerAudio(string, string, audio.PCMFrame)
}

func newRoomGraph(parent context.Context, scheduler clock.TimerSource, format rooms.AudioFormat, participants []*activeParticipant, onError func(error), recorders ...audioRecorder) (*roomGraph, error) {
	if scheduler == nil {
		return nil, mixer.ErrClockUnavailable
	}
	if parent == nil {
		return nil, mixer.ErrContextUnavailable
	}
	if format == (rooms.AudioFormat{}) {
		format = mixer.DefaultFormat()
	}
	frameSamples, err := format.FrameSamples()
	if err != nil {
		return nil, err
	}
	graph := newGraph(parent, recorders)
	if err := graph.initOutputs(scheduler, format, frameSamples, participants); err != nil {
		return graph.closeWithError(err)
	}
	if err := graph.initRoutes(participants); err != nil {
		return graph.closeWithError(err)
	}
	graph.startWorkers(participants, onError)
	return graph, nil
}

func newGraph(parent context.Context, recorders []audioRecorder) *roomGraph {
	ctx, cancel := context.WithCancel(parent)
	var recorder audioRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &roomGraph{
		ctx: ctx, cancel: cancel, mixers: make(map[string]*mixer.Mixer),
		inputs: make(map[string][]*mixer.Input), routeTo: make(map[*mixer.Input]string),
		retired: make(map[string]struct{}), recorder: recorder,
	}
}

func (g *roomGraph) closeWithError(err error) (*roomGraph, error) {
	return nil, errors.Join(err, g.Close())
}

func (g *roomGraph) initOutputs(scheduler clock.TimerSource, format rooms.AudioFormat, frameSamples int, participants []*activeParticipant) error {
	for _, target := range participants {
		if target == nil || target.participant.ID == "" {
			continue
		}
		output, err := g.newOutput(scheduler, format, frameSamples, target)
		if err != nil {
			return err
		}
		g.mixers[target.participant.ID] = output.mixer
		g.outputs = append(g.outputs, output)
	}
	return nil
}

func (g *roomGraph) newOutput(scheduler clock.TimerSource, format rooms.AudioFormat, frameSamples int, target *activeParticipant) (*graphOutput, error) {
	mix, err := mixer.New(g.ctx, scheduler, mixer.Config{Format: format, StreamID: "room:" + target.participant.ID})
	if err != nil {
		return nil, fmt.Errorf("create mixer for %q: %w", target.participant.ID, err)
	}
	output := &graphOutput{target: target, mixer: mix, epoch: 1, provider: target.endpoints.Outbound, playback: target.media.Playback}
	if output.playback == nil {
		return output, nil
	}
	producer, consumer, control, err := audio.NewFrameBuffer(playbackQueueFrames, frameSamples)
	if err != nil {
		return nil, fmt.Errorf("create playback queue for %q: %w", target.participant.ID, errors.Join(err, mix.Close()))
	}
	output.queue, output.consumer, output.queueControl = producer, consumer, control
	output.queueControl.Invalidate(output.epoch)
	return output, nil
}

func (g *roomGraph) initRoutes(participants []*activeParticipant) error {
	for _, source := range participants {
		if source == nil || source.participant.ID == "" {
			continue
		}
		if err := g.routeSource(source, participants); err != nil {
			return err
		}
	}
	return nil
}

func (g *roomGraph) routeSource(source *activeParticipant, participants []*activeParticipant) error {
	for _, target := range participants {
		if target == nil || target.participant.ID == "" || target.participant.ID == source.participant.ID {
			continue
		}
		mix := g.mixers[target.participant.ID]
		if mix == nil {
			continue
		}
		input, err := mix.AddInput(source.participant.ID)
		if err != nil {
			return fmt.Errorf("route %q to %q: %w", source.participant.ID, target.participant.ID, err)
		}
		g.inputs[source.participant.ID] = append(g.inputs[source.participant.ID], input)
		g.routeTo[input] = target.participant.ID
	}
	return nil
}

func (g *roomGraph) startWorkers(participants []*activeParticipant, onError func(error)) {
	for _, output := range g.outputs {
		g.startOutput(output, onError)
	}
	for _, source := range participants {
		if source == nil {
			continue
		}
		if source.endpoints.Inbound != nil {
			g.startSource(source.participant.ID, source.endpoints.Inbound, onError)
		}
		if source.media.Capture != nil {
			g.startCapture(source.participant.ID, onError)
		}
	}
}

func (g *roomGraph) startSource(sourceID string, inbound audio.InboundMedia, onError func(error)) {
	g.workers.Add(1)
	go func() {
		defer g.workers.Done()
		g.readSource(sourceID, inbound, onError)
	}()
}

func (g *roomGraph) readSource(sourceID string, inbound audio.InboundMedia, onError func(error)) {
	fanout := frameFanout{graph: g, sourceID: sourceID, recorder: g.recorder}
	for {
		frame, err := inbound.ReadFrame(g.ctx)
		if err != nil {
			if !isGraphNormalStop(err) {
				g.reportParticipantError(sourceID, err, onError)
			}
			return
		}
		if err := fanout.WriteFrame(g.ctx, frame); err != nil {
			if !isGraphNormalStop(err) {
				g.reportParticipantError(sourceID, fmt.Errorf("fan out provider audio for %q: %w", sourceID, err), onError)
			}
			return
		}
	}
}

func (g *roomGraph) startCapture(sourceID string, onError func(error)) {
	participant := g.participant(sourceID)
	if participant == nil || participant.media.Capture == nil {
		return
	}
	g.workers.Add(1)
	go func() {
		defer g.workers.Done()
		err := participant.media.Capture.Pump(g.ctx, frameFanout{graph: g, sourceID: sourceID, recorder: g.recorder})
		if err != nil && !isGraphNormalStop(err) {
			g.reportParticipantError(sourceID, fmt.Errorf("fan out capture for %q: %w", sourceID, err), onError)
		}
	}()
}

func (g *roomGraph) startOutput(output *graphOutput, onError func(error)) {
	if output == nil || output.mixer == nil || (output.provider == nil && output.playback == nil) {
		// A mixer output is evidence only after it has a consumer. In
		// particular, a capture-only human has no provider-bound or local
		// playback delivery, so emitting cadence-sized silence here would
		// fabricate received PCM for an unconsumed path.
		return
	}
	if output.playback != nil {
		g.startPlayback(output, onError)
	}
	g.workers.Add(1)
	go func() {
		defer g.workers.Done()
		g.readOutput(output, onError)
	}()
}

func (g *roomGraph) startPlayback(output *graphOutput, onError func(error)) {
	g.workers.Add(1)
	go func() {
		defer g.workers.Done()
		err := (bufferedInbound{consumer: output.consumer}).pump(g.ctx, output.playback)
		if err != nil && !isGraphNormalStop(err) {
			g.reportParticipantError(output.target.participant.ID, fmt.Errorf("play back room mix for %q: %w", output.target.participant.ID, err), onError)
		}
	}()
}

func (g *roomGraph) readOutput(output *graphOutput, onError func(error)) {
	inbound := output.mixer.OutputWithSources()
	for {
		mixed, err := inbound.ReadMixedFrame(g.ctx)
		if err != nil {
			if !isGraphNormalStop(err) {
				g.reportParticipantError(output.target.participant.ID, fmt.Errorf("read room mix for %q: %w", output.target.participant.ID, err), onError)
			}
			return
		}
		if g.isRetired(output.target.participant.ID) {
			return
		}
		frame := mixed.Frame
		frame.Epoch = output.epoch
		mixed.Frame = frame
		if err := g.deliverOutput(output, mixed); err != nil {
			if !isGraphNormalStop(err) {
				g.reportParticipantError(output.target.participant.ID, err, onError)
			}
			return
		}
	}
}

func (g *roomGraph) deliverOutput(output *graphOutput, mixed mixer.MixedFrame) error {
	frame := mixed.Frame
	if output.provider != nil {
		if err := output.provider.WriteFrame(g.ctx, frame); err != nil {
			return fmt.Errorf("write room mix for %q: %w", output.target.participant.ID, err)
		}
		if g.recorder != nil {
			g.recorder.RecordReceived(output.target.participant.ID, frame)
		}
		g.observePeerAudio(mixed.Sources, output.target.participant.ID, frame)
	}
	if output.queue == (audio.FrameProducer{}) {
		return nil
	}
	if err := output.queue.Submit(g.ctx, frame); err != nil {
		return fmt.Errorf("queue room playback for %q: %w", output.target.participant.ID, err)
	}
	if output.provider == nil && g.recorder != nil {
		g.recorder.RecordReceived(output.target.participant.ID, frame)
	}
	if output.provider == nil {
		g.observePeerAudio(mixed.Sources, output.target.participant.ID, frame)
	}
	return nil
}

func (g *roomGraph) observePeerAudio(sources []string, targetID string, frame audio.PCMFrame) {
	observer, ok := g.recorder.(latencyRecorder)
	if !ok || len(sources) == 0 {
		return
	}
	for _, sourceID := range sources {
		if sourceID == "" || sourceID == targetID {
			continue
		}
		observer.ObservePeerAudio(sourceID, targetID, frame)
	}
}
