package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestRoomGraphRoutesEachSourceToPeersOnly(t *testing.T) {
	aliceInbound := &graphInbound{frames: make(chan audio.PCMFrame, 1)}
	bobInbound := &graphInbound{frames: make(chan audio.PCMFrame, 1)}
	aliceOutbound := &graphOutbound{frames: make(chan audio.PCMFrame, 4)}
	bobOutbound := &graphOutbound{frames: make(chan audio.PCMFrame, 4)}
	participants := []*activeParticipant{
		{participant: rooms.Participant{ID: "alice", Kind: rooms.ParticipantKindAgent}, endpoints: audio.MediaEndpoints{Inbound: aliceInbound, Outbound: aliceOutbound}, finished: make(chan struct{})},
		{participant: rooms.Participant{ID: "bob", Kind: rooms.ParticipantKindAgent}, endpoints: audio.MediaEndpoints{Inbound: bobInbound, Outbound: bobOutbound}, finished: make(chan struct{})},
	}
	graph, err := newRoomGraph(context.Background(), clock.Real{}, rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 2 * time.Millisecond}, participants, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := graph.Close(); err != nil {
			t.Errorf("close graph: %v", err)
		}
	}()
	aliceInbound.frames <- audio.PCMFrame{Samples: []int16{11, 13}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var got audio.PCMFrame
	select {
	case got = <-bobOutbound.frames:
	case <-ctx.Done():
		t.Fatalf("bob did not receive alice media (graph error: %v)", graph.Err())
	}
	if len(got.Samples) != 2 || got.Samples[0] != 11 || got.Samples[1] != 13 {
		t.Fatalf("bob received %v, want alice frame", got.Samples)
	}
	if got.Epoch != 1 || got.StreamID != "room:bob" || got.Sequence != 0 || got.StartSample != 0 {
		t.Fatalf("bob mix lineage = epoch:%d stream:%q sequence:%d start:%d", got.Epoch, got.StreamID, got.Sequence, got.StartSample)
	}
	select {
	case self := <-aliceOutbound.frames:
		if len(self.Samples) != 2 || self.Samples[0] != 0 || self.Samples[1] != 0 {
			t.Fatalf("alice received self media %v, want silence", self.Samples)
		}
		if self.Epoch != 1 || self.StreamID != "room:alice" {
			t.Fatalf("alice output lineage = epoch:%d stream:%q", self.Epoch, self.StreamID)
		}
	case <-ctx.Done():
		t.Fatal("alice did not receive a peer cadence frame")
	}
}

func TestRoomGraphRetiresFailedSourceAndKeepsSurvivorMesh(t *testing.T) {
	failedErr := errors.New("alice provider transport failed")
	aliceInbound := &failingGraphInbound{err: failedErr}
	bobInbound := &graphInbound{frames: make(chan audio.PCMFrame, 1)}
	charlieInbound := &graphInbound{frames: make(chan audio.PCMFrame, 1)}
	bobOutbound := &graphOutbound{frames: make(chan audio.PCMFrame, 8)}
	charlieOutbound := &graphOutbound{frames: make(chan audio.PCMFrame, 8)}
	participantFailed := make(chan error, 1)
	participants := []*activeParticipant{
		{participant: rooms.Participant{ID: "alice", Kind: rooms.ParticipantKindAgent}, endpoints: audio.MediaEndpoints{Inbound: aliceInbound}, finished: make(chan struct{}), onMediaError: func(err error) { participantFailed <- err }},
		{participant: rooms.Participant{ID: "bob", Kind: rooms.ParticipantKindAgent}, endpoints: audio.MediaEndpoints{Inbound: bobInbound, Outbound: bobOutbound}, finished: make(chan struct{})},
		{participant: rooms.Participant{ID: "charlie", Kind: rooms.ParticipantKindAgent}, endpoints: audio.MediaEndpoints{Inbound: charlieInbound, Outbound: charlieOutbound}, finished: make(chan struct{})},
	}
	graph, err := newRoomGraph(context.Background(), clock.Real{}, rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 2 * time.Millisecond}, participants, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := graph.Close(); err != nil {
			t.Errorf("close graph: %v", err)
		}
	}()
	select {
	case got := <-participantFailed:
		if !errors.Is(got, failedErr) {
			t.Fatalf("participant failure = %v, want %v", got, failedErr)
		}
	case <-time.After(time.Second):
		t.Fatal("failed participant was not retired")
	}
	if graph.Err() != nil {
		t.Fatalf("graph error = %v, want survivor graph to remain active", graph.Err())
	}
	bobInbound.frames <- audio.PCMFrame{Samples: []int16{17, 19}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case got := <-charlieOutbound.frames:
		if len(got.Samples) != 2 || got.Samples[0] != 17 || got.Samples[1] != 19 {
			t.Fatalf("survivor peer output = %v, want Bob PCM", got.Samples)
		}
	case <-ctx.Done():
		t.Fatalf("survivor mesh stopped after Alice failure: %v", ctx.Err())
	}
}

func TestRunnerRetiresHandleFailureBeforeSurvivorOutput(t *testing.T) {
	fixture := newHandleFailureFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, len(fixture.manifest.Participants))
	failureObserved := make(chan error, 1)
	runDone := fixture.start(ctx, ready, failureObserved)
	fixture.waitReady(t, ready)
	fixture.fail(t, failureObserved)
	fixture.waitSurvivorOutput(t)
	fixture.assertRetiredOutput(t)
	cancel()
	fixture.waitDone(t, runDone)
}

type handleFailureFixture struct {
	failedID   string
	survivorID string
	peerID     string
	failed     *fakeLiveHandle
	failedOut  *graphOutbound
	survivorIn *graphInbound
	peerOut    *graphOutbound
	runner     Runner
	manifest   rooms.Manifest
}

func newHandleFailureFixture() *handleFailureFixture {
	const (
		failedID   = "alice"
		survivorID = "bob"
		peerID     = "charlie"
	)
	failedInbound := &graphInbound{frames: make(chan audio.PCMFrame, 1)}
	failedOutbound := &graphOutbound{frames: make(chan audio.PCMFrame, 8)}
	survivorInbound := &graphInbound{frames: make(chan audio.PCMFrame, 4)}
	survivorOutbound := &graphOutbound{frames: make(chan audio.PCMFrame, 8)}
	peerInbound := &graphInbound{frames: make(chan audio.PCMFrame, 4)}
	peerOutbound := &graphOutbound{frames: make(chan audio.PCMFrame, 8)}
	failed := newFakeLiveHandle()
	failed.media = audio.MediaEndpoints{Inbound: failedInbound, Outbound: failedOutbound}
	survivor := newFakeLiveHandle()
	survivor.media = audio.MediaEndpoints{Inbound: survivorInbound, Outbound: survivorOutbound}
	peer := newFakeLiveHandle()
	peer.media = audio.MediaEndpoints{Inbound: peerInbound, Outbound: peerOutbound}
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		failedID: failed, survivorID: survivor, peerID: peer,
	}}
	return &handleFailureFixture{
		failedID: failedID, survivorID: survivorID, peerID: peerID,
		failed: failed, failedOut: failedOutbound, survivorIn: survivorInbound, peerOut: peerOutbound,
		runner: New(Dependencies{Live: service, Clock: clock.Real{}}),
		manifest: rooms.Manifest{
			SchemaVersion: rooms.SchemaVersion,
			Room:          rooms.Room{Interactive: true, MaxTurns: 100},
			Participants: []rooms.Participant{
				{ID: failedID, Kind: rooms.ParticipantKindAgent, SystemPrompt: failedID, OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "ALICE_KEY", Tools: []string{}},
				{ID: survivorID, Kind: rooms.ParticipantKindAgent, SystemPrompt: survivorID, OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "BOB_KEY", Tools: []string{}},
				{ID: peerID, Kind: rooms.ParticipantKindAgent, SystemPrompt: peerID, OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "CHARLIE_KEY", Tools: []string{}},
			},
		},
	}
}

func (f *handleFailureFixture) start(ctx context.Context, ready chan<- string, failureObserved chan<- error) <-chan roomRunOutcome {
	runDone := make(chan roomRunOutcome, 1)
	go func() {
		result, err := f.runner.Run(ctx, nil, rooms.RoomRunOptions{
			Manifest:    f.manifest,
			AudioFormat: rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 2 * time.Millisecond},
			OnParticipantReady: func(value rooms.RoomParticipantReady) {
				ready <- value.ParticipantID
			},
			OnDiagnostic: func(participantID string, record rooms.RoomDiagnosticRecord) {
				if participantID == f.failedID && record.Event == "participant_finished_with_error" {
					select {
					case failureObserved <- errors.New(record.Fields["error"]):
					default:
					}
				}
			},
		})
		runDone <- roomRunOutcome{result: result, err: err}
	}()
	return runDone
}

func (f *handleFailureFixture) waitReady(t *testing.T, ready <-chan string) {
	t.Helper()
	for range f.manifest.Participants {
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatal("room participant did not reach ready boundary")
		}
	}
}

func (f *handleFailureFixture) fail(t *testing.T, observed <-chan error) {
	t.Helper()
	failure := errors.New("alice provider transport failed")
	f.failed.Cancel(failure)
	select {
	case got := <-observed:
		if got.Error() != failure.Error() {
			t.Fatalf("participant failure = %q, want %q", got, failure)
		}
	case <-time.After(time.Second):
		t.Fatal("handle failure did not reach room lifecycle")
	}
}

func (f *handleFailureFixture) waitSurvivorOutput(t *testing.T) {
	t.Helper()
	f.survivorIn.frames <- audio.PCMFrame{Samples: []int16{17, 19}, Format: audio.PCM16DeviceFormat(1000)}
	deadline := time.After(time.Second)
	for {
		select {
		case frame := <-f.peerOut.frames:
			if len(frame.Samples) == 2 && frame.Samples[0] == 17 && frame.Samples[1] == 19 {
				return
			}
		case <-deadline:
			t.Fatal("survivor output did not reach the surviving peer")
		}
	}
}

func (f *handleFailureFixture) assertRetiredOutput(t *testing.T) {
	t.Helper()
	select {
	case frame := <-f.failedOut.frames:
		if len(frame.Samples) == 2 && frame.Samples[0] == 17 && frame.Samples[1] == 19 {
			t.Fatal("retired participant received survivor output")
		}
	default:
	}
}

func (f *handleFailureFixture) waitDone(t *testing.T, runDone <-chan roomRunOutcome) {
	t.Helper()
	select {
	case outcome := <-runDone:
		if outcome.err != nil {
			t.Fatalf("room run error = %v", outcome.err)
		}
		if outcome.result.TerminationReason != rooms.RoomTerminationStopped {
			t.Fatalf("room termination = %q, want stopped", outcome.result.TerminationReason)
		}
		if outcome.result.Participants[f.failedID].TerminationReason != rooms.ParticipantTerminationError {
			t.Fatalf("failed participant result = %+v, want error", outcome.result.Participants[f.failedID])
		}
	case <-time.After(time.Second):
		t.Fatal("room did not join survivor cleanup")
	}
}

func TestRoomGraphRecordsReceivedOnlyAfterProviderAdmission(t *testing.T) {
	failure := errors.New("provider write failed")
	source := &graphInbound{frames: make(chan audio.PCMFrame, 1)}
	failed := make(chan error, 1)
	probe := &graphRecorderProbe{}
	participants := []*activeParticipant{
		{participant: rooms.Participant{ID: "alice", Kind: rooms.ParticipantKindAgent}, endpoints: audio.MediaEndpoints{Inbound: source}, finished: make(chan struct{})},
		{participant: rooms.Participant{ID: "bob", Kind: rooms.ParticipantKindAgent}, endpoints: audio.MediaEndpoints{Outbound: &failingGraphOutbound{err: failure}}, finished: make(chan struct{}), onMediaError: func(err error) { failed <- err }},
	}
	graph, err := newRoomGraph(context.Background(), clock.Real{}, rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 2 * time.Millisecond}, participants, nil, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := graph.Close(); err != nil {
			t.Errorf("close graph: %v", err)
		}
	}()
	source.frames <- audio.PCMFrame{Samples: []int16{23, 29}}
	select {
	case got := <-failed:
		if !errors.Is(got, failure) {
			t.Fatalf("provider failure = %v, want %v", got, failure)
		}
	case <-time.After(time.Second):
		t.Fatal("provider failure was not observed")
	}
	probe.mu.Lock()
	received := probe.received
	sources := probe.sources
	probe.mu.Unlock()
	if sources == 0 {
		t.Fatal("source evidence was not recorded")
	}
	if received != 0 {
		t.Fatalf("received evidence count = %d, want zero after rejected provider write", received)
	}
}

type graphInbound struct {
	frames chan audio.PCMFrame
}

func (i *graphInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case frame := <-i.frames:
		return frame, nil
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}
func (*graphInbound) Close() error { return nil }

type graphOutbound struct {
	frames chan audio.PCMFrame
}

func (o *graphOutbound) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	select {
	case o.frames <- audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...), Format: frame.Format, StreamID: frame.StreamID, Epoch: frame.Epoch, Sequence: frame.Sequence, StartSample: frame.StartSample, EndOfResponse: frame.EndOfResponse}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*graphOutbound) Close() error { return nil }

type failingGraphInbound struct{ err error }

func (i *failingGraphInbound) ReadFrame(context.Context) (audio.PCMFrame, error) {
	return audio.PCMFrame{}, i.err
}

func (*failingGraphInbound) Close() error { return nil }

type failingGraphOutbound struct{ err error }

func (o *failingGraphOutbound) WriteFrame(context.Context, audio.PCMFrame) error { return o.err }

func (*failingGraphOutbound) Close() error { return nil }

type graphRecorderProbe struct {
	mu       sync.Mutex
	sources  int
	received int
}

func (p *graphRecorderProbe) RecordSource(string, audio.PCMFrame) {
	p.mu.Lock()
	p.sources++
	p.mu.Unlock()
}

func (p *graphRecorderProbe) RecordReceived(string, audio.PCMFrame) {
	p.mu.Lock()
	p.received++
	p.mu.Unlock()
}
