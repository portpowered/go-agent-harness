package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestDeadguardBoundsHungScenarioExecution(t *testing.T) {
	hungExec := func(ctx context.Context, _ probe.Scenario) (probe.ObservationSnapshot, error) {
		<-ctx.Done()
		return probe.ObservationSnapshot{}, ctx.Err()
	}
	snapshot, err := Deadguard(hungExec, 50*time.Millisecond)(context.Background(), probe.Scenario{ID: "hung", Name: "hung"})
	if err == nil || !strings.Contains(err.Error(), "deadguard") || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("deadguard error = %v, want deadline indication", err)
	}
	if snapshot.FrameCount != 0 || snapshot.TerminalReason != "" {
		t.Fatalf("hung scenario must not produce an observation: %+v", snapshot)
	}
}

func TestDeadguardDoesNotFireForQuickHealthyExecution(t *testing.T) {
	quickExec := func(context.Context, probe.Scenario) (probe.ObservationSnapshot, error) {
		return probe.ObservationSnapshot{FrameCount: 2, HasObservedTick: true, ObservedTick: 1, TerminalReason: "disconnect"}, nil
	}
	snapshot, err := Deadguard(quickExec, 5*time.Second)(context.Background(), probe.Scenario{ID: "quick", Name: "quick"})
	if err != nil || snapshot.FrameCount != 2 || snapshot.TerminalReason != "disconnect" {
		t.Fatalf("deadguard quick execution = %+v, %v", snapshot, err)
	}
}

func TestLoadV2FileUsesOfflineCorpusLookup(t *testing.T) {
	data := []byte(`{"schema_version":"probe.scenario.v2","id":"cli-v2","steps":[{"type":"send_audio","corpus_id":"utterance-hello-there"}],"expectations":[{"type":"no_pending_invocations"}]}`)
	scenario, err := probe.LoadScenarioV2(data, "", replay.Lookup{})
	if err != nil || scenario.ID != "cli-v2" || scenario.Steps[0].CorpusID != "utterance-hello-there" {
		t.Fatalf("load v2 scenario = %#v, %v", scenario, err)
	}
	unknown := []byte(`{"schema_version":"probe.scenario.v2","id":"cli-v2-unknown","steps":[{"type":"send_audio","corpus_id":"not-committed"}],"expectations":[{"type":"no_pending_invocations"}]}`)
	if _, err := probe.LoadScenarioV2(unknown, "", replay.Lookup{}); !errors.Is(err, probe.ErrScenarioV2UnknownCorpus) {
		t.Fatalf("unknown corpus error = %v", err)
	}
	fromFile, err := LoadV2File(writeFile(t, t.TempDir(), "scenario.json", data))
	if err != nil || fromFile.ID != scenario.ID {
		t.Fatalf("load v2 scenario file = %#v, %v", fromFile, err)
	}
}

func TestResolveDispatchesVersionedScenarioLoader(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "versioned.scenario.json", []byte(`{"schema_version":"probe.scenario.v2","id":"cli-v2-selection","steps":[{"type":"send_text","text":"hello"},{"type":"close"}],"expectations":[{"type":"transcript_contains","text":"hello"}]}`))
	scenarios, err := Resolve(path)
	if err != nil || len(scenarios) != 1 || scenarios[0].ID != "cli-v2-selection" || scenarios[0].Steps[0].Type != probe.StepSendText {
		t.Fatalf("resolved scenarios = %#v, %v; want projected v2 session scenario", scenarios, err)
	}
	unsafePath := writeFile(t, dir, "unsafe.scenario.json", []byte(`{"schema_version":"probe.scenario.v2","id":"cli-v2-unsafe","browser_fixture":"../outside.json","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`))
	if _, err := Resolve(unsafePath); !errors.Is(err, probe.ErrScenarioV2FixturePath) {
		t.Fatalf("unsafe v2 selection error = %v, want contained-path error", err)
	}
	if hasV2, err := ContainsV2([]string{"s2s-v6a-error-auth", path}); err != nil || !hasV2 {
		t.Fatalf("ContainsV2 = %t, %v; want v2 detected", hasV2, err)
	}
}

func TestParseAcceptsAliasSpellingsAndRejectsUnknownVariants(t *testing.T) {
	scenario, err := Parse([]byte(`{"id":"alias","steps":[{"type":"send_text","text":"hi"},{"type":"close"}],
		"expected_behavior":[{"type":"terminal_output_state","value":"complete"},{"type":"latency_within_ticks","count":2,"at":3}]}`))
	if err != nil {
		t.Fatalf("parse aliases: %v", err)
	}
	if scenario.Expectations[0].Kind != probe.ExpectOutputState || scenario.Expectations[1].Kind != probe.ExpectLatencyWithinTicks || !scenario.Expectations[1].HasAt {
		t.Fatalf("parsed expectations = %+v", scenario.Expectations)
	}
	for _, test := range []struct{ document, want string }{
		{`{`, "malformed scenario JSON"},
		{`{"id":"x","steps":[]}`, "at least one step"},
		{`{"id":"x","steps":[{"type":"fly"}]}`, `unknown step variant "fly"`},
		{`{"id":"x","steps":[{"type":"close"}]}`, "at least one expected behavior"},
		{`{"id":"x","steps":[{"type":"close"}],"expected":[{"type":"vibes"}]}`, `unknown expectation variant "vibes"`},
	} {
		if _, err := Parse([]byte(test.document)); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("Parse(%s) error = %v, want %q", test.document, err, test.want)
		}
	}
}

func TestResolveAllExpandsSuitesDeduplicatesAndNamesUnknownSelections(t *testing.T) {
	scenarios, err := ResolveAll(Selections([]string{"s2s-v6a-error-auth"}, []string{"s2s-v6a-error-auth", "s2s-v6a-error-auth-healthy-control"}))
	if err != nil || len(scenarios) != 2 {
		t.Fatalf("suite selection = %d scenarios, %v; want two deduplicated cases", len(scenarios), err)
	}
	if _, err := ResolveAll(nil); err == nil || err.Error() != NoSelectionMessage {
		t.Fatalf("empty selection error = %v", err)
	}
	if _, err := ResolveAll([]string{"no-such-scenario"}); err == nil || !strings.Contains(err.Error(), `unknown probe scenario "no-such-scenario"`) {
		t.Fatalf("unknown selection error = %v", err)
	}
	bad := writeFile(t, t.TempDir(), "bad.json", []byte(`{`))
	if _, err := Resolve(bad); err == nil || !strings.Contains(err.Error(), "load probe scenario") {
		t.Fatalf("malformed file error = %v", err)
	}
	if _, _, err := ReplayPlan(t.Context(), []string{"s2s-v6a-error-auth"}, replay.Executor{}); err == nil || !strings.Contains(err.Error(), "replay service is not configured") {
		t.Fatalf("missing replay service error = %v", err)
	}
}

func TestResultRouterSeparatesSummaryLines(t *testing.T) {
	var results, summary bytes.Buffer
	router := &ResultRouter{Results: &results, Summary: &summary}
	for _, line := range []string{`{"name":"a","pass":true}`, `{"total":1,"status":"pass"}`} {
		if _, err := router.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("route %s: %v", line, err)
		}
	}
	if !strings.Contains(results.String(), `"name":"a"`) || !strings.Contains(summary.String(), `"status":"pass"`) {
		t.Fatalf("routed results=%q summary=%q", results.String(), summary.String())
	}
}

func TestDeviceSkipNamesSelectionsByDocumentIdentity(t *testing.T) {
	dir := t.TempDir()
	named := writeFile(t, dir, "named.json", []byte(`{"id":"id-only","name":"Named"}`))
	idOnly := writeFile(t, dir, "id.json", []byte(`{"id":"id-only"}`))
	blank := writeFile(t, dir, "blank.json", []byte(`{}`))
	malformed := writeFile(t, dir, "malformed.json", []byte(`{`))
	availability := serviceDevices.DeviceProbeAvailability{Status: serviceDevices.DeviceProbeStatusSkip, ReasonCode: "no_input", Reason: "no microphone"}
	results, summary := DeviceSkip([]string{named, idOnly, blank, malformed, "registered"}, availability)
	names := make([]string, 0, len(results))
	for _, result := range results {
		names = append(names, result.Name)
	}
	want := []string{"Named", "id-only", blank, malformed, "registered"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("skip names = %v, want %v", names, want)
	}
	encoded, err := json.Marshal(summary)
	if err != nil || string(encoded) != `{"total":5,"passed":0,"failed":0,"skipped":5,"status":"skip"}` {
		t.Fatalf("skip summary = %s, %v", encoded, err)
	}
}
