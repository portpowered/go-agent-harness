package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
	"go.uber.org/goleak"
)

type cancellationRun struct {
	browserconversation.Run
	recorded []browserconversation.BrowserConversationCancellationEvidence
	err      error
}

func (r *cancellationRun) RecordCancellation(evidence browserconversation.BrowserConversationCancellationEvidence) error {
	r.recorded = append(r.recorded, evidence)
	return r.err
}

func interruptScenario(tool string) browserconversation.BrowserConversationScenario {
	return browserconversation.BrowserConversationScenario{Steps: []browserconversation.BrowserConversationStep{
		{ID: "ask"},
		{ID: "barge", Interrupt: &browserconversation.BrowserConversationInterrupt{Trigger: browserconversation.BrowserInterruptOnInFlightInvocation, ToolName: tool}},
		{ID: "stop", Cancel: &browserconversation.BrowserConversationCancelRequest{Reason: "changed mind"}},
	}}
}

func audioFor(ids ...string) map[string]browserconversation.ScheduledAudioInput {
	audio := make(map[string]browserconversation.ScheduledAudioInput, len(ids))
	for index, id := range ids {
		audio[id] = browserconversation.ScheduledAudioInput{PCM: []byte{byte(index + 1)}, SourceSampleRate: 24000}
	}
	return audio
}

func drain(c *interruptionController) []browserconversation.ScheduledAudioInput {
	c.close()
	var got []browserconversation.ScheduledAudioInput
	for input := range c.Inputs() {
		got = append(got, input)
	}
	return got
}

// TestInterruptionReleasesOverlapThenCancelOnceForMatchingInvocation proves
// the declared barge-in audio and its following cancel are released only for
// the matching in-flight tool, in order, exactly once, with evidence.
func TestInterruptionReleasesOverlapThenCancelOnceForMatchingInvocation(t *testing.T) {
	run := &cancellationRun{}
	scenario := interruptScenario("click")
	tracker := newEvidenceTracker(run, scenario)
	audio := audioFor("barge", "stop")
	controller := newInterruptionController(run, tracker, scenario, audio)
	audio["barge"].PCM[0] = 99 // the controller owns a private copy

	controller.observeInFlight("ask", "", "click")         // no invocation identity
	controller.observeInFlight("ask", "inv-1", "navigate") // another tool
	if len(run.recorded) != 0 {
		t.Fatalf("released for a non-matching invocation: %+v", run.recorded)
	}
	controller.observeInFlight("ask", "inv-1", "click")
	controller.observeInFlight("ask", "inv-2", "click") // already triggered
	got := drain(controller)
	if len(got) != 2 || got[0].PCM[0] != 1 || got[1].PCM[0] != 2 {
		t.Fatalf("released audio = %+v, want barge then stop", got)
	}
	if len(run.recorded) != 1 {
		t.Fatalf("recorded %d cancellations, want 1", len(run.recorded))
	}
	evidence := run.recorded[0]
	if !evidence.Interrupted || !evidence.OverlappingAudioSent || !evidence.ExplicitCancelAudioSent || evidence.InvocationID != "inv-1" || evidence.InterruptedStepID != "ask" {
		t.Fatalf("evidence = %+v", evidence)
	}
	if err := tracker.err(); err != nil {
		t.Fatalf("tracker error = %v", err)
	}
	// After close nothing more is admitted and nothing panics.
	if controller.enqueue(browserconversation.ScheduledAudioInput{PCM: []byte{1}}) {
		t.Fatal("enqueue after close succeeded")
	}
	controller.close()
}

func TestInterruptionCancelOnlyStepFollowsCurrentStep(t *testing.T) {
	run := &cancellationRun{}
	scenario := browserconversation.BrowserConversationScenario{Steps: []browserconversation.BrowserConversationStep{
		{ID: "ask"},
		{ID: "stop", Cancel: &browserconversation.BrowserConversationCancelRequest{Reason: "stop"}},
	}}
	controller := newInterruptionController(run, newEvidenceTracker(run, scenario), scenario, audioFor("stop"))
	controller.observeInFlight("stop", "inv-1", "click") // the cancel is not after itself
	controller.observeInFlight("ask", "inv-1", "click")
	if got := drain(controller); len(got) != 1 {
		t.Fatalf("released %d inputs, want the cancel audio", len(got))
	}
	if len(run.recorded) != 1 || run.recorded[0].ExplicitCancelAudioSent {
		t.Fatalf("evidence = %+v", run.recorded)
	}
}

func TestInterruptionMissingAudioBecomesEvidenceError(t *testing.T) {
	t.Run("missing interruption audio", func(t *testing.T) {
		run := &cancellationRun{}
		scenario := interruptScenario("")
		tracker := newEvidenceTracker(run, scenario)
		controller := newInterruptionController(run, tracker, scenario, audioFor("stop"))
		controller.observeInFlight("ask", "inv-1", "any")
		if err := tracker.err(); err == nil || !strings.Contains(err.Error(), "declared interruption has no audio payload") {
			t.Fatalf("tracker error = %v", err)
		}
		if got := drain(controller); len(got) != 0 || len(run.recorded) != 0 {
			t.Fatalf("released %d inputs with missing audio", len(got))
		}
	})
	t.Run("missing cancellation audio", func(t *testing.T) {
		run := &cancellationRun{}
		scenario := interruptScenario("")
		tracker := newEvidenceTracker(run, scenario)
		controller := newInterruptionController(run, tracker, scenario, audioFor("barge"))
		controller.observeInFlight("ask", "inv-1", "any")
		if err := tracker.err(); err == nil || !strings.Contains(err.Error(), "declared cancellation has no audio payload") {
			t.Fatalf("tracker error = %v", err)
		}
	})
}

func TestInterruptionReleaseFailuresBecomeEvidenceErrors(t *testing.T) {
	t.Run("record failure", func(t *testing.T) {
		recordErr := errors.New("collector closed")
		run := &cancellationRun{err: recordErr}
		scenario := interruptScenario("")
		tracker := newEvidenceTracker(run, scenario)
		controller := newInterruptionController(run, tracker, scenario, audioFor("barge", "stop"))
		controller.observeInFlight("ask", "inv-1", "any")
		if err := tracker.err(); !errors.Is(err, recordErr) || !errors.Is(err, browserconversation.ErrBrowserConversationEvidence) {
			t.Fatalf("tracker error = %v", err)
		}
	})
	t.Run("queue full", func(t *testing.T) {
		run := &cancellationRun{}
		scenario := interruptScenario("")
		tracker := newEvidenceTracker(run, scenario)
		controller := newInterruptionController(run, tracker, scenario, audioFor("barge", "stop"))
		for range cap(controller.channel) {
			controller.channel <- browserconversation.ScheduledAudioInput{}
		}
		controller.observeInFlight("ask", "inv-1", "any")
		if err := tracker.err(); err == nil || !strings.Contains(err.Error(), "queue is full") {
			t.Fatalf("tracker error = %v", err)
		}
		if len(run.recorded) != 0 {
			t.Fatal("cancellation evidence recorded although audio was not released")
		}
	})
	t.Run("closed before trigger", func(t *testing.T) {
		run := &cancellationRun{}
		scenario := interruptScenario("")
		controller := newInterruptionController(run, nil, scenario, audioFor("barge", "stop"))
		controller.close()
		controller.observeInFlight("ask", "inv-1", "any")
		if len(run.recorded) != 0 {
			t.Fatal("closed controller recorded evidence")
		}
	})
}

func TestInterruptionControllerIsAbsentWithoutDeclaredSteps(t *testing.T) {
	controller := newInterruptionController(nil, nil, browserconversation.BrowserConversationScenario{Steps: []browserconversation.BrowserConversationStep{{ID: "a"}}}, nil)
	if controller != nil {
		t.Fatal("controller created for a scenario without interruptions")
	}
	if controller.Inputs() != nil || controller.enqueue(browserconversation.ScheduledAudioInput{PCM: []byte{1}}) {
		t.Fatal("nil controller is not inert")
	}
	controller.observeInFlight("a", "inv", "tool")
	controller.close()
	if cloneAudioMap(nil) != nil {
		t.Fatal("cloneAudioMap(nil) != nil")
	}
}

// TestPartitionAudioHoldsInterruptionAudioOutOfTheTurnSchedule proves held
// interruption/cancel audio does not count as a completed scheduled turn.
func TestPartitionAudioHoldsInterruptionAudioOutOfTheTurnSchedule(t *testing.T) {
	scenario := browserconversation.BrowserConversationScenario{Steps: []browserconversation.BrowserConversationStep{
		{ID: "ask"},
		{ID: "barge", Interrupt: &browserconversation.BrowserConversationInterrupt{}},
		{ID: "stop", Cancel: &browserconversation.BrowserConversationCancelRequest{}},
		{ID: "next"},
		{ID: "barge2", Interrupt: &browserconversation.BrowserConversationInterrupt{}},
		{ID: "plain"},
	}}
	inputs := make([]browserconversation.ScheduledAudioInput, 7)
	for index := range inputs {
		inputs[index].PCM = []byte{byte(index)}
	}
	normal, held := partitionAudio(scenario, inputs)
	if len(held) != 3 || held["barge"].PCM[0] != 1 || held["stop"].PCM[0] != 2 || held["barge2"].PCM[0] != 4 {
		t.Fatalf("held = %+v", held)
	}
	wantTurns := []int{0, 1, 2, 3}
	if len(normal) != len(wantTurns) {
		t.Fatalf("normal = %+v", normal)
	}
	for index, input := range normal {
		if input.AfterCompletedTurns != wantTurns[index] {
			t.Fatalf("normal[%d].AfterCompletedTurns = %d, want %d", index, input.AfterCompletedTurns, wantTurns[index])
		}
	}
}

// TestMain fails the package when any test leaves a goroutine running.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
