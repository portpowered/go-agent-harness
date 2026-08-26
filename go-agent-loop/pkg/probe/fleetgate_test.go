package probe

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func gateArtifact(name string, lines ...string) FleetArtifact {
	return FleetArtifact{Name: name, Reader: strings.NewReader(strings.Join(lines, "\n") + "\n")}
}

func gateResultLine(name string, pass bool, terminalReason string) string {
	line := map[string]any{
		"name":            name,
		"pass":            pass,
		"expectations":    []map[string]any{{"index": 0, "kind": "frame-count", "passed": pass}},
		"ticks":           5,
		"frames":          3,
		"terminal_reason": terminalReason,
	}
	if terminalReason == "" {
		delete(line, "terminal_reason")
	}
	encoded, err := json.Marshal(line)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

const gateSummaryLine = `{"total":1,"passed":1,"failed":0,"status":"pass"}`

func TestEvaluateFleetGateAllPass(t *testing.T) {
	verdict, err := EvaluateFleetGate([]FleetArtifact{
		gateArtifact("run-a.jsonl", gateResultLine("s2s-v1", true, "disconnect"), gateSummaryLine),
	})
	if err != nil {
		t.Fatalf("EvaluateFleetGate failed: %v", err)
	}
	if verdict.Status != StatusPass {
		t.Fatalf("status = %q, want pass: %+v", verdict.Status, verdict)
	}
	if verdict.Total != 1 || verdict.Passed != 1 || verdict.Failed != 0 || verdict.Stuck != 0 {
		t.Fatalf("unexpected counts: %+v", verdict)
	}
	if len(verdict.Sources) != 1 || verdict.Sources[0].Source != "run-a.jsonl" || verdict.Sources[0].Status != StatusPass {
		t.Fatalf("unexpected sources: %+v", verdict.Sources)
	}
	if len(verdict.Failing) != 0 {
		t.Fatalf("failing = %v, want empty", verdict.Failing)
	}
}

func TestEvaluateFleetGateMixedSourcesFail(t *testing.T) {
	verdict, err := EvaluateFleetGate([]FleetArtifact{
		gateArtifact("run-b.jsonl",
			gateResultLine("s2s-v2", true, "synthetic"),
			gateResultLine("s2s-v3", false, "error:authentication"),
			gateSummaryLine,
		),
		gateArtifact("run-a.jsonl",
			gateResultLine("s2s-v1", true, "disconnect"),
			gateResultLine("s2s-v4", false, "disconnect"),
			gateSummaryLine,
		),
	})
	if err != nil {
		t.Fatalf("EvaluateFleetGate failed: %v", err)
	}
	if verdict.Status != StatusFail {
		t.Fatalf("status = %q, want fail: %+v", verdict.Status, verdict)
	}
	if verdict.Total != 4 || verdict.Passed != 2 || verdict.Failed != 2 || verdict.Stuck != 0 {
		t.Fatalf("unexpected counts: %+v", verdict)
	}
	if len(verdict.Sources) != 2 ||
		verdict.Sources[0].Source != "run-a.jsonl" || verdict.Sources[0].Status != StatusFail ||
		verdict.Sources[1].Source != "run-b.jsonl" || verdict.Sources[1].Status != StatusFail {
		t.Fatalf("sources not sorted by name or wrong status: %+v", verdict.Sources)
	}
	want := []string{"run-a.jsonl:s2s-v4", "run-b.jsonl:s2s-v3"}
	if strings.Join(verdict.Failing, "|") != strings.Join(want, "|") {
		t.Fatalf("failing = %v, want %v (sorted, qualified by source)", verdict.Failing, want)
	}
}

func TestEvaluateFleetGateStuckOnlyMarkerCountsAsFailure(t *testing.T) {
	verdict, err := EvaluateFleetGate([]FleetArtifact{
		gateArtifact("run-a.jsonl", gateResultLine("s2s-stuck", true, StuckTerminalReason)),
	})
	if err != nil {
		t.Fatalf("EvaluateFleetGate failed: %v", err)
	}
	if verdict.Status != StatusFail {
		t.Fatalf("status = %q, want fail for stuck evidence: %+v", verdict.Status, verdict)
	}
	if verdict.Passed != 0 || verdict.Stuck != 1 || verdict.Total != 1 {
		t.Fatalf("stuck marker must land in the stuck bucket: %+v", verdict)
	}
	if got, want := verdict.Failing, []string{"run-a.jsonl:s2s-stuck"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("failing = %v, want %v", got, want)
	}
}

func TestEvaluateFleetGateNoArtifactsIsTypedError(t *testing.T) {
	_, err := EvaluateFleetGate(nil)
	if !errors.Is(err, ErrNoFleetArtifacts) {
		t.Fatalf("err = %v, want ErrNoFleetArtifacts", err)
	}
	_, err = EvaluateFleetGate([]FleetArtifact{})
	if !errors.Is(err, ErrNoFleetArtifacts) {
		t.Fatalf("err = %v, want ErrNoFleetArtifacts for empty slice", err)
	}
}

func TestEvaluateFleetGateEmptySourceIsReported(t *testing.T) {
	_, err := EvaluateFleetGate([]FleetArtifact{
		gateArtifact("run-a.jsonl"),
		gateArtifact("run-b.jsonl", gateResultLine("s2s-v1", true, "disconnect")),
	})
	var gateErr *FleetGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("err = %v, want *FleetGateError", err)
	}
	if !errors.Is(gateErr, ErrEmptyFleetSource) {
		t.Fatalf("err = %v, want ErrEmptyFleetSource", err)
	}
	if gateErr.Source != "run-a.jsonl" || gateErr.Line != 0 {
		t.Fatalf("empty-source error must name the source without a line: %+v", gateErr)
	}
	if !strings.Contains(gateErr.Error(), "run-a.jsonl") {
		t.Fatalf("error message must carry file context: %q", gateErr.Error())
	}
}

func TestEvaluateFleetGateMalformedLineNamesFileAndLine(t *testing.T) {
	for _, tt := range []struct {
		name string
		line string
	}{
		{name: "invalid json", line: `{"name": `},
		{name: "not a scenario result", line: `{"unrelated": true}`},
		{name: "missing name", line: `{"pass": true}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EvaluateFleetGate([]FleetArtifact{gateArtifact("run-a.jsonl", tt.line)})
			var gateErr *FleetGateError
			if !errors.As(err, &gateErr) {
				t.Fatalf("err = %v, want *FleetGateError", err)
			}
			if gateErr.Source != "run-a.jsonl" || gateErr.Line != 1 {
				t.Fatalf("error must carry file and line context: %+v", gateErr)
			}
			if !strings.Contains(gateErr.Error(), "run-a.jsonl:1") {
				t.Fatalf("error message must carry file:line context: %q", gateErr.Error())
			}
		})
	}
}

func TestEvaluateFleetGateMalformedLineNumberCountsEveryLine(t *testing.T) {
	_, err := EvaluateFleetGate([]FleetArtifact{
		gateArtifact(
			"run-a.jsonl",
			gateResultLine("s2s-v1", true, "disconnect"),
			"",
			`not json at all`,
		),
	})
	var gateErr *FleetGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("err = %v, want *FleetGateError", err)
	}
	if gateErr.Line != 3 {
		t.Fatalf("line = %d, want 3 (blank lines still counted)", gateErr.Line)
	}
}

func TestEvaluateFleetGateDeterministicJSON(t *testing.T) {
	build := func() []FleetArtifact {
		return []FleetArtifact{
			gateArtifact("run-b.jsonl",
				gateResultLine("s2s-v3", false, "disconnect"),
				gateResultLine("s2s-v1", true, "synthetic"),
			),
			gateArtifact("run-a.jsonl",
				gateResultLine("s2s-v9", false, "disconnect"),
				gateResultLine("s2s-v2", true, "synthetic"),
				gateResultLine("s2s-v2", true, "synthetic"),
			),
		}
	}
	first, err := EvaluateFleetGate(build())
	if err != nil {
		t.Fatalf("first EvaluateFleetGate failed: %v", err)
	}
	second, err := EvaluateFleetGate(build())
	if err != nil {
		t.Fatalf("second EvaluateFleetGate failed: %v", err)
	}
	firstBytes, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondBytes, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatalf("identical inputs produced different JSON:\n%s\n%s", firstBytes, secondBytes)
	}
	if first.Failing[0] != "run-a.jsonl:s2s-v9" || first.Failing[1] != "run-b.jsonl:s2s-v3" {
		t.Fatalf("failing list not sorted: %v", first.Failing)
	}
}

func TestEvaluateFleetGateMergesDuplicateSourceNames(t *testing.T) {
	verdict, err := EvaluateFleetGate([]FleetArtifact{
		gateArtifact("run-a.jsonl", gateResultLine("s2s-v1", true, "disconnect")),
		gateArtifact("run-a.jsonl", gateResultLine("s2s-v2", false, "disconnect")),
	})
	if err != nil {
		t.Fatalf("EvaluateFleetGate failed: %v", err)
	}
	if len(verdict.Sources) != 1 {
		t.Fatalf("sources = %+v, want one merged entry per source name", verdict.Sources)
	}
	source := verdict.Sources[0]
	if source.Total != 2 || source.Passed != 1 || source.Failed != 1 || source.Status != StatusFail {
		t.Fatalf("merged source summary wrong: %+v", source)
	}
}

func TestEvaluateFleetGateRejectsUnnamedArtifact(t *testing.T) {
	_, err := EvaluateFleetGate([]FleetArtifact{{Name: "  ", Reader: strings.NewReader("{}\n")}})
	var gateErr *FleetGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("err = %v, want *FleetGateError", err)
	}
}
