package probe

import (
	"errors"
	"fmt"
	"strings"
)

// AcceptanceTransport identifies how an acceptance probe was driven. Replay
// and live runs share the same runner and verdict contract.
type AcceptanceTransport string

const (
	AcceptanceTransportLive   AcceptanceTransport = "live"
	AcceptanceTransportReplay AcceptanceTransport = "replay"
)

// AcceptanceTerminalState is the terminal state of the probe session. A
// stuck session is kept distinct from an ordinary execution error because the
// downstream acceptance fleet reports those findings separately.
type AcceptanceTerminalState string

const (
	AcceptanceCompleted              AcceptanceTerminalState = "completed"
	AcceptanceErrored                AcceptanceTerminalState = "errored"
	AcceptanceStuckPendingDownstream AcceptanceTerminalState = "stuck-pending-downstream"
)

// SubjectiveRating is the probe agent's experience rating. The acceptance
// contract rejects confusing runs; a missing or unknown rating is also not a
// usable acceptance result.
type SubjectiveRating string

const (
	SubjectiveEasy      SubjectiveRating = "easy"
	SubjectiveWorkable  SubjectiveRating = "workable"
	SubjectiveConfusing SubjectiveRating = "confusing"
)

var (
	// ErrObjectiveEvidenceAbsent identifies a result that supplied no
	// recorded artifact proving the goal.
	ErrObjectiveEvidenceAbsent = errors.New("objective evidence absent")
	// ErrObjectiveEvidenceMismatch identifies an artifact that did not contain
	// the checked claim supplied by the probe.
	ErrObjectiveEvidenceMismatch = errors.New("objective evidence does not verify claim")
	// ErrSubjectiveRatingMissing identifies a report with no valid rating.
	ErrSubjectiveRatingMissing = errors.New("subjective rating missing")
	// ErrSubjectiveRatingConfusing identifies the explicit failing rating.
	ErrSubjectiveRatingConfusing = errors.New("subjective rating is confusing")
	// ErrSubjectiveRatingInvalid identifies a report with an unknown rating.
	ErrSubjectiveRatingInvalid = errors.New("subjective rating is invalid")
	// ErrAcceptanceTerminalState identifies an invalid terminal state.
	ErrAcceptanceTerminalState = errors.New("invalid acceptance terminal state")
)

// AcceptanceInput is the complete context intentionally exposed to a blind
// probe agent: a resolved executable path, a plain-English goal, and an empty
// working directory. Do not add flags, scenario hints, or repository context
// to this type.
type AcceptanceInput struct {
	BinaryPath       string `json:"binary_path"`
	Goal             string `json:"goal"`
	WorkingDirectory string `json:"working_directory"`
}

// ObjectiveEvidence is a reference into the recorded run artifacts. Verified
// is set only by the artifact verifier after it has read the referenced bytes;
// a probe's claim alone cannot set it.
type ObjectiveEvidence struct {
	ArtifactPath string `json:"artifact_path"`
	CheckedClaim string `json:"checked_claim"`
	Verified     bool   `json:"verified"`
}

// AcceptanceAgentReport is the small report a probe transport may return in
// addition to captured stdout, stderr, and transcript bytes. The runner uses
// the report as a claim and evaluates objective evidence separately.
type AcceptanceAgentReport struct {
	ClaimedSuccess        bool                    `json:"claimed_success"`
	ObjectiveArtifactPath string                  `json:"objective_artifact_path,omitempty"`
	CheckedClaim          string                  `json:"checked_claim,omitempty"`
	SubjectiveRating      SubjectiveRating        `json:"subjective_rating,omitempty"`
	TerminalState         AcceptanceTerminalState `json:"terminal_state,omitempty"`
}

// AcceptanceVerdict extends the existing probe ScenarioResult shape. The
// embedded result keeps name/pass/expectations/ticks/frames/terminal_reason
// available to existing JSONL fleet consumers while the acceptance-specific
// fields provide the objective and subjective gate inputs.
type AcceptanceVerdict struct {
	ScenarioResult
	Goal              string                  `json:"goal"`
	ObjectiveEvidence ObjectiveEvidence       `json:"objective_evidence"`
	SubjectiveRating  SubjectiveRating        `json:"subjective_rating"`
	TerminalState     AcceptanceTerminalState `json:"terminal_state"`
	RunDirectory      string                  `json:"run_directory,omitempty"`
	Transport         AcceptanceTransport     `json:"transport,omitempty"`
}

// AcceptanceResult is a compatibility spelling for downstream callers that
// call every emitted record a result rather than a verdict.
type AcceptanceResult = AcceptanceVerdict

// EvaluateAcceptance combines independently-verified objective evidence with
// the probe's subjective rating. It is deliberately pure: artifact reading
// belongs to the transport-specific runner and only the verifier may set
// evidence.Verified.
func EvaluateAcceptance(goal string, report AcceptanceAgentReport, evidence ObjectiveEvidence, transport AcceptanceTransport) AcceptanceVerdict {
	state := report.TerminalState
	if state == "" {
		state = AcceptanceErrored
	}

	verdict := AcceptanceVerdict{
		ScenarioResult: ScenarioResult{
			Name:           "acceptance",
			TerminalReason: acceptanceTerminalReason(state),
		},
		Goal:              goal,
		ObjectiveEvidence: evidence,
		SubjectiveRating:  report.SubjectiveRating,
		TerminalState:     state,
		Transport:         transport,
	}

	var reasons []string
	if !evidence.Verified {
		reasons = append(reasons, ErrObjectiveEvidenceAbsent.Error())
	}
	if report.SubjectiveRating == "" {
		reasons = append(reasons, ErrSubjectiveRatingMissing.Error())
	} else if report.SubjectiveRating == SubjectiveConfusing {
		reasons = append(reasons, ErrSubjectiveRatingConfusing.Error())
	} else if report.SubjectiveRating != SubjectiveEasy && report.SubjectiveRating != SubjectiveWorkable {
		reasons = append(reasons, fmt.Sprintf("%s: %q", ErrSubjectiveRatingInvalid, report.SubjectiveRating))
	}
	if !validAcceptanceTerminalState(state) {
		reasons = append(reasons, fmt.Sprintf("%s: %q", ErrAcceptanceTerminalState, state))
	} else if state != AcceptanceCompleted {
		reasons = append(reasons, fmt.Sprintf("probe terminal state is %s", state))
	}
	if len(reasons) == 0 {
		verdict.Pass = true
		return verdict
	}
	verdict.Error = strings.Join(reasons, "; ")
	return verdict
}

func validAcceptanceTerminalState(state AcceptanceTerminalState) bool {
	switch state {
	case AcceptanceCompleted, AcceptanceErrored, AcceptanceStuckPendingDownstream:
		return true
	default:
		return false
	}
}

func acceptanceTerminalReason(state AcceptanceTerminalState) string {
	if state == AcceptanceStuckPendingDownstream {
		return "stuck"
	}
	return string(state)
}
