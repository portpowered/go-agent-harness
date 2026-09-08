package lifecycle

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestRunnerStopsAllParticipantsAtSharedTurnBound(t *testing.T) {
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		"alice": newFakeLiveHandle(),
		"bob":   newFakeLiveHandle(),
	}}
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	manifest := testManifest()
	var ready, terminated int
	result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{
		Manifest:           manifest,
		OnParticipantReady: func(rooms.RoomParticipantReady) { ready++ },
		OnParticipantTerminated: func(value rooms.RoomParticipantResult) {
			terminated++
			if value.TerminationReason != rooms.ParticipantTerminationEnded {
				t.Errorf("participant termination = %q, want ended", value.TerminationReason)
			}
		},
	})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination = %q, want max turns", result.TerminationReason)
	}
	if ready != 2 || terminated != 2 {
		t.Fatalf("callbacks ready=%d terminated=%d, want 2/2", ready, terminated)
	}
	for id, handle := range service.handles {
		if handle.startCount != 1 || handle.closeCount != 1 || handle.cancelCount != 1 {
			t.Errorf("%s lifecycle start=%d cancel=%d close=%d, want 1/1/1", id, handle.startCount, handle.cancelCount, handle.closeCount)
		}
	}
}

func TestRunnerClosesMediaWhenLiveOpenFails(t *testing.T) {
	media := &fakeMediaFactory{}
	service := &fakeLiveService{openErr: errors.New("provider unavailable")}
	runner := New(Dependencies{Live: service, Media: media, Clock: platformclock.Real{}})
	result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: testManifest()})
	if err == nil || result.TerminationReason != rooms.RoomTerminationFailed {
		t.Fatalf("Run result=%+v err=%v, want failed participant", result, err)
	}
	if media.closeCount != 2 {
		t.Fatalf("media close count = %d, want one per participant", media.closeCount)
	}
}

func TestRunnerPassesOpeningPromptAndReplayCaptureToLive(t *testing.T) {
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		"alice": newFakeLiveHandle(),
		"bob":   newFakeLiveHandle(),
	}}
	plan := rooms.RoomReplayPlan{Participants: []rooms.RoomReplayParticipant{
		{ID: "alice", Kind: rooms.ParticipantKindAgent, CapturePath: "/capture/alice.json"},
		{ID: "bob", Kind: rooms.ParticipantKindAgent, CapturePath: "/capture/bob.json"},
	}}
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: testManifest(), ReplayPlan: &plan})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
		t.Fatalf("termination = %q, want max turns", result.TerminationReason)
	}
	service.mu.Lock()
	requests := append([]session.LiveRequest(nil), service.requests...)
	service.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("live request count = %d, want 2", len(requests))
	}
	for _, request := range requests {
		if request.OpeningPrompt != "start" {
			t.Errorf("%s opening prompt = %q, want manifest prompt", request.SessionID, request.OpeningPrompt)
		}
		if request.Replay.InputCapturePath != "/capture/"+request.SessionID+".json" {
			t.Errorf("%s replay capture = %q", request.SessionID, request.Replay.InputCapturePath)
		}
		if request.Replay.Timing != session.LiveReplayTimingFast {
			t.Errorf("%s replay timing = %q, want fast", request.SessionID, request.Replay.Timing)
		}
	}
}

func TestRunnerRecordsBoundedRoomEvidenceThroughGraphLifecycle(t *testing.T) {
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		"alice": newFakeLiveHandle(),
		"bob":   newFakeLiveHandle(),
	}}
	output := t.TempDir()
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: testManifest(), OutputDir: output})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.RecordingStatus != nil {
		t.Fatalf("recording status = %+v, want healthy evidence", result.RecordingStatus)
	}
	if _, err := evidence.New().Load(output); err != nil {
		t.Fatalf("load runner evidence: %v", err)
	}
}

func TestRunnerForwardsOrderedLiveEventsToBoundedSink(t *testing.T) {
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		"alice": newFakeLiveHandle(),
		"bob":   newFakeLiveHandle(),
	}}
	service.handles["alice"].startEvents = []session.LiveEvent{
		{Kind: "response.audio_transcript.delta", Text: "hello "},
		{Kind: "response.audio_transcript.done", Text: "hello world"},
	}
	service.handles["bob"].startEvents = []session.LiveEvent{}
	sink := &recordingRoomEventSink{}
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: testManifest(), EventSink: sink})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
		t.Fatalf("termination = %q, want max turns", result.TerminationReason)
	}
	var aliceEvents []session.LiveEvent
	for index, participantID := range sink.participants {
		if participantID == "alice" {
			aliceEvents = append(aliceEvents, sink.events[index])
		}
	}
	if len(aliceEvents) < 2 || aliceEvents[0].Kind != "response.audio_transcript.delta" || aliceEvents[1].Kind != "response.audio_transcript.done" {
		t.Fatalf("forwarded Alice events = %#v, want ordered transcript events", aliceEvents)
	}
}

func TestRunnerEventSinkErrorFailsRoomAndJoinsParticipants(t *testing.T) {
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		"alice": newFakeLiveHandle(),
		"bob":   newFakeLiveHandle(),
	}}
	sink := &failingRoomEventSink{err: errors.New("stream closed")}
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: testManifest(), EventSink: sink})
	if err == nil || result.TerminationReason != rooms.RoomTerminationFailed {
		t.Fatalf("Run result=%+v err=%v, want failed event sink", result, err)
	}
	for id, handle := range service.handles {
		if handle.closeCount != 1 {
			t.Errorf("%s close count = %d, want joined participant close", id, handle.closeCount)
		}
	}
}

func TestRunnerRetainsTypedSilentTerminalAndIsolatesPeer(t *testing.T) {
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		"silent": func() *fakeLiveHandle {
			handle := newFakeLiveHandle()
			handle.startEvents = []session.LiveEvent{{
				Kind: string(session.LiveEventTerminal),
				Terminal: &messages.SessionCloseValue{
					Classification:     "silent_provider_empty_response",
					TerminalReason:     messages.TerminalReasonPartialOutput,
					TerminalProvenance: messages.TerminalProvenanceProvider,
					OutputState:        messages.TerminalOutputNone,
				},
			}}
			return handle
		}(),
		"peer": newFakeLiveHandle(),
	}}
	manifest := rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{Interactive: true},
		Participants: []rooms.Participant{
			{ID: "silent", SystemPrompt: "silent", OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "SILENT", Tools: []string{}},
			{ID: "peer", SystemPrompt: "peer", OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "PEER", Tools: []string{}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	diagnosticSeen := make(chan struct{})
	var diagnosticOnce sync.Once
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	resultCh := make(chan struct {
		result rooms.RoomResult
		err    error
	}, 1)
	go func() {
		result, err := runner.Run(ctx, nil, rooms.RoomRunOptions{
			Manifest: manifest,
			OnDiagnostic: func(participantID string, record rooms.RoomDiagnosticRecord) {
				if participantID == "silent" && record.Event == "live_terminal" {
					diagnosticOnce.Do(func() { close(diagnosticSeen) })
				}
			},
		})
		resultCh <- struct {
			result rooms.RoomResult
			err    error
		}{result, err}
	}()
	select {
	case <-diagnosticSeen:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("silent terminal diagnostic was not published")
	}
	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			t.Fatalf("room run error = %v", outcome.err)
		}
		silent := outcome.result.Participants["silent"]
		if silent.Classification != "silent_provider_empty_response" || silent.TerminalReason != string(messages.TerminalReasonPartialOutput) || silent.TerminalProvenance != string(messages.TerminalProvenanceProvider) || silent.OutputState != string(messages.TerminalOutputNone) {
			t.Fatalf("silent result = %+v, want typed terminal metadata", silent)
		}
		peer := outcome.result.Participants["peer"]
		if peer.Classification == "silent_provider_empty_response" {
			t.Fatalf("peer inherited silent classification: %+v", peer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not join participant cleanup")
	}
}

func TestMediaBridgePreservesPCMAndUsesBoundedFrames(t *testing.T) {
	input := &capturePump{frame: []int16{1, 2, 3}}
	output := &playbackPump{}
	inbound := &recordingInbound{frame: audio.PCMFrame{Samples: []int16{4, 5, 6}}}
	outbound := &recordingOutbound{}
	bridge := newMediaBridge(context.Background(), audio.MediaEndpoints{Inbound: inbound, Outbound: outbound}, rooms.MediaPorts{Capture: input, Playback: output}, nil)
	if err := bridge.Wait(); err != nil {
		t.Fatalf("media bridge Wait error = %v", err)
	}
	if got := outbound.frames; len(got) != 1 || got[0][0] != 1 || got[0][1] != 2 || got[0][2] != 3 {
		t.Fatalf("outbound frames = %#v, want one copied PCM frame", got)
	}
	if got := output.frames; len(got) != 1 || got[0][0] != 4 || got[0][1] != 5 || got[0][2] != 6 {
		t.Fatalf("playback frames = %#v, want provider frame preserved", got)
	}
	input.frame[0] = 99
	if outbound.frames[0][0] != 1 {
		t.Fatal("outbound retained caller-owned capture storage")
	}
}

func TestEventDrainReportsOverflowWithoutBlockingProducer(t *testing.T) {
	events := make(chan session.LiveEvent, eventQueueCapacity+32)
	for index := 0; index < cap(events); index++ {
		events <- session.LiveEvent{Kind: "delta"}
	}
	close(events)
	entered := make(chan struct{})
	release := make(chan struct{})
	drain := newEventDrain(context.Background(), events, "alice", func(string, rooms.RoomDiagnosticRecord) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}, nil, nil, time.Now, nil, nil)
	<-entered
	time.Sleep(10 * time.Millisecond)
	close(release)
	if err := drain.Wait(); err == nil {
		t.Fatal("event drain accepted an over-capacity diagnostic queue")
	}
}

func testManifest() rooms.Manifest {
	return rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 1},
		Participants: []rooms.Participant{
			{ID: "alice", Kind: rooms.ParticipantKindAgent, SystemPrompt: "a", OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "ALICE_KEY", Tools: []string{}},
			{ID: "bob", Kind: rooms.ParticipantKindAgent, SystemPrompt: "b", OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "BOB_KEY", Tools: []string{}},
		},
	}
}

type fakeLiveService struct {
	mu       sync.Mutex
	handles  map[string]*fakeLiveHandle
	openErr  error
	requests []session.LiveRequest
}

func (s *fakeLiveService) OpenLive(_ context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	handle := s.handles[request.SessionID]
	if handle != nil {
		handle.mu.Lock()
		handle.capturePath = request.Replay.OutputCapturePath
		handle.mu.Unlock()
	}
	return handle, nil
}

type fakeLiveHandle struct {
	mu          sync.Mutex
	media       audio.MediaEndpoints
	events      chan session.LiveEvent
	done        chan struct{}
	closeEvents sync.Once
	startCount  int
	cancelCount int
	closeCount  int
	cancelErr   error
	controls    []session.LiveControl
	startEvents []session.LiveEvent
	capturePath string
}

func newFakeLiveHandle() *fakeLiveHandle {
	return &fakeLiveHandle{events: make(chan session.LiveEvent, 4), done: make(chan struct{})}
}

func (h *fakeLiveHandle) Media() audio.MediaEndpoints { return h.media }

func (h *fakeLiveHandle) Events() <-chan session.LiveEvent { return h.events }

func (h *fakeLiveHandle) Send(ctx context.Context, control session.LiveControl) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	switch control.Kind {
	case session.LiveControlText, session.LiveControlAudioCommit, session.LiveControlResponseCancel, session.LiveControlResponseCreate, session.LiveControlClose:
	default:
		return errors.New("unsupported fake live control")
	}
	h.mu.Lock()
	h.controls = append(h.controls, control)
	h.mu.Unlock()
	return nil
}

func (h *fakeLiveHandle) Start(context.Context) error {
	h.mu.Lock()
	h.startCount++
	startEvents := append([]session.LiveEvent(nil), h.startEvents...)
	h.mu.Unlock()
	for _, event := range startEvents {
		h.events <- event
	}
	h.events <- session.LiveEvent{Kind: "turn_completed"}
	return nil
}

func (h *fakeLiveHandle) Cancel(err error) {
	h.mu.Lock()
	h.cancelCount++
	h.cancelErr = err
	h.mu.Unlock()
	select {
	case <-h.done:
	default:
		close(h.done)
	}
}

func (h *fakeLiveHandle) cancelCallsSnapshot() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancelCount
}

func (h *fakeLiveHandle) Wait() error {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancelErr
}

func (h *fakeLiveHandle) Close() error {
	h.mu.Lock()
	h.closeCount++
	capturePath := h.capturePath
	h.mu.Unlock()
	if capturePath != "" {
		if err := os.WriteFile(capturePath, []byte("[]\n"), 0o600); err != nil {
			return err
		}
	}
	h.closeEvents.Do(func() { close(h.events) })
	return nil
}

type fakeMediaFactory struct {
	mu         sync.Mutex
	closeCount int
}

type recordingRoomEventSink struct {
	mu           sync.Mutex
	participants []string
	events       []session.LiveEvent
}

func (s *recordingRoomEventSink) Publish(_ context.Context, participantID string, event session.LiveEvent) error {
	s.mu.Lock()
	s.participants = append(s.participants, participantID)
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

type failingRoomEventSink struct{ err error }

func (s *failingRoomEventSink) Publish(context.Context, string, session.LiveEvent) error {
	return s.err
}

func (f *fakeMediaFactory) OpenMedia(context.Context, rooms.Participant, rooms.AudioFormat) (rooms.MediaPorts, error) {
	return rooms.MediaPorts{CloseFunc: func() error {
		f.mu.Lock()
		f.closeCount++
		f.mu.Unlock()
		return nil
	}}, nil
}

type capturePump struct {
	frame []int16
}

func (s *capturePump) Pump(ctx context.Context, outbound audio.OutboundMedia) error {
	return outbound.WriteFrame(ctx, audio.PCMFrame{Samples: append([]int16(nil), s.frame...)})
}
func (*capturePump) Close() error { return nil }

type playbackPump struct {
	mu     sync.Mutex
	frames [][]int16
}

func (s *playbackPump) Pump(ctx context.Context, inbound audio.InboundMedia) error {
	frame, err := inbound.ReadFrame(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.frames = append(s.frames, append([]int16(nil), frame.Samples...))
	s.mu.Unlock()
	return nil
}
func (*playbackPump) Close() error { return nil }

type recordingInbound struct {
	frame audio.PCMFrame
	done  bool
}

func (s *recordingInbound) ReadFrame(context.Context) (audio.PCMFrame, error) {
	if s.done {
		return audio.PCMFrame{}, errors.New("inbound frame exhausted")
	}
	s.done = true
	return s.frame, nil
}
func (*recordingInbound) Close() error { return nil }

type recordingOutbound struct {
	mu     sync.Mutex
	frames [][]int16
}

func (s *recordingOutbound) WriteFrame(_ context.Context, frame audio.PCMFrame) error {
	s.mu.Lock()
	s.frames = append(s.frames, append([]int16(nil), frame.Samples...))
	s.mu.Unlock()
	return nil
}
func (*recordingOutbound) Close() error { return nil }

var _ session.LiveService = (*fakeLiveService)(nil)
var _ session.LiveHandle = (*fakeLiveHandle)(nil)
var _ rooms.MediaFactory = (*fakeMediaFactory)(nil)
var _ rooms.EventSink = (*recordingRoomEventSink)(nil)
