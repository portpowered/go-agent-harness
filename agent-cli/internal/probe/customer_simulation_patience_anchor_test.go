package probe

import (
	"testing"
	"time"
)

// TestPatiencePolicyStopsRepromptingAtMaxReprompts pins the re-prompt budget
// at both sides of the boundary. The snapshot is past every re-prompt
// threshold but short of dead air, so only MaxReprompts decides whether the
// customer may check in again.
func TestPatiencePolicyStopsRepromptingAtMaxReprompts(t *testing.T) {
	thresholds := PatienceThresholds{
		ListenBeforeFollowUp: 100 * time.Millisecond,
		ResponseStart:        750 * time.Millisecond,
		InProgressWork:       750 * time.Millisecond,
		Reprompt:             time.Second,
		AbsoluteDeadAir:      1500 * time.Millisecond,
	}
	snapshot := PatienceSnapshot{
		At: 2200 * time.Millisecond, ResponseStarted: true, HasProgress: true,
		LastProgressAt: time.Second, RepromptAt: []time.Duration{500 * time.Millisecond},
	}
	for _, testCase := range []struct {
		maxReprompts int
		want         PatienceDecisionKind
	}{
		{maxReprompts: 0, want: PatienceDecisionWait},
		{maxReprompts: 1, want: PatienceDecisionWait},
		{maxReprompts: 2, want: PatienceDecisionReprompt},
	} {
		thresholds.MaxReprompts = testCase.maxReprompts
		policy, err := NewPatiencePolicy(thresholds)
		if err != nil {
			t.Fatalf("NewPatiencePolicy(max=%d): %v", testCase.maxReprompts, err)
		}
		decision, err := policy.Decide(snapshot)
		if err != nil {
			t.Fatalf("Decide(max=%d): %v", testCase.maxReprompts, err)
		}
		if decision.Kind != testCase.want || decision.RepromptCount != 1 {
			t.Fatalf("Decide(max=%d, one re-prompt sent) = %+v, want %q", testCase.maxReprompts, decision, testCase.want)
		}
	}
}

// TestStartPatienceListeningAnchorsWallClockAtChildStart proves the default
// wall-clock controller measures patience from the runner's child start, not
// from when the controller was built during run preparation.
func TestStartPatienceListeningAnchorsWallClockAtChildStart(t *testing.T) {
	controller, err := NewPatienceController(NewFamilyEScenario(), FamilyEActionID, FamilyETurnID, nil)
	if err != nil {
		t.Fatalf("NewPatienceController: %v", err)
	}
	run := &customerSimulationRun{patienceController: controller}
	childStartedAt := time.Now().Add(-time.Hour)
	run.startPatienceListening(childStartedAt)
	if !controller.startedAt.Equal(childStartedAt) {
		t.Fatalf("controller origin = %s, want child start %s", controller.startedAt, childStartedAt)
	}
	if at := controller.listenStartedAt(); at < time.Hour {
		t.Fatalf("listen started at %s, want it measured from the child start an hour ago", at)
	}
}
