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
	if run.Error == "" || !strings.Contains(run.Error, "child") && !strings.Contains(run.Error, "validator") && !strings.Contains(run.Error, "deadline") {
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

func TestReadCustomerSimulationStreamUsesRecordedCorrectionBoundaries(t *testing.T) {
	root := t.TempDir()
	base := time.Unix(0, 0)
	var data bytes.Buffer
	sequence := uint64(0)
	write := func(at time.Duration, direction transcript.Direction, message messages.StreamMessage) {
		t.Helper()
		payload, err := gatewaytesting.MarshalStreamMessage(message)
		if err != nil {
			t.Fatalf("MarshalStreamMessage(%s): %v", message.Type, err)
		}
		sequence++
		encoded, err := transcript.Encode(transcript.NewRecord(sequence, base.Add(at), transcript.PeerAgent, direction, transcript.StreamWS, payload))
		if err != nil {
			t.Fatalf("Encode(%s): %v", message.Type, err)
		}
		data.Write(encoded)
	}

	write(time.Millisecond, transcript.DirectionIn, messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 0, 1, 0}),
	})
	write(2*time.Millisecond, transcript.DirectionOut, messages.StreamMessage{
		Type: messages.StreamTypeMessageStart, ResponseID: "response-original", Value: messages.NewMessageStartValue(),
	})
	write(2500*time.Microsecond, transcript.DirectionOut, messages.StreamMessage{
		Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: "response-original", Value: messages.NewTranscriptDeltaValue("created the draft"),
	})
	write(3*time.Millisecond, transcript.DirectionOut, messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, ResponseID: "response-original", Value: messages.NewAudioDeltaValue([]byte{1, 0, 1, 0}),
	})
	// The cancellation deliberately omits ResponseID. The parser must bind it
	// to the currently open response, while still requiring this actual
	// provider-boundary event rather than a later response or PCM marker.
	write(4*time.Millisecond, transcript.DirectionIn, messages.StreamMessage{
		Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue(),
	})
	write(5*time.Millisecond, transcript.DirectionIn, messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{2, 0, 2, 0}),
	})
	write(6*time.Millisecond, transcript.DirectionOut, messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, ResponseID: "response-original", Value: &messages.MessageEndValue{Type: "message_end", Status: "cancelled"},
	})
	write(7*time.Millisecond, transcript.DirectionOut, messages.StreamMessage{
		Type: messages.StreamTypeMessageStart, ResponseID: "response-replacement", Value: messages.NewMessageStartValue(),
	})
	write(7500*time.Microsecond, transcript.DirectionOut, messages.StreamMessage{
		Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: "response-replacement", Value: messages.NewTranscriptDeltaValue("created the final"),
	})
	write(8*time.Millisecond, transcript.DirectionOut, messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, ResponseID: "response-replacement", Value: messages.NewAudioDeltaValue([]byte{3, 0, 3, 0}),
	})
	write(9*time.Millisecond, transcript.DirectionOut, messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, ResponseID: "response-replacement", Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if err := os.WriteFile(filepath.Join(root, "agent.transcript.jsonl"), data.Bytes(), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	facts, err := readCustomerSimulationStream(root, NewFamilyBScenario(), 0)
	if err != nil {
		t.Fatalf("readCustomerSimulationStream: %v", err)
	}
	if !facts.cancelObserved || facts.cancelAt != 3*time.Millisecond || facts.cancelResponseID != "response-original" {
		t.Fatalf("recorded cancellation = observed:%t at:%s response:%q, want 3ms on original response", facts.cancelObserved, facts.cancelAt, facts.cancelResponseID)
	}
	if len(facts.inputSpeechStarts) != 2 || facts.inputSpeechStarts[0] != 0 || facts.inputSpeechStarts[1] != 4*time.Millisecond {
		t.Fatalf("recorded input speech starts = %v, want initial 0 and correction 4ms", facts.inputSpeechStarts)
	}
	original := customerSimulationRecordedResponse(facts, 0)
	replacement := customerSimulationRecordedResponse(facts, 1)
	if !original.AudioObserved || !replacement.AudioObserved || original.AudioStart != 2*time.Millisecond || replacement.AudioStart != 7*time.Millisecond {
		t.Fatalf("recorded response audio boundaries = %+v / %+v, want original 2ms and replacement 7ms", original, replacement)
	}

	evidence := customerSimulationCorrectionEvidence(NewFamilyBScenario(), nil, ProcessFacts{}, facts)
	if !evidence.CancellationEventRecorded || evidence.CancellationResponseID != "response-original" || evidence.CancellationSentAt != 3*time.Millisecond || evidence.CorrectionStartedAt != 4*time.Millisecond {
		t.Fatalf("correction evidence = %+v, want recorded cancellation and correction boundaries", evidence)
	}
	if evidence.OriginalResponseStartedAt != 2*time.Millisecond || evidence.OriginalResponseEndedAt <= evidence.OriginalResponseStartedAt {
		t.Fatalf("original response output interval = %s-%s, want positive recorded interval", evidence.OriginalResponseStartedAt, evidence.OriginalResponseEndedAt)
	}

	withoutCancellation := facts
	withoutCancellation.cancelObserved = false
	withoutCancellation.cancelAt = 0
	withoutCancellation.cancelResponseID = ""
	withoutCancellationEvidence := customerSimulationCorrectionEvidence(NewFamilyBScenario(), nil, ProcessFacts{}, withoutCancellation)
	if withoutCancellationEvidence.CancellationEventRecorded || withoutCancellationEvidence.CancellationSentAt != 0 {
		t.Fatalf("missing cancellation evidence = %+v, must not infer cancellation from output/input boundaries", withoutCancellationEvidence)
	}
}

func TestCustomerSimulationAudioEventsCorrelateMultipleReadsToRecordedResponses(t *testing.T) {
	scenario := NewFamilyBScenario()
	facts := customerSimulationRecordingFacts{responses: []customerSimulationResponse{
		{ID: "response-original-output", Text: "Created draft/brief.md", AudioBytes: 4},
		{ID: "response-replacement-output", Text: "Created final/brief.md", AudioBytes: 4},
	}}
	result := DuplexRunResult{Output: []DuplexOutputEvent{
		{Bytes: 2, Total: 2, At: time.Millisecond},
		{Bytes: 2, Total: 4, At: 2 * time.Millisecond},
		{Bytes: 2, Total: 6, At: 3 * time.Millisecond},
		{Bytes: 2, Total: 8, At: 4 * time.Millisecond},
	}}

	events := customerSimulationAudioEvents(scenario, result, DefaultDuplexFrameDuration, facts)
	var output []AudioTurnEvent
	for _, event := range events {
		if event.Direction == "output" {
			output = append(output, event)
		}
	}
	if len(output) != 4 {
		t.Fatalf("output audio events = %d, want one event per read", len(output))
	}
	wantTurns := []string{"turn-1", "turn-1", "turn-2", "turn-2"}
	for index, event := range output {
		if event.TurnID != wantTurns[index] {
			t.Fatalf("output event %d = %+v, want recorded response turn %q", index, event, wantTurns[index])
		}
		if event.Kind != "product_speech" {
			t.Fatalf("output event %d kind = %q, want product_speech", index, event.Kind)
		}
	}

	continuationResult := DuplexRunResult{Output: []DuplexOutputEvent{
		{Bytes: 2, Total: 2, At: time.Millisecond},
		{Bytes: 2, Total: 4, At: 2 * time.Millisecond},
		{Bytes: 2, Total: 6, At: 3 * time.Millisecond},
		{Bytes: 2, Total: 8, At: 4 * time.Millisecond},
	}}
	withToolContinuations := customerSimulationAudioEvents(scenario, continuationResult, DefaultDuplexFrameDuration, customerSimulationRecordingFacts{responses: []customerSimulationResponse{
		{ID: "response-original-tool-continuation", AudioBytes: 2},
		{ID: "response-original-output", Text: "Created draft/brief.md", AudioBytes: 2},
		{ID: "response-replacement-tool-continuation", AudioBytes: 2},
		{ID: "response-replacement-output", Text: "Created final/brief.md", AudioBytes: 2},
	}})
	var continuationOutput []AudioTurnEvent
	for _, event := range withToolContinuations {
		if event.Direction == "output" {
			continuationOutput = append(continuationOutput, event)
		}
	}
	wantContinuationTurns := []string{"turn-1", "turn-1", "turn-2", "turn-2"}
	if len(continuationOutput) != len(wantContinuationTurns) {
		t.Fatalf("tool-continuation output events = %+v, want %d events", continuationOutput, len(wantContinuationTurns))
	}
	for index, event := range continuationOutput {
		if event.TurnID != wantContinuationTurns[index] {
			t.Fatalf("tool-continuation output event %d = %+v, want turn %q", index, event, wantContinuationTurns[index])
		}
	}

	crossing := customerSimulationAudioEvents(scenario, DuplexRunResult{Output: []DuplexOutputEvent{{Bytes: 6, Total: 6, At: 5 * time.Millisecond}}}, DefaultDuplexFrameDuration, customerSimulationRecordingFacts{responses: []customerSimulationResponse{
		{ID: "response-original-output", Text: "draft", AudioBytes: 4},
		{ID: "response-replacement-output", Text: "final", AudioBytes: 4},
	}})
	if len(crossing) != 2 || crossing[0].TurnID != "turn-1" || crossing[0].Bytes != 4 || crossing[1].TurnID != "turn-2" || crossing[1].Bytes != 2 {
		t.Fatalf("one read crossing a response boundary = %+v, want 4 bytes on turn-1 and 2 bytes on turn-2", crossing)
	}
}
