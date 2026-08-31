package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// gateResultLine renders one ScenarioResult JSONL record as the probe runner
// emits it, with the fields the fleet gate consumes.
func gateResultLine(t *testing.T, name string, pass bool, terminalReason string) string {
	t.Helper()
	record := map[string]any{
		"name":         name,
		"pass":         pass,
		"expectations": []map[string]any{{"index": 0, "kind": "frame-count", "passed": pass}},
		"ticks":        5,
		"frames":       3,
	}
	if terminalReason != "" {
		record["terminal_reason"] = terminalReason
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal result line: %v", err)
	}
	return string(encoded)
}

func writeGateArtifact(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write artifact %s: %v", path, err)
	}
	return path
}

func runGateCLI(t *testing.T, args []string, stdin io.Reader) (string, string, error) {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetIn(stdin)
	rootCmd.SetArgs(args)
	execErr := rootCmd.ExecuteContext(context.Background())
	writeSimulatedMainError(writer.Stderr(), execErr)
	return writer.StdoutString(), writer.StderrString(), execErr
}

func TestProbeGateAllPassFleetExitsZeroWithVerdictJSON(t *testing.T) {
	dir := t.TempDir()
	first := writeGateArtifact(t, dir, "fleet-a.jsonl",
		gateResultLine(t, "s2s-v1-text-in-audio-out", true, "disconnect"),
		gateResultLine(t, "s2s-v3b-barge-in-delivered", true, "synthetic"),
	)
	second := writeGateArtifact(t, dir, "fleet-b.jsonl",
		gateResultLine(t, "s2s-v7a-metrics-modality", true, "synthetic"),
	)
	jsonPath := filepath.Join(dir, "verdict.json")

	stdout, stderr, execErr := runGateCLI(t, []string{
		"probe", "gate",
		"--out", first,
		"--out", second,
		"--json", jsonPath,
	}, bytes.NewReader(nil))
	if execErr != nil {
		t.Fatalf("gate must exit zero on an all-pass fleet: %v\nstderr=%s", execErr, stderr)
	}

	var verdict probe.FleetGateVerdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &verdict); err != nil {
		t.Fatalf("decode stdout verdict %q: %v", stdout, err)
	}
	if verdict.Status != probe.StatusPass || verdict.Total != 3 || verdict.Passed != 3 ||
		verdict.Failed != 0 || verdict.Stuck != 0 {
		t.Fatalf("unexpected verdict: %+v", verdict)
	}
	if len(verdict.Sources) != 2 ||
		verdict.Sources[0].Source != first || verdict.Sources[0].Status != probe.StatusPass ||
		verdict.Sources[1].Source != second || verdict.Sources[1].Status != probe.StatusPass {
		t.Fatalf("sources must list both artifacts in order: %+v", verdict.Sources)
	}
	if len(verdict.Failing) != 0 {
		t.Fatalf("failing = %v, want empty on all-pass fleet", verdict.Failing)
	}

	fileBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read --json output: %v", err)
	}
	if string(fileBytes) != stdout {
		t.Fatalf("--json bytes must equal stdout bytes:\nfile=%q\nstdout=%q", fileBytes, stdout)
	}
}

func TestProbeGateFailingFleetListsScenariosAndExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	passing := writeGateArtifact(t, dir, "fleet-green.jsonl",
		gateResultLine(t, "s2s-v1-text-in-audio-out", true, "disconnect"),
	)
	failing := writeGateArtifact(t, dir, "fleet-red.jsonl",
		gateResultLine(t, "s2s-v6a-error-auth", false, "error:authentication"),
		gateResultLine(t, "s2s-v3b-barge-in-stuck", true, probe.StuckTerminalReason),
	)

	stdout, _, execErr := runGateCLI(t, []string{
		"probe", "gate", "--out", passing, "--out", failing,
	}, bytes.NewReader(nil))
	if execErr == nil {
		t.Fatalf("gate must exit non-zero when any scenario fails: stdout=%s", stdout)
	}

	var verdict probe.FleetGateVerdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &verdict); err != nil {
		t.Fatalf("decode stdout verdict %q: %v", stdout, err)
	}
	if verdict.Status != probe.StatusFail {
		t.Fatalf("status = %q, want fail: %+v", verdict.Status, verdict)
	}
	if verdict.Total != 3 || verdict.Passed != 1 || verdict.Failed != 1 || verdict.Stuck != 1 {
		t.Fatalf("stuck evidence must count as failure, counts wrong: %+v", verdict)
	}
	want := []string{failing + ":s2s-v3b-barge-in-stuck", failing + ":s2s-v6a-error-auth"}
	if strings.Join(verdict.Failing, "|") != strings.Join(want, "|") {
		t.Fatalf("failing = %v, want %v sorted and qualified by source", verdict.Failing, want)
	}
}

func TestProbeGateReadsStdinArtifact(t *testing.T) {
	dir := t.TempDir()
	onDisk := writeGateArtifact(t, dir, "fleet-a.jsonl",
		gateResultLine(t, "s2s-v1-text-in-audio-out", true, "disconnect"),
	)
	stdin := strings.NewReader(
		gateResultLine(t, "s2s-v2d-multi-utterance", true, "synthetic") + "\n" +
			`{"total":1,"passed":1,"failed":0,"status":"pass"}` + "\n")

	stdout, _, execErr := runGateCLI(t, []string{
		"probe", "gate", "--out", onDisk, "--out", "-",
	}, stdin)
	if execErr != nil {
		t.Fatalf("gate over file+stdin must exit zero: %v", execErr)
	}
	var verdict probe.FleetGateVerdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &verdict); err != nil {
		t.Fatalf("decode verdict %q: %v", stdout, err)
	}
	if verdict.Status != probe.StatusPass || verdict.Total != 2 {
		t.Fatalf("unexpected verdict over mixed sources: %+v", verdict)
	}
	foundStdinSource := false
	for _, source := range verdict.Sources {
		if source.Source == "-" {
			foundStdinSource = true
		}
	}
	if !foundStdinSource {
		t.Fatalf("stdin source missing from verdict: %+v", verdict.Sources)
	}
}

func TestProbeGateMalformedLineSurfacesTypedErrorWithFileAndLine(t *testing.T) {
	dir := t.TempDir()
	path := writeGateArtifact(t, dir, "broken.jsonl",
		gateResultLine(t, "s2s-v1-text-in-audio-out", true, "disconnect"),
		`{"name": `,
	)

	_, stderr, execErr := runGateCLI(t, []string{"probe", "gate", "--out", path}, bytes.NewReader(nil))
	if execErr == nil {
		t.Fatal("malformed line must produce a non-zero exit")
	}
	if !strings.Contains(stderr, "broken.jsonl:2") {
		t.Fatalf("error must name file and line, got stderr: %q", stderr)
	}
}

func TestProbeGateUnreadableFileAndEmptyArtifactsAreTypedErrors(t *testing.T) {
	dir := t.TempDir()

	_, stderr, execErr := runGateCLI(t, []string{
		"probe", "gate", "--out", filepath.Join(dir, "does-not-exist.jsonl"),
	}, bytes.NewReader(nil))
	if execErr == nil {
		t.Fatal("unreadable artifact must produce a non-zero exit")
	}
	if !strings.Contains(stderr, "does-not-exist.jsonl") {
		t.Fatalf("unreadable-file error must name the file, got stderr: %q", stderr)
	}

	empty := writeGateArtifact(t, dir, "empty.jsonl")
	_, stderr, execErr = runGateCLI(t, []string{"probe", "gate", "--out", empty}, bytes.NewReader(nil))
	if execErr == nil {
		t.Fatal("artifact without scenario results must produce a non-zero exit")
	}
	if !strings.Contains(stderr, "empty.jsonl") {
		t.Fatalf("empty-artifact error must name the source, got stderr: %q", stderr)
	}

	_, _, execErr = runGateCLI(t, []string{"probe", "gate"}, bytes.NewReader(nil))
	if execErr == nil {
		t.Fatal("no artifacts at all must produce a non-zero exit")
	}
}
