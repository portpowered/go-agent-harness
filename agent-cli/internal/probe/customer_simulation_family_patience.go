package probe

import "time"

// Customer-simulation timing policy shared by the scenario families.
const (
	customerFollowUpListen     = 250 * time.Millisecond
	familyAFollowUpListen      = 500 * time.Millisecond
	customerResponseStartWait  = time.Second
	customerInProgressWorkWait = 2 * time.Second
	customerRepromptWait       = 3 * time.Second
	customerAbsoluteDeadAir    = 10 * time.Second
	familyEAbsoluteDeadAir     = 8 * time.Second
	customerScenarioDeadline   = 30 * time.Second
	familyEScenarioDeadline    = 20 * time.Second
	customerSingleReprompt     = 1
	familyAMaxReprompts        = 2
)

// customerPatience returns the shared patience thresholds with the
// family-specific follow-up listen window, dead-air bound, and reprompt cap.
func customerPatience(listen, deadAir time.Duration, maxReprompts int) PatienceThresholds {
	return PatienceThresholds{
		ListenBeforeFollowUp: listen,
		ResponseStart:        customerResponseStartWait,
		InProgressWork:       customerInProgressWorkWait,
		Reprompt:             customerRepromptWait,
		AbsoluteDeadAir:      deadAir,
		MaxReprompts:         maxReprompts,
	}
}
