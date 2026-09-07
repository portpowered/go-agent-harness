package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

// TestShippedSessionProcessFamilyBCorrection drives one persistent shipped
// session through an original tool action, an output-time correction, and an
// independently verified replacement action. The correction audio is gated
// on streamed output bytes while the original response is intentionally left
// open, so the test proves the interruption at the public PCM boundary.
func TestShippedSessionProcessFamilyBCorrection(t *testing.T) {
	scenario := loadFamilyBScenario(t)
	fixture := newFamilyBProviderFixture(scenario)
	defer fixture.Close()

	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(sandbox, 0o700); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	oracle, err := probe.NewFilesystemOracle(sandbox)
	if err != nil {
		t.Fatalf("NewFilesystemOracle: %v", err)
	}
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("tools:\n  exec:\n    enable_deny_patterns: true\n"), 0o600); err != nil {
		t.Fatalf("write hermetic session config: %v", err)
	}

	startedAt := time.Now()
	fixture.SetStartedAt(startedAt)
	var checkpointMu sync.Mutex
	checkpoints := make([]probe.FilesystemCheckpoint, 0, 2)
	captureOriginal := func(ctx context.Context, _ *probe.DuplexProgress) error {
		checkpoint, checkpointErr := oracle.Checkpoint(
			"checkpoint-original",
			probe.FamilyBOriginalActionID,
			time.Since(startedAt),
			scenario.Actions[0].Oracle.Checkpoints,
		)
		checkpointMu.Lock()
		checkpoints = append(checkpoints, checkpoint)
		checkpointMu.Unlock()
		return checkpointErr
	}

	result, runErr := probe.RunDuplexSession(context.Background(), probe.DuplexSessionConfig{
		BinaryPath:       buildAgentBinary(t),
		RecordDir:        filepath.Join(t.TempDir(), "record"),
		WorkingDirectory: sandbox,
		ConfigDir:        configDir,
		Provider:         "openai",
		Model:            "gpt-realtime",
		BaseURL:          fixture.WebSocketURL(),
		APIKey:           "hermetic-key",
		SystemPrompt:     scenario.TextSeed,
		MaxDuration:      scenario.Deadline,
		FrameDuration:    5 * time.Millisecond,
		AdditionalArgs:   []string{"--wait-for-close"},
		Segments: []probe.DuplexAudioSegment{
			{ID: "original-request", PCM16: familyBFrame(1)},
			{ID: "correction-request", PCM16: familyBFrame(2), WaitForOutputSequence: []byte{1, 0x42, 0x52, 0x42}, Before: captureOriginal},
			{ID: "correction-silence", SilenceFor: 5 * time.Millisecond},
		},
	})
	observation := fixture.Snapshot()
	if runErr != nil || observation.ProtocolError != "" {
		t.Fatalf("Family B shipped-process run failed: run=%v provider=%+v\nresult=%+v\nstdout=%x\nstderr=%s", runErr, observation, result, result.Stdout, result.Stderr)
	}

	if observation.ConnectionCount != 1 || observation.SessionUpdates != 1 {
		t.Fatalf("provider lifecycle = connections:%d session_updates:%d, want one open session and one update", observation.ConnectionCount, observation.SessionUpdates)
	}
	if len(observation.CustomerTranscript) != 2 || observation.CustomerTranscript[1].At <= observation.CustomerTranscript[0].At {
		t.Fatalf("customer correction transcript = %+v, want two ordered turns", observation.CustomerTranscript)
	}
	if len(observation.FunctionCalls) != 2 || observation.FunctionCalls[0].ActionID != probe.FamilyBOriginalActionID || observation.FunctionCalls[1].ActionID != probe.FamilyBReplacementActionID {
		t.Fatalf("provider function calls = %+v, want original then replacement", observation.FunctionCalls)
	}
	if len(observation.ToolObservations) != 2 || observation.ToolObservations[0].Status != "completed" || observation.ToolObservations[1].Status != "completed" {
		t.Fatalf("tool observations = %+v, want two completed results", observation.ToolObservations)
	}
	if got, want := strings.Join(observation.ResponseTerminalStatuses, ","), "cancelled,completed"; got != want {
		t.Fatalf("response terminal statuses = %q, want %q", got, want)
	}
	correction := observation.Correction
	if correction.OriginalResponseStatus != "cancelled" || correction.ReplacementResponseStatus != "completed" {
		t.Fatalf("correction response statuses = %+v, want cancelled then completed", correction)
	}
	if !(correction.OriginalResponseStartedAt < correction.CorrectionStartedAt && correction.CorrectionStartedAt < correction.OriginalResponseEndedAt) {
		t.Fatalf("correction timing = %+v, want correction inside original output interval", correction)
	}
	if !(correction.CancellationSentAt < correction.CorrectionStartedAt) {
		t.Fatalf("cancellation timing = %+v, want cancellation before correction audio reaches provider", correction)
	}

	if result.ExitCode != 0 || !result.ChildWaited || !result.InputFinished || !result.InputClosed || !result.StdoutClosed || !result.StderrClosed {
		t.Fatalf("process lifecycle result = %+v, want fully reaped normal run", result)
	}
	if len(result.Input) < 3 || len(result.Output) < 2 {
		t.Fatalf("stream evidence input=%d output_reads=%d, want correction on one open paced stream", len(result.Input), len(result.Output))
	}
	for marker := byte(1); marker <= 2; marker++ {
		if !bytes.Contains(result.Stdout, []byte{marker, 0x42, 0x52, 0x42}) {
			t.Fatalf("captured stdout = %x, missing response audio marker %x", result.Stdout, []byte{marker, 0x42, 0x52, 0x42})
		}
	}
	if strings.Contains(result.Command, "hermetic-key") || strings.Contains(strings.Join(result.SanitizedArgs, "\x00"), "hermetic-key") {
		t.Fatalf("API key leaked into process evidence: command=%q args=%q", result.Command, result.SanitizedArgs)
	}
	for _, forbidden := range []string{"--audio-in-turn", "--api-key"} {
		if containsIntegrationString(result.SanitizedArgs, forbidden) {
			t.Fatalf("runner unexpectedly used forbidden boundary/credential argument %q: %v", forbidden, result.SanitizedArgs)
		}
	}

	finalCheckpoint, err := oracle.Checkpoint(
		"checkpoint-replacement",
		probe.FamilyBReplacementActionID,
		time.Since(startedAt),
		scenario.Actions[1].Oracle.Checkpoints,
	)
	if err != nil {
		t.Fatalf("capture replacement checkpoint: %v", err)
	}
	checkpointMu.Lock()
	checkpoints = append(checkpoints, finalCheckpoint)
	checkpointCopy := append([]probe.FilesystemCheckpoint(nil), checkpoints...)
	checkpointMu.Unlock()
	if len(checkpointCopy) != 2 {
		t.Fatalf("filesystem checkpoints = %d, want original and replacement boundaries", len(checkpointCopy))
	}

	actionResults := []probe.ActionResult{
		{
			ActionID:           probe.FamilyBOriginalActionID,
			TurnID:             "turn-1",
			Confirmed:          true,
			ConfirmedAt:        observation.ProductTranscript[0].At,
			Disposition:        probe.DispositionCancelled,
			OutcomeReason:      "correction interrupted the original action after the draft write; preserved draft state was recorded",
			EvidenceRefs:       []string{"filesystem-checkpoints.jsonl", "tool-observations.jsonl", "transcripts/product.jsonl"},
			CheckpointIDs:      []string{"checkpoint-original"},
			ToolObservationIDs: []string{observation.ToolObservations[0].ID},
		},
		{
			ActionID:           probe.FamilyBReplacementActionID,
			TurnID:             "turn-2",
			Confirmed:          true,
			ConfirmedAt:        observation.ProductTranscript[1].At,
			Disposition:        probe.DispositionCompleted,
			EvidenceRefs:       []string{"filesystem-checkpoints.jsonl", "tool-observations.jsonl", "transcripts/product.jsonl"},
			CheckpointIDs:      []string{"checkpoint-replacement"},
			ToolObservationIDs: []string{observation.ToolObservations[1].ID},
		},
	}
	process := &probe.ProcessFacts{
		PID:                result.PID,
		ExitCode:           result.ExitCode,
		ExitClassification: "normal",
		ChildWaited:        result.ChildWaited,
		InputClosed:        result.InputClosed,
		OutputClosed:       result.StdoutClosed && result.StderrClosed,
		StartedAt:          0,
		EndedAt:            result.Duration,
	}
	correction.Process = process
	mechanical, err := probe.EvaluateCustomerSimulationCorrection(
		scenario,
		actionResults,
		checkpointCopy,
		observation.ToolObservations,
		observation.ProductTranscript,
		correction,
	)
	if err != nil {
		t.Fatalf("Family B mechanical evaluation: %v", err)
	}
	if !mechanical.Pass || len(mechanical.Findings) != 0 {
		t.Fatalf("Family B mechanical verdict = %+v, want pass without findings", mechanical)
	}
}

// TestRunCustomerSimulationSuiteFamilyBUsesRecordedCorrectionBoundaries is
// the production-path regression for the suite builder. It intentionally
// runs through RunCustomerSimulationSuite rather than assembling the
// correction ledger in the test, so response IDs, raw record timestamps, the
// input gate, and the finalized validator bundle are all exercised together.
func TestRunCustomerSimulationSuiteFamilyBUsesRecordedCorrectionBoundaries(t *testing.T) {
	scenario := loadFamilyBScenario(t)
	fixture := newFamilyBProviderFixture(scenario)
	defer fixture.Close()
	fixture.SetStartedAt(time.Now())

	validator := probe.CustomerSimulationValidatorAgentFunc(func(_ context.Context, request probe.CustomerSimulationValidatorRequest) ([]byte, error) {
		if !request.Input.Mechanical.Pass {
			return nil, fmt.Errorf("production Family B mechanical verdict failed: %+v", request.Input.Mechanical)
		}
		return json.Marshal(probe.ValidatorVerdict{
			Verdict:      probe.ValidatorWorked,
			Summary:      "The correction interrupted the recorded original response and the replacement was independently completed.",
			EvidenceRefs: append([]string(nil), request.Input.EvidenceRefs...),
		})
	})

	script := probe.FamilyBSpokenScript()
	result, runErr := probe.RunCustomerSimulationSuite(context.Background(), probe.CustomerSimulationSuiteOptions{
		BinaryPath: buildAgentBinary(t), RunRoot: filepath.Join(t.TempDir(), "runs"), Provider: "openai", Model: "gpt-realtime",
		BaseURL: fixture.WebSocketURL(), APIKey: "hermetic-key", SystemPrompt: scenario.TextSeed,
		Runs:      []probe.CustomerSimulationRunSpec{{Scenario: scenario, Script: script, Audio: [][]byte{familyBFrame(1), familyBFrame(2)}}},
		Validator: validator, MaxDuration: scenario.Deadline, FrameDuration: 5 * time.Millisecond, SilenceDuration: 5 * time.Millisecond, ShutdownGrace: time.Second,
	})
	if runErr != nil {
		t.Fatalf("RunCustomerSimulationSuite: %v\nprovider=%+v", runErr, fixture.Snapshot())
	}
	if len(result.Runs) != 1 {
		t.Fatalf("suite runs = %d, want 1", len(result.Runs))
	}
	run := result.Runs[0]
	if !run.Mechanical.Pass || !run.Validator.Pass() {
		t.Fatalf("suite Family B result = %+v, want mechanical and validator pass", run)
	}

	data, err := os.ReadFile(filepath.Join(run.BundleRoot, "events", "correction.json"))
	if err != nil {
		t.Fatalf("read correction evidence: %v", err)
	}
	var correction probe.CorrectionEvidence
	if err := json.Unmarshal(data, &correction); err != nil {
		t.Fatalf("decode correction evidence: %v", err)
	}
	if correction.OriginalResponseID != "response-original-output" {
		t.Fatalf("original response ID = %q, want recorded active response ID", correction.OriginalResponseID)
	}
	if correction.OriginalResponseStatus != "cancelled" || correction.ReplacementResponseStatus != "completed" {
		t.Fatalf("response statuses = %q/%q, want cancelled/completed", correction.OriginalResponseStatus, correction.ReplacementResponseStatus)
	}
	if !(correction.OriginalResponseStartedAt < correction.CorrectionStartedAt && correction.CorrectionStartedAt < correction.OriginalResponseEndedAt) {
		t.Fatalf("correction timing = %+v, want correction inside original response interval", correction)
	}
	if !(correction.CancellationSentAt < correction.CorrectionStartedAt) {
		t.Fatalf("cancellation timing = %+v, want cancellation before correction input", correction)
	}
	if _, err := probe.VerifyCustomerEvidenceBundle(run.BundleRoot); err != nil {
		t.Fatalf("VerifyCustomerEvidenceBundle(%q): %v", run.BundleRoot, err)
	}
}

func loadFamilyBScenario(t *testing.T) probe.CustomerScenario {
	t.Helper()
	path := filepath.Join(agentCLIRoot(t), "testdata", "customer-simulation", "family-b.scenario.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Family B scenario: %v", err)
	}
	scenario, err := probe.ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("parse Family B scenario: %v", err)
	}
	return scenario
}
