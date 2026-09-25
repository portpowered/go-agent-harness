package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// The committed s2s-v3c fixtures live with the integration suite's replay
// corpus; every assertion here drives the public probe entrypoint offline.
var v3cFixtureDir = filepath.Join("..", "..", "..", "test", "integration", "testdata")

func v3cFixture(name string) string {
	return filepath.Join(v3cFixtureDir, name)
}

// The positive case replays three mid-response barge-ins whose bookkeeping
// reconciles: cancels land exactly once each and cumulative message counts
// match the declared composition with no loss or duplication.
func TestProbeRunS2SV3CBargeInRepeatedReconcilesOffline(t *testing.T) {
	run := executeCLI("probe", "run", "--replay",
		v3cFixture("s2s-v3c-barge-in-repeated.session.json"),
		"--json", "--scenario", "s2s-v3c-barge-in-repeated")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if summary["status"] != probeStatusPass {
		t.Fatalf("summary must record pass: %v", summary)
	}
	result := results[0]
	if result["pass"] != true || result["name"] != "s2s-v3c-barge-in-repeated" {
		t.Fatalf("unexpected result line: %v", result)
	}
	kinds := map[string]bool{}
	for _, expectation := range jsonSlice(t, result["expectations"]) {
		outcome := jsonObject(t, expectation)
		if outcome["passed"] != true {
			t.Fatalf("expectation must pass: %v", outcome)
		}
		kinds[jsonString(t, outcome["kind"])] = true
	}
	if !kinds["barge-in-cancel-once"] || !kinds["message-counts-reconcile"] || !kinds["terminal-reason"] {
		t.Fatalf("result must carry passing cancel-once, reconciliation, and terminal-reason outcomes: %v", kinds)
	}
}

// Negative controls: each violating fixture fails through the same CLI path,
// naming the invariant it breaks.
func TestProbeRunS2SV3CNegativeControlsFailNamingTheirInvariant(t *testing.T) {
	cases := []struct {
		name        string
		scenario    string
		fixture     string
		failingKind string
		detail      string
	}{
		{
			name:        "double cancel",
			scenario:    "s2s-v3c-barge-in-repeated-double-cancel",
			fixture:     "s2s-v3c-barge-in-repeated-double-cancel.session.json",
			failingKind: "barge-in-cancel-once",
			detail:      "stray or duplicate cancels",
		},
		{
			name:        "dropped commit",
			scenario:    "s2s-v3c-barge-in-repeated-dropped-commit",
			fixture:     "s2s-v3c-barge-in-repeated-dropped-commit.session.json",
			failingKind: "message-counts-reconcile",
			detail:      "user_turns: expected 7, actual 6",
		},
		{
			name:        "duplicated delivered turn",
			scenario:    "s2s-v3c-barge-in-repeated-duplicated-turn",
			fixture:     "s2s-v3c-barge-in-repeated-duplicated-turn.session.json",
			failingKind: "message-counts-reconcile",
			detail:      "assistant_delivered: expected 4, actual 5",
		},
	}
	for _, testCase := range cases {
		run := executeCLI("probe", "run", "--replay", v3cFixture(testCase.fixture),
			"--json", "--scenario", testCase.scenario)
		if run.exitCode == 0 {
			t.Fatalf("%s: exit code = 0, want non-zero; stderr=%q", testCase.name, run.stderr)
		}
		results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
		if summary["status"] != probeStatusFail {
			t.Fatalf("%s: summary must record failure: %v", testCase.name, summary)
		}
		result := results[0]
		if result["pass"] != false {
			t.Fatalf("%s: negative control must fail: %v", testCase.name, result)
		}
		found := false
		for _, expectation := range jsonSlice(t, result["expectations"]) {
			outcome := jsonObject(t, expectation)
			if outcome["kind"] != testCase.failingKind || outcome["passed"] != false {
				continue
			}
			found = true
			actual := jsonText(outcome["actual"])
			if !strings.Contains(actual, testCase.detail) && !strings.Contains(jsonString(t, outcome["error"]), testCase.detail) {
				t.Fatalf("%s: failure detail must name %q, got actual=%q error=%q",
					testCase.name, testCase.detail, actual, outcome["error"])
			}
		}
		if !found {
			t.Fatalf("%s: %s must be the failing kind: %v", testCase.name, testCase.failingKind, result["expectations"])
		}
	}
}

// Running every v3c case in one invocation splits cleanly: the positive case
// passes while all three negative controls fail, and the summary reports
// exactly that.
func TestProbeRunS2SV3CSuiteSelectionSplitsPositiveFromControls(t *testing.T) {
	run := executeCLI("probe", "run", "--replay", v3cFixtureDir,
		"--json",
		"--scenario", "s2s-v3c-barge-in-repeated",
		"--scenario", "s2s-v3c-barge-in-repeated-duplicated-turn",
		"--scenario", "s2s-v3c-barge-in-repeated-dropped-commit",
		"--scenario", "s2s-v3c-barge-in-repeated-double-cancel")
	if run.exitCode == 0 {
		t.Fatalf("suite containing failing controls must exit non-zero; stdout=%q", run.stdout)
	}
	results, summary := decodeProbeLines(t, 4, run.stdout, run.stderr)
	passed, failed := 0, 0
	for _, result := range results {
		switch result["pass"] {
		case true:
			passed++
		case false:
			failed++
		default:
			t.Fatalf("result line missing pass verdict: %v", result)
		}
	}
	if passed != 1 || failed != 3 {
		t.Fatalf("expected 1 passing positive and 3 failing controls, got %d/%d: %v", passed, failed, results)
	}
	if summary["total"] != float64(4) || summary["status"] != probeStatusFail {
		t.Fatalf("unexpected summary: %v", summary)
	}
}
