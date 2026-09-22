package wire

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

func TestServiceRunAdmitsBeforeFixtureCreation(t *testing.T) {
	scenario := testBrowserConversationScenario()
	scenario.Version = "wrong-version"
	created := false
	_, err := NewService().Run(context.Background(), browserconversation.RunRequest{
		Scenario: scenario,
		FixtureFactory: func(context.Context, browserconversation.BrowserConversationScenario) (browserconversation.Fixture, error) {
			created = true
			return nil, errors.New("must not create fixture")
		},
	})
	if err == nil || created {
		t.Fatalf("Run err=%v fixture-created=%t, want admission failure before fixture creation", err, created)
	}
}

func TestRunPublishesCancellationBeforeLateInvocationObservation(t *testing.T) {
	run, err := NewService().NewRun(testBrowserConversationScenario())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if err := run.ObserveBrokerCall(browserconversation.BrowserConversationBrokerCall{StepID: "step-1", Operation: browserconversation.BrowserConversationInvoke, InvocationID: "invocation-1", State: "dispatched"}); err != nil {
		t.Fatalf("ObserveBrokerCall: %v", err)
	}
	if err := run.RecordCancellation(browserconversation.BrowserConversationCancellationEvidence{Requested: true, InvocationID: "invocation-1", FinalState: "canceled"}); err != nil {
		t.Fatalf("RecordCancellation: %v", err)
	}
	if err := run.ObserveInvocationPublication("late_event", "invocation-1", "completed", false); err != nil {
		t.Fatalf("ObserveInvocationPublication: %v", err)
	}
	result := run.Snapshot()
	if result.Cancellation.FinalState != "canceled" || len(result.InvocationObservations) != 2 {
		t.Fatalf("result cancellation=%+v observations=%+v", result.Cancellation, result.InvocationObservations)
	}
	if result.InvocationObservations[0].Source != "record_cancellation" || result.InvocationObservations[1].Source != "late_event" {
		t.Fatalf("observation order=%+v", result.InvocationObservations)
	}
	if !result.InvocationObservations[0].Terminal || result.InvocationObservations[1].Terminal {
		t.Fatalf("observation terminality=%+v", result.InvocationObservations)
	}
	if _, err := run.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := run.ObserveBrokerCall(browserconversation.BrowserConversationBrokerCall{StepID: "step-1", Operation: browserconversation.BrowserConversationSelectPage}); !errors.Is(err, browserconversation.ErrBrowserConversationRunFinalized) {
		t.Fatalf("late mutation err=%v, want finalized error", err)
	}
}

func testBrowserConversationScenario() browserconversation.BrowserConversationScenario {
	return browserconversation.BrowserConversationScenario{
		Version: browserconversation.BrowserConversationScenarioVersion, ID: "scenario-1", Name: "one step",
		Fixture: browserconversation.BrowserConversationFixture{
			ID: "fixture-1", Pages: []browserconversation.BrowserConversationPage{{ID: "home", URL: "https://example.test/home"}}, InitialPage: "home",
		},
		PostSession: browserconversation.BrowserConversationTabStateRequired{PageID: "home", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true},
		RunTimeout:  2 * time.Second,
		Steps:       []browserconversation.BrowserConversationStep{{ID: "step-1", Utterance: "set the label", PageID: "home", Deadline: time.Second, ExpectedState: &browserconversation.BrowserStateTransition{PageID: "home", Before: json.RawMessage(`{"value":"before"}`), After: json.RawMessage(`{"value":"after"}`)}}},
	}
}
