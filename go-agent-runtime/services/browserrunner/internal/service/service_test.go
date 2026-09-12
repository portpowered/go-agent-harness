package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
)

type serviceRun struct {
	mu            sync.Mutex
	turns         []string
	navigations   []browserrunner.NavigationObservation
	cancellations []browserrunner.CancellationObservation
	hasInvocation bool
	recordErr     error
}

func (r *serviceRun) ObserveCustomerTurn(stepID, observed string) error {
	r.mu.Lock()
	r.turns = append(r.turns, "customer:"+stepID+":"+observed)
	r.mu.Unlock()
	return nil
}

func (r *serviceRun) ObserveAssistantTurn(stepID, observed string) error {
	r.mu.Lock()
	r.turns = append(r.turns, "assistant:"+stepID+":"+observed)
	r.mu.Unlock()
	return nil
}

func (r *serviceRun) RecordNavigation(observation browserrunner.NavigationObservation) error {
	r.mu.Lock()
	r.navigations = append(r.navigations, observation)
	err := r.recordErr
	r.mu.Unlock()
	return err
}

func (r *serviceRun) RecordCancellation(observation browserrunner.CancellationObservation) error {
	r.mu.Lock()
	r.cancellations = append(r.cancellations, observation)
	err := r.recordErr
	r.mu.Unlock()
	return err
}

func (r *serviceRun) HasInvocation(string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hasInvocation
}

type serviceNavigation struct {
	mu         sync.Mutex
	generation uint64
	err        error
}

func (n *serviceNavigation) Navigate(context.Context, browserrunner.Navigation) error {
	n.mu.Lock()
	n.generation++
	err := n.err
	n.mu.Unlock()
	return err
}

func (n *serviceNavigation) Generation() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.generation
}

type serviceErrors struct {
	mu  sync.Mutex
	err error
}

func (s *serviceErrors) SetError(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

func (s *serviceErrors) Error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func streamTranscript(text string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue(text)}
}

func streamAssistantText(text string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant},
	}
}

func TestEvidenceTrackerWaitsForCustomerBoundaryAndRecordsNavigation(t *testing.T) {
	run := &serviceRun{}
	navigation := &serviceNavigation{generation: 2}
	tracker := newEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "first"},
			{ID: "second", Navigation: &browserrunner.Navigation{FromPageID: "home", ToPageID: "cart", URL: "fixture://cart"}},
		},
		Run: run,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracker.Configure(ctx, cancel, navigation)

	stepCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		step, err := tracker.InvocationStep(ctx)
		stepCh <- step
		errCh <- err
	}()
	tracker.Observe(streamTranscript("first request"))
	select {
	case step := <-stepCh:
		if step != "first" {
			t.Fatalf("first invocation step = %q", step)
		}
	case <-time.After(time.Second):
		t.Fatal("InvocationStep did not wake for the customer boundary")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("InvocationStep error = %v", err)
	}
	for _, message := range streamAssistantText("done") {
		tracker.Observe(message)
	}

	run.hasInvocation = true
	blockedCtx, blockedCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer blockedCancel()
	if _, err := tracker.InvocationStep(blockedCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("post-invocation InvocationStep error = %v", err)
	}
	tracker.StopDeadline()
	tracker.Observe(streamTranscript("open cart"))

	run.mu.Lock()
	turns := append([]string(nil), run.turns...)
	navigations := append([]browserrunner.NavigationObservation(nil), run.navigations...)
	run.mu.Unlock()
	if len(turns) != 3 || turns[0] != "customer:first:first request" || turns[1] != "assistant:first:done" || turns[2] != "customer:second:open cart" {
		t.Fatalf("turns = %v", turns)
	}
	if len(navigations) != 1 || navigations[0].PreviousGeneration != 2 || navigations[0].Generation != 3 {
		t.Fatalf("navigation evidence = %+v", navigations)
	}
}

func TestEvidenceTrackerRejectsMalformedAndNavigationFailures(t *testing.T) {
	tracker := newEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{{ID: "move", Navigation: &browserrunner.Navigation{ToPageID: "cart"}}},
		Run:   &serviceRun{},
	})
	tracker.Configure(context.Background(), func() {}, nil)
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd})
	if !errors.Is(tracker.Err(), browserrunner.ErrEvidence) {
		t.Fatalf("malformed transcript error = %v", tracker.Err())
	}

	navigationRun := &serviceRun{}
	navigation := &serviceNavigation{err: errors.New("navigation failed")}
	navigationTracker := newEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{{ID: "move", Navigation: &browserrunner.Navigation{ToPageID: "cart"}}},
		Run:   navigationRun,
	})
	navigationTracker.Configure(context.Background(), func() {}, navigation)
	navigationTracker.Observe(streamTranscript("move"))
	if !errors.Is(navigationTracker.Err(), browserrunner.ErrEvidence) {
		t.Fatalf("navigation error = %v", navigationTracker.Err())
	}
	if len(navigationRun.navigations) != 1 || navigationRun.navigations[0].Error == nil {
		t.Fatalf("navigation failure was not retained: %+v", navigationRun.navigations)
	}
}

func TestEvidenceTrackerTimesOutAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracker := newEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{{ID: "slow", Deadline: 10 * time.Millisecond}},
		Run:   &serviceRun{},
	})
	tracker.Configure(ctx, cancel, nil)
	tracker.Observe(streamTranscript("slow"))
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("deadline did not cancel context")
	}
	if !errors.Is(tracker.Err(), browserrunner.ErrTimeout) || !errors.Is(tracker.Err(), browserrunner.ErrEvidence) {
		t.Fatalf("timeout error = %v", tracker.Err())
	}
}

func TestEvidenceTrackerCancelsAndSuppressesLateEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &serviceRun{}
	tracker := newEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "work"},
			{ID: "stop", Cancel: &browserrunner.CancellationBoundary{Reason: "stop now"}},
		},
		Run: run,
	})
	tracker.Configure(ctx, cancel, nil)
	var callbackID string
	tracker.SetCancelInvocation(func(_ context.Context, invocationID, _ string) error {
		callbackID = invocationID
		return nil
	})
	tracker.Observe(streamTranscript("work"))
	for _, message := range streamAssistantText("working") {
		tracker.Observe(message)
	}
	tracker.NoteInFlight("work", "inv-1")
	tracker.Observe(streamTranscript("stop"))
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("late")})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant})
	if callbackID != "inv-1" || tracker.LateEventCount() != 3 || tracker.Err() != nil {
		t.Fatalf("cancel state callback=%q late=%d err=%v", callbackID, tracker.LateEventCount(), tracker.Err())
	}
	if len(run.cancellations) != 1 || !run.cancellations[0].Requested {
		t.Fatalf("cancellation evidence = %+v", run.cancellations)
	}
}

func TestEvidenceTrackerAllowsDeclaredInterruptionBeforeAssistantBoundary(t *testing.T) {
	run := &serviceRun{}
	tracker := newEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "work"},
			{ID: "overlap", Interrupt: &browserrunner.InterruptionBoundary{}},
		},
		Run: run,
	})
	tracker.Configure(context.Background(), func() {}, nil)
	tracker.Observe(streamTranscript("work"))
	tracker.Observe(streamTranscript("interrupt now"))
	if tracker.CurrentStep() != "overlap" || tracker.Err() != nil {
		t.Fatalf("interruption boundary state step=%q err=%v", tracker.CurrentStep(), tracker.Err())
	}
}

func TestEvidenceTrackerSetErrorUnblocksInvocationWait(t *testing.T) {
	tracker := newEvidenceTracker(browserrunner.EvidenceTrackerConfig{Steps: []browserrunner.StepBoundary{{ID: "step"}}})
	tracker.SetError(errors.New("forced evidence failure"))
	if _, err := tracker.InvocationStep(context.Background()); !errors.Is(err, browserrunner.ErrEvidence) {
		t.Fatalf("InvocationStep error = %v", err)
	}
	if tracker.CurrentStep() != "" || !errors.Is(tracker.Err(), browserrunner.ErrEvidence) {
		t.Fatalf("error state step=%q err=%v", tracker.CurrentStep(), tracker.Err())
	}
}

func TestInterruptionControllerQueuesControlsAndReportsFullQueue(t *testing.T) {
	run := &serviceRun{}
	controller := newInterruptionController(browserrunner.InterruptionControllerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "work"},
			{ID: "overlap", Interrupt: &browserrunner.InterruptionBoundary{}},
			{ID: "stop", Cancel: &browserrunner.CancellationBoundary{}},
		},
		Audio: map[string]browserrunner.AudioInput{
			"overlap": {PCM: []byte{1}},
			"stop":    {PCM: []byte{2}},
		},
		Run: run,
	})
	controller.ObserveInFlight("work", "inv-1", "")
	if first := <-controller.AudioInterruptions(); string(first.PCM) != string([]byte{1}) {
		t.Fatalf("overlap input = %v", first.PCM)
	}
	if second := <-controller.AudioInterruptions(); string(second.PCM) != string([]byte{2}) {
		t.Fatalf("cancel input = %v", second.PCM)
	}
	controller.Close()
	controller.Close()
	if _, ok := <-controller.AudioInterruptions(); ok {
		t.Fatal("controller channel remained open")
	}
	if len(run.cancellations) != 1 {
		t.Fatalf("cancellation records = %d", len(run.cancellations))
	}

	sink := &serviceErrors{}
	full := newInterruptionController(browserrunner.InterruptionControllerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "work"},
			{ID: "overlap", Interrupt: &browserrunner.InterruptionBoundary{}},
			{ID: "stop", Cancel: &browserrunner.CancellationBoundary{}},
		},
		Audio:         map[string]browserrunner.AudioInput{"overlap": {PCM: []byte{1}}, "stop": {PCM: []byte{2}}},
		QueueCapacity: 1,
		ErrorSink:     sink,
	})
	full.ObserveInFlight("work", "inv-2", "")
	if !errors.Is(sink.Error(), browserrunner.ErrInterruptionQueueFull) {
		t.Fatalf("full queue error = %v", sink.Error())
	}
	full.Close()
}

func TestInterruptionControllerRejectsMissingAudioAndNilReceiver(t *testing.T) {
	sink := &serviceErrors{}
	controller := newInterruptionController(browserrunner.InterruptionControllerConfig{
		Steps:     []browserrunner.StepBoundary{{ID: "overlap", Interrupt: &browserrunner.InterruptionBoundary{}}},
		ErrorSink: sink,
	})
	controller.ObserveInFlight("overlap", "inv-1", "")
	if sink.Error() == nil {
		t.Fatal("missing audio was not reported")
	}
	var nilController *interruptionController
	if nilController.AudioInterruptions() != nil {
		t.Fatal("nil controller returned a channel")
	}
	nilController.ObserveInFlight("", "", "")
	nilController.Close()
	if newInterruptionController(browserrunner.InterruptionControllerConfig{Steps: []browserrunner.StepBoundary{{ID: "ordinary"}}}) != nil {
		t.Fatal("ordinary-only scenario created interruption controller")
	}
}

func TestInterruptionControllerPropagatesRecorderFailure(t *testing.T) {
	recordErr := errors.New("recorder closed")
	run := &serviceRun{recordErr: recordErr}
	controller := newInterruptionController(browserrunner.InterruptionControllerConfig{
		Steps: []browserrunner.StepBoundary{
			{ID: "work"},
			{ID: "overlap", Interrupt: &browserrunner.InterruptionBoundary{}},
		},
		Audio:     map[string]browserrunner.AudioInput{"overlap": {PCM: []byte{1}}},
		ErrorSink: &serviceErrors{},
		Run:       run,
	})
	controller.ObserveInFlight("work", "inv-1", "")
	if _, ok := <-controller.AudioInterruptions(); !ok {
		t.Fatal("recorder-failure controller did not enqueue overlap")
	}
	controller.Close()
}
