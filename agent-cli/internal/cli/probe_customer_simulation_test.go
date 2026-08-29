package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

func TestCustomerSimulationCommandRequiresExplicitLiveOptIn(t *testing.T) {
	command := NewCustomerSimulationCommand(flags.NewGlobalFlags())
	if command.Model != "gpt-realtime-2.1-mini" {
		t.Fatalf("default realtime model = %q, want cost-bounded gpt-realtime-2.1-mini", command.Model)
	}
	root := command.Generate()
	root.SetArgs([]string{"--family", "A"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--live") {
		t.Fatalf("Execute error = %v, want explicit --live guidance", err)
	}
}

func TestCustomerSimulationRunSpecsLoadsFamilyERepromptAudioSeparately(t *testing.T) {
	scenario := probe.NewFamilyEScenario()
	temp := t.TempDir()
	turnPath := filepath.Join(temp, "turn.pcm")
	repromptPath := filepath.Join(temp, "check-in.pcm")
	if err := os.WriteFile(turnPath, []byte{1, 0, 2, 0}, 0o600); err != nil {
		t.Fatalf("write turn audio: %v", err)
	}
	if err := os.WriteFile(repromptPath, []byte{3, 0, 4, 0}, 0o600); err != nil {
		t.Fatalf("write re-prompt audio: %v", err)
	}

	runs, err := customerSimulationRunSpecs([]probe.CustomerScenario{scenario}, []string{turnPath}, "", repromptPath)
	if err != nil {
		t.Fatalf("customerSimulationRunSpecs: %v", err)
	}
	if len(runs) != 1 || len(runs[0].Audio) != 1 || len(runs[0].PatienceRepromptAudio) != 4 {
		t.Fatalf("Family E run audio = %+v, want one action recording plus separate four-byte re-prompt", runs)
	}
	if string(runs[0].PatienceRepromptAudio) != string([]byte{3, 0, 4, 0}) {
		t.Fatalf("Family E re-prompt audio = %x, want 03000400", runs[0].PatienceRepromptAudio)
	}
}

func TestCustomerSimulationRunSpecsRejectsMissingOrMisplacedFamilyERepromptAudio(t *testing.T) {
	temp := t.TempDir()
	audioPath := filepath.Join(temp, "turn.pcm")
	if err := os.WriteFile(audioPath, []byte{1, 0}, 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if _, err := customerSimulationRunSpecs([]probe.CustomerScenario{probe.NewFamilyEScenario()}, []string{audioPath}, ""); err == nil || !strings.Contains(err.Error(), "patience-reprompt-audio") {
		t.Fatalf("missing Family E re-prompt error = %v, want explicit flag guidance", err)
	}
	if _, err := customerSimulationRunSpecs([]probe.CustomerScenario{probe.NewFamilyAScenario()}, make([]string, 4), "", audioPath); err == nil || !strings.Contains(err.Error(), "only valid") {
		t.Fatalf("misplaced Family E re-prompt error = %v, want selection-specific failure", err)
	}
}

func TestCustomerSimulationCommandResolvesAudioAndKeepsCredentialOutOfReport(t *testing.T) {
	temp := t.TempDir()
	binaryPath := filepath.Join(temp, "agent")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	audioPath := filepath.Join(temp, "turn.pcm")
	if err := os.WriteFile(audioPath, []byte{1, 0, 2, 0}, 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	reportPath := filepath.Join(temp, "report.json")
	const envName = "CUSTOMER_SIMULATION_TEST_KEY"
	const secret = "customer-simulation-test-secret"
	t.Setenv(envName, secret)

	var received probe.CustomerSimulationSuiteOptions
	command := NewCustomerSimulationCommand(flags.NewGlobalFlags())
	command.SetValidator(probe.CustomerSimulationValidatorAgentFunc(func(_ context.Context, _ probe.CustomerSimulationValidatorRequest) ([]byte, error) {
		return json.Marshal(probe.ValidatorVerdict{Verdict: probe.ValidatorBroken, FirstFailingTurn: "turn-1", Behavior: "test", Violation: "test", EvidenceRefs: []string{"scenario.json"}, CustomerImpact: "test"})
	}))
	command.SetRunner(func(_ context.Context, options probe.CustomerSimulationSuiteOptions) (probe.CustomerSimulationSuiteResult, error) {
		received = options
		return probe.CustomerSimulationSuiteResult{
			Root: "/tmp/customer-simulation-test-root",
			Runs: []probe.CustomerSimulationRunResult{{
				RunID: "family-a-iterative-project-001", ScenarioID: probe.FamilyAScenarioID, Family: probe.ScenarioFamilyA, Termination: probe.TerminationNatural,
				BundleRoot: "/tmp/customer-simulation-test-root/evidence", RecordRoot: "/tmp/customer-simulation-test-root/record", WorkspaceRoot: "/tmp/customer-simulation-test-root/workspace",
				Mechanical: probe.MechanicalVerdict{Pass: true},
				Validator:  probe.CustomerSimulationValidatorResult{Status: probe.ValidatorStatusWorked, Accepted: true, Mechanical: probe.MechanicalVerdict{Pass: true}, Verdict: probe.ValidatorVerdict{Verdict: probe.ValidatorWorked}},
				Error:      secret,
			}},
		}, nil
	})
	root := command.Generate()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"--live", "--family", "A", "--binary", binaryPath,
		"--audio", audioPath, "--audio", audioPath, "--audio", audioPath, "--audio", audioPath,
		"--api-key-env", envName, "--validator-api-key-env", envName,
		"--secret-file", filepath.Join(temp, "missing-secret"), "--validator-secret-file", filepath.Join(temp, "missing-validator-secret"),
		"--report", reportPath,
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if received.APIKey != secret {
		t.Fatalf("runner API key = %q, want in-memory credential", received.APIKey)
	}
	if len(received.Runs) != 1 || len(received.Runs[0].Audio) != 4 {
		t.Fatalf("runner selection = %d runs/%d turns, want one A run with four turns", len(received.Runs), len(received.Runs[0].Audio))
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("report leaked credential: %q", data)
	}
	if _, ok := os.LookupEnv(envName); ok {
		t.Fatalf("credential environment %s remained set after command", envName)
	}
}

func TestCustomerSimulationCommandRejectsNonPassingResultWithNilRunnerError(t *testing.T) {
	temp := t.TempDir()
	binaryPath := filepath.Join(temp, "agent")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	audioPath := filepath.Join(temp, "turn.pcm")
	if err := os.WriteFile(audioPath, []byte{1, 0, 2, 0}, 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	const envName = "CUSTOMER_SIMULATION_NONPASSING_KEY"
	t.Setenv(envName, "test-key")
	// Keep the test hermetic even when the custom flag is not the first
	// credential source selected by a command implementation under test.
	t.Setenv(defaultCustomerSimulationAPIKeyEnv, "test-key")

	command := NewCustomerSimulationCommand(flags.NewGlobalFlags())
	command.SetValidator(probe.CustomerSimulationValidatorAgentFunc(func(_ context.Context, _ probe.CustomerSimulationValidatorRequest) ([]byte, error) {
		return nil, nil
	}))
	command.SetRunner(func(_ context.Context, options probe.CustomerSimulationSuiteOptions) (probe.CustomerSimulationSuiteResult, error) {
		return probe.CustomerSimulationSuiteResult{
			Root: "/tmp/customer-simulation-test-root",
			Runs: []probe.CustomerSimulationRunResult{{
				RunID: "family-a-iterative-project-001", ScenarioID: probe.FamilyAScenarioID, Family: probe.ScenarioFamilyA, Termination: probe.TerminationNatural,
				BundleRoot: "/tmp/customer-simulation-test-root/evidence", RecordRoot: "/tmp/customer-simulation-test-root/record", WorkspaceRoot: "/tmp/customer-simulation-test-root/workspace",
				Mechanical: probe.MechanicalVerdict{Pass: false},
				Validator:  probe.CustomerSimulationValidatorResult{Status: probe.ValidatorStatusBroken, Verdict: probe.ValidatorVerdict{Verdict: probe.ValidatorBroken}},
			}},
		}, nil
	})
	root := command.Generate()
	root.SetArgs([]string{
		"--live", "--family", "A", "--binary", binaryPath,
		"--audio", audioPath, "--audio", audioPath, "--audio", audioPath, "--audio", audioPath,
		"--api-key-env", envName, "--secret-file", filepath.Join(temp, "missing-secret"),
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "accepted WORKED") {
		t.Fatalf("Execute error = %v, want non-passing result failure", err)
	}
}

func TestCustomerSimulationCommandCleansValidatorCredentialOnPrimaryFailure(t *testing.T) {
	const primaryEnv = "CUSTOMER_SIMULATION_PRIMARY_MISSING_KEY"
	const validatorEnv = "CUSTOMER_SIMULATION_VALIDATOR_LEFTOVER_KEY"
	_ = os.Unsetenv(primaryEnv)
	t.Setenv(validatorEnv, "validator-secret")

	command := NewCustomerSimulationCommand(flags.NewGlobalFlags())
	root := command.Generate()
	root.SetArgs([]string{
		"--live", "--family", "A", "--binary", os.Args[0],
		"--api-key-env", primaryEnv, "--validator-api-key-env", validatorEnv,
		"--secret-file", filepath.Join(t.TempDir(), "missing-primary"),
		"--validator-secret-file", filepath.Join(t.TempDir(), "missing-validator"),
	})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "credentials are required") {
		t.Fatalf("Execute error = %v, want missing-primary-credential failure", err)
	}
	if _, ok := os.LookupEnv(validatorEnv); ok {
		t.Fatalf("validator credential environment %s remained set after early failure", validatorEnv)
	}
}

func TestCustomerSimulationCommandRejectsMissingAudioInsteadOfSkipping(t *testing.T) {
	t.Setenv(defaultCustomerSimulationAPIKeyEnv, "test-key")
	command := NewCustomerSimulationCommand(flags.NewGlobalFlags())
	command.SetRunner(func(_ context.Context, _ probe.CustomerSimulationSuiteOptions) (probe.CustomerSimulationSuiteResult, error) {
		t.Fatal("runner called despite missing audio")
		return probe.CustomerSimulationSuiteResult{}, nil
	})
	command.SetValidator(probe.CustomerSimulationValidatorAgentFunc(func(_ context.Context, _ probe.CustomerSimulationValidatorRequest) ([]byte, error) {
		return nil, nil
	}))
	os.Setenv("CUSTOMER_SIMULATION_MISSING_AUDIO_KEY", "test-key")
	t.Cleanup(func() { _ = os.Unsetenv("CUSTOMER_SIMULATION_MISSING_AUDIO_KEY") })
	root := command.Generate()
	root.SetArgs([]string{"--live", "--family", "A", "--binary", os.Args[0], "--api-key-env", "CUSTOMER_SIMULATION_MISSING_AUDIO_KEY", "--secret-file", filepath.Join(t.TempDir(), "missing")})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "audio is required") {
		t.Fatalf("Execute error = %v, want missing-audio failure", err)
	}
}
