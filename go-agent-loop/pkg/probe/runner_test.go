package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func passingScenario() Scenario {
	return Scenario{
		ID:   "all-pass",
		Steps: []Step{
			{Type: StepSendText, Kind: StepSendText, Text: "hello"},
			{Type: StepAdvanceTo, Kind: StepAdvanceTo, At: 5, Time: 5},
			{Type: StepClose, Kind: StepClose},
		},
		Expectations: []ExpectedBehavior{
			{Type: ExpectTranscriptContains, Kind: ExpectTranscriptContains, Text: "hello"},
			{Type: ExpectFrameCount, Kind: ExpectFrameCount, Count: 3},
		},
	}
}

func failingScenario() Scenario {
	return Scenario{
		ID:   "failing",
		Steps: []Step{
			{Type: StepSendText, Kind: StepSendText, Text: "hi"},
			{Type: StepAdvanceTo, Kind: StepAdvanceTo, At: 2, Time: 2},
			{Type: StepClose, Kind: StepClose},
		},
		Expectations: []ExpectedBehavior{
			{Type: ExpectTranscriptContains, Kind: ExpectTranscriptContains, Text: "goodbye"},
			{Type: ExpectToolCalled, Kind: ExpectToolCalled, ToolName: "lookup"},
			{Type: ExpectFrameCount, Kind: ExpectFrameCount, Count: 3},
		},
	}
}

func passingExec(t *testing.T) ExecFunc {
	t.Helper()
	return func(ctx context.Context, scenario Scenario) (ObservationSnapshot, error) {
		return ObservationSnapshot{
			Transcript:      "hello",
			TerminalReason:  "close",
			ObservedTick:    5,
			HasObservedTick: true,
			FrameCount:      3,
		}, nil
	}
}

func decodeLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	var decoded []map[string]any
	for index, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if line == "" {
			t.Fatalf("line %d is empty", index)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("line %d is not valid JSON (%q): %v", index, line, err)
		}
		decoded = append(decoded, value)
	}
	return decoded
}

func TestRunnerAllPassGolden(t *testing.T) {
	var buf bytes.Buffer
	runner := Runner{Exec: passingExec(t), Out: &buf}
	summary, err := runner.Run(context.Background(), []Scenario{passingScenario()})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if summary != (RunSummary{Total: 1, Passed: 1, Failed: 0, Status: StatusPass}) {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	got := buf.String()
	want := `{"name":"all-pass","pass":true,"expectations":[{"index":0,"kind":"transcript-contains","passed":true},{"index":1,"kind":"frame-count","passed":true}],"ticks":5,"frames":3,"terminal_reason":"close"}` + "\n" +
		`{"total":1,"passed":1,"failed":0,"status":"pass"}` + "\n"
	if got != want {
		t.Fatalf("golden mismatch:\n got: %s\nwant: %s", got, want)
	}
	lines := decodeLines(t, got)
	if len(lines) != 2 {
		t.Fatalf("expected one result line plus summary, got %d lines", len(lines))
	}
}

func TestRunnerSummaryMixedAndEmpty(t *testing.T) {
	var buf bytes.Buffer
	runner := Runner{Exec: func(ctx context.Context, s Scenario) (ObservationSnapshot, error) {
		if s.ID == "failing" {
			return ObservationSnapshot{Transcript: "hi", FrameCount: 3}, nil
		}
		return ObservationSnapshot{Transcript: "hello", ObservedTick: 5, HasObservedTick: true, FrameCount: 3}, nil
	}, Out: &buf}
	summary, err := runner.Run(context.Background(), []Scenario{passingScenario(), failingScenario()})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if summary.Total != 2 || summary.Passed != 1 || summary.Failed != 1 || summary.Status != StatusFail {
		t.Fatalf("mixed-run summary wrong: %+v", summary)
	}
	parts := strings.SplitN(buf.String(), "\n", 3)
	lastLine := strings.TrimSpace(parts[2])
	want := `{"total":2,"passed":1,"failed":1,"status":"fail"}`
	if lastLine != want {
		t.Fatalf("summary golden mismatch:\n got: %s\nwant: %s", lastLine, want)
	}

	var empty bytes.Buffer
	emptyRunner := Runner{Exec: passingExec(t), Out: &empty}
	emptySummary, err := emptyRunner.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("zero-scenario Run failed: %v", err)
	}
	if emptySummary != (RunSummary{Status: StatusFail}) {
		t.Fatalf("zero-scenario summary wrong: %+v", emptySummary)
	}
	wantZero := "{\"total\":0,\"passed\":0,\"failed\":0,\"status\":\"fail\"}\n"
	if empty.String() != wantZero {
		t.Fatalf("zero-scenario output wrong:\n got: %s\nwant: %s", empty.String(), wantZero)
	}
}

func TestRunnerFailingExpectationDetailAndIsolation(t *testing.T) {
	var buf bytes.Buffer
	runner := Runner{Exec: func(ctx context.Context, s Scenario) (ObservationSnapshot, error) {
		if s.ID == "failing" {
			return ObservationSnapshot{Transcript: "hi", FrameCount: 3}, nil
		}
		return ObservationSnapshot{Transcript: "hello", ObservedTick: 5, HasObservedTick: true, FrameCount: 3}, nil
	}, Out: &buf}
	summary, err := runner.Run(context.Background(), []Scenario{failingScenario(), passingScenario()})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if summary.Failed != 1 || summary.Passed != 1 {
		t.Fatalf("isolation broken, summary: %+v", summary)
	}
	lines := decodeLines(t, buf.String())
	first := lines[0]
	if first["name"] != "failing" || first["pass"] != false {
		t.Fatalf("first result wrong: %v", first)
	}
	expectations := first["expectations"].([]any)
	failedCount := 0
	for _, raw := range expectations {
		outcome := raw.(map[string]any)
		if outcome["passed"] == true {
			continue
		}
		failedCount++
		if outcome["expected"] == "" || outcome["actual"] == "" {
			t.Fatalf("failed expectation missing observed-vs-expected detail: %v", outcome)
		}
	}
	if failedCount != 2 {
		t.Fatalf("expected two failing expectations, got %d in %v", failedCount, expectations)
	}
	second := lines[1]
	if second["name"] != "all-pass" || second["pass"] != true {
		t.Fatalf("subsequent scenario did not run after failure: %v", second)
	}
}

func panickedScenario() Scenario {
	s := passingScenario()
	s.ID = "panicked"
	return s
}

func TestRunnerMalformedAndAbortedScenarios(t *testing.T) {
	malformed := Scenario{ID: "malformed"}
	abortedErr := errors.New("replay divergence at sequence 4")
	var buf bytes.Buffer
	runner := Runner{
		Exec: func(ctx context.Context, s Scenario) (ObservationSnapshot, error) {
			switch s.ID {
			case "aborted":
				return ObservationSnapshot{}, abortedErr
			case "panicked":
				panic("boom")
			default:
				return ObservationSnapshot{Transcript: "hello", FrameCount: 3}, nil
			}
		},
		Out: &buf,
	}
	scenarios := []Scenario{passingScenario(), malformed, {ID: "aborted"}, panickedScenario()}
	summary, err := runner.Run(context.Background(), scenarios)
	if err != nil {
		t.Fatalf("Run returned error instead of reporting failures: %v", err)
	}
	if summary.Total != 4 || summary.Passed != 1 || summary.Failed != 3 || summary.Status != StatusFail {
		t.Fatalf("summary wrong: %+v", summary)
	}
	lines := decodeLines(t, buf.String())
	if len(lines) != 5 {
		t.Fatalf("expected 4 result lines + summary, got %d", len(lines))
	}
	for _, index := range []int{1, 2, 3} {
		result := lines[index]
		if result["pass"] != false {
			t.Fatalf("line %d should be failed: %v", index, result)
		}
		if detail, _ := result["error"].(string); detail == "" {
			t.Fatalf("line %d missing failure detail: %v", index, result)
		}
	}
	if !strings.Contains(lines[3]["error"].(string), "panicked") {
		t.Fatalf("panic not reported as failure detail: %v", lines[3])
	}
}

func TestRunnerWritesOnlyToInjectedWriter(t *testing.T) {
	var buf bytes.Buffer
	runner := Runner{Exec: passingExec(t), Out: &buf}
	if _, err := runner.Run(context.Background(), []Scenario{passingScenario()}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("\n")) {
		t.Fatal("expected newline-delimited JSONL output")
	}
	var noOut Runner
	if _, err := noOut.Run(context.Background(), nil); err == nil {
		t.Fatal("expected error when output writer missing")
	}
	var noExec Runner
	noExec.Out = &buf
	if _, err := noExec.Run(context.Background(), nil); err == nil {
		t.Fatal("expected error when execution function missing")
	}
}
