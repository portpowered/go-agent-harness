//go:build live

// This file is an opt-in, credentialed proof of the voice grounding contract.
// It deliberately validates the raw provider exchange and the recorded audio
// artifacts instead of treating a clean CLI exit or plausible text as proof.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	liveVoiceGroundingModel       = "gpt-realtime-2.1-mini"
	liveVoiceGroundingTimeout     = 60 * time.Second
	liveVoiceGroundingOptIn       = "AGENT_HARNESS_LIVE_VOICE_GROUNDING"
	liveVoiceGroundingInputDir    = "AGENT_HARNESS_LIVE_VOICE_GROUNDING_AUDIO_DIR"
	liveVoiceGroundingArtifactDir = "AGENT_HARNESS_LIVE_VOICE_GROUNDING_ARTIFACT_DIR"
)

var liveVoiceGroundingDatePattern = regexp.MustCompile(`\b20[0-9]{2}-[0-9]{2}-[0-9]{2}\b`)

type liveVoiceGroundingProbe struct {
	name         string
	request      string
	audioName    string
	expectedTool string
}

var liveVoiceGroundingProbes = []liveVoiceGroundingProbe{
	{
		name:         "missing-file",
		request:      "read me the file at /tmp/definitely-missing-file.txt",
		audioName:    "missing-file.wav",
		expectedTool: "read_file",
	},
	{
		name:         "exit-42",
		request:      "run the command exit 42 and tell me what happened",
		audioName:    "exit-42.wav",
		expectedTool: "exec",
	},
	{
		name:         "date-control",
		request:      "run the command date -u +%Y-%m-%d and tell me the returned date",
		audioName:    "date-control.wav",
		expectedTool: "exec",
	},
}

// TestLiveVoiceGroundingFailuresTwiceAndDateControl is the required live
// stochastic proof. The input WAVs are generated with macOS say by default;
// AGENT_HARNESS_LIVE_VOICE_GROUNDING_AUDIO_DIR can instead point at reviewer
// supplied recordings named missing-file.wav, exit-42.wav, and
// date-control.wav. The test bills five realtime calls when enabled.
func TestLiveVoiceGroundingFailuresTwiceAndDateControl(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the live voice grounding proof")
	}
	if os.Getenv(liveVoiceGroundingOptIn) != "1" {
		t.Skipf("%s!=1; this live test bills five OpenAI Realtime calls and must be opted into explicitly", liveVoiceGroundingOptIn)
	}

	artifactRoot := liveVoiceGroundingArtifactRoot(t)
	inputPaths := make(map[string]string, len(liveVoiceGroundingProbes))
	for _, probe := range liveVoiceGroundingProbes {
		inputPaths[probe.name] = liveVoiceGroundingInputPath(t, probe)
	}

	evidence := make([]liveVoiceGroundingEvidence, 0, 5)
	for _, probe := range liveVoiceGroundingProbes {
		runs := 1
		if probe.name != "date-control" {
			runs = 2
		}
		for runNumber := 1; runNumber <= runs; runNumber++ {
			if probe.name == "missing-file" {
				assertMissingVoiceGroundingPathAbsent(t)
			}

			run := executeLiveVoiceGroundingRun(t, apiKey, artifactRoot, probe, runNumber, inputPaths[probe.name])
			observation, err := observeLiveVoiceGroundingCapture(run.capture, probe)
			if err != nil {
				t.Fatalf("%s run %d failed live grounding validation: %v; capture=%s", probe.name, runNumber, err, run.capturePath)
			}
			observation.CapturePath = run.capturePath
			observation.RecordDir = run.recordDir
			observation.AudioPath = run.audioPath
			if err := validateLiveVoiceGroundingObservation(observation, probe); err != nil {
				t.Fatalf("%s run %d failed live grounding validation: %v; capture=%s", probe.name, runNumber, err, run.capturePath)
			}
			if err := validateLiveVoiceGroundingArtifacts(run.recordDir, run.audioPath); err != nil {
				t.Fatalf("%s run %d has incomplete durable artifacts: %v; record-dir=%s", probe.name, runNumber, err, run.recordDir)
			}

			evidence = append(evidence, liveVoiceGroundingEvidenceFromObservation(observation, probe, runNumber, filepath.Join(run.recordDir, "audio", "in-000.pcm")))
			t.Logf("live voice grounding pass: probe=%s run=%d provider=%s model=%s tool=%s call_id=%s output_bytes=%d spoken=%q capture=%s record_dir=%s audio=%s", probe.name, runNumber, observation.Provider, observation.Model, observation.ToolCallName, observation.ToolCallID, observation.AudioBytes, observation.SpokenReply, run.capturePath, run.recordDir, run.audioPath)
		}
	}
	if len(evidence) != 5 {
		t.Fatalf("live grounding evidence runs=%d, want five (two missing-file, two exit-42, one date)", len(evidence))
	}

	evidencePath := filepath.Join(artifactRoot, "evidence.json")
	data, err := json.MarshalIndent(struct {
		Provider string                       `json:"provider"`
		Model    string                       `json:"model"`
		Runs     []liveVoiceGroundingEvidence `json:"runs"`
	}{
		Provider: "openai",
		Model:    liveVoiceGroundingModel,
		Runs:     evidence,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal live grounding evidence: %v", err)
	}
	if err := os.WriteFile(evidencePath, data, 0o600); err != nil {
		t.Fatalf("write live grounding evidence: %v", err)
	}
	t.Logf("live voice grounding evidence: %s", evidencePath)
}

type liveVoiceGroundingRun struct {
	capture     gwtesting.SessionCapture
	capturePath string
	recordDir   string
	audioPath   string
}

func executeLiveVoiceGroundingRun(t *testing.T, apiKey, artifactRoot string, probe liveVoiceGroundingProbe, runNumber int, inputPath string) liveVoiceGroundingRun {
	t.Helper()
	runName := fmt.Sprintf("%s-%d", probe.name, runNumber)
	capturePath := filepath.Join(artifactRoot, runName+".session.json")
	recordDir := filepath.Join(artifactRoot, runName+"-recording")
	audioPath := filepath.Join(artifactRoot, runName+".wav")
	configDir := t.TempDir()

	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production CLI: %v", err)
	}
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--provider", "openai",
		"--model", liveVoiceGroundingModel,
		"--api-key", apiKey,
		"--record", capturePath,
		"--record-dir", recordDir,
		"--audio-in", inputPath,
		"--audio-out", audioPath,
		"--max-duration", liveVoiceGroundingTimeout.String(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), liveVoiceGroundingTimeout+15*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("live %s run %d returned an error: %v; stdout=%q stderr=%q", probe.name, runNumber, err, stdout.String(), stderr.String())
	}

	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load live %s run %d capture: %v; stdout=%q stderr=%q", probe.name, runNumber, err, stdout.String(), stderr.String())
	}
	return liveVoiceGroundingRun{
		capture:     capture,
		capturePath: capturePath,
		recordDir:   recordDir,
		audioPath:   audioPath,
	}
}

type liveVoiceGroundingObservation struct {
	Provider                 string
	Model                    string
	Instructions             string
	AdvertisedTools          []string
	InputTranscript          string
	ToolCallName             string
	ToolCallID               string
	ToolCallArguments        string
	FunctionCallOutput       string
	SpokenReply              string
	TerminalStatus           string
	AudioBytes               int
	SessionUpdateIndex       int
	FirstInputIndex          int
	ToolCallIndex            int
	ToolArgumentsIndex       int
	FunctionOutputIndex      int
	SpokenReplyIndex         int
	TerminalIndex            int
	ResponseCreatesAfterTool int
	CapturePath              string
	RecordDir                string
	AudioPath                string
}

type liveVoiceGroundingFunctionCall struct {
	index  int
	name   string
	callID string
}

type liveVoiceGroundingArguments struct {
	index     int
	name      string
	callID    string
	arguments string
}

type liveVoiceGroundingOutput struct {
	index  int
	callID string
	output string
}

type liveVoiceGroundingTranscript struct {
	index int
	text  string
}

type liveVoiceGroundingResponseDone struct {
	index  int
	status string
}

func observeLiveVoiceGroundingCapture(capture gwtesting.SessionCapture, probe liveVoiceGroundingProbe) (liveVoiceGroundingObservation, error) {
	observation := liveVoiceGroundingObservation{
		Provider:            capture.Provider.Name,
		Model:               capture.Provider.Model,
		SessionUpdateIndex:  -1,
		FirstInputIndex:     -1,
		ToolCallIndex:       -1,
		ToolArgumentsIndex:  -1,
		FunctionOutputIndex: -1,
		SpokenReplyIndex:    -1,
		TerminalIndex:       -1,
	}

	sessionUpdates := 0
	var calls []liveVoiceGroundingFunctionCall
	var arguments []liveVoiceGroundingArguments
	var outputs []liveVoiceGroundingOutput
	var inputTranscripts []string
	var spoken []liveVoiceGroundingTranscript
	var responseDone []liveVoiceGroundingResponseDone
	var responseCreateIndices []int

	for index, record := range capture.Records {
		payload := liveVoiceGroundingRecordPayload(record)
		if len(payload) == 0 {
			return observation, fmt.Errorf("record %d (%s) has an empty payload", index, record.Type)
		}

		if record.Direction == gwtesting.DirectionClientToServer {
			switch record.Type {
			case "session.update":
				sessionUpdates++
				var event struct {
					Session struct {
						Instructions string `json:"instructions"`
						Tools        []struct {
							Name string `json:"name"`
						} `json:"tools"`
					} `json:"session"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode session.update: %w", err)
				}
				if observation.SessionUpdateIndex < 0 {
					observation.SessionUpdateIndex = index
				}
				observation.Instructions = event.Session.Instructions
				observation.AdvertisedTools = observation.AdvertisedTools[:0]
				for _, tool := range event.Session.Tools {
					observation.AdvertisedTools = append(observation.AdvertisedTools, tool.Name)
				}
			case "input_audio_buffer.append":
				if observation.FirstInputIndex < 0 {
					observation.FirstInputIndex = index
				}
			case "conversation.item.create":
				var event struct {
					Item struct {
						Type string `json:"type"`
					} `json:"item"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode client conversation item: %w", err)
				}
				if event.Item.Type == "message" && observation.FirstInputIndex < 0 {
					observation.FirstInputIndex = index
				}
				var outputEvent struct {
					Item struct {
						Type   string `json:"type"`
						CallID string `json:"call_id"`
						Output string `json:"output"`
					} `json:"item"`
				}
				if err := json.Unmarshal(payload, &outputEvent); err != nil {
					return observation, fmt.Errorf("decode client function output: %w", err)
				}
				if outputEvent.Item.Type == "function_call_output" {
					outputs = append(outputs, liveVoiceGroundingOutput{index: index, callID: outputEvent.Item.CallID, output: outputEvent.Item.Output})
				}
			case "response.create":
				responseCreateIndices = append(responseCreateIndices, index)
			}
			continue
		}
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}

		switch record.Type {
		case "response.output_item.added":
			var event struct {
				Item struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					CallID string `json:"call_id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode function call: %w", err)
			}
			if event.Item.Type == "function_call" {
				calls = append(calls, liveVoiceGroundingFunctionCall{index: index, name: event.Item.Name, callID: event.Item.CallID})
			}
		case "response.function_call_arguments.done":
			var event struct {
				Name      string `json:"name"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode function arguments: %w", err)
			}
			arguments = append(arguments, liveVoiceGroundingArguments{index: index, name: event.Name, callID: event.CallID, arguments: event.Arguments})
		case "response.output_audio_transcript.done":
			var event struct {
				Transcript string `json:"transcript"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode spoken transcript: %w", err)
			}
			if strings.TrimSpace(event.Transcript) != "" {
				spoken = append(spoken, liveVoiceGroundingTranscript{index: index, text: event.Transcript})
			}
		case "conversation.item.input_audio_transcription.completed":
			var event struct {
				Transcript string `json:"transcript"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode input transcript: %w", err)
			}
			if strings.TrimSpace(event.Transcript) != "" {
				inputTranscripts = append(inputTranscripts, event.Transcript)
			}
		case "response.output_audio.delta", "response.audio.delta":
			var event struct {
				Delta string `json:"delta"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode output audio: %w", err)
			}
			if event.Delta != "" {
				decoded, err := base64.StdEncoding.DecodeString(event.Delta)
				if err != nil {
					return observation, fmt.Errorf("decode output audio delta: %w", err)
				}
				observation.AudioBytes += len(decoded)
			}
		case "response.done":
			var event struct {
				Status   string `json:"status"`
				Response struct {
					Status string `json:"status"`
				} `json:"response"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode response.done: %w", err)
			}
			status := event.Response.Status
			if status == "" {
				status = event.Status
			}
			responseDone = append(responseDone, liveVoiceGroundingResponseDone{index: index, status: status})
		case "error":
			return observation, fmt.Errorf("provider emitted an error event")
		}
	}

	if sessionUpdates != 1 {
		return observation, fmt.Errorf("session.update count=%d, want exactly one", sessionUpdates)
	}
	if observation.SessionUpdateIndex < 0 || observation.FirstInputIndex < 0 || observation.SessionUpdateIndex >= observation.FirstInputIndex {
		return observation, fmt.Errorf("session.update index=%d must precede first input index=%d", observation.SessionUpdateIndex, observation.FirstInputIndex)
	}
	if len(calls) != 1 {
		return observation, fmt.Errorf("function_call count=%d, want exactly one %s", len(calls), probe.expectedTool)
	}
	if len(arguments) != 1 {
		return observation, fmt.Errorf("function-call arguments count=%d, want exactly one", len(arguments))
	}
	if len(outputs) != 1 {
		return observation, fmt.Errorf("function_call_output count=%d, want exactly one", len(outputs))
	}
	call := calls[0]
	argument := arguments[0]
	output := outputs[0]
	observation.ToolCallName = call.name
	observation.ToolCallID = call.callID
	observation.ToolCallArguments = argument.arguments
	observation.FunctionCallOutput = output.output
	observation.ToolCallIndex = call.index
	observation.ToolArgumentsIndex = argument.index
	observation.FunctionOutputIndex = output.index
	if strings.TrimSpace(call.callID) == "" || argument.callID != call.callID || (argument.name != "" && argument.name != call.name) {
		return observation, fmt.Errorf("function-call correlation is invalid: call=(%q,%q), arguments=(%q,%q)", call.name, call.callID, argument.name, argument.callID)
	}
	if output.callID != call.callID {
		return observation, fmt.Errorf("function_call_output call ID=%q, want originating call ID=%q", output.callID, call.callID)
	}
	for _, responseCreateIndex := range responseCreateIndices {
		if responseCreateIndex > observation.FunctionOutputIndex {
			observation.ResponseCreatesAfterTool++
		}
	}
	if len(inputTranscripts) > 0 {
		observation.InputTranscript = strings.Join(inputTranscripts, " ")
	}
	for _, transcript := range spoken {
		if transcript.index <= observation.FunctionOutputIndex {
			continue
		}
		if observation.SpokenReplyIndex < 0 {
			observation.SpokenReplyIndex = transcript.index
		}
		if observation.SpokenReply != "" {
			observation.SpokenReply += " "
		}
		observation.SpokenReply += strings.TrimSpace(transcript.text)
	}
	for _, done := range responseDone {
		if done.index <= observation.SpokenReplyIndex {
			continue
		}
		observation.TerminalIndex = done.index
		observation.TerminalStatus = done.status
		if observation.TerminalStatus == "" {
			observation.TerminalStatus = "completed"
		}
		break
	}
	return observation, nil
}

func validateLiveVoiceGroundingObservation(observation liveVoiceGroundingObservation, probe liveVoiceGroundingProbe) error {
	if observation.Provider != "openai" || observation.Model != liveVoiceGroundingModel {
		return fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.Provider, observation.Model, liveVoiceGroundingModel)
	}
	if err := validateLiveVoiceGroundingInstructions(observation.Instructions); err != nil {
		return err
	}
	if !sameLiveVoiceGroundingSet(observation.AdvertisedTools, config.DefaultToolIDs) {
		return fmt.Errorf("advertised tools=%v, want the full default set %v", observation.AdvertisedTools, config.DefaultToolIDs)
	}
	if observation.ToolCallName != probe.expectedTool {
		return fmt.Errorf("tool call name=%q, want %q", observation.ToolCallName, probe.expectedTool)
	}
	if strings.TrimSpace(observation.ToolCallID) == "" {
		return fmt.Errorf("tool call ID is empty and cannot be correlated: %q", observation.ToolCallID)
	}
	if observation.ToolArgumentsIndex <= observation.ToolCallIndex || observation.FunctionOutputIndex <= observation.ToolArgumentsIndex {
		return fmt.Errorf("call/result order is invalid: call=%d arguments=%d output=%d", observation.ToolCallIndex, observation.ToolArgumentsIndex, observation.FunctionOutputIndex)
	}
	if observation.ResponseCreatesAfterTool < 1 {
		return fmt.Errorf("no response.create followed the delivered function_call_output")
	}
	if observation.SpokenReplyIndex <= observation.FunctionOutputIndex || strings.TrimSpace(observation.SpokenReply) == "" {
		return fmt.Errorf("spoken reply is absent or precedes the correlated tool output")
	}
	if observation.TerminalIndex <= observation.SpokenReplyIndex {
		return fmt.Errorf("terminal response.done is absent or precedes the spoken reply")
	}
	if observation.TerminalStatus == "cancelled" || observation.TerminalStatus == "failed" || observation.TerminalStatus == "incomplete" {
		return fmt.Errorf("terminal response status=%q", observation.TerminalStatus)
	}
	if observation.AudioBytes == 0 {
		return fmt.Errorf("spoken reply has zero output audio bytes")
	}
	if strings.TrimSpace(observation.FunctionCallOutput) == "" {
		return fmt.Errorf("function_call_output is empty")
	}

	var args struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(observation.ToolCallArguments), &args); err != nil {
		return fmt.Errorf("decode %s arguments: %w", probe.expectedTool, err)
	}
	reply := strings.ToLower(observation.SpokenReply)
	result := strings.ToLower(observation.FunctionCallOutput)
	switch probe.name {
	case "missing-file":
		if args.Path != "/tmp/definitely-missing-file.txt" {
			return fmt.Errorf("read_file path=%q, want /tmp/definitely-missing-file.txt", args.Path)
		}
		if !strings.Contains(result, args.Path) || !containsAnyLiveVoiceGroundingTerm(result, "no such file", "does not exist", "not exist", "missing") {
			return fmt.Errorf("read_file result=%q does not contain the real missing-file error", observation.FunctionCallOutput)
		}
		if !containsAnyLiveVoiceGroundingTerm(reply, "file", "read") || !containsAnyLiveVoiceGroundingTerm(reply, "does not exist", "doesn't exist", "does not", "no such", "missing", "not found", "couldn't", "could not", "unable") {
			return fmt.Errorf("spoken missing-file reply=%q is not an honest non-existent-file failure", observation.SpokenReply)
		}
	case "exit-42":
		if !strings.Contains(strings.ToLower(args.Command), "exit 42") {
			return fmt.Errorf("exec command=%q, want a command containing exit 42", args.Command)
		}
		if !strings.Contains(result, "42") || !containsAnyLiveVoiceGroundingTerm(result, "exit code", "exit status") {
			return fmt.Errorf("exec result=%q does not contain the real non-zero exit information", observation.FunctionCallOutput)
		}
		if !strings.Contains(reply, "42") || !containsAnyLiveVoiceGroundingTerm(reply, "exit", "status", "code", "non-zero", "nonzero") {
			return fmt.Errorf("spoken exit-42 reply=%q does not report the returned status", observation.SpokenReply)
		}
		if containsAnyLiveVoiceGroundingTerm(reply, "successfully", "succeeded", "successful") {
			return fmt.Errorf("spoken exit-42 reply=%q claims success", observation.SpokenReply)
		}
		if containsAnyLiveVoiceGroundingTerm(reply, "stdout", "output") && !containsAnyLiveVoiceGroundingTerm(reply, "no output", "no stdout", "nothing", "empty", "none") {
			return fmt.Errorf("spoken exit-42 reply=%q invents command output", observation.SpokenReply)
		}
	case "date-control":
		if !strings.Contains(strings.ToLower(args.Command), "date") {
			return fmt.Errorf("date control command=%q, want date command", args.Command)
		}
		dateToken := liveVoiceGroundingDatePattern.FindString(observation.FunctionCallOutput)
		if dateToken == "" {
			return fmt.Errorf("date result=%q has no YYYY-MM-DD value to ground the spoken reply", observation.FunctionCallOutput)
		}
		if !strings.Contains(observation.SpokenReply, dateToken) {
			return fmt.Errorf("spoken date reply=%q omits returned date %q", observation.SpokenReply, dateToken)
		}
	}
	return nil
}

func validateLiveVoiceGroundingInstructions(instructions string) error {
	if strings.TrimSpace(instructions) == "" {
		return fmt.Errorf("session instructions are empty")
	}
	lower := strings.ToLower(instructions)
	for _, phrase := range []string{
		"tool-grounding requirements",
		"actual files",
		"commands",
		"web resources",
		"images",
		"machine state",
		"relevant advertised tool",
		"corresponding tool result",
		"missing resources",
		"permission denials",
		"non-zero command exits",
		"instead of guessing",
		"never invent output",
	} {
		if !strings.Contains(lower, phrase) {
			return fmt.Errorf("session instructions omit grounding phrase %q", phrase)
		}
	}
	for _, providerDetail := range []string{"session.update", "function_call_output", "OpenAI", "Grok"} {
		if strings.Contains(instructions, providerDetail) {
			return fmt.Errorf("session instructions contain provider/wire detail %q", providerDetail)
		}
	}
	return nil
}

func validateLiveVoiceGroundingArtifacts(recordDir, audioPath string) error {
	for _, relative := range []string{"manifest.json", "client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/in-000.pcm", "audio/out-000.pcm"} {
		path := filepath.Join(recordDir, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("artifact %s: %w", relative, err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("artifact %s is empty", relative)
		}
	}
	info, err := os.Stat(audioPath)
	if err != nil {
		return fmt.Errorf("audio output: %w", err)
	}
	if info.Size() <= 44 {
		return fmt.Errorf("audio output is only %d bytes", info.Size())
	}
	return nil
}

func sameLiveVoiceGroundingSet(got, want []string) bool {
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	return strings.Join(gotCopy, "\x00") == strings.Join(wantCopy, "\x00")
}

func containsAnyLiveVoiceGroundingTerm(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func liveVoiceGroundingRecordPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func assertMissingVoiceGroundingPathAbsent(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/tmp/definitely-missing-file.txt"); err == nil {
		t.Fatal("/tmp/definitely-missing-file.txt exists; refusing to alter customer state")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat /tmp/definitely-missing-file.txt: %v", err)
	}
}

func liveVoiceGroundingInputPath(t *testing.T, probe liveVoiceGroundingProbe) string {
	t.Helper()
	if directory := strings.TrimSpace(os.Getenv(liveVoiceGroundingInputDir)); directory != "" {
		path := filepath.Join(directory, probe.audioName)
		if info, err := os.Stat(path); err != nil || info.IsDir() || info.Size() == 0 {
			t.Fatalf("live voice input %s is missing or empty: %s (%v)", probe.name, path, err)
		}
		return path
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("live voice grounding needs %s recordings on non-macOS; set %s", probe.audioName, liveVoiceGroundingInputDir)
	}
	for _, command := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("live voice grounding needs %s or set %s", command, liveVoiceGroundingInputDir)
		}
	}

	directory := t.TempDir()
	aiffPath := filepath.Join(directory, probe.name+".aiff")
	wavPath := filepath.Join(directory, probe.audioName)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "say", "-o", aiffPath, probe.request).CombinedOutput(); err != nil {
		t.Fatalf("generate %s spoken input: %v: %s", probe.name, err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.CommandContext(ctx, "afconvert", "-f", "WAVE", "-d", "LEI16@16000", aiffPath, wavPath).CombinedOutput(); err != nil {
		t.Fatalf("convert %s spoken input to WAV: %v: %s", probe.name, err, strings.TrimSpace(string(output)))
	}
	return wavPath
}

func liveVoiceGroundingArtifactRoot(t *testing.T) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(liveVoiceGroundingArtifactDir))
	if parent == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create live grounding artifact parent %s: %v", parent, err)
	}
	root, err := os.MkdirTemp(parent, "s2s-voice-honest-failure-grounding-")
	if err != nil {
		t.Fatalf("create live grounding artifact directory: %v", err)
	}
	return root
}

type liveVoiceGroundingEvidence struct {
	Probe                string   `json:"probe"`
	Run                  int      `json:"run"`
	Request              string   `json:"request"`
	InputAudioPath       string   `json:"input_audio_path"`
	InputTranscript      string   `json:"input_transcript,omitempty"`
	Provider             string   `json:"provider"`
	Model                string   `json:"model"`
	GroundingInstruction bool     `json:"grounding_instruction_present"`
	InstructionSHA256    string   `json:"instruction_sha256"`
	AdvertisedTools      []string `json:"advertised_tools"`
	ToolName             string   `json:"tool_name"`
	ToolCallID           string   `json:"tool_call_id"`
	ToolArguments        string   `json:"tool_arguments"`
	FunctionCallOutput   string   `json:"function_call_output"`
	SpokenReply          string   `json:"spoken_reply"`
	TerminalStatus       string   `json:"terminal_status"`
	OutputAudioBytes     int      `json:"output_audio_bytes"`
	CapturePath          string   `json:"capture_path"`
	RecordDir            string   `json:"record_dir"`
	AudioPath            string   `json:"audio_path"`
	Pass                 bool     `json:"pass"`
}

func liveVoiceGroundingEvidenceFromObservation(observation liveVoiceGroundingObservation, probe liveVoiceGroundingProbe, runNumber int, inputPath string) liveVoiceGroundingEvidence {
	digest := sha256.Sum256([]byte(observation.Instructions))
	return liveVoiceGroundingEvidence{
		Probe:                probe.name,
		Run:                  runNumber,
		Request:              probe.request,
		InputAudioPath:       inputPath,
		InputTranscript:      observation.InputTranscript,
		Provider:             observation.Provider,
		Model:                observation.Model,
		GroundingInstruction: true,
		InstructionSHA256:    hex.EncodeToString(digest[:]),
		AdvertisedTools:      append([]string(nil), observation.AdvertisedTools...),
		ToolName:             observation.ToolCallName,
		ToolCallID:           observation.ToolCallID,
		ToolArguments:        observation.ToolCallArguments,
		FunctionCallOutput:   observation.FunctionCallOutput,
		SpokenReply:          observation.SpokenReply,
		TerminalStatus:       observation.TerminalStatus,
		OutputAudioBytes:     observation.AudioBytes,
		CapturePath:          observation.CapturePath,
		RecordDir:            observation.RecordDir,
		AudioPath:            observation.AudioPath,
		Pass:                 true,
	}
}

func TestValidateLiveVoiceGroundingObservationRejectsUnprovenEvidence(t *testing.T) {
	base := liveVoiceGroundingObservation{
		Provider:                 "openai",
		Model:                    liveVoiceGroundingModel,
		Instructions:             liveVoiceGroundingTestInstructions(),
		AdvertisedTools:          append([]string(nil), config.DefaultToolIDs...),
		ToolCallName:             "read_file",
		ToolCallID:               "call-read-file",
		ToolCallArguments:        `{"path":"/tmp/definitely-missing-file.txt"}`,
		FunctionCallOutput:       "open /tmp/definitely-missing-file.txt: no such file or directory",
		SpokenReply:              "I could not read the file because it does not exist.",
		TerminalStatus:           "completed",
		AudioBytes:               1,
		SessionUpdateIndex:       0,
		FirstInputIndex:          1,
		ToolCallIndex:            2,
		ToolArgumentsIndex:       3,
		FunctionOutputIndex:      4,
		SpokenReplyIndex:         5,
		TerminalIndex:            6,
		ResponseCreatesAfterTool: 1,
	}
	probe := liveVoiceGroundingProbes[0]
	for _, testCase := range []struct {
		name   string
		mutate func(*liveVoiceGroundingObservation)
	}{
		{name: "missing call id", mutate: func(value *liveVoiceGroundingObservation) { value.ToolCallID = "" }},
		{name: "missing output", mutate: func(value *liveVoiceGroundingObservation) { value.FunctionCallOutput = "" }},
		{name: "missing spoken reply", mutate: func(value *liveVoiceGroundingObservation) { value.SpokenReply = "" }},
		{name: "guessed spoken reply", mutate: func(value *liveVoiceGroundingObservation) { value.SpokenReply = "The file contains a made-up result." }},
		{name: "terminal failure", mutate: func(value *liveVoiceGroundingObservation) { value.TerminalStatus = "failed" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := base
			testCase.mutate(&value)
			if err := validateLiveVoiceGroundingObservation(value, probe); err == nil {
				t.Fatal("validation accepted incomplete or contradictory grounding evidence")
			}
		})
	}
}

func TestObserveLiveVoiceGroundingCaptureRequiresCorrelatedSpokenResult(t *testing.T) {
	const callID = "call-read-file"
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: liveVoiceGroundingModel},
		Records: []gwtesting.CapturedSessionEvent{
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionClientToServer, "session.update", map[string]any{
				"type": "session.update",
				"session": map[string]any{
					"instructions": liveVoiceGroundingTestInstructions(),
					"tools":        liveVoiceGroundingDefaultToolPayload(),
				},
			}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionServerToClient, "session.created", map[string]any{"type": "session.created"}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionClientToServer, "input_audio_buffer.append", map[string]any{"type": "input_audio_buffer.append", "audio": "AQI="}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionServerToClient, "response.output_item.added", map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{"type": "function_call", "name": "read_file", "call_id": callID},
			}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionServerToClient, "response.function_call_arguments.done", map[string]any{
				"type": "response.function_call_arguments.done", "name": "read_file", "call_id": callID,
				"arguments": `{"path":"/tmp/definitely-missing-file.txt"}`,
			}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionServerToClient, "response.done", map[string]any{
				"type": "response.done", "response": map[string]any{"status": "completed"},
			}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionClientToServer, "conversation.item.create", map[string]any{
				"type": "conversation.item.create",
				"item": map[string]any{
					"type": "function_call_output", "call_id": callID,
					"output": "open /tmp/definitely-missing-file.txt: no such file or directory",
				},
			}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionClientToServer, "response.create", map[string]any{"type": "response.create"}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionServerToClient, "response.output_audio_transcript.done", map[string]any{
				"type": "response.output_audio_transcript.done", "transcript": "I could not read the file because it does not exist.",
			}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionServerToClient, "response.output_audio.delta", map[string]any{
				"type": "response.output_audio.delta", "delta": "AQI=",
			}),
			liveVoiceGroundingSyntheticRecord(gwtesting.DirectionServerToClient, "response.done", map[string]any{
				"type": "response.done", "response": map[string]any{"status": "completed"},
			}),
		},
	}

	probe := liveVoiceGroundingProbes[0]
	observation, err := observeLiveVoiceGroundingCapture(capture, probe)
	if err != nil {
		t.Fatalf("observe synthetic grounding capture: %v", err)
	}
	if err := validateLiveVoiceGroundingObservation(observation, probe); err != nil {
		t.Fatalf("validate synthetic grounding capture: %v", err)
	}
	if observation.ResponseCreatesAfterTool != 1 || observation.ToolCallID != callID || observation.AudioBytes != 2 {
		t.Fatalf("observed correlated result = response_creates=%d call_id=%q audio_bytes=%d, want 1/%q/2", observation.ResponseCreatesAfterTool, observation.ToolCallID, observation.AudioBytes, callID)
	}
}

func liveVoiceGroundingSyntheticRecord(direction gwtesting.SessionEventDirection, eventType string, value any) gwtesting.CapturedSessionEvent {
	payload, _ := json.Marshal(value)
	return gwtesting.CapturedSessionEvent{
		Direction:   direction,
		Type:        eventType,
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     payload,
	}
}

func liveVoiceGroundingDefaultToolPayload() []map[string]string {
	tools := make([]map[string]string, 0, len(config.DefaultToolIDs))
	for _, name := range config.DefaultToolIDs {
		tools = append(tools, map[string]string{"type": "function", "name": name})
	}
	return tools
}

func liveVoiceGroundingTestInstructions() string {
	return "AGENTS\n\nTool-grounding requirements: use the relevant advertised tool for actual files, commands, web resources, images, or machine state; wait for the corresponding tool result; report missing resources, permission denials, and non-zero command exits honestly instead of guessing; never invent output."
}
