package results

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

const fixtureDir = "../../transport/cli/testdata/probe-reports"

func TestRenderFrictionSummaryRendersEverySectionDeterministically(t *testing.T) {
	inputs, closeInputs, err := OpenFrictionInputs([]string{" " + filepath.Join(fixtureDir, "mixed.jsonl") + " "}, nil)
	defer closeInputs()
	if err != nil {
		t.Fatalf("open inputs: %v", err)
	}
	report, err := probe.AggregateFrictionReport(inputs...)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	summary := RenderFrictionSummary(report)
	for _, want := range []string{"Probe friction report\n", "Scenario rollups:\n  ", "Terminal reasons:\n", "Error classes:\n", "Expectation misses:\n", "Top frictions:\n", "Health: fail\n"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
	empty := RenderFrictionSummary(probe.FrictionReport{})
	if strings.Count(empty, "  (none)\n") != 5 || !strings.HasSuffix(empty, "Health: pass\n") {
		t.Fatalf("empty summary = %q", empty)
	}
	if scenarioNames(nil) != noneLabel || scenarioNames([]string{"a", "b"}) != "a, b" {
		t.Fatal("scenario names rendering changed")
	}
}

func TestOpenSourcesMapsStdinAndTypedFailures(t *testing.T) {
	stdin := strings.NewReader("")
	inputs, closeInputs, err := OpenFrictionInputs([]string{StdinSource}, stdin)
	closeInputs()
	if err != nil || len(inputs) != 1 || inputs[0].Name != StdinSource || inputs[0].Reader != stdin {
		t.Fatalf("stdin input = %+v, %v", inputs, err)
	}
	if _, _, err := OpenFrictionInputs([]string{filepath.Join(fixtureDir, "run-a.jsonl"), " "}, stdin); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty input error = %v", err)
	}
	var typed *probe.FrictionReportError
	if _, _, err := OpenFrictionInputs([]string{"/nonexistent/input.jsonl"}, stdin); !errors.As(err, &typed) || typed.Source != "/nonexistent/input.jsonl" {
		t.Fatalf("missing input error = %v, want typed friction error", err)
	}
	artifacts, closeArtifacts, err := OpenGateArtifacts([]string{filepath.Join(fixtureDir, "run-a.jsonl"), StdinSource}, stdin)
	closeArtifacts()
	if err != nil || len(artifacts) != 2 || artifacts[1].Name != StdinSource {
		t.Fatalf("gate artifacts = %+v, %v", artifacts, err)
	}
	if _, _, err := OpenGateArtifacts([]string{filepath.Join(fixtureDir, "run-a.jsonl"), "/nonexistent.jsonl"}, stdin); err == nil || !strings.HasPrefix(err.Error(), "read result artifact: ") {
		t.Fatalf("gate missing artifact error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "verdict.json")
	if err := WriteFile(path, []byte("{}\n")); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != artifactFilePermission {
		t.Fatalf("artifact mode = %v, %v", info, err)
	}
}
