package customersim

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create %s parent: %v", path, err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestRunSpecsLoadsFamilyERepromptAudioSeparately(t *testing.T) {
	temp := t.TempDir()
	turnPath := writeTestFile(t, filepath.Join(temp, "turn.pcm"), []byte{1, 0, 2, 0}, 0o600)
	repromptPath := writeTestFile(t, filepath.Join(temp, "check-in.pcm"), []byte{3, 0, 4, 0}, 0o600)

	runs, err := RunSpecs([]probe.CustomerScenario{probe.NewFamilyEScenario()}, []string{turnPath}, "", repromptPath)
	if err != nil {
		t.Fatalf("RunSpecs: %v", err)
	}
	if len(runs) != 1 || len(runs[0].Audio) != 1 || string(runs[0].PatienceRepromptAudio) != string([]byte{3, 0, 4, 0}) {
		t.Fatalf("Family E run audio = %+v, want one action recording plus separate four-byte re-prompt", runs)
	}
}

func TestRunSpecsRejectsMissingOrMisplacedFamilyERepromptAudio(t *testing.T) {
	audioPath := writeTestFile(t, filepath.Join(t.TempDir(), "turn.pcm"), []byte{1, 0}, 0o600)
	if _, err := RunSpecs([]probe.CustomerScenario{probe.NewFamilyEScenario()}, []string{audioPath}, ""); err == nil || !strings.Contains(err.Error(), "patience-reprompt-audio") {
		t.Fatalf("missing Family E re-prompt error = %v, want explicit flag guidance", err)
	}
	if _, err := RunSpecs([]probe.CustomerScenario{probe.NewFamilyAScenario()}, make([]string, 4), "", audioPath); err == nil || !strings.Contains(err.Error(), "only valid") {
		t.Fatalf("misplaced Family E re-prompt error = %v, want selection-specific failure", err)
	}
	if _, err := RunSpecs(nil, nil, "", "a", "b"); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("multiple re-prompt error = %v", err)
	}
}

func TestRunSpecsResolvesAudioDirectoryLayoutsAndValidatesPCM(t *testing.T) {
	scenario := probe.NewFamilyAScenario()
	script := probe.CustomerSimulationScenarioScript(scenario)
	root := t.TempDir()
	for index, turn := range script {
		name := filepath.Join(root, scenario.ID, turn.ActionID+".pcm")
		if index%2 == 1 {
			name = filepath.Join(root, scenario.ID+"-"+turn.ActionID+".raw")
		}
		writeTestFile(t, name, []byte{byte(index + 1), 0}, 0o600)
	}
	runs, err := RunSpecs([]probe.CustomerScenario{scenario}, nil, root)
	if err != nil || len(runs) != 1 || len(runs[0].Audio) != len(script) || runs[0].Audio[1][0] != 2 {
		t.Fatalf("audio-dir runs = %+v, %v", runs, err)
	}
	if _, err := RunSpecs([]probe.CustomerScenario{scenario}, nil, t.TempDir()); err == nil || !strings.Contains(err.Error(), "has no file for scenario") {
		t.Fatalf("empty audio-dir error = %v", err)
	}
	odd := writeTestFile(t, filepath.Join(t.TempDir(), "odd.pcm"), []byte{1}, 0o600)
	paths := []string{odd, odd, odd, odd}
	if _, err := RunSpecs([]probe.CustomerScenario{scenario}, paths, ""); err == nil || !strings.Contains(err.Error(), "even-length PCM16") {
		t.Fatalf("odd PCM error = %v", err)
	}
	if _, err := RunSpecs([]probe.CustomerScenario{scenario}, paths[:1], ""); err == nil || !strings.Contains(err.Error(), "exactly one file per selected customer turn") {
		t.Fatalf("turn count error = %v", err)
	}
	if _, err := RunSpecs([]probe.CustomerScenario{scenario}, paths, root); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("combined audio error = %v", err)
	}
}

func TestRequestAdmissionAndSelection(t *testing.T) {
	request := DefaultRequest()
	if err := request.validate(); err == nil || !strings.Contains(err.Error(), "--live") {
		t.Fatalf("missing --live error = %v", err)
	}
	request.Live = true
	request.MaxDuration = 0
	if err := request.validate(); err == nil || !strings.Contains(err.Error(), "--max-duration") {
		t.Fatalf("max duration error = %v", err)
	}
	request = DefaultRequest()
	request.Live, request.FrameDuration = true, 0
	if err := request.validate(); err == nil || !strings.Contains(err.Error(), "--frame-duration") {
		t.Fatalf("frame duration error = %v", err)
	}
	request.Required = true
	selectors, paths, err := request.selection(nil)
	if err != nil || strings.Join(selectors, ",") != "A,B,D-SIGINT,D-NATURAL" || len(paths) != 0 {
		t.Fatalf("required selection = %v %v %v", selectors, paths, err)
	}
	if _, _, err := request.selection([]string{"x.json"}); err == nil || !strings.Contains(err.Error(), "--required cannot be combined") {
		t.Fatalf("required conflict error = %v", err)
	}
	request.Required = false
	if _, _, err := request.selection(nil); err == nil || !strings.Contains(err.Error(), "no customer simulation selected") {
		t.Fatalf("empty selection error = %v", err)
	}
	if got := splitSelectors([]string{"A, B", " ", "C"}); strings.Join(got, "|") != "A|B|C" {
		t.Fatalf("split selectors = %v", got)
	}
}

func TestLoadScenariosRejectsDuplicatesAndUnreadableFiles(t *testing.T) {
	if _, err := loadScenarios(nil, []string{filepath.Join(t.TempDir(), "missing.json")}); err == nil || !strings.Contains(err.Error(), "read customer simulation scenario") {
		t.Fatalf("missing file error = %v", err)
	}
	malformed := writeTestFile(t, filepath.Join(t.TempDir(), "bad.json"), []byte(`{`), 0o600)
	if _, err := loadScenarios(nil, []string{malformed}); err == nil || !strings.Contains(err.Error(), "load customer simulation scenario") {
		t.Fatalf("malformed file error = %v", err)
	}
	scenarios, err := loadScenarios([]string{"A"}, nil)
	if err != nil || len(scenarios) != 1 {
		t.Fatalf("family A = %v, %v", scenarios, err)
	}
}

func TestReadCredentialPrefersEnvironmentThenSecretFile(t *testing.T) {
	home := t.TempDir()
	host := Host{HomeDir: func() (string, error) { return home, nil }}
	writeTestFile(t, filepath.Join(home, "secret"), []byte("file-key\r\n"), 0o600)
	t.Setenv("CUSTOMERSIM_TEST_BLANK", " \n")
	if key, err := host.readCredential("CUSTOMERSIM_TEST_BLANK", "~/secret"); err != nil || key != "file-key" {
		t.Fatalf("secret file credential = %q, %v", key, err)
	}
	t.Setenv("CUSTOMERSIM_TEST_KEY", "env-key\n")
	if key, err := host.readCredential("CUSTOMERSIM_TEST_KEY", "~/secret"); err != nil || key != "env-key" {
		t.Fatalf("environment credential = %q, %v", key, err)
	}
	if _, err := host.readCredential("CUSTOMERSIM_TEST_UNSET", "~/missing"); err == nil || !strings.Contains(err.Error(), "credentials are required") {
		t.Fatalf("missing credential error = %v", err)
	}
	if path, err := host.expandHome("~"); err != nil || path != home {
		t.Fatalf("expand ~ = %q, %v", path, err)
	}
	failing := Host{HomeDir: func() (string, error) { return "", errors.New("no home") }}
	if _, err := failing.expandHome("~/x"); err == nil || !strings.Contains(err.Error(), "resolve secret home") {
		t.Fatalf("home failure error = %v", err)
	}
	clearEnvironment([]string{"CUSTOMERSIM_TEST_KEY"})
	if _, ok := os.LookupEnv("CUSTOMERSIM_TEST_KEY"); ok {
		t.Fatal("credential environment remained set")
	}
}

func TestBinaryAndRunRootChecksUseInjectedWorkingDirectory(t *testing.T) {
	checkout := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatalf("create checkout marker: %v", err)
	}
	nested := filepath.Join(checkout, "agent-cli")
	host := Host{WorkingDir: func() (string, error) { return nested, nil }}
	if err := host.ensureRunRootOutsideCheckout(filepath.Join(checkout, "runs")); err == nil || !strings.Contains(err.Error(), "must be outside checkout") {
		t.Fatalf("inside run root error = %v", err)
	}
	if err := host.ensureRunRootOutsideCheckout(t.TempDir()); err != nil {
		t.Fatalf("outside run root error = %v", err)
	}
	binary := writeTestFile(t, filepath.Join(checkout, "bin", shippedBinaryName), []byte("#!/bin/sh\n"), 0o700)
	path, cleanup, err := host.locateBinary(context.Background(), "")
	cleanup()
	if err != nil || path != binary {
		t.Fatalf("located binary = %q, %v; want %q", path, err, binary)
	}
	notExecutable := writeTestFile(t, filepath.Join(t.TempDir(), "yui"), []byte("x"), 0o600)
	if _, _, err := host.locateBinary(context.Background(), notExecutable); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("non-executable error = %v", err)
	}
	if _, err := validateBinary(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory binary error = %v", err)
	}
}

func TestReportRedactsSecretsAndValidatesResults(t *testing.T) {
	scenario := probe.NewFamilyAScenario()
	result := probe.CustomerSimulationSuiteResult{Root: "/root", Runs: []probe.CustomerSimulationRunResult{{
		RunID: "run-1", ScenarioID: scenario.ID, Family: scenario.Family, Termination: scenario.Termination,
		BundleRoot: "/b", RecordRoot: "/r", WorkspaceRoot: "/w", Error: "leaked sk-secret",
		Mechanical: probe.MechanicalVerdict{Pass: true},
		Validator:  probe.CustomerSimulationValidatorResult{Status: probe.ValidatorStatusWorked, Accepted: true, Mechanical: probe.MechanicalVerdict{Pass: true}, Verdict: probe.ValidatorVerdict{Verdict: probe.ValidatorWorked}},
	}}}
	data, err := EncodeReport(result, "sk-secret", " ")
	if err != nil || bytes.Contains(data, []byte("sk-secret")) || !bytes.Contains(data, []byte(redactedSecret)) {
		t.Fatalf("report = %s, %v", data, err)
	}
	var out bytes.Buffer
	if err := WriteReport(&out, "", data); err != nil || out.Len() != len(data) {
		t.Fatalf("stdout report = %d bytes, %v", out.Len(), err)
	}
	reportPath := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := WriteReport(nil, reportPath, data); err != nil {
		t.Fatalf("file report: %v", err)
	}
	if err := ValidateResult(result, []probe.CustomerScenario{scenario}); err != nil || WorkedCount(result) != 1 {
		t.Fatalf("passing result = %v (worked %d)", err, WorkedCount(result))
	}
	broken := result
	broken.Root = ""
	broken.Runs = append(append([]probe.CustomerSimulationRunResult(nil), result.Runs...), probe.CustomerSimulationRunResult{ScenarioID: scenario.ID}, probe.CustomerSimulationRunResult{ScenarioID: "unexpected"})
	err = ValidateResult(broken, []probe.CustomerScenario{scenario, probe.NewFamilyBScenario()})
	for _, want := range []string{"no evidence root", "returned 3 run results", "appears more than once", "no run ID", "unexpected scenario", "contradictory", "incomplete", "status missing", "has no result"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("broken result error = %v, want %q", err, want)
		}
	}
}

func TestRunInvokesInjectedRunnerWithResolvedInputs(t *testing.T) {
	temp := t.TempDir()
	binary := writeTestFile(t, filepath.Join(temp, "agent"), []byte("#!/bin/sh\n"), 0o700)
	audio := writeTestFile(t, filepath.Join(temp, "turn.pcm"), []byte{1, 0}, 0o600)
	t.Setenv("CUSTOMERSIM_RUN_KEY", "run-key")
	request := DefaultRequest()
	request.Live, request.Families, request.BinaryPath = true, []string{"A"}, binary
	request.AudioPaths = []string{audio, audio, audio, audio}
	request.APIKeyEnv, request.ValidatorAPIKeyEnv = "CUSTOMERSIM_RUN_KEY", "CUSTOMERSIM_RUN_KEY"
	var received probe.CustomerSimulationSuiteOptions
	runnerErr := errors.New("suite failed")
	outcome, err := Run(context.Background(), request, nil, Dependencies{
		Host:      Host{WorkingDir: func() (string, error) { return temp, nil }},
		Validator: probe.CustomerSimulationValidatorAgentFunc(func(context.Context, probe.CustomerSimulationValidatorRequest) ([]byte, error) { return nil, nil }),
		Runner: func(_ context.Context, options probe.CustomerSimulationSuiteOptions) (probe.CustomerSimulationSuiteResult, error) {
			received = options
			return probe.CustomerSimulationSuiteResult{}, runnerErr
		},
	})
	if err != nil || received.APIKey != "run-key" || received.BinaryPath != binary || len(received.Runs) != 1 {
		t.Fatalf("run = %+v, %v; received %+v", outcome, err, received)
	}
	if !errors.Is(outcome.Err(), runnerErr) || strings.Join(outcome.Secrets, ",") != "run-key,run-key" {
		t.Fatalf("outcome err = %v secrets = %v", outcome.Err(), outcome.Secrets)
	}
	if _, ok := os.LookupEnv("CUSTOMERSIM_RUN_KEY"); ok {
		t.Fatal("credential environment remained set after the run")
	}
	if _, err := buildValidator(context.Background(), "grok", "m", "", "k"); err == nil || !strings.Contains(err.Error(), "stateless provider") {
		t.Fatalf("grok validator error = %v", err)
	}
	if _, err := buildValidator(context.Background(), "nope", "m", "", "k"); err == nil || !strings.Contains(err.Error(), "unsupported --validator-provider") {
		t.Fatalf("unknown validator error = %v", err)
	}
}
