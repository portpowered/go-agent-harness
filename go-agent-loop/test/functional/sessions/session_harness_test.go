package sessions

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestSessionScenarioCapturesTickCorrelatedCrossings(t *testing.T) {
	base := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	logicalClock := clock.NewDeterministic(base, time.Millisecond)
	collector := NewSessionTranscript()
	recordsReady := make(chan struct{}, 16)
	sink := &notifyingSessionSink{collector: collector, records: recordsReady}
	inf := NewMockSessionInferencer()
	scenario := NewSessionScenarioWithConfig(t, inf, NewMockToolExecutor(), SessionScenarioOptions{
		Clock:   logicalClock,
		Capture: sink,
	})

	scenario.Start()
	stopped := false
	defer func() {
		if !stopped {
			stopSessionScenario(t, scenario)
		}
	}()

	// The mock provider seeds SESSION.OPEN as the first agent-to-client
	// crossing. Waiting on the sink makes this test event-driven.
	awaitCapturedRecords(t, recordsReady, 2)

	logicalClock.AdvanceTo(1)
	scenario.SendText("client request")
	awaitCapturedRecords(t, recordsReady, 2)

	logicalClock.AdvanceTo(2)
	inf.AddServerEvent(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("agent response"),
	})
	awaitCapturedRecords(t, recordsReady, 2)

	stopSessionScenario(t, scenario)
	stopped = true

	records := collector.Records()
	if len(records) != 6 {
		t.Fatalf("captured records = %d, want exactly 6 records for three crossings", len(records))
	}
	assertTranscriptPair(t, records[0], records[1], transcript.PeerAgent, transcript.DirectionOut, transcript.PeerClient, transcript.DirectionIn, 0, base, []byte(`{"Type":"SESSION.OPEN","Role":"","ToolCallId":"","Value":{"type":"session_open","session_id":"mock-session","mode":"session"},"GlobalIndex":0,"ActorProvidedID":"","ActorProvidedIndex":0,"ActorStreamID":"","ActorID":"model","LoopPassID":0}`))
	assertTranscriptPair(t, records[2], records[3], transcript.PeerClient, transcript.DirectionOut, transcript.PeerAgent, transcript.DirectionIn, 1, base, []byte("client request"))
	assertTranscriptPair(t, records[4], records[5], transcript.PeerAgent, transcript.DirectionOut, transcript.PeerClient, transcript.DirectionIn, 2, base, []byte("agent response"))

	if !correspondingTranscriptRecords(collector.ClientRecords(), collector.AgentRecords()) {
		t.Fatal("positive client/agent correspondence failed")
	}
	mutated := collector.AgentRecords()
	mutated[1].Payload[0] ^= 0xff
	if correspondingTranscriptRecords(collector.ClientRecords(), mutated) {
		t.Fatal("changed payload still passed the correspondence check")
	}

	if got := logicalClock.Tick(); got != 2 {
		t.Fatalf("logical clock tick = %d, want 2; capture must not advance it implicitly", got)
	}
}

func TestSessionScenarioCaptureIsOptInAndNilClockUsesRealTime(t *testing.T) {
	inf := NewMockSessionInferencer()
	scenario := NewSessionScenario(t, inf, NewMockToolExecutor())
	if _, ok := scenario.Clock().(clock.Real); !ok {
		t.Fatalf("resolved nil clock = %T, want clock.Real", scenario.Clock())
	}
	scenario.SendText("not captured")
	if records := scenario.CapturedRecords(); records != nil {
		t.Fatalf("capture-disabled records = %+v, want nil", records)
	}

	logicalClock := clock.NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	collector := NewSessionTranscript()
	enabled := NewSessionScenarioWithConfig(t, NewMockSessionInferencer(), NewMockToolExecutor(), SessionScenarioOptions{
		Clock:   logicalClock,
		Capture: collector,
	})
	enabled.SendText("captured")
	if records := enabled.CapturedRecords(); len(records) != 2 {
		t.Fatalf("capture-enabled records = %d, want exactly 2", len(records))
	}

	audioCollector := NewSessionTranscript()
	audio := NewSessionScenarioWithConfig(t, NewMockSessionInferencer(), NewMockToolExecutor(), SessionScenarioOptions{
		Clock:   clock.NewDeterministic(time.Unix(42, 0).UTC(), time.Second),
		Capture: audioCollector,
	})
	audioPayload := []byte{0x00, 0xff, 0x01}
	audio.SendAudioInput(audioPayload)
	audioRecords := audio.CapturedRecords()
	if len(audioRecords) != 2 || audioRecords[0].Stream != transcript.StreamRTCAudio || audioRecords[1].Stream != transcript.StreamRTCAudio || !bytes.Equal(audioRecords[0].Payload, audioPayload) || !bytes.Equal(audioRecords[1].Payload, audioPayload) {
		t.Fatalf("audio capture = %+v, want two RTC-audio records with opaque payload", audioRecords)
	}
}

type notifyingSessionSink struct {
	mu        sync.Mutex
	collector *SessionTranscript
	records   chan<- struct{}
}

func (s *notifyingSessionSink) Write(record transcript.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.collector.Write(record); err != nil {
		return err
	}
	select {
	case s.records <- struct{}{}:
	default:
	}
	return nil
}

func awaitCapturedRecords(t *testing.T, records <-chan struct{}, count int) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for range count {
		select {
		case <-records:
		case <-deadline.C:
			t.Fatal("timed out waiting for captured session record")
		}
	}
}

func stopSessionScenario(t *testing.T, scenario *SessionScenario) {
	t.Helper()
	scenario.Inf.Close()
	scenario.cancel()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	select {
	case err := <-scenario.errCh:
		if err != nil && err != context.Canceled {
			t.Fatalf("session loop stopped with error: %v", err)
		}
	case <-deadline.C:
		t.Fatal("timed out stopping session loop")
	}
}

func assertTranscriptPair(t *testing.T, left, right transcript.Record, leftPeer transcript.Peer, leftDirection transcript.Direction, rightPeer transcript.Peer, rightDirection transcript.Direction, tick uint64, base time.Time, payload []byte) {
	t.Helper()
	wantTimestamp := base.Add(time.Duration(tick) * time.Millisecond).Format(time.RFC3339Nano)
	if left.Peer != leftPeer || left.Direction != leftDirection || right.Peer != rightPeer || right.Direction != rightDirection {
		t.Fatalf("pair peers/directions = (%s,%s) and (%s,%s), want (%s,%s) and (%s,%s)", left.Peer, left.Direction, right.Peer, right.Direction, leftPeer, leftDirection, rightPeer, rightDirection)
	}
	if left.Version != transcript.FormatVersion || right.Version != transcript.FormatVersion || left.Tick != tick || right.Tick != tick || left.Timestamp != wantTimestamp || right.Timestamp != wantTimestamp {
		t.Fatalf("pair metadata = (%+v, %+v), want version=%d tick=%d timestamp=%q", left, right, transcript.FormatVersion, tick, wantTimestamp)
	}
	if left.Stream != transcript.StreamWS || right.Stream != transcript.StreamWS || !bytes.Equal(left.Payload, payload) || !bytes.Equal(right.Payload, payload) || len(left.Payload) == 0 || len(right.Payload) == 0 {
		t.Fatalf("pair stream/payload = (%q,%q,%q) and (%q,%q,%q), want non-empty matching WS payload %q", left.Stream, left.Payload, left.Timestamp, right.Stream, right.Payload, right.Timestamp, payload)
	}
}

func correspondingTranscriptRecords(client, agent []transcript.Record) bool {
	if len(client) != len(agent) {
		return false
	}
	for index := range client {
		if client[index].Tick != agent[index].Tick || client[index].Timestamp != agent[index].Timestamp || client[index].Stream != agent[index].Stream || !bytes.Equal(client[index].Payload, agent[index].Payload) {
			return false
		}
	}
	return true
}
