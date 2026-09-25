package wire

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

func evidenceService(live session.LiveService, media rooms.MediaFactory, evidence roomevidence.Service) rooms.Service {
	return NewRoomService(live, media, clock.Real{}, nil, evidence, NewRoomLatencyService())
}

func readRunManifest(t *testing.T, output string) roomevidence.RunManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(output, roomevidence.ManifestPath))
	if err != nil {
		t.Fatalf("read room manifest: %v", err)
	}
	var manifest roomevidence.RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode room manifest: %v", err)
	}
	return manifest
}

// humanMedia is one customer's device pair. Capture speaks a fixed frame
// until the room stops; playback records the audible frames the room delivered.
type humanMedia struct {
	heard     chan audio.PCMFrame
	closeOnce sync.Once
	closes    chan struct{}
}

func (m *humanMedia) Close() error {
	m.closeOnce.Do(func() { close(m.closes) })
	return nil
}

type humanCapture struct{ *humanMedia }

func (c humanCapture) Pump(ctx context.Context, room audio.OutboundMedia) error {
	for ctx.Err() == nil {
		if err := room.WriteFrame(ctx, audio.PCMFrame{Samples: constantFrame(9)}); err != nil {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

type humanPlayback struct{ *humanMedia }

func (p humanPlayback) Pump(ctx context.Context, room audio.InboundMedia) error {
	for {
		frame, err := room.ReadFrame(ctx)
		if err != nil {
			return nil
		}
		if silent(frame) {
			continue
		}
		select {
		case p.heard <- frame:
		default:
		}
	}
}

// constantFrame fills one complete frame at the room's default cadence.
func constantFrame(sample int16) []int16 {
	samples, _ := mixer.DefaultFormat().FrameSamples()
	frame := make([]int16, samples)
	for index := range frame {
		frame[index] = sample
	}
	return frame
}

func silent(frame audio.PCMFrame) bool {
	for _, sample := range frame.Samples {
		if sample != 0 {
			return false
		}
	}
	return true
}

func humanRoom() rooms.Manifest {
	return rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{Interactive: true}, Participants: []rooms.Participant{
		{ID: humanCustomerID, Kind: rooms.ParticipantKindHuman, SystemPrompt: "customer", Tools: []string{}, InputDevice: "input:mic", OutputDevice: "output:speaker"},
		agentParticipant(agentID),
	}}
}

func TestServiceFinalizesHumanRoomEvidenceWhenCallerCancels(t *testing.T) {
	live := newContractLive()
	live.media = true
	device := &humanMedia{heard: make(chan audio.PCMFrame, 64), closes: make(chan struct{})}
	var opens int
	media := rooms.MediaFactoryFunc(func(_ context.Context, participant rooms.Participant, _ rooms.AudioFormat) (rooms.MediaPorts, error) {
		if participant.Kind != rooms.ParticipantKindHuman {
			return rooms.MediaPorts{}, nil
		}
		opens++
		return rooms.MediaPorts{Capture: humanCapture{device}, Playback: humanPlayback{device}, CloseFunc: device.Close}, nil
	})
	service := evidenceService(live, media, NewRoomEvidenceService())
	output := filepath.Join(t.TempDir(), "evidence")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var readiness sync.Map
	done, ready := startRun(ctx, service, rooms.RoomRunOptions{Manifest: humanRoom(), OutputDir: output, OnParticipantReady: func(value rooms.RoomParticipantReady) { readiness.Store(value.ParticipantID, value) }})
	awaitReady(t, ready, 2)
	agent := live.handle(t, agentID)
	agent.inbound.frames <- audio.PCMFrame{Samples: constantFrame(5)}
	awaitFrameContaining(t, agent.outbound.frames, 9)
	awaitFrameContaining(t, device.heard, 5)
	cancel()
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.result.TerminationReason != rooms.RoomTerminationStopped {
		t.Fatalf("room = %+v / %v, want caller cancellation to stop cleanly", outcome.result, outcome.err)
	}
	assertHumanReadiness(t, &readiness)
	manifest := readRunManifest(t, output)
	if !manifest.Finalized || manifest.Error != "" {
		t.Fatalf("manifest finalized=%t error=%q, want a finalized clean bundle", manifest.Finalized, manifest.Error)
	}
	customer, agentFacts := manifest.Participants[humanCustomerID], manifest.Participants[agentID]
	if customer.Kind != rooms.ParticipantKindHuman || customer.InputDevice != "input:mic" || customer.OutputDevice != "output:speaker" || agentFacts.Provider != fakeProvider || agentFacts.Model != fakeModel {
		t.Fatalf("manifest participants = %+v / %+v, want human devices and agent provider facts", customer, agentFacts)
	}
	assertNoTemporaryEvidence(t, output)
	if _, closes := agent.counts(); closes != 1 || opens != 1 {
		t.Fatalf("agent closes=%d media opens=%d, want one of each", closes, opens)
	}
	select {
	case <-device.closes:
	default:
		t.Fatal("human media was not closed")
	}
}

func assertHumanReadiness(t *testing.T, readiness *sync.Map) {
	t.Helper()
	value, _ := readiness.Load(humanCustomerID)
	human, _ := value.(rooms.RoomParticipantReady)
	if human.Kind != rooms.ParticipantKindHuman || human.InputDevice != "input:mic" || human.OutputDevice != "output:speaker" || human.Provider != "" {
		t.Fatalf("human readiness = %+v, want devices without a provider", human)
	}
}

func awaitFrameContaining(t *testing.T, frames <-chan audio.PCMFrame, sample int16) {
	t.Helper()
	deadline := time.After(contractWait)
	for {
		select {
		case frame := <-frames:
			for _, value := range frame.Samples {
				if value == sample {
					return
				}
			}
		case <-deadline:
			t.Fatalf("no delivered frame carried sample %d", sample)
		}
	}
}

func assertNoTemporaryEvidence(t *testing.T, output string) {
	t.Helper()
	for _, name := range []string{roomevidence.ManifestPath, roomevidence.TimelinePath, roomevidence.MixPath} {
		if info, err := os.Stat(filepath.Join(output, name)); err != nil || info.Size() == 0 {
			t.Fatalf("evidence artifact %s missing or empty: %v", name, err)
		}
	}
	err := filepath.WalkDir(output, func(path string, _ os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".tmp") {
			t.Errorf("temporary evidence artifact left behind: %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk evidence: %v", err)
	}
}

func TestServicePreservesFailedEvidenceWhenParticipantAdmissionFails(t *testing.T) {
	live := newContractLive()
	live.openErr["beta"] = errors.New("provider dial refused")
	service := evidenceService(live, nil, NewRoomEvidenceService())
	output := filepath.Join(t.TempDir(), "evidence")
	result, err := service.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: agentRoom(rooms.Room{Interactive: true}, "alpha", "beta"), OutputDir: output})
	if err == nil || !strings.Contains(err.Error(), "provider dial refused") || result.TerminationReason != rooms.RoomTerminationFailed {
		t.Fatalf("room = %+v / %v, want an admission failure", result, err)
	}
	assertParticipant(t, result, "beta", rooms.ParticipantTerminationError)
	manifest := readRunManifest(t, output)
	if manifest.Finalized || !strings.Contains(manifest.Error, "provider dial refused") || manifest.TerminationReason != rooms.RoomTerminationFailed {
		t.Fatalf("manifest finalized=%t reason=%q error=%q, want preserved failed evidence", manifest.Finalized, manifest.TerminationReason, manifest.Error)
	}
}

// degradingEvidence fails the first live-event observation for one
// participant. Evidence health must degrade without failing the room.
type degradingEvidence struct {
	roomevidence.Service
	participant string
}

func (e degradingEvidence) Open(request roomevidence.RecordingRequest) (roomevidence.Recorder, error) {
	recorder, err := e.Service.Open(request)
	if err != nil {
		return nil, err
	}
	return &degradingRecorder{Recorder: recorder, participant: e.participant}, nil
}

type degradingRecorder struct {
	roomevidence.Recorder
	participant string
	once        sync.Once
}

func (r *degradingRecorder) Observe(observation roomevidence.Observation) error {
	if observation.Kind == rooms.EvidenceObservationLiveEvent && observation.ParticipantID == r.participant {
		var failed bool
		r.once.Do(func() { failed = true })
		if failed {
			return errors.New("evidence disk full")
		}
	}
	return r.Recorder.Observe(observation)
}

type countingSink struct {
	mu     sync.Mutex
	events map[string]int
}

func (s *countingSink) Publish(_ context.Context, participantID string, _ session.LiveEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[participantID]++
	return nil
}

func (s *countingSink) count(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[id]
}

func TestServiceDegradesEvidenceFailureWithoutStoppingParticipants(t *testing.T) {
	live := newContractLive()
	service := evidenceService(live, nil, degradingEvidence{Service: NewRoomEvidenceService(), participant: "alpha"})
	output := filepath.Join(t.TempDir(), "evidence")
	sink := &countingSink{events: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, ready := startRun(ctx, service, rooms.RoomRunOptions{Manifest: agentRoom(rooms.Room{Interactive: true}, "alpha", "beta"), OutputDir: output, EventSink: sink})
	awaitReady(t, ready, 2)
	for _, id := range []string{"alpha", "beta"} {
		live.handle(t, id).events <- session.LiveEvent{Kind: string(session.LiveEventText), SessionID: id, Message: &messages.StreamMessage{Type: messages.StreamTypeTextDelta}}
	}
	waitFor(t, "both live events", func() bool { return sink.count("alpha") == 1 && sink.count("beta") == 1 })
	for _, id := range []string{"alpha", "beta"} {
		if cancels, closes := live.handle(t, id).counts(); cancels != 0 || closes != 0 {
			t.Fatalf("participant %q cancels=%d closes=%d after evidence degraded, want still running", id, cancels, closes)
		}
	}
	cancel()
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.result.TerminationReason != rooms.RoomTerminationStopped {
		t.Fatalf("room = %+v / %v, want evidence failure to leave the room healthy", outcome.result, outcome.err)
	}
	if status := outcome.result.RecordingStatus; status == nil || status.State != "partial" {
		t.Fatalf("room recording status = %+v, want partial evidence", status)
	}
	if manifest := readRunManifest(t, output); !manifest.Finalized || manifest.RecordingStatus == nil || manifest.RecordingStatus.State != "partial" {
		t.Fatalf("manifest finalized=%t status=%+v, want finalized partial evidence", manifest.Finalized, manifest.RecordingStatus)
	}
}
