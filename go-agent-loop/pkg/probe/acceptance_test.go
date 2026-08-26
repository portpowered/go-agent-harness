package probe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAcceptanceVerdictRetainsScenarioResultJSONShape(t *testing.T) {
	verdict := EvaluateAcceptance(
		"Create a result",
		AcceptanceAgentReport{
			SubjectiveRating: SubjectiveEasy,
			TerminalState:    AcceptanceCompleted,
		},
		ObjectiveEvidence{ArtifactPath: "result.txt", CheckedClaim: "done", Verified: true},
		AcceptanceTransportReplay,
	)

	data, err := json.Marshal(verdict)
	if err != nil {
		t.Fatalf("marshal verdict: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("decode verdict: %v", err)
	}
	for _, field := range []string{"name", "pass", "goal", "objective_evidence", "subjective_rating", "terminal_state", "transport"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("verdict JSON missing %q: %s", field, data)
		}
	}
	if fields["name"] != "acceptance" || fields["pass"] != true || fields["transport"] != string(AcceptanceTransportReplay) {
		t.Fatalf("compatibility fields = %#v", fields)
	}

	var legacy ScenarioResult
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatalf("decode as ScenarioResult: %v", err)
	}
	if legacy.Name != "acceptance" || !legacy.Pass {
		t.Fatalf("legacy result = %+v, want passing acceptance result", legacy)
	}
}

func TestEvaluateAcceptanceRejectsConfusingAndNonTerminalReports(t *testing.T) {
	tests := []struct {
		name   string
		report AcceptanceAgentReport
		want   string
	}{
		{
			name: "confusing",
			report: AcceptanceAgentReport{
				SubjectiveRating: SubjectiveConfusing,
				TerminalState:    AcceptanceCompleted,
			},
			want: ErrSubjectiveRatingConfusing.Error(),
		},
		{
			name: "errored",
			report: AcceptanceAgentReport{
				SubjectiveRating: SubjectiveEasy,
				TerminalState:    AcceptanceErrored,
			},
			want: "probe terminal state is errored",
		},
		{
			name: "stuck",
			report: AcceptanceAgentReport{
				SubjectiveRating: SubjectiveEasy,
				TerminalState:    AcceptanceStuckPendingDownstream,
			},
			want: "probe terminal state is stuck-pending-downstream",
		},
		{
			name: "unknown rating",
			report: AcceptanceAgentReport{
				SubjectiveRating: SubjectiveRating("uncertain"),
				TerminalState:    AcceptanceCompleted,
			},
			want: ErrSubjectiveRatingInvalid.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict := EvaluateAcceptance("goal", tt.report, ObjectiveEvidence{Verified: true}, AcceptanceTransportLive)
			if verdict.Pass || verdict.Error == "" {
				t.Fatalf("verdict = %+v, want failure", verdict)
			}
			if !strings.Contains(verdict.Error, tt.want) {
				t.Fatalf("error = %q, want %q", verdict.Error, tt.want)
			}
		})
	}
}

func TestEvaluateAcceptanceRequiresEvidenceRatingAndKnownTerminalState(t *testing.T) {
	tests := []struct {
		name     string
		report   AcceptanceAgentReport
		evidence ObjectiveEvidence
		want     string
	}{
		{
			name: "missing objective evidence",
			report: AcceptanceAgentReport{
				SubjectiveRating: SubjectiveEasy,
				TerminalState:    AcceptanceCompleted,
			},
			want: ErrObjectiveEvidenceAbsent.Error(),
		},
		{
			name: "missing subjective rating",
			report: AcceptanceAgentReport{
				TerminalState: AcceptanceCompleted,
			},
			evidence: ObjectiveEvidence{Verified: true},
			want:     ErrSubjectiveRatingMissing.Error(),
		},
		{
			name: "unknown terminal state",
			report: AcceptanceAgentReport{
				SubjectiveRating: SubjectiveEasy,
				TerminalState:    AcceptanceTerminalState("unknown"),
			},
			evidence: ObjectiveEvidence{Verified: true},
			want:     ErrAcceptanceTerminalState.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict := EvaluateAcceptance("goal", tt.report, tt.evidence, AcceptanceTransportLive)
			if verdict.Pass || !strings.Contains(verdict.Error, tt.want) {
				t.Fatalf("verdict = %+v, want failing reason %q", verdict, tt.want)
			}
		})
	}
}
