package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAggregatesRepeatedPackagesAndAcceptsExplicitZero(t *testing.T) {
	observations, err := Parse(strings.NewReader(strings.Join([]string{
		`{"Action":"start","Package":"example.com/repeated"}`,
		`{"Action":"pass","Package":"example.com/repeated","Elapsed":2.25}`,
		`{"Action":"start","Package":"example.com/zero"}`,
		`{"Action":"pass","Package":"example.com/zero","Elapsed":0}`,
		`{"Action":"start","Package":"example.com/repeated"}`,
		`{"Action":"pass","Package":"example.com/repeated","Elapsed":0.75}`,
	}, "\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(observations) != 3 {
		t.Fatalf("observation count = %d, want 3", len(observations))
	}

	result, err := Evaluate(observations)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Total != 3*time.Second {
		t.Fatalf("total = %s, want 3s", result.Total)
	}
	if result.PackageCount != 2 || result.Packages[0].Package != "example.com/repeated" || result.Packages[0].Count != 2 {
		t.Fatalf("package aggregation = %+v, want repeated package first with two completions", result.Packages)
	}
}

func TestEvaluateAtBudgetPasses(t *testing.T) {
	result, err := Evaluate([]Observation{{Package: "example.com/exact", Elapsed: Budget}})
	if err != nil {
		t.Fatalf("Evaluate at budget: %v", err)
	}
	if result.Total != Budget {
		t.Fatalf("total = %s, want %s", result.Total, Budget)
	}
}

func TestMeasureFailureModes(t *testing.T) {
	overBudget := string(readFixture(t, "over_budget.jsonl"))
	cases := []struct {
		name     string
		input    string
		wantType func(error) bool
		contains string
	}{
		{
			name:  "over budget",
			input: overBudget,
			wantType: func(err error) bool {
				var target *BudgetExceededError
				return errors.As(err, &target) && target.Total == 61*time.Second+500*time.Millisecond
			},
			contains: "exceeds budget",
		},
		{
			name:  "malformed input",
			input: "not-json\n",
			wantType: func(err error) bool {
				var target *MalformedInputError
				return errors.As(err, &target)
			},
			contains: "line 1",
		},
		{
			name:  "terminal timing omitted",
			input: `{"Action":"pass","Package":"example.com/missing"}` + "\n",
			wantType: func(err error) bool {
				var target *MissingTimingError
				return errors.As(err, &target) && target.Terminal && target.Package == "example.com/missing"
			},
			contains: "explicit elapsed duration",
		},
		{
			name:  "package timing omitted",
			input: `{"Action":"start","Package":"example.com/incomplete"}` + "\n",
			wantType: func(err error) bool {
				var target *MissingTimingError
				return errors.As(err, &target) && !target.Terminal && target.Package == "example.com/incomplete"
			},
			contains: "timed terminal record",
		},
		{
			name:  "empty run",
			input: "\n",
			wantType: func(err error) bool {
				var target *EmptyRunError
				return errors.As(err, &target)
			},
			contains: "no package completions",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Measure(strings.NewReader(testCase.input))
			if err == nil {
				t.Fatal("Measure returned nil error")
			}
			if !testCase.wantType(err) {
				t.Fatalf("error type/value = %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), testCase.contains) {
				t.Fatalf("error %q does not contain %q", err, testCase.contains)
			}
		})
	}
}

func TestWriteBudgetReportGolden(t *testing.T) {
	result, err := Measure(bytes.NewReader(readFixture(t, "over_budget.jsonl")))
	if err == nil {
		t.Fatal("Measure returned nil error for over-budget fixture")
	}
	var budgetError *BudgetExceededError
	if !errors.As(err, &budgetError) {
		t.Fatalf("error = %T %v, want BudgetExceededError", err, err)
	}
	if result.Total != budgetError.Total {
		t.Fatalf("result total = %s, error total = %s", result.Total, budgetError.Total)
	}

	var output bytes.Buffer
	if err := WriteBudgetReport(&output, budgetError); err != nil {
		t.Fatalf("WriteBudgetReport: %v", err)
	}
	want := string(readFixture(t, "over_budget.golden"))
	if output.String() != want {
		t.Fatalf("report mismatch\n--- got ---\n%s--- want ---\n%s", output.String(), want)
	}
}

func TestCommandOverBudgetFixtureFails(t *testing.T) {
	output, err := runFixtureCommand(t, "over_budget.jsonl")
	if err == nil {
		t.Fatalf("command succeeded; output:\n%s", output)
	}
	for _, want := range []string{"Measured total: 1m1.5s", "Budget: 1m0s", "example.com/a", "example.com/z"} {
		if !strings.Contains(output, want) {
			t.Fatalf("command output missing %q:\n%s", want, output)
		}
	}
}

func TestCommandWithinBudgetFixturePasses(t *testing.T) {
	output, err := runFixtureCommand(t, "within_budget.jsonl")
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, output)
	}
	want := "PR-tier package-time total: 3s (3 package completions across 2 packages)\nBudget: 1m0s\n"
	if output != want {
		t.Fatalf("command output = %q, want %q", output, want)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func runFixtureCommand(t *testing.T, name string) (string, error) {
	t.Helper()
	command := exec.Command("go", "run", ".")
	command.Dir, _ = os.Getwd()
	command.Env = append(os.Environ(), "GOWORK=off")
	command.Stdin = bytes.NewReader(readFixture(t, name))
	output, err := command.CombinedOutput()
	return string(output), err
}
