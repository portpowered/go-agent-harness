package roommedia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

var errLifecycleHandleClosed = errors.New("room-media fake live handle is closed")

// Request is the intentionally small stdin contract of cmd/room-media.
// Paths are resolved by the caller and are never interpreted as credentials.
type Request struct {
	Mode      string `json:"mode"`
	OutputDir string `json:"output_dir"`
	Mutation  string `json:"mutation,omitempty"`
}

// Report is the machine-readable candidate evidence emitted by the child.
// The fields are deliberately projections of public contracts rather than
// private runtime structures.
type Report struct {
	SchemaVersion      int                            `json:"schema_version"`
	Status             string                         `json:"status"`
	Mode               string                         `json:"mode"`
	OutputDir          string                         `json:"output_dir"`
	SourceInputs       []InputObservation             `json:"source_inputs,omitempty"`
	PeerOutputs        map[string][]int16             `json:"peer_outputs,omitempty"`
	Epochs             EpochReport                    `json:"epochs,omitempty"`
	ProviderAdmission  AdmissionReport                `json:"provider_admission,omitempty"`
	Playback           PlaybackReport                 `json:"playback,omitempty"`
	PlaybackBoundary   PlaybackReport                 `json:"playback_boundary,omitempty"`
	Terminal           map[string]TerminalObservation `json:"terminal,omitempty"`
	Lifecycle          LifecycleReport                `json:"lifecycle,omitempty"`
	Recording          RecordingReport                `json:"recording,omitempty"`
	ReplayRejected     bool                           `json:"replay_rejected,omitempty"`
	ReplayRejectReason string                         `json:"replay_reject_reason,omitempty"`
	Room               RoomObservation                `json:"room,omitempty"`
	Cancellation       *CancellationReport            `json:"cancellation,omitempty"`
	RawEvents          []RawEvent                     `json:"raw_events,omitempty"`
}

type InputObservation struct {
	Participant string  `json:"participant"`
	Epoch       uint64  `json:"epoch"`
	Sequence    uint64  `json:"sequence"`
	Samples     []int16 `json:"samples"`
	End         bool    `json:"end_of_response"`
}

type EpochReport struct {
	StalePendingSamples int     `json:"stale_pending_samples"`
	HealthyTail         []int16 `json:"healthy_tail"`
	EndBeforeTerminal   bool    `json:"end_of_response_before_terminal"`
}

type AdmissionReport struct {
	Frames  int `json:"frames"`
	Samples int `json:"samples"`
}

type PlaybackReport struct {
	Domain            string             `json:"domain"`
	Admitted          []int16            `json:"admitted"`
	Consumed          []int16            `json:"consumed"`
	Underflow         []int16            `json:"underflow"`
	DiscardedStale    []int16            `json:"discarded_stale"`
	BeforeRender      uint64             `json:"rendered_before_callback"`
	CallbackCount     uint64             `json:"callback_count"`
	RenderedSamples   uint64             `json:"rendered_samples"`
	UnderflowSamples  uint64             `json:"underflow_samples"`
	DiscardedSamples  uint64             `json:"discarded_samples"`
	AdmittedSamples   uint64             `json:"admitted_samples"`
	ConsumedSamples   uint64             `json:"consumed_samples"`
	ZeroFilledSamples uint64             `json:"zero_filled_samples"`
	PhysicalDevice    bool               `json:"physical_device"`
	CapabilityGap     string             `json:"capability_gap,omitempty"`
	Observed          []FrameObservation `json:"observed,omitempty"`
}

// FrameObservation is an exact copy of one frame crossing the public media
// boundary. Summary fields are derived from these observations.
type FrameObservation struct {
	Epoch    uint64  `json:"epoch"`
	Sequence uint64  `json:"sequence"`
	Samples  []int16 `json:"samples"`
	End      bool    `json:"end_of_response"`
}

// RawEvent preserves the observed order of source audio, peer output,
// software playback input, and terminal events. A nonmatching frame is never
// silently discarded from this ledger.
type RawEvent struct {
	Kind           string  `json:"kind"`
	Participant    string  `json:"participant"`
	Target         string  `json:"target,omitempty"`
	Epoch          uint64  `json:"epoch"`
	Sequence       uint64  `json:"sequence"`
	Samples        []int16 `json:"samples,omitempty"`
	End            bool    `json:"end_of_response,omitempty"`
	AfterTerminal  bool    `json:"after_terminal,omitempty"`
	Reason         string  `json:"reason,omitempty"`
	Classification string  `json:"classification,omitempty"`
	TerminalReason string  `json:"terminal_reason,omitempty"`
	Provenance     string  `json:"provenance,omitempty"`
	OutputState    string  `json:"output_state,omitempty"`
}

type TerminalObservation struct {
	Kind           string `json:"kind"`
	Sequence       uint64 `json:"sequence"`
	Reason         string `json:"reason"`
	Classification string `json:"classification"`
	TerminalReason string `json:"terminal_reason"`
	Provenance     string `json:"provenance"`
	OutputState    string `json:"output_state"`
}

type LifecycleReport struct {
	Opened          int  `json:"opened"`
	Started         int  `json:"started"`
	Waited          int  `json:"waited"`
	Closed          int  `json:"closed"`
	RepeatedCloseOK bool `json:"repeated_close_ok"`
	WorkersJoined   bool `json:"workers_joined"`
	NaturalExit     bool `json:"natural_exit"`
}

type RecordingReport struct {
	State         string `json:"state"`
	ProviderTrace string `json:"provider_trace"`
	Reason        string `json:"reason"`
	PCMBytes      int    `json:"pcm_bytes"`
	Replayable    bool   `json:"replayable"`
}

type RoomObservation struct {
	TerminationReason string            `json:"termination_reason"`
	Error             string            `json:"error,omitempty"`
	Participants      map[string]string `json:"participants"`
}

type CancellationReport struct {
	Mode             string            `json:"mode"`
	StartError       string            `json:"start_error,omitempty"`
	WaitError        string            `json:"wait_error,omitempty"`
	FirstCloseError  string            `json:"first_close_error,omitempty"`
	SecondCloseError string            `json:"second_close_error,omitempty"`
	RoomTermination  string            `json:"room_termination,omitempty"`
	RoomError        string            `json:"room_error,omitempty"`
	Participants     map[string]string `json:"participants,omitempty"`
	Opened           int               `json:"opened"`
	Started          int               `json:"started"`
	Waited           int               `json:"waited"`
	Closed           int               `json:"closed"`
	CloseCalls       int               `json:"close_calls"`
	Joined           bool              `json:"joined"`
	RunReturned      bool              `json:"run_returned"`
	PublicServiceRun bool              `json:"public_service_run"`
	ClosedByService  bool              `json:"closed_by_service"`
	RepeatedCloseOK  bool              `json:"repeated_close_ok"`
	SourceFrames     int               `json:"source_frames"`
	OutputFrames     int               `json:"output_frames"`
	MediaPumpActive  bool              `json:"media_pump_active"`
}

type coordinator struct {
	mu               sync.Mutex
	outputs          map[string][]int16
	playback         []int16
	inputs           []InputObservation
	rawEvents        []RawEvent
	terminalSeen     map[string]bool
	outputFrames     map[string]int
	outputEnded      map[string]bool
	playbackFrames   int
	handles          map[string]*fakeHandle
	done             chan struct{}
	healthyReady     chan struct{}
	routedReady      chan struct{}
	activeMediaReady chan struct{}
	healthyCount     int
	routedCount      int
	readyOnce        sync.Once
	routedOnce       sync.Once
	activeMediaOnce  sync.Once
	finished         bool
	finish           sync.Once
}

func newCoordinator() *coordinator {
	return &coordinator{
		outputs:      make(map[string][]int16),
		terminalSeen: make(map[string]bool),
		outputFrames: make(map[string]int),
		outputEnded:  make(map[string]bool),
		handles:      make(map[string]*fakeHandle),
		done:             make(chan struct{}),
		healthyReady:     make(chan struct{}),
		routedReady:      make(chan struct{}),
		activeMediaReady: make(chan struct{}),
	}
}

func (c *coordinator) awaitHealthyInput(ctx context.Context) error {
	c.mu.Lock()
	c.healthyCount++
	if c.healthyCount == 2 {
		c.readyOnce.Do(func() { close(c.healthyReady) })
	}
	ready := c.healthyReady
	c.mu.Unlock()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *coordinator) inputsReady() bool {
	select {
	case <-c.healthyReady:
		return true
	default:
		return false
	}
}

func (c *coordinator) markInputRouted() {
	c.mu.Lock()
	c.routedCount++
	if c.routedCount == 2 {
		c.routedOnce.Do(func() { close(c.routedReady) })
	}
	c.mu.Unlock()
}

func (c *coordinator) inputsRoutedReady() bool {
	select {
	case <-c.routedReady:
		return true
	default:
		return false
	}
}

func (c *coordinator) recordInput(participant string, frame audio.PCMFrame) error {
	c.mu.Lock()
	samples := append([]int16(nil), frame.Samples...)
	afterTerminal := c.terminalSeen[participant]
	c.inputs = append(c.inputs, InputObservation{
		Participant: participant, Epoch: frame.Epoch, Sequence: frame.Sequence,
		Samples: samples, End: frame.EndOfResponse,
	})
	c.rawEvents = append(c.rawEvents, RawEvent{
		Kind: "source_audio", Participant: participant, Epoch: frame.Epoch,
		Sequence: frame.Sequence, Samples: samples, End: frame.EndOfResponse,
		AfterTerminal: afterTerminal,
	})
	c.mu.Unlock()
	if afterTerminal {
		return fmt.Errorf("source audio for %s arrived after terminal", participant)
	}
	return nil
}

func (c *coordinator) register(handle *fakeHandle) {
	c.mu.Lock()
	c.handles[handle.id] = handle
	c.mu.Unlock()
}

func (c *coordinator) recordOutput(target string, frame audio.PCMFrame) error {
	c.mu.Lock()
	copySamples := append([]int16(nil), frame.Samples...)
	afterTerminal := c.terminalSeen[target]
	c.rawEvents = append(c.rawEvents, RawEvent{
		Kind: "peer_output", Participant: target, Target: target,
		Epoch: frame.Epoch, Sequence: frame.Sequence, Samples: copySamples,
		End: frame.EndOfResponse, AfterTerminal: afterTerminal,
	})
	if afterTerminal {
		c.mu.Unlock()
		return fmt.Errorf("peer output for %s arrived after terminal", target)
	}
	if c.finished {
		c.mu.Unlock()
		return context.Canceled
	}
	if frame.EndOfResponse {
		c.outputEnded[target] = true
	}
	c.outputs[target] = append(c.outputs[target], frame.Samples...)
	c.outputFrames[target]++
	c.mu.Unlock()
	// Handle.Start proves provider admission only. This signal is emitted by
	// the public room graph after a nonempty frame crosses an output endpoint,
	// so cancellation cannot claim an active media run too early.
	if len(frame.Samples) > 0 || frame.EndOfResponse {
		c.activeMediaOnce.Do(func() { close(c.activeMediaReady) })
	}
	c.maybeFinish()
	return nil
}

func (c *coordinator) recordPlayback(frame audio.PCMFrame) error {
	c.mu.Lock()
	copySamples := append([]int16(nil), frame.Samples...)
	afterTerminal := c.terminalSeen["listener"]
	c.rawEvents = append(c.rawEvents, RawEvent{
		Kind: "playback_input", Participant: "listener", Target: "listener",
		Epoch: frame.Epoch, Sequence: frame.Sequence, Samples: copySamples,
		End: frame.EndOfResponse, AfterTerminal: afterTerminal,
	})
	if afterTerminal {
		c.mu.Unlock()
		return errors.New("playback input arrived after terminal")
	}
	if c.finished {
		c.mu.Unlock()
		return context.Canceled
	}
	if len(frame.Samples) > 0 {
		c.playback = append(c.playback, frame.Samples...)
		c.playbackFrames++
	}
	c.mu.Unlock()
	c.maybeFinish()
	return nil
}

func (c *coordinator) maybeFinish() {
	c.mu.Lock()
	ready := c.outputFrames["alice"] > 0 && c.outputFrames["bob"] > 0 &&
		c.outputEnded["alice"] && c.outputEnded["bob"] && c.playbackFrames > 0
	c.mu.Unlock()
	if !ready {
		return
	}
	c.finish.Do(func() {
		c.mu.Lock()
		c.finished = true
		handles := make([]*fakeHandle, 0, len(c.handles))
		for _, handle := range c.handles {
			handles = append(handles, handle)
		}
		c.mu.Unlock()
		for _, handle := range handles {
			handle.publishTerminal()
		}
		close(c.done)
	})
}

func (c *coordinator) snapshotInputs() []InputObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	inputs := append([]InputObservation(nil), c.inputs...)
	sort.SliceStable(inputs, func(i, j int) bool {
		if inputs[i].Participant != inputs[j].Participant {
			return inputs[i].Participant < inputs[j].Participant
		}
		return inputs[i].Sequence < inputs[j].Sequence
	})
	return inputs
}

func (c *coordinator) snapshotOutputs() map[string][]int16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	outputs := make(map[string][]int16, len(c.outputs))
	for participant, samples := range c.outputs {
		outputs[participant] = append([]int16(nil), samples...)
	}
	return outputs
}

func (c *coordinator) snapshotRawEvents() []RawEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	events := append([]RawEvent(nil), c.rawEvents...)
	for index := range events {
		events[index].Samples = append([]int16(nil), events[index].Samples...)
	}
	return events
}

func (c *coordinator) frameCounts() (source, output int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inputs), c.outputFrames["alice"] + c.outputFrames["bob"]
}

func (c *coordinator) recordTerminal(participant string, event session.LiveEvent) {
	c.mu.Lock()
	c.terminalSeen[participant] = true
	if terminal := event.Terminal; terminal != nil {
		c.rawEvents = append(c.rawEvents, RawEvent{
			Kind: "terminal", Participant: participant, Sequence: event.Sequence,
			Reason: terminal.Reason, Classification: terminal.Classification,
			TerminalReason: string(terminal.TerminalReason),
			Provenance:     string(terminal.TerminalProvenance), OutputState: string(terminal.OutputState),
		})
	} else {
		c.rawEvents = append(c.rawEvents, RawEvent{
			Kind: "terminal", Participant: participant, Sequence: event.Sequence,
		})
	}
	c.mu.Unlock()
}

type fakeLiveService struct {
	coord  *coordinator
	mu     sync.Mutex
	opened int
}

func (s *fakeLiveService) OpenLive(_ context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	if s == nil || s.coord == nil {
		return nil, errors.New("fake live service is unavailable")
	}
	handle := newFakeHandle(request.ParticipantID, s.coord)
	s.mu.Lock()
	s.opened++
	s.mu.Unlock()
	s.coord.register(handle)
	return handle, nil
}

type fakeHandle struct {
	id       string
	coord    *coordinator
	inbound  *scriptedInbound
	outbound *captureOutbound
	events   chan session.LiveEvent
	done     chan struct{}

	mu            sync.Mutex
	started       bool
	canceled      bool
	closed        bool
	closeEvents   sync.Once
	closeDone     sync.Once
	terminal      sync.Once
	startCount    int
	waitCount     int
	waitReturned  int
	closeCount    int
	closeCalls    int
	terminalEvent *session.LiveEvent
	waitReady     chan struct{}
	waitOnce      sync.Once
}

func newFakeHandle(id string, coord *coordinator) *fakeHandle {
	return &fakeHandle{
		id: id, coord: coord, events: make(chan session.LiveEvent, 8), done: make(chan struct{}),
		inbound:   &scriptedInbound{id: id, frames: fixtureFrames(id), coord: coord},
		outbound:  &captureOutbound{id: id, coord: coord},
		waitReady: make(chan struct{}),
	}
}

func (h *fakeHandle) Media() audio.MediaEndpoints {
	return audio.MediaEndpoints{Inbound: h.inbound, Outbound: h.outbound}
}

func (h *fakeHandle) Events() <-chan session.LiveEvent { return h.events }

func (h *fakeHandle) Start(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.canceled {
		return errLifecycleHandleClosed
	}
	if h.started {
		return errors.New("fake live handle started twice")
	}
	h.started = true
	h.startCount++
	return nil
}

func (h *fakeHandle) Send(context.Context, session.LiveControl) error { return nil }

func (h *fakeHandle) Cancel(error) {
	h.mu.Lock()
	h.canceled = true
	h.mu.Unlock()
	h.closeDone.Do(func() { close(h.done) })
}

func (h *fakeHandle) Wait() error {
	h.mu.Lock()
	h.waitCount++
	h.mu.Unlock()
	<-h.done
	h.mu.Lock()
	h.waitReturned++
	h.mu.Unlock()
	h.waitOnce.Do(func() { close(h.waitReady) })
	return nil
}

func (h *fakeHandle) Close() error {
	h.mu.Lock()
	h.closed = true
	h.closeCalls++
	if h.closeCount == 0 {
		h.closeCount++
	}
	h.mu.Unlock()
	h.closeDone.Do(func() { close(h.done) })
	_ = h.inbound.Close()
	_ = h.outbound.Close()
	h.closeEvents.Do(func() { close(h.events) })
	return nil
}

func (h *fakeHandle) publishTerminal() {
	h.terminal.Do(func() {
		value := messages.NewSessionCloseValueWithTerminal(
			h.id,
			"fixture_complete",
			"fixture_complete",
			messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputComplete,
		)
		event := session.LiveEvent{
			Sequence: 99, Kind: string(session.LiveEventTerminal), SessionID: h.id,
			ParticipantID: h.id, Terminal: value, Critical: true,
		}
		h.mu.Lock()
		h.terminalEvent = &event
		h.mu.Unlock()
		h.coord.recordTerminal(h.id, event)
		h.events <- event
		h.closeDone.Do(func() { close(h.done) })
	})
}

type scriptedInbound struct {
	id     string
	mu     sync.Mutex
	frames []audio.PCMFrame
	index  int
	routed bool
	closed bool
	coord  *coordinator
}

func (s *scriptedInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	if err := ctx.Err(); err != nil {
		return audio.PCMFrame{}, err
	}
	s.mu.Lock()
	if s.index >= len(s.frames) {
		if !s.routed {
			s.routed = true
			if s.coord != nil {
				s.coord.markInputRouted()
			}
		}
		s.mu.Unlock()
		return audio.PCMFrame{}, io.EOF
	}
	frame := s.frames[s.index]
	s.index++
	frame.Samples = append([]int16(nil), frame.Samples...)
	index := s.index
	s.mu.Unlock()
	if s.coord != nil {
		if err := s.coord.recordInput(s.id, frame); err != nil {
			return audio.PCMFrame{}, err
		}
		if index == 2 {
			if err := s.coord.awaitHealthyInput(ctx); err != nil {
				return audio.PCMFrame{}, err
			}
		}
	}
	return frame, nil
}

func (s *scriptedInbound) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

type captureOutbound struct {
	id     string
	coord  *coordinator
	mu     sync.Mutex
	closed bool
	ended  bool
}

func (o *captureOutbound) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	closed, ended := o.closed, o.ended
	o.mu.Unlock()
	if closed || ended {
		// EndOfResponse is the public output boundary. Once the consumer has
		// accepted it, the endpoint is complete and must not admit the mixer's
		// cadence-sized silence that follows an exhausted source.
		return context.Canceled
	}
	if err := o.coord.recordOutput(o.id, frame); err != nil {
		return err
	}
	if frame.EndOfResponse {
		o.mu.Lock()
		o.ended = true
		o.mu.Unlock()
	}
	return nil
}

func (o *captureOutbound) Close() error {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	return nil
}

type softwarePlayback struct {
	coord  *coordinator
	queue  *audio.PlaybackQueue
	mu     sync.Mutex
	report PlaybackReport
}

func newSoftwarePlayback(coord *coordinator, format audio.DeviceFormat) (*softwarePlayback, error) {
	queue, err := audio.NewPlaybackQueueWithLatency(format, 20*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &softwarePlayback{
		coord: coord, queue: queue,
		report: PlaybackReport{
			Domain: "software-playback", PhysicalDevice: false,
			CapabilityGap: "no physical device was opened; this is a software admission/consumption proof",
		},
	}, nil
}

// Pump separates room/provider admission from device-clock consumption. The
// queue is the public bounded playback seam; no host device is opened.
func (p *softwarePlayback) Pump(ctx context.Context, inbound audio.InboundMedia) error {
	for {
		frame, err := inbound.ReadFrame(ctx)
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.report.Observed = append(p.report.Observed, FrameObservation{
			Epoch: frame.Epoch, Sequence: frame.Sequence,
			Samples: append([]int16(nil), frame.Samples...), End: frame.EndOfResponse,
		})
		p.mu.Unlock()
		if err := p.coord.recordPlayback(frame); err != nil {
			return err
		}
		if len(frame.Samples) == 0 || allZero(frame.Samples) {
			continue
		}
		p.mu.Lock()
		p.report.Admitted = append(p.report.Admitted, frame.Samples...)
		p.report.AdmittedSamples += uint64(len(frame.Samples))
		p.queue.Enqueue(frame.Samples)
		p.report.BeforeRender = p.queue.Snapshot().RenderedSamples
		first := make([]int16, len(frame.Samples))
		p.queue.RenderInto(first)
		p.report.Consumed = append(p.report.Consumed, first...)
		second := make([]int16, 2)
		p.queue.RenderInto(second)
		p.report.Underflow = append(p.report.Underflow, second...)
		stats := p.queue.Snapshot()
		p.report.CallbackCount = stats.CallbackCount
		p.report.RenderedSamples = stats.RenderedSamples
		p.report.UnderflowSamples = stats.UnderflowSamples
		p.report.DiscardedSamples = stats.DiscardedSamples
		p.report.ZeroFilledSamples = stats.ZeroFilledSamples
		p.report.ConsumedSamples = stats.RenderedSamples - stats.UnderflowSamples
		p.mu.Unlock()
		<-p.coord.done
		return context.Canceled
	}
}

func (p *softwarePlayback) Close() error { return nil }

func (p *softwarePlayback) queueSnapshot() PlaybackReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.report
}

func fixtureFrames(id string) []audio.PCMFrame {
	first, second := []int16{101, 102}, []int16{111, 112, 113, 114}
	if id == "bob" {
		first, second = []int16{201, 202}, []int16{211, 212, 213, 214}
	}
	return []audio.PCMFrame{
		{Samples: first, Format: audio.PCM16DeviceFormat(1000), StreamID: id + "-provider", Epoch: 1, Sequence: 1, StartSample: 0},
		{Samples: second, Format: audio.PCM16DeviceFormat(1000), StreamID: id + "-provider", Epoch: 2, Sequence: 2, StartSample: 2, EndOfResponse: true},
	}
}

func allZero(samples []int16) bool {
	if len(samples) == 0 {
		return true
	}
	for _, sample := range samples {
		if sample != 0 {
			return false
		}
	}
	return true
}

func buildManifest() rooms.Manifest {
	return rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{Interactive: true},
		Participants: []rooms.Participant{
			{Kind: rooms.ParticipantKindAgent, ID: "alice", SystemPrompt: "fixture", OpeningPrompt: "begin", Provider: "fixture", Model: "fixture", APIKeyEnv: "C36_FIXTURE_KEY", Tools: []string{}},
			{Kind: rooms.ParticipantKindAgent, ID: "bob", SystemPrompt: "fixture", OpeningPrompt: "begin", Provider: "fixture", Model: "fixture", APIKeyEnv: "C36_FIXTURE_KEY", Tools: []string{}},
			{Kind: rooms.ParticipantKindHuman, ID: "listener", SystemPrompt: "fixture", InputDevice: "software-input", OutputDevice: "software-output", Tools: []string{}},
		},
	}
}

func buildCancellationManifest() rooms.Manifest {
	return rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{Interactive: true},
		Participants: []rooms.Participant{
			{Kind: rooms.ParticipantKindAgent, ID: "alice", SystemPrompt: "fixture", OpeningPrompt: "begin", Provider: "fixture", Model: "fixture", APIKeyEnv: "C36_FIXTURE_KEY", Tools: []string{}},
			{Kind: rooms.ParticipantKindAgent, ID: "bob", SystemPrompt: "fixture", Provider: "fixture", Model: "fixture", APIKeyEnv: "C36_FIXTURE_KEY", Tools: []string{}},
		},
	}
}

func runLiveScenario(outputDir string, mode string) (Report, error) {
	if outputDir == "" {
		return Report{}, errors.New("output directory is required")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return Report{}, fmt.Errorf("create output directory: %w", err)
	}
	format := mixer.Format{SampleRate: 1000, Channels: 1, FrameDuration: 4 * time.Millisecond}
	deviceFormat := audio.PCM16DeviceFormat(format.SampleRate)
	scheduler := clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond)
	coord := newCoordinator()
	live := &fakeLiveService{coord: coord}
	playback, err := newSoftwarePlayback(coord, deviceFormat)
	if err != nil {
		return Report{}, err
	}
	media := rooms.MediaFactoryFunc(func(_ context.Context, participant rooms.Participant, _ rooms.AudioFormat) (rooms.MediaPorts, error) {
		if participant.ID != "listener" {
			return rooms.MediaPorts{}, nil
		}
		return rooms.MediaPorts{Playback: playback}, nil
	})
	service := wire.NewService(wire.Dependencies{Live: live, Media: media, Clock: scheduler})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan struct {
		result rooms.RoomResult
		err    error
	}, 1)
	go func() {
		result, runErr := service.Run(runCtx, io.Discard, rooms.RoomRunOptions{
			Manifest: buildManifest(), OutputDir: outputDir, ConfigDir: outputDir,
			WorkDir: outputDir, AllowPaths: []string{outputDir}, AudioFormat: format,
		})
		resultCh <- struct {
			result rooms.RoomResult
			err    error
		}{result, runErr}
	}()

	var outcome struct {
		result rooms.RoomResult
		err    error
	}
	deadline := time.Now().Add(8 * time.Second)
	primed := false
	for {
		select {
		case outcome = <-resultCh:
			goto finished
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			select {
			case outcome = <-resultCh:
			case <-time.After(500 * time.Millisecond):
				return Report{}, errors.New("room service exceeded bounded scenario timeout")
			}
			return Report{}, fmt.Errorf("room service timeout: %w", outcome.err)
		}
		if !coord.inputsRoutedReady() {
			runtime.Gosched()
			continue
		}
		if !primed {
			for i := 0; i < 1000; i++ {
				runtime.Gosched()
			}
			primed = true
		}
		scheduler.Advance()
		runtime.Gosched()
	}

finished:
	liveHandles := live.handles()
	inputs := coord.snapshotInputs()
	outputs := coord.snapshotOutputs()
	rawEvents := coord.snapshotRawEvents()
	report := Report{
		SchemaVersion: 1, Status: "complete", Mode: mode, OutputDir: outputDir,
		SourceInputs: inputs, PeerOutputs: outputs,
		ProviderAdmission: admissionFromInputs(inputs), Playback: playback.queueSnapshot(),
		Epochs: epochReport(inputs, outputs, rawEvents), Terminal: make(map[string]TerminalObservation),
		Room:      RoomObservation{TerminationReason: string(outcome.result.TerminationReason), Participants: map[string]string{}},
		Lifecycle: LifecycleReport{Opened: len(liveHandles), NaturalExit: outcome.err == nil},
		Recording: RecordingReport{State: "partial", ProviderTrace: "unavailable", Reason: "fixture live handles intentionally do not emit provider capture artifacts", PCMBytes: inputPCMBytes(inputs), Replayable: false},
		RawEvents: rawEvents,
	}
	for id, participant := range outcome.result.Participants {
		report.Room.Participants[id] = string(participant.TerminationReason)
	}
	for id, handle := range liveHandles {
		handle.mu.Lock()
		report.Lifecycle.Started += handle.startCount
		report.Lifecycle.Waited += handle.waitReturned
		report.Lifecycle.Closed += handle.closeCount
		terminal := handle.terminalEvent
		handle.mu.Unlock()
		if terminal == nil || terminal.Terminal == nil {
			continue
		}
		report.Terminal[id] = TerminalObservation{
			Kind: terminal.Kind, Sequence: terminal.Sequence, Reason: terminal.Terminal.Reason,
			Classification: terminal.Terminal.Classification, TerminalReason: string(terminal.Terminal.TerminalReason),
			Provenance: string(terminal.Terminal.TerminalProvenance), OutputState: string(terminal.Terminal.OutputState),
		}
	}
	report.Lifecycle.WorkersJoined = report.Lifecycle.Opened > 0 && report.Lifecycle.Waited == report.Lifecycle.Opened && report.Lifecycle.Closed == report.Lifecycle.Opened
	report.Lifecycle.RepeatedCloseOK = true
	for _, handle := range liveHandles {
		report.Lifecycle.RepeatedCloseOK = report.Lifecycle.RepeatedCloseOK && handle.Close() == nil && handle.Close() == nil
	}
	if outcome.err != nil {
		report.Status = "failed"
		report.Room.Error = outcome.err.Error()
	}
	if _, replayErr := service.LoadReplayPlan(outputDir); replayErr != nil {
		report.ReplayRejected = true
		report.ReplayRejectReason = replayErr.Error()
	} else {
		report.ReplayRejected = false
		report.ReplayRejectReason = "partial output unexpectedly loaded as a replay plan"
	}
	if outcome.err == nil {
		if validationErr := validateObservedReport(report); validationErr != nil {
			report.Status = "failed"
			report.Room.Error = validationErr.Error()
			return report, validationErr
		}
	}
	return report, outcome.err
}

func validateObservedReport(report Report) error {
	if report.Status != "complete" {
		return fmt.Errorf("room report status is %q", report.Status)
	}
	if len(report.RawEvents) == 0 {
		return errors.New("room report has no raw observations")
	}
	sources := make(map[string][]InputObservation)
	outputs := make(map[string][]RawEvent)
	playback := make([]RawEvent, 0)
	terminals := make(map[string]RawEvent)
	for index, event := range report.RawEvents {
		if event.AfterTerminal {
			return fmt.Errorf("raw event %d arrived after terminal: %+v", index, event)
		}
		switch event.Kind {
		case "source_audio":
			sources[event.Participant] = append(sources[event.Participant], InputObservation{
				Participant: event.Participant, Epoch: event.Epoch, Sequence: event.Sequence,
				Samples: append([]int16(nil), event.Samples...), End: event.End,
			})
		case "peer_output":
			if event.Target == "" || event.Target != event.Participant {
				return fmt.Errorf("peer output has inconsistent participant/target: %+v", event)
			}
			outputs[event.Target] = append(outputs[event.Target], event)
		case "playback_input":
			playback = append(playback, event)
		case "terminal":
			if _, exists := terminals[event.Participant]; exists {
				return fmt.Errorf("participant %s published duplicate terminal", event.Participant)
			}
			terminals[event.Participant] = event
		default:
			return fmt.Errorf("unknown raw observation kind %q", event.Kind)
		}
	}
	if len(sources) != 2 || len(outputs) != 2 || len(terminals) != 2 {
		return fmt.Errorf("raw participant observations incomplete: sources=%d outputs=%d terminals=%d", len(sources), len(outputs), len(terminals))
	}
	for _, participant := range []string{"alice", "bob"} {
		if len(sources[participant]) != 2 {
			return fmt.Errorf("participant %s source frame count = %d, want 2", participant, len(sources[participant]))
		}
		if len(outputs[participant]) != 1 {
			return fmt.Errorf("participant %s output frame count = %d, want 1", participant, len(outputs[participant]))
		}
		output := outputs[participant][0]
		if len(output.Samples) == 0 || !output.End || output.Epoch == 0 {
			return fmt.Errorf("participant %s output is empty or missing epoch/end marker: %+v", participant, output)
		}
		for _, source := range sources[participant] {
			if reflect.DeepEqual(output.Samples, source.Samples) {
				return fmt.Errorf("participant %s output is a self-echo of source PCM", participant)
			}
		}
	}
	if len(playback) != 1 || len(playback[0].Samples) == 0 {
		return fmt.Errorf("playback observation count = %d, want one nonempty frame", len(playback))
	}
	derivedOutputs := map[string][]int16{}
	for participant, events := range outputs {
		for _, event := range events {
			derivedOutputs[participant] = append(derivedOutputs[participant], event.Samples...)
		}
	}
	if !reflect.DeepEqual(report.PeerOutputs, derivedOutputs) {
		return fmt.Errorf("peer output summary does not match raw observations: summary=%v raw=%v", report.PeerOutputs, derivedOutputs)
	}
	derivedInputs := make([]InputObservation, 0, len(sources["alice"])+len(sources["bob"]))
	for _, participant := range []string{"alice", "bob"} {
		derivedInputs = append(derivedInputs, sources[participant]...)
	}
	if !reflect.DeepEqual(report.SourceInputs, derivedInputs) {
		return fmt.Errorf("source input summary does not match raw observations: summary=%v raw=%v", report.SourceInputs, derivedInputs)
	}
	derivedAdmission := AdmissionReport{}
	for _, frames := range sources {
		derivedAdmission.Frames += len(frames)
		for _, frame := range frames {
			derivedAdmission.Samples += len(frame.Samples)
		}
	}
	if report.ProviderAdmission != derivedAdmission {
		return fmt.Errorf("provider admission summary does not match raw observations: summary=%+v raw=%+v", report.ProviderAdmission, derivedAdmission)
	}
	observedPlayback := make([]FrameObservation, 0, len(playback))
	for _, event := range playback {
		observedPlayback = append(observedPlayback, FrameObservation{
			Epoch: event.Epoch, Sequence: event.Sequence,
			Samples: append([]int16(nil), event.Samples...), End: event.End,
		})
	}
	if !reflect.DeepEqual(report.Playback.Observed, observedPlayback) {
		return fmt.Errorf("playback summary does not match raw observations: summary=%v raw=%v", report.Playback.Observed, observedPlayback)
	}
	if !report.Epochs.EndBeforeTerminal {
		return errors.New("room report did not prove end-of-response before terminal")
	}
	for _, participant := range []string{"alice", "bob"} {
		terminal := report.Terminal[participant]
		rawTerminal := terminals[participant]
		if terminal.Sequence != rawTerminal.Sequence || terminal.Reason != rawTerminal.Reason ||
			terminal.Classification != rawTerminal.Classification || terminal.TerminalReason != rawTerminal.TerminalReason ||
			terminal.Provenance != rawTerminal.Provenance || terminal.OutputState != rawTerminal.OutputState {
			return fmt.Errorf("terminal summary does not match raw observation for %s", participant)
		}
	}
	return nil
}

func (s *fakeLiveService) handles() map[string]*fakeHandle {
	s.coord.mu.Lock()
	defer s.coord.mu.Unlock()
	result := make(map[string]*fakeHandle, len(s.coord.handles))
	for id, handle := range s.coord.handles {
		result[id] = handle
	}
	return result
}

func admissionFromInputs(inputs []InputObservation) AdmissionReport {
	result := AdmissionReport{}
	for _, input := range inputs {
		result.Frames++
		result.Samples += len(input.Samples)
	}
	return result
}

func inputPCMBytes(inputs []InputObservation) int {
	result := 0
	for _, input := range inputs {
		result += len(input.Samples) * 2
	}
	return result
}

func epochReport(inputs []InputObservation, outputs map[string][]int16, events []RawEvent) EpochReport {
	result := EpochReport{}
	for _, input := range inputs {
		if input.Epoch == 1 {
			result.StalePendingSamples += len(input.Samples)
		}
	}
	result.HealthyTail = append([]int16(nil), outputs["bob"]...)
	endIndexes := make(map[string]int)
	outputEndIndexes := make(map[string]int)
	terminalIndexes := make(map[string]int)
	for index, event := range events {
		if event.Kind == "source_audio" && event.End {
			endIndexes[event.Participant] = index
		}
		if event.Kind == "peer_output" && event.End {
			outputEndIndexes[event.Participant] = index
		}
		if event.Kind == "terminal" {
			terminalIndexes[event.Participant] = index
		}
	}
	aliceEnd, aliceHasEnd := endIndexes["alice"]
	bobEnd, bobHasEnd := endIndexes["bob"]
	aliceOutputEnd, aliceHasOutputEnd := outputEndIndexes["alice"]
	bobOutputEnd, bobHasOutputEnd := outputEndIndexes["bob"]
	aliceTerminal, aliceHasTerminal := terminalIndexes["alice"]
	bobTerminal, bobHasTerminal := terminalIndexes["bob"]
	result.EndBeforeTerminal = aliceHasEnd && bobHasEnd && aliceHasOutputEnd && bobHasOutputEnd &&
		aliceHasTerminal && bobHasTerminal && aliceEnd < aliceTerminal && bobEnd < bobTerminal &&
		aliceOutputEnd < aliceTerminal && bobOutputEnd < bobTerminal
	return result
}

func runPlaybackBoundaryProbe() (PlaybackReport, error) {
	format := audio.PCM16DeviceFormat(1000)
	queue, err := audio.NewPlaybackQueueWithLatency(format, 20*time.Millisecond)
	if err != nil {
		return PlaybackReport{}, err
	}
	stale := []int16{7, 8, 9}
	queue.Enqueue(stale)
	discarded := queue.Discard()
	if discarded != len(stale) {
		return PlaybackReport{}, fmt.Errorf("discarded %d stale samples, want %d", discarded, len(stale))
	}
	healthy := []int16{41, 42}
	queue.Enqueue(healthy)
	before := queue.Snapshot()
	consumed := make([]int16, 4)
	queue.RenderInto(consumed)
	stats := queue.Snapshot()
	return PlaybackReport{
		Domain: "software-playback-boundary", Admitted: healthy, Consumed: consumed,
		Underflow: []int16{consumed[2], consumed[3]}, DiscardedStale: stale,
		BeforeRender: before.RenderedSamples, CallbackCount: stats.CallbackCount,
		RenderedSamples: stats.RenderedSamples, UnderflowSamples: stats.UnderflowSamples,
		DiscardedSamples: stats.DiscardedSamples, AdmittedSamples: uint64(len(healthy)),
		ConsumedSamples: stats.RenderedSamples - stats.UnderflowSamples, ZeroFilledSamples: stats.ZeroFilledSamples,
		PhysicalDevice: false,
		CapabilityGap:  "software queue proof only; no physical playback device was opened",
	}, nil
}

func writeReport(outputDir string, report Report) error {
	if outputDir == "" {
		return nil
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(outputDir, "report.json"), encoded, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func Run(request Request) (Report, error) {
	switch request.Mode {
	case "positive", "boundary", "routing-epochs", "mutations", "partial-recording", "consumption", "lifecycle":
		report, err := runLiveScenario(request.OutputDir, request.Mode)
		if err != nil {
			if writeErr := writeReport(request.OutputDir, report); writeErr != nil {
				return report, errors.Join(err, writeErr)
			}
			return report, err
		}
		boundary, boundaryErr := runPlaybackBoundaryProbe()
		if boundaryErr != nil {
			return report, boundaryErr
		}
		report.PlaybackBoundary = boundary
		report.Playback.DiscardedStale = boundary.DiscardedStale
		report.Playback.DiscardedSamples = boundary.DiscardedSamples
		report.Playback.CapabilityGap = boundary.CapabilityGap
		if request.Mode == "mutations" && request.Mutation != "" {
			candidate, mutationErr := mutatedReport(report, request.Mutation)
			if mutationErr != nil {
				return report, mutationErr
			}
			if writeErr := writeReport(request.OutputDir, candidate); writeErr != nil {
				return candidate, writeErr
			}
			if err := compareMutationOracle(candidate); err == nil {
				return Report{}, fmt.Errorf("mutation %q unexpectedly matched the frozen oracle", request.Mutation)
			} else {
				return candidate, fmt.Errorf("literal oracle mismatch: %w", err)
			}
		}
		if err := writeReport(request.OutputDir, report); err != nil {
			return report, err
		}
		return report, nil
	case "cancel-before-start", "cancel-active":
		cancellation, err := runCancellationProbe(request.Mode, request.OutputDir)
		report := Report{SchemaVersion: 1, Status: "complete", Mode: request.Mode, OutputDir: request.OutputDir, Cancellation: cancellation}
		if err != nil {
			report.Status = "failed"
			if cancellation != nil {
				report.Room = RoomObservation{Error: err.Error()}
			}
		}
		if writeErr := writeReport(request.OutputDir, report); writeErr != nil {
			return report, errors.Join(err, writeErr)
		}
		if err != nil {
			return report, err
		}
		return report, nil
	default:
		return Report{}, fmt.Errorf("unknown mode %q", request.Mode)
	}
}

func runCancellationProbe(mode, outputDir string) (*CancellationReport, error) {
	coord := newCoordinator()
	live := &fakeLiveService{coord: coord}
	scheduler := clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond)
	service := wire.NewService(wire.Dependencies{Live: live, Clock: scheduler})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode == "cancel-before-start" {
		cancel()
	}
	type roomRunOutcome struct {
		result rooms.RoomResult
		err    error
	}
	resultCh := make(chan roomRunOutcome, 1)
	go func() {
		result, err := service.Run(ctx, io.Discard, rooms.RoomRunOptions{
			Manifest: buildCancellationManifest(), OutputDir: outputDir, ConfigDir: outputDir,
			WorkDir: outputDir, AllowPaths: []string{outputDir},
			AudioFormat: mixer.Format{SampleRate: 1000, Channels: 1, FrameDuration: 4 * time.Millisecond},
		})
		resultCh <- roomRunOutcome{result: result, err: err}
	}()
	var outcome roomRunOutcome
	if mode == "cancel-active" {
		activeDeadline := time.NewTimer(2 * time.Second)
		defer activeDeadline.Stop()
		active := false
		for !active {
			select {
			case <-coord.activeMediaReady:
				active = true
			case outcome = <-resultCh:
				sourceFrames, outputFrames := coord.frameCounts()
				cancel()
				return nil, fmt.Errorf("public room service returned before active media: source_frames=%d output_frames=%d err=%v", sourceFrames, outputFrames, outcome.err)
			case <-activeDeadline.C:
				sourceFrames, outputFrames := coord.frameCounts()
				cancel()
				return nil, fmt.Errorf("public room service did not produce an active media frame: source_frames=%d output_frames=%d", sourceFrames, outputFrames)
			default:
				if !coord.inputsRoutedReady() {
					runtime.Gosched()
					continue
				}
				scheduler.Advance()
				runtime.Gosched()
			}
		}
		cancel()
	}
	select {
	case outcome = <-resultCh:
	case <-time.After(3 * time.Second):
		cancel()
		return nil, errors.New("public room service cancellation did not return within bounded deadline")
	}
	handles := live.handles()
	report := &CancellationReport{
		Mode: mode, RoomTermination: string(outcome.result.TerminationReason),
		RoomError: outcome.result.Error, Participants: map[string]string{}, RunReturned: true,
		PublicServiceRun: true,
	}
	for id, participant := range outcome.result.Participants {
		report.Participants[id] = string(participant.TerminationReason)
	}
	report.SourceFrames, report.OutputFrames = coord.frameCounts()
	report.MediaPumpActive = report.OutputFrames > 0
	for _, handle := range handles {
		handle.mu.Lock()
		report.Opened++
		report.Started += handle.startCount
		report.Waited += handle.waitReturned
		report.Closed += handle.closeCount
		handle.mu.Unlock()
	}
	for id, handle := range handles {
		select {
		case <-handle.waitReady:
		case <-time.After(time.Second):
			return report, fmt.Errorf("participant %s did not prove Wait returned", id)
		}
	}
	report.Joined = report.RunReturned && report.Waited == report.Opened
	report.ClosedByService = report.Closed == report.Opened
	if mode == "cancel-before-start" {
		if report.Started != 0 {
			return report, fmt.Errorf("pre-start cancellation unexpectedly started %d participants", report.Started)
		}
		report.StartError = "public room admission observed no start before cancellation"
	} else if report.Opened != 2 || report.Started != report.Opened || !report.MediaPumpActive || report.SourceFrames == 0 {
		return report, fmt.Errorf("active cancellation started %d of %d admitted participants", report.Started, report.Opened)
	}
	report.RepeatedCloseOK = true
	for _, handle := range handles {
		firstErr := handle.Close()
		secondErr := handle.Close()
		if firstErr != nil && report.FirstCloseError == "" {
			report.FirstCloseError = firstErr.Error()
		}
		if secondErr != nil && report.SecondCloseError == "" {
			report.SecondCloseError = secondErr.Error()
		}
		report.RepeatedCloseOK = report.RepeatedCloseOK && firstErr == nil && secondErr == nil
		handle.mu.Lock()
		report.CloseCalls += handle.closeCalls
		handle.mu.Unlock()
	}
	if outcome.err != nil {
		report.WaitError = outcome.err.Error()
	}
	if !report.PublicServiceRun || !report.RunReturned || !report.Joined || !report.ClosedByService || !report.RepeatedCloseOK {
		return report, fmt.Errorf("public cancellation cleanup was not joined/service-owned/idempotent: %+v", report)
	}
	return report, nil
}

func frozenReportOracle() Report {
	return Report{
		SourceInputs: []InputObservation{
			{Participant: "alice", Epoch: 1, Sequence: 1, Samples: []int16{101, 102}},
			{Participant: "alice", Epoch: 2, Sequence: 2, Samples: []int16{111, 112, 113, 114}, End: true},
			{Participant: "bob", Epoch: 1, Sequence: 1, Samples: []int16{201, 202}},
			{Participant: "bob", Epoch: 2, Sequence: 2, Samples: []int16{211, 212, 213, 214}, End: true},
		},
		PeerOutputs: map[string][]int16{"alice": {211, 212, 213, 214}, "bob": {111, 112, 113, 114}},
		Epochs:      EpochReport{StalePendingSamples: 4, HealthyTail: []int16{111, 112, 113, 114}, EndBeforeTerminal: true},
		Terminal: map[string]TerminalObservation{
			"alice": {Kind: "terminal", Sequence: 99, Reason: "fixture_complete", Classification: "fixture_complete", TerminalReason: "provider_close", Provenance: "provider", OutputState: "complete"},
			"bob":   {Kind: "terminal", Sequence: 99, Reason: "fixture_complete", Classification: "fixture_complete", TerminalReason: "provider_close", Provenance: "provider", OutputState: "complete"},
		},
	}
}

func cloneReport(report Report) Report {
	candidate := report
	candidate.SourceInputs = append([]InputObservation(nil), report.SourceInputs...)
	for index := range candidate.SourceInputs {
		candidate.SourceInputs[index].Samples = append([]int16(nil), candidate.SourceInputs[index].Samples...)
	}
	candidate.PeerOutputs = make(map[string][]int16, len(report.PeerOutputs))
	for participant, samples := range report.PeerOutputs {
		candidate.PeerOutputs[participant] = append([]int16(nil), samples...)
	}
	candidate.Epochs.HealthyTail = append([]int16(nil), report.Epochs.HealthyTail...)
	candidate.Terminal = make(map[string]TerminalObservation, len(report.Terminal))
	for participant, terminal := range report.Terminal {
		candidate.Terminal[participant] = terminal
	}
	candidate.RawEvents = append([]RawEvent(nil), report.RawEvents...)
	for index := range candidate.RawEvents {
		candidate.RawEvents[index].Samples = append([]int16(nil), candidate.RawEvents[index].Samples...)
	}
	candidate.Playback.Admitted = append([]int16(nil), report.Playback.Admitted...)
	candidate.Playback.Consumed = append([]int16(nil), report.Playback.Consumed...)
	candidate.Playback.Underflow = append([]int16(nil), report.Playback.Underflow...)
	candidate.Playback.DiscardedStale = append([]int16(nil), report.Playback.DiscardedStale...)
	candidate.Playback.Observed = append([]FrameObservation(nil), report.Playback.Observed...)
	for index := range candidate.Playback.Observed {
		candidate.Playback.Observed[index].Samples = append([]int16(nil), candidate.Playback.Observed[index].Samples...)
	}
	candidate.PlaybackBoundary.Admitted = append([]int16(nil), report.PlaybackBoundary.Admitted...)
	candidate.PlaybackBoundary.Consumed = append([]int16(nil), report.PlaybackBoundary.Consumed...)
	candidate.PlaybackBoundary.Underflow = append([]int16(nil), report.PlaybackBoundary.Underflow...)
	candidate.PlaybackBoundary.DiscardedStale = append([]int16(nil), report.PlaybackBoundary.DiscardedStale...)
	return candidate
}

func mutatedReport(report Report, mutation string) (Report, error) {
	candidate := cloneReport(report)
	switch mutation {
	case "peer-participant-key":
		candidate.PeerOutputs["mallory"] = []int16{111, 112, 113, 114}
		for index := range candidate.RawEvents {
			if candidate.RawEvents[index].Kind == "peer_output" {
				candidate.RawEvents[index].Participant = "mallory"
				candidate.RawEvents[index].Target = "mallory"
				break
			}
		}
	case "source-order":
		for left, right := 0, len(candidate.SourceInputs)-1; left < right; left, right = left+1, right-1 {
			candidate.SourceInputs[left], candidate.SourceInputs[right] = candidate.SourceInputs[right], candidate.SourceInputs[left]
		}
	case "epoch":
		candidate.Epochs.StalePendingSamples = 0
		for index := range candidate.RawEvents {
			if candidate.RawEvents[index].Kind == "source_audio" {
				candidate.RawEvents[index].Epoch = 0
				break
			}
		}
	case "pcm":
		candidate.PeerOutputs["alice"][0] = 999
		for index := range candidate.RawEvents {
			if candidate.RawEvents[index].Kind == "peer_output" && candidate.RawEvents[index].Participant == "alice" {
				candidate.RawEvents[index].Samples[0] = 999
				break
			}
		}
	case "terminal":
		terminal := candidate.Terminal["alice"]
		terminal.Provenance = "replay"
		candidate.Terminal["alice"] = terminal
		for index := range candidate.RawEvents {
			if candidate.RawEvents[index].Kind == "terminal" && candidate.RawEvents[index].Participant == "alice" {
				candidate.RawEvents[index].Provenance = "replay"
				break
			}
		}
	default:
		return Report{}, fmt.Errorf("unknown mutation %q", mutation)
	}
	return candidate, nil
}

func compareMutationOracle(report Report) error {
	want := frozenReportOracle()
	if !reflect.DeepEqual(report.SourceInputs, want.SourceInputs) {
		return errors.New("source input order or samples differ")
	}
	if !reflect.DeepEqual(report.PeerOutputs, want.PeerOutputs) {
		return errors.New("peer participant/PCM oracle differs")
	}
	if !reflect.DeepEqual(report.Epochs, want.Epochs) {
		return errors.New("epoch oracle differs")
	}
	if !reflect.DeepEqual(report.Terminal, want.Terminal) {
		return errors.New("terminal provenance oracle differs")
	}
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ session.LiveHandle = (*fakeHandle)(nil)
var _ session.LiveService = (*fakeLiveService)(nil)
