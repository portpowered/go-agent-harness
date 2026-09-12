package browserrunner_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
	browserrunnerwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner/wire"
)

type recordingRun struct {
	mu            sync.Mutex
	turns         []string
	navigations   []browserrunner.NavigationObservation
	cancellations []browserrunner.CancellationObservation
	invocations   map[string]bool
}

func (r *recordingRun) ObserveCustomerTurn(stepID, observed string) error {
	r.mu.Lock()
	r.turns = append(r.turns, "customer:"+stepID+":"+observed)
	r.mu.Unlock()
	return nil
}

func (r *recordingRun) ObserveAssistantTurn(stepID, observed string) error {
	r.mu.Lock()
	r.turns = append(r.turns, "assistant:"+stepID+":"+observed)
	r.mu.Unlock()
	return nil
}

func (r *recordingRun) RecordNavigation(observation browserrunner.NavigationObservation) error {
	r.mu.Lock()
	r.navigations = append(r.navigations, observation)
	r.mu.Unlock()
	return nil
}

func (r *recordingRun) RecordCancellation(observation browserrunner.CancellationObservation) error {
	r.mu.Lock()
	r.cancellations = append(r.cancellations, observation)
	r.mu.Unlock()
	return nil
}

func (r *recordingRun) HasInvocation(stepID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.invocations != nil && r.invocations[stepID]
}

type navigationPort struct {
	mu         sync.Mutex
	generation uint64
	seen       []browserrunner.Navigation
}

func (p *navigationPort) Navigate(_ context.Context, navigation browserrunner.Navigation) error {
	p.mu.Lock()
	p.generation++
	p.seen = append(p.seen, navigation)
	p.mu.Unlock()
	return nil
}

func (p *navigationPort) Generation() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.generation
}

type errorSink struct {
	mu  sync.Mutex
	err error
}

func (s *errorSink) SetError(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

func (s *errorSink) Error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func TestEvidenceTrackerRecordsOrderedTurnsAndNavigation(t *testing.T) {
	run := &recordingRun{}
	tracker := browserrunnerwire.NewEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "welcome"},
			{ID: "move", Navigation: &browserrunner.Navigation{FromPageID: "home", ToPageID: "cart", URL: "https://fixture.test/cart"}},
		},
		Run: run,
	})
	tracker.Configure(context.Background(), func() {}, &navigationPort{generation: 4})

	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("show me the cart")})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("I can do that")})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("open it")})

	run.mu.Lock()
	if got, want := len(run.turns), 3; got != want {
		t.Fatalf("turn count = %d, want %d (%v)", got, want, run.turns)
	}
	if run.turns[0] != "customer:welcome:show me the cart" || run.turns[1] != "assistant:welcome:I can do that" || run.turns[2] != "customer:move:open it" {
		t.Fatalf("turn order = %v", run.turns)
	}
	if len(run.navigations) != 1 {
		t.Fatalf("navigation count = %d, want 1", len(run.navigations))
	}
	navigation := run.navigations[0]
	run.mu.Unlock()
	if navigation.PreviousGeneration != 4 || navigation.Generation != 5 {
		t.Fatalf("navigation generations = %d -> %d, want 4 -> 5", navigation.PreviousGeneration, navigation.Generation)
	}
	if got := tracker.CurrentStep(); got != "move" {
		t.Fatalf("current step = %q, want move", got)
	}
	if err := tracker.Err(); err != nil {
		t.Fatalf("tracker error = %v", err)
	}
}

func TestEvidenceTrackerDeadlineCancelsWithTypedErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracker := browserrunnerwire.NewEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{{ID: "slow", Deadline: 20 * time.Millisecond}},
		Run:   &recordingRun{},
	})
	tracker.Configure(ctx, cancel, nil)
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("wait")})

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("tracker did not cancel after the step deadline")
	}
	if err := tracker.Err(); !errors.Is(err, browserrunner.ErrTimeout) || !errors.Is(err, browserrunner.ErrEvidence) {
		t.Fatalf("deadline error = %v, want timeout and evidence", err)
	}
}

func TestEvidenceTrackerSuppressesLateEventsAfterExplicitCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &recordingRun{}
	var canceledID, canceledReason string
	tracker := browserrunnerwire.NewEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "work"},
			{ID: "stop", Cancel: &browserrunner.CancellationBoundary{Reason: "customer stopped"}},
		},
		Run: run,
	})
	tracker.Configure(ctx, cancel, nil)
	tracker.SetCancelInvocation(func(_ context.Context, invocationID, reason string) error {
		canceledID, canceledReason = invocationID, reason
		return nil
	})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("start")})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("working")})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant})
	tracker.NoteInFlight("work", "invocation-7")
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("stop")})

	if canceledID != "invocation-7" || canceledReason != "customer stopped" {
		t.Fatalf("cancel callback = (%q, %q)", canceledID, canceledReason)
	}
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("late")})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta})
	if got := tracker.LateEventCount(); got != 4 {
		t.Fatalf("late event count = %d, want 4", got)
	}
	if err := tracker.Err(); err != nil {
		t.Fatalf("tracker error = %v", err)
	}
	if len(run.cancellations) != 1 || !run.cancellations[0].Requested {
		t.Fatalf("cancellation evidence = %+v", run.cancellations)
	}
}

func TestInterruptionControllerQueuesInOrderAndCloses(t *testing.T) {
	run := &recordingRun{}
	controller := browserrunnerwire.NewInterruptionController(browserrunner.InterruptionControllerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "work"},
			{ID: "overlap", Interrupt: &browserrunner.InterruptionBoundary{Trigger: "in_flight_invocation", ToolName: "write_state"}},
			{ID: "stop", Cancel: &browserrunner.CancellationBoundary{Reason: "stop"}},
		},
		Audio: map[string]browserrunner.AudioInput{
			"overlap": {PCM: []byte{1, 2}, SourceSampleRate: 24000},
			"stop":    {PCM: []byte{3, 4}, SourceSampleRate: 24000, EndOfTurn: true},
		},
		Run: run,
	})
	if controller == nil {
		t.Fatal("controller is nil for interruption steps")
	}
	controller.ObserveInFlight("work", "invocation-1", "write_state")
	first, ok := <-controller.AudioInterruptions()
	if !ok || string(first.PCM) != string([]byte{1, 2}) {
		t.Fatalf("first interruption = %#v, open = %v", first, ok)
	}
	second, ok := <-controller.AudioInterruptions()
	if !ok || string(second.PCM) != string([]byte{3, 4}) || !second.EndOfTurn {
		t.Fatalf("second interruption = %#v, open = %v", second, ok)
	}
	controller.Close()
	if _, ok := <-controller.AudioInterruptions(); ok {
		t.Fatal("interruption channel remained open after Close")
	}
	if len(run.cancellations) != 1 || !run.cancellations[0].OverlappingAudioSent || !run.cancellations[0].ExplicitCancelAudioSent {
		t.Fatalf("cancellation evidence = %+v", run.cancellations)
	}
}

func TestInterruptionControllerReportsQueueAdmissionFailure(t *testing.T) {
	sink := &errorSink{}
	controller := browserrunnerwire.NewInterruptionController(browserrunner.InterruptionControllerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "work"},
			{ID: "overlap", Interrupt: &browserrunner.InterruptionBoundary{Trigger: "in_flight_invocation"}},
			{ID: "stop", Cancel: &browserrunner.CancellationBoundary{Reason: "stop"}},
		},
		Audio: map[string]browserrunner.AudioInput{
			"overlap": {PCM: []byte{1}},
			"stop":    {PCM: []byte{2}},
		},
		QueueCapacity: 1,
		ErrorSink:     sink,
	})
	controller.ObserveInFlight("work", "invocation-1", "")
	if err := sink.Error(); !errors.Is(err, browserrunner.ErrInterruptionQueueFull) {
		t.Fatalf("queue error = %v, want queue-full sentinel", err)
	}
	controller.Close()
}

func TestPartitionAudioInputsRebasesOrdinaryTurns(t *testing.T) {
	steps := []browserrunner.StepBoundary{
		{ID: "one"},
		{ID: "overlap", Interrupt: &browserrunner.InterruptionBoundary{}},
		{ID: "stop", Cancel: &browserrunner.CancellationBoundary{}},
		{ID: "two"},
	}
	normal, special := browserrunner.PartitionAudioInputs(steps, []browserrunner.AudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}},
		{AfterCompletedTurns: 1, PCM: []byte{2}},
		{AfterCompletedTurns: 2, PCM: []byte{3}},
		{AfterCompletedTurns: 3, PCM: []byte{4}},
	})
	if len(normal) != 2 || len(special) != 2 {
		t.Fatalf("partition sizes = normal %d special %d", len(normal), len(special))
	}
	if normal[0].AfterCompletedTurns != 0 || normal[1].AfterCompletedTurns != 1 || string(normal[1].PCM) != string([]byte{4}) {
		t.Fatalf("rebased normal inputs = %+v", normal)
	}
	if string(special["overlap"].PCM) != string([]byte{2}) || string(special["stop"].PCM) != string([]byte{3}) {
		t.Fatalf("special inputs = %+v", special)
	}
}
