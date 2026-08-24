//go:build nomicrophone

package sessions

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
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

func TestNewSessionScenarioPreservesTypedOptionForwarding(t *testing.T) {
	options := []agentloop.Option{agentloop.WithBufferCapacity(8)}
	scenario := NewSessionScenario(t, NewMockSessionInferencer(), NewMockToolExecutor(), options...)
	if scenario == nil || scenario.Loop == nil {
		t.Fatal("typed agentloop.Option forwarding did not construct a session scenario")
	}
}

func TestSessionScenarioConfigAliasesExposeSharedCapture(t *testing.T) {
	logicalClock := clock.NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	collector := NewSessionTranscript()
	options := SessionScenarioOptions{}
	WithSessionClock(logicalClock)(&options)
	WithTranscriptCapture(collector)(&options)

	scenario := NewSessionScenarioWithOptions(t, NewMockSessionInferencer(), NewMockToolExecutor(), options)
	scenario.SendText("captured through config aliases")
	if scenario.Clock() != logicalClock {
		t.Fatalf("scenario clock = %T, want injected deterministic clock", scenario.Clock())
	}
	if scenario.Transcript != collector {
		t.Fatal("scenario did not expose the configured session transcript")
	}
	if len(scenario.CapturedRecords()) != 2 || len(scenario.ClientRecords()) != 1 || len(scenario.AgentRecords()) != 1 {
		t.Fatalf("scenario capture views = total:%d client:%d agent:%d, want 2/1/1", len(scenario.CapturedRecords()), len(scenario.ClientRecords()), len(scenario.AgentRecords()))
	}

	configured := SessionScenarioOptions{}
	WithClock(logicalClock)(&configured)
	WithCapture()(&configured)
	auto := NewSessionScenarioWithConfig(t, NewMockSessionInferencer(), NewMockToolExecutor(), configured)
	auto.SendText("captured by an auto-created collector")
	if len(auto.CapturedRecords()) != 2 {
		t.Fatalf("auto-created capture records = %d, want 2", len(auto.CapturedRecords()))
	}
}

func TestSessionCaptureSerializesConcurrentCrossings(t *testing.T) {
	sink := newBlockingSessionSink()
	capture := newSessionCapture(clock.NewDeterministic(time.Unix(42, 0).UTC(), time.Second), sink)

	clientDone := make(chan struct{})
	go func() {
		capture.clientToAgent(transcript.StreamWS, []byte("client"))
		close(clientDone)
	}()
	awaitSignal(t, sink.firstWrite, "first crossing record")

	agentDone := make(chan struct{})
	go func() {
		capture.agentToClient(messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue("agent"),
		})
		close(agentDone)
	}()

	select {
	case <-sink.secondWrite:
		close(sink.releaseFirst)
		<-clientDone
		<-agentDone
		t.Fatal("a concurrent crossing wrote before the first crossing completed")
	case <-time.After(250 * time.Millisecond):
		// The second crossing is blocked on the crossing lock. Release the
		// first crossing and let the event-driven completion checks below prove
		// that both records remain adjacent.
	}
	close(sink.releaseFirst)
	awaitSignal(t, clientDone, "client crossing completion")
	awaitSignal(t, agentDone, "agent crossing completion")

	records := sink.Records()
	if len(records) != 4 {
		t.Fatalf("concurrent capture records = %d, want 4", len(records))
	}
	assertAdjacentTranscriptPair(t, records[0], records[1], []byte("client"))
	assertAdjacentTranscriptPair(t, records[2], records[3], []byte("agent"))
}

func TestSessionHarnessPayloadAndStreamContracts(t *testing.T) {
	payloadCases := []struct {
		name    string
		message messages.Message
		want    []byte
	}{
		{name: "text", message: messages.NewTextMessage(messages.RoleUser, "text"), want: []byte("text")},
		{name: "control", message: messages.Message{ContentParts: []messages.ContentPart{messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing}}}, want: []byte(messages.ControlPlaneMessageTypePing)},
		{name: "audio", message: messages.Message{ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: []byte{1, 2}}}}, want: []byte{1, 2}},
		{name: "image", message: messages.Message{ContentParts: []messages.ContentPart{messages.ImagePart{Bytes: []byte{3, 4}}}}, want: []byte{3, 4}},
		{name: "video", message: messages.Message{ContentParts: []messages.ContentPart{messages.VideoPart{Bytes: []byte{5, 6}}}}, want: []byte{5, 6}},
		{name: "file", message: messages.Message{ContentParts: []messages.ContentPart{messages.FilePart{Bytes: []byte{7, 8}}}}, want: []byte{7, 8}},
	}
	for _, testCase := range payloadCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := messagePayload(testCase.message); !bytes.Equal(got, testCase.want) {
				t.Fatalf("message payload = %v, want %v", got, testCase.want)
			}
		})
	}
	if got := messagePayload(messages.Message{Role: messages.RoleUser}); len(got) == 0 {
		t.Fatal("fallback message payload is empty")
	}
	if got := marshalPayload(make(chan int), []byte("fallback")); !bytes.Equal(got, []byte("fallback")) {
		t.Fatalf("marshal fallback = %q, want fallback", got)
	}

	streamCases := []struct {
		name   string
		delta  messages.StreamMessage
		want   []byte
		stream transcript.Stream
	}{
		{name: "text", delta: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("text")}, want: []byte("text"), stream: transcript.StreamWS},
		{name: "reasoning", delta: messages.StreamMessage{Type: messages.StreamTypeReasoningDelta, Value: messages.NewReasoningDeltaValue("reasoning")}, want: []byte("reasoning"), stream: transcript.StreamWS},
		{name: "audio", delta: messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{9, 10})}, want: []byte{9, 10}, stream: transcript.StreamRTCAudio},
		{name: "transcript", delta: messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("transcript")}, want: []byte("transcript"), stream: transcript.StreamWS},
		{name: "transcript end", delta: messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("complete")}, want: []byte("complete"), stream: transcript.StreamWS},
	}
	for _, testCase := range streamCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := streamPayload(testCase.delta); !bytes.Equal(got, testCase.want) {
				t.Fatalf("stream payload = %v, want %v", got, testCase.want)
			}
			if got := streamForDelta(testCase.delta); got != testCase.stream {
				t.Fatalf("stream identity = %q, want %q", got, testCase.stream)
			}
		})
	}
	if got := streamPayload(messages.StreamMessage{Type: messages.StreamTypeSessionOpen}); len(got) == 0 {
		t.Fatal("fallback stream payload is empty")
	}
	if got := streamForDelta(messages.StreamMessage{Type: messages.StreamTypeVADSpeechStarted}); got != transcript.StreamRTCAudio {
		t.Fatalf("VAD stream identity = %q, want %q", got, transcript.StreamRTCAudio)
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

type blockingSessionSink struct {
	mu           sync.Mutex
	records      []transcript.Record
	firstWrite   chan struct{}
	secondWrite  chan struct{}
	releaseFirst chan struct{}
}

func newBlockingSessionSink() *blockingSessionSink {
	return &blockingSessionSink{
		firstWrite:   make(chan struct{}),
		secondWrite:  make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (s *blockingSessionSink) Write(record transcript.Record) error {
	s.mu.Lock()
	index := len(s.records)
	s.records = append(s.records, record)
	s.mu.Unlock()
	switch index {
	case 0:
		close(s.firstWrite)
		<-s.releaseFirst
	case 1:
		close(s.secondWrite)
	}
	return nil
}

func (s *blockingSessionSink) Records() []transcript.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTranscriptRecords(s.records)
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

func awaitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
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

func assertAdjacentTranscriptPair(t *testing.T, left, right transcript.Record, payload []byte) {
	t.Helper()
	if left.Peer != transcript.PeerClient && left.Peer != transcript.PeerAgent {
		t.Fatalf("left pair peer = %q, want client or agent", left.Peer)
	}
	if left.Peer == right.Peer || left.Direction == right.Direction {
		t.Fatalf("pair peers/directions = (%s,%s) and (%s,%s), want opposite peers and directions", left.Peer, left.Direction, right.Peer, right.Direction)
	}
	if left.Tick != right.Tick || left.Timestamp != right.Timestamp || left.Stream != right.Stream || !bytes.Equal(left.Payload, right.Payload) {
		t.Fatalf("pair metadata/payload mismatch: left=%+v right=%+v", left, right)
	}
	if len(left.Payload) == 0 || !bytes.Equal(left.Payload, payload) {
		t.Fatalf("pair payload = %q and %q, want %q", left.Payload, right.Payload, payload)
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
