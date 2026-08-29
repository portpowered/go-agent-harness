//go:build live

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	scheduledAudioBargeInOptInEnv    = "AGENT_HARNESS_LIVE_SCHEDULED_AUDIO_BARGE_IN"
	scheduledAudioBargeInMaxDuration = 45 * time.Second
	scheduledAudioBargeInTestTimeout = 60 * time.Second
)

// TestLiveSessionScheduledAudioBargeIn is the credential-gated confirmation
// for the shipped repeated --audio-in-turn path. It is separate from the
// stdin proof because this is the customer-facing opt-in that restores a
// scripted interruption without changing ordinary scheduled-turn behavior.
func TestLiveSessionScheduledAudioBargeIn(t *testing.T) {
	if os.Getenv(scheduledAudioBargeInOptInEnv) != "1" {
		t.Skipf("%s!=1; scheduled audio barge-in confirmation is explicit opt-in", scheduledAudioBargeInOptInEnv)
	}
	apiKey := strings.TrimSpace(os.Getenv(liveBargeInAPIKeyEnv))
	if apiKey == "" {
		t.Skipf("%s is not set; scheduled audio barge-in confirmation is inconclusive", liveBargeInAPIKeyEnv)
	}
	t.Setenv("AGENT_MODEL__OPENAI__API_KEY", apiKey)

	runtimeObserver := &liveBargeInRuntimeObserver{}
	streamTrace := newLiveBargeInTrace()
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, runtimeObserver),
	)
	if err != nil {
		t.Fatalf("initialize shipped CLI for scheduled audio barge-in: %v", err)
	}
	agentCLI.SetSessionStreamObserver(streamTrace.observe)

	workDir := t.TempDir()
	configDir := filepath.Join(workDir, "config")
	writeLiveBargeInNoToolConfig(t, configDir)
	promptPath := filepath.Join(workDir, "long-first-answer.txt")
	if err := os.WriteFile(promptPath, []byte(scheduledAudioBargeInPrompt), 0o600); err != nil {
		t.Fatalf("write scheduled audio barge-in prompt: %v", err)
	}
	capturePath := filepath.Join(workDir, "scheduled-audio-barge-in.session.json")
	recordDir := filepath.Join(workDir, "recording")
	audioOutPath := filepath.Join(workDir, "assistant.wav")
	firstAudio := locateCLIFixture(t, "multiturn_turn1.wav")
	secondAudio := locateCLIFixture(t, "multiturn_turn2.wav")

	root := agentCLI.Generate()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--record", capturePath,
		"--record-dir", recordDir,
		"--audio-out", audioOutPath,
		"--provider", "openai",
		"--model", liveBargeInModel,
		"--system-prompt", promptPath,
		"--max-duration", scheduledAudioBargeInMaxDuration.String(),
		"--audio-in-turn", firstAudio,
		"--audio-in-turn", secondAudio,
		"--audio-in-turn-barge",
	})

	ctx, cancel := context.WithTimeout(context.Background(), scheduledAudioBargeInTestTimeout)
	defer cancel()
	runErr := awaitLiveBargeInCommand(ctx, root)
	capture, loadErr := gwtesting.LoadSessionCapture(capturePath)
	if loadErr != nil {
		if liveBargeInRunErrorClass(runErr) != "runtime-contract-failure" {
			t.Skipf("INCONCLUSIVE scheduled audio barge-in confirmation: provider/setup result did not produce a capture")
		}
		t.Fatalf("scheduled audio barge-in capture was not written: %v", runErr)
	}

	ledger, facts, validationErr := normalizeLiveBargeInCapture(capture)
	var inconclusive *liveBargeInInconclusiveError
	if errors.As(validationErr, &inconclusive) {
		t.Skipf("INCONCLUSIVE scheduled audio barge-in confirmation: %s", inconclusive.Reason)
	}
	if validationErr != nil {
		t.Fatalf("scheduled audio barge-in capture adapter failed: %v", validationErr)
	}
	if runErr != nil {
		if liveBargeInRunErrorClass(runErr) != "runtime-contract-failure" {
			t.Skipf("INCONCLUSIVE scheduled audio barge-in confirmation: provider result class=%s", liveBargeInRunErrorClass(runErr))
		}
		t.Fatalf("scheduled audio barge-in command returned a contract failure: %v", runErr)
	}
	if boundaryErr := validateScheduledAudioBargeInBoundaries(facts, streamTrace); boundaryErr != nil {
		if errors.As(boundaryErr, &inconclusive) {
			t.Skipf("INCONCLUSIVE scheduled audio barge-in confirmation: %s", inconclusive.Reason)
		}
		t.Fatalf("scheduled audio barge-in boundary failed: %v", boundaryErr)
	}
	if err := ledger.Validate(scheduledAudioBargeInContract()); err != nil {
		t.Fatalf("scheduled audio barge-in identity ledger failed: %v", err)
	}
	if err := validateScheduledAudioBargeInRecordDir(recordDir); err != nil {
		t.Fatalf("scheduled audio barge-in recording failed: %v", err)
	}
	if info, err := os.Stat(audioOutPath); err != nil || info.Size() <= 44 {
		t.Fatalf("scheduled audio barge-in assistant audio artifact is missing or empty")
	}
	validateScheduledAudioBargeInRuntime(t, runtimeObserver.snapshot())

	t.Logf("OpenAI scheduled audio barge-in proof (gpt-realtime): %s", scheduledAudioBargeInSanitizedLedger(facts, streamTrace))
}

const scheduledAudioBargeInPrompt = "For the first spoken request, respond with at least twelve concise complete sentences and keep speaking for several seconds so the response remains active when the second scheduled spoken turn is released. When that second request arrives, stop the first response cleanly and answer it distinctly. Do not use tools."

func scheduledAudioBargeInContract() probe.BargeInContract {
	return probe.BargeInContract{
		Inputs: []probe.BargeInInputExpectation{
			{ID: "input-1", TurnID: "turn-1"},
			{ID: "input-2", TurnID: "turn-2"},
		},
		Responses: []probe.BargeInResponseExpectation{
			{
				ID:            "response-1",
				InputID:       "input-1",
				TurnID:        "turn-1",
				Disposition:   probe.BargeInDispositionCancelled,
				RequireCancel: true,
			},
			{
				ID:                  "response-2",
				InputID:             "input-2",
				TurnID:              "turn-2",
				Disposition:         probe.BargeInDispositionCompleted,
				ForbidCancel:        true,
				RequireOutput:       true,
				RequireContinuation: true,
			},
		},
		RequireSessionTerminal: true,
	}
}

func validateScheduledAudioBargeInBoundaries(facts liveBargeInCaptureFacts, trace *liveBargeInTrace) error {
	if facts.SessionCreated != 1 || facts.SessionUpdated == 0 {
		return &liveBargeInInconclusiveError{Reason: "session readiness boundary was not observed"}
	}
	if facts.Appends != 2 || facts.Commits != 2 || facts.UserItems != 2 || len(facts.InputStarts) != 2 || len(facts.Responses) != 2 {
		return fmt.Errorf("scheduled lifecycle counts appends=%d commits=%d user_turns=%d input_starts=%d responses=%d; want two of each", facts.Appends, facts.Commits, facts.UserItems, len(facts.InputStarts), len(facts.Responses))
	}
	if facts.Cancels == 0 {
		return &liveBargeInInconclusiveError{Reason: "active-response cancellation boundary was not won"}
	}
	if facts.Cancels != 1 {
		return fmt.Errorf("scheduled active-response cancellation count=%d, want exactly one", facts.Cancels)
	}
	first, second := facts.Responses[0], facts.Responses[1]
	if first.Created == 0 || first.Done == 0 || second.Created == 0 || second.Done == 0 {
		return &liveBargeInInconclusiveError{Reason: "two complete response boundaries were not observed"}
	}
	if !(first.Created < facts.InputStarts[1] && facts.InputStarts[1] < first.Done) {
		return &liveBargeInInconclusiveError{Reason: "second scheduled input did not arrive while response 1 was active"}
	}
	if first.Cancel == 0 {
		return &liveBargeInInconclusiveError{Reason: "response 1 was not cancelled"}
	}
	if first.Cancel >= first.Done {
		return fmt.Errorf("response 1 cancellation sequence=%d did not precede terminal sequence=%d", first.Cancel, first.Done)
	}
	if second.Cancel != 0 || second.firstOutput() == 0 {
		return fmt.Errorf("replacement response was cancelled or emitted no output: cancel=%d output=%d", second.Cancel, second.firstOutput())
	}
	if err := validateLiveBargeInDeliveredAudio(facts, trace); err != nil {
		return fmt.Errorf("stale post-cancel audio crossed the customer stream: %w", err)
	}
	return nil
}

func validateScheduledAudioBargeInRecordDir(path string) error {
	for _, name := range []string{"manifest.json", "client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl"} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || info.Size() == 0 {
			return fmt.Errorf("recording artifact %q is missing or empty", name)
		}
	}
	file, err := os.Open(filepath.Join(path, "session-log.jsonl"))
	if err != nil {
		return fmt.Errorf("open session log: %w", err)
	}
	defer file.Close()
	entries := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry struct {
			TurnIndex int `json:"turn_index"`
			Response  struct {
				Complete   bool   `json:"complete"`
				AudioBytes uint64 `json:"audio_bytes"`
			} `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("decode session log entry: %w", err)
		}
		entries++
		if entry.TurnIndex != entries {
			return fmt.Errorf("session log entry %d has turn index %d", entries, entry.TurnIndex)
		}
		if entries == 2 && (!entry.Response.Complete || entry.Response.AudioBytes == 0) {
			return fmt.Errorf("replacement session log entry does not prove a completed audio response")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read session log: %w", err)
	}
	if entries != 2 {
		return fmt.Errorf("session log entries=%d, want 2", entries)
	}
	return nil
}

func validateScheduledAudioBargeInRuntime(t *testing.T, observations []liveBargeInRuntimeFact) {
	t.Helper()
	turns, outputBytes, terminals := 0, 0, 0
	for _, observation := range observations {
		switch observation.Kind {
		case services.SessionRuntimeObservationTurnCompleted:
			turns++
		case services.SessionRuntimeObservationAudioOutput:
			outputBytes += observation.PayloadBytes
		case services.SessionRuntimeObservationTerminal:
			terminals++
			if !observation.Clean || observation.HasError || !observation.HasAccounting {
				t.Fatalf("scheduled runtime terminal was not clean and accounted: %s", liveBargeInRuntimeEvidence(observations))
			}
		}
	}
	if turns != 2 || terminals != 1 || outputBytes == 0 {
		t.Fatalf("scheduled runtime did not reconcile two turns, output, and terminal: %s", liveBargeInRuntimeEvidence(observations))
	}
}

func scheduledAudioBargeInSanitizedLedger(facts liveBargeInCaptureFacts, trace *liveBargeInTrace) string {
	events, _ := trace.snapshot()
	audioByResponse := make(map[int]int)
	textByResponse := make(map[int]int)
	for _, event := range events {
		audioByResponse[event.ResponseOrdinal] += event.AudioBytes
		textByResponse[event.ResponseOrdinal] += event.TextBytes
	}
	return fmt.Sprintf("T1{append_group=1,commit=1,user_turn=1} R1{cancelled,audio_bytes=%d,text_bytes=%d}; T2{append_group=1,commit=1,user_turn=1} R2{completed,audio_bytes=%d,text_bytes=%d} terminal={clean=true,unresolved=0} counts={append_groups=2,commits=2,user_turns=2,responses=2,cancels=1,provider_late_output_discarded=%d}",
		audioByResponse[1], textByResponse[1], audioByResponse[2], textByResponse[2], facts.ProviderLateOutput)
}
