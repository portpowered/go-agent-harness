package probe

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

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestCustomerSimulationScenariosForSelectorsExpandsDAndRejectsDuplicates(t *testing.T) {
	scenarios, err := CustomerSimulationScenariosForSelectors("A", "D")
	if err != nil {
		t.Fatalf("CustomerSimulationScenariosForSelectors: %v", err)
	}
	if len(scenarios) != 3 || scenarios[0].ID != FamilyAScenarioID || scenarios[1].ID != FamilyDScenarioSIGINTID || scenarios[2].ID != FamilyDScenarioNaturalID {
		t.Fatalf("selected scenarios = %v, want A, D-SIGINT, D-natural", []string{scenarios[0].ID, scenarios[1].ID, scenarios[2].ID})
	}
	if _, err := CustomerSimulationScenariosForSelectors("A", "family-a"); !errors.Is(err, ErrCustomerSimulationSelection) {
		t.Fatalf("duplicate selector error = %v, want ErrCustomerSimulationSelection", err)
	}
	if _, err := CustomerSimulationScenariosForSelectors("unknown"); !errors.Is(err, ErrCustomerSimulationSelection) {
		t.Fatalf("unknown selector error = %v, want ErrCustomerSimulationSelection", err)
	}
}

func TestCustomerSimulationScenarioScriptUsesVisibleWordingForBuiltInsAndCustoms(t *testing.T) {
	for _, scenario := range BuiltInCustomerSimulationScenarios() {
		script := CustomerSimulationScenarioScript(scenario)
		if len(script) != len(scenario.Actions) {
			t.Fatalf("%s script length = %d, actions = %d", scenario.ID, len(script), len(scenario.Actions))
		}
		for index, turn := range script {
			if turn.ActionID != scenario.Actions[index].ID || strings.TrimSpace(turn.Text) == "" {
				t.Fatalf("%s script[%d] = %+v, want visible action wording", scenario.ID, index, turn)
			}
		}
	}
}

func TestRunCustomerSimulationSuiteLeavesTypedBrokenBundleWithoutProductRecord(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "child.sh")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nwhile IFS= read -r -n 1 byte; do :; done\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write child: %v", err)
	}
	scenario := NewFamilyAScenario()
	script := FamilyASpokenScript()
	audio := make([][]byte, len(script))
	for index := range audio {
		audio[index] = []byte{byte(index + 1), 0}
	}
	validator := CustomerSimulationValidatorAgentFunc(func(_ context.Context, request CustomerSimulationValidatorRequest) ([]byte, error) {
		return json.Marshal(ValidatorVerdict{Verdict: ValidatorBroken, FirstFailingTurn: "turn-1", Behavior: "the fake child exited before producing product evidence", Violation: "the session did not finish", EvidenceRefs: []string{"scenario.json", "process.json"}, CustomerImpact: "the customer received no verified response"})
	})
	result, err := RunCustomerSimulationSuite(context.Background(), CustomerSimulationSuiteOptions{
		BinaryPath: binaryPath, RunRoot: t.TempDir(), Provider: "openai", Model: "gpt-realtime", APIKey: "test-key", Runs: []CustomerSimulationRunSpec{{Scenario: scenario, Script: script, Audio: audio}},
		Validator: validator, MaxDuration: time.Second, FrameDuration: time.Millisecond, SilenceDuration: 0, ShutdownGrace: 100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("RunCustomerSimulationSuite error = nil, want failed child/non-passing run")
	}
	if len(result.Runs) != 1 {
		t.Fatalf("run count = %d, want 1", len(result.Runs))
	}
	run := result.Runs[0]
	if run.Validator.Verdict.Verdict != ValidatorBroken || run.Validator.Pass() {
		t.Fatalf("validator result = %+v, want structured BROKEN", run.Validator)
	}
	if _, err := VerifyCustomerEvidenceBundle(run.BundleRoot); err != nil {
		t.Fatalf("VerifyCustomerEvidenceBundle(%q): %v", run.BundleRoot, err)
	}
	if run.Error == "" || !strings.Contains(run.Error, "child") && !strings.Contains(run.Error, "validator") {
		t.Fatalf("run error = %q, want bounded non-secret diagnosis", run.Error)
	}
	if strings.Contains(run.Error, "test-key") {
		t.Fatalf("run error leaked API key: %q", run.Error)
	}
}

func TestRunCustomerSimulationSuiteLeavesTypedBrokenTerminationBundles(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "child.sh")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nwhile IFS= read -r -n 1 byte; do :; done\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write child: %v", err)
	}
	validator := CustomerSimulationValidatorAgentFunc(func(_ context.Context, request CustomerSimulationValidatorRequest) ([]byte, error) {
		return json.Marshal(ValidatorVerdict{
			Verdict: ValidatorBroken, FirstFailingTurn: request.Input.Scenario.Actions[0].ID,
			Behavior: "the fake child exited before producing product evidence", Violation: "the session did not finish",
			EvidenceRefs: []string{"scenario.json", "process.json"}, CustomerImpact: "the customer received no verified response",
		})
	})
	for _, scenario := range []CustomerScenario{
		NewFamilyBScenario(),
		NewFamilyDScenario(TerminationSIGINT),
		NewFamilyDScenario(TerminationNatural),
	} {
		t.Run(scenario.ID, func(t *testing.T) {
			script := CustomerSimulationScenarioScript(scenario)
			audio := make([][]byte, len(script))
			for index := range audio {
				audio[index] = []byte{byte(index + 1), 0}
			}
			result, err := RunCustomerSimulationSuite(context.Background(), CustomerSimulationSuiteOptions{
				BinaryPath: binaryPath, RunRoot: filepath.Join(t.TempDir(), "runs"), Provider: "openai", Model: "gpt-realtime", APIKey: "test-key",
				Runs: []CustomerSimulationRunSpec{{Scenario: scenario, Script: script, Audio: audio}}, Validator: validator,
				MaxDuration: time.Second, FrameDuration: time.Millisecond, SilenceDuration: 0, ShutdownGrace: 100 * time.Millisecond,
			})
			if err == nil {
				t.Fatal("RunCustomerSimulationSuite error = nil, want failed child/non-passing run")
			}
			if len(result.Runs) != 1 {
				t.Fatalf("run count = %d, want 1", len(result.Runs))
			}
			run := result.Runs[0]
			if run.Validator.Verdict.Verdict != ValidatorBroken || run.Validator.Pass() {
				t.Fatalf("validator result = %+v, want structured BROKEN", run.Validator)
			}
			if _, err := VerifyCustomerEvidenceBundle(run.BundleRoot); err != nil {
				t.Fatalf("VerifyCustomerEvidenceBundle(%q): %v", run.BundleRoot, err)
			}
			if strings.Contains(run.Error, "test-key") {
				t.Fatalf("run error leaked API key: %q", run.Error)
			}
		})
	}
}

func TestReadCustomerSimulationStreamCorrelatesCompleteToolMessage(t *testing.T) {
	root := t.TempDir()
	base := time.Unix(0, 0)
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ActorID: messages.Model, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ActorID: messages.Model, Value: messages.NewToolCallEndValue("call-1", "write_file", `{}`)},
		{Type: messages.StreamTypeSystemFullMessage, ActorID: messages.Tool, Value: messages.NewInferenceResultValue("tool", messages.Message{Role: messages.RoleTool, ToolCallID: "call-1", Name: "write_file", ContentParts: []messages.ContentPart{messages.NewTextPart("written")}})},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ActorID: messages.Model, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	var data bytes.Buffer
	for index, event := range events {
		payload, err := gatewaytesting.MarshalStreamMessage(event)
		if err != nil {
			t.Fatalf("MarshalStreamMessage(%d): %v", index, err)
		}
		encoded, err := transcript.Encode(transcript.NewRecord(uint64(index+1), base.Add(time.Duration(index)*time.Second), transcript.PeerAgent, transcript.DirectionOut, transcript.StreamWS, payload))
		if err != nil {
			t.Fatalf("Encode(%d): %v", index, err)
		}
		data.Write(encoded)
	}
	if err := os.WriteFile(filepath.Join(root, "agent.transcript.jsonl"), data.Bytes(), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	facts, err := readCustomerSimulationStream(root, NewFamilyAScenario(), 0)
	if err != nil {
		t.Fatalf("readCustomerSimulationStream: %v", err)
	}
	if len(facts.tools) != 1 {
		t.Fatalf("tool observations = %d, want 1", len(facts.tools))
	}
	if !facts.tools[0].ResultSeen || facts.tools[0].Status != "completed" || facts.tools[0].Duration <= 0 {
		t.Fatalf("tool observation = %+v, want completed result with positive duration", facts.tools[0])
	}
}
