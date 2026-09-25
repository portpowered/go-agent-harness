//go:build live

// This is an explicitly opted-in, credentialed proof of the spoken tool
// grounding contract. It validates raw provider events and durable recording
// artifacts, rather than treating a clean command exit as evidence.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	liveVoiceToolGroundingModel       = "gpt-realtime-2.1-mini"
	liveVoiceToolGroundingTimeout     = 60 * time.Second
	liveVoiceToolGroundingMissingPath = "/tmp/definitely_missing_file.txt"
	liveVoiceToolGroundingProbeDelay  = 15 * time.Second
	liveVoiceToolGroundingOptIn       = "AGENT_HARNESS_LIVE_VOICE_GROUNDING"
	liveVoiceToolGroundingInputDir    = "AGENT_HARNESS_LIVE_VOICE_GROUNDING_AUDIO_DIR"
	liveVoiceToolGroundingArtifactDir = "AGENT_HARNESS_LIVE_VOICE_GROUNDING_ARTIFACT_DIR"
)

var liveVoiceToolGroundingDate = regexp.MustCompile(`20[0-9]{2}-[0-9]{2}-[0-9]{2}`)

type liveVoiceToolGroundingCase struct {
	Name         string
	Request      string
	AudioName    string
	ExpectedTool string
}

var liveVoiceToolGroundingCases = []liveVoiceToolGroundingCase{
	{
		Name:         "missing-file",
		Request:      "read me the file at /tmp/definitely-missing-file.txt",
		AudioName:    "missing-file.wav",
		ExpectedTool: "read_file",
	},
	{
		Name:         "exit-42",
		Request:      "run the command exit 42 and tell me what happened",
		AudioName:    "exit-42.wav",
		ExpectedTool: "exec",
	},
	{
		Name:         "date-control",
		Request:      "run the command date -u +%Y-%m-%d and tell me the returned date",
		AudioName:    "date-control.wav",
		ExpectedTool: "exec",
	},
}

// TestLiveVoiceToolGroundingFailuresTwiceAndDateControl runs the two required
// failure probes twice and the successful date control once. It is skipped by
// default because each enabled test run makes a real Realtime request.
func TestLiveVoiceToolGroundingFailuresTwiceAndDateControl(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the live voice grounding proof")
	}
	if os.Getenv(liveVoiceToolGroundingOptIn) != "1" {
		t.Skipf("%s!=1; this proof makes five OpenAI Realtime calls and requires explicit opt-in", liveVoiceToolGroundingOptIn)
	}

	artifactRoot := liveVoiceToolGroundingArtifactRoot(t)
	inputs := make(map[string]string, len(liveVoiceToolGroundingCases))
	for _, testCase := range liveVoiceToolGroundingCases {
		inputs[testCase.Name] = liveVoiceToolGroundingInput(t, testCase)
	}

	evidence := make([]liveVoiceToolGroundingEvidence, 0, 5)
	for _, testCase := range liveVoiceToolGroundingCases {
		runs := 1
		if testCase.Name != "date-control" {
			runs = 2
		}
		for runNumber := 1; runNumber <= runs; runNumber++ {
			if len(evidence) > 0 {
				// The live model is token-rate limited. Space independent probes
				// so the required five-run matrix does not turn its final control
				// into a provider rate-limit failure.
				time.Sleep(liveVoiceToolGroundingProbeDelay)
			}
			if testCase.Name == "missing-file" {
				assertMissingVoiceToolGroundingPath(t)
			}

			run := runLiveVoiceToolGrounding(t, apiKey, artifactRoot, testCase, runNumber, inputs[testCase.Name])
			observation, err := inspectLiveVoiceToolGroundingCapture(run.capture, testCase)
			if err != nil {
				t.Fatalf("%s run %d capture validation failed: %v; capture=%s", testCase.Name, runNumber, err, run.capturePath)
			}
			if err := validateLiveVoiceToolGroundingObservation(observation, testCase); err != nil {
				t.Fatalf("%s run %d grounding validation failed: %v; capture=%s", testCase.Name, runNumber, err, run.capturePath)
			}
			if err := validateLiveVoiceToolGroundingArtifacts(run.recordDir, run.audioPath); err != nil {
				t.Fatalf("%s run %d recording validation failed: %v; record-dir=%s", testCase.Name, runNumber, err, run.recordDir)
			}

			evidence = append(evidence, liveVoiceToolGroundingEvidenceFromObservation(observation, testCase, runNumber, run))
			t.Logf("live grounding pass: probe=%s run=%d provider=%s model=%s tool=%s call_id=%s arguments=%q result=%q spoken=%q terminal=%s output_audio_bytes=%d capture=%s record_dir=%s", testCase.Name, runNumber, observation.Provider, observation.Model, observation.ToolName, observation.ToolCallID, observation.ToolArguments, observation.FunctionCallOutput, observation.SpokenReply, observation.TerminalStatus, observation.AudioBytesAfterTool, run.capturePath, run.recordDir)
		}
	}
	if len(evidence) != 5 {
		t.Fatalf("live grounding evidence count=%d, want five runs", len(evidence))
	}

	data, err := json.MarshalIndent(struct {
		Provider string                           `json:"provider"`
		Model    string                           `json:"model"`
		Runs     []liveVoiceToolGroundingEvidence `json:"runs"`
	}{
		Provider: liveProviderOpenAI,
		Model:    liveVoiceToolGroundingModel,
		Runs:     evidence,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal live grounding evidence: %v", err)
	}
	evidencePath := filepath.Join(artifactRoot, "evidence.json")
	if err := os.WriteFile(evidencePath, data, 0o600); err != nil {
		t.Fatalf("write live grounding evidence: %v", err)
	}
	t.Logf("live grounding evidence: %s", evidencePath)
}

type liveVoiceToolGroundingRun struct {
	capture     gwtesting.SessionCapture
	capturePath string
	recordDir   string
	audioPath   string
}

func runLiveVoiceToolGrounding(t *testing.T, apiKey, artifactRoot string, testCase liveVoiceToolGroundingCase, runNumber int, inputPath string) liveVoiceToolGroundingRun {
	t.Helper()
	runName := fmt.Sprintf("%s-%d", testCase.Name, runNumber)
	capturePath := filepath.Join(artifactRoot, runName+".session.json")
	recordDir := filepath.Join(artifactRoot, runName+"-recording")
	audioPath := filepath.Join(artifactRoot, runName+".wav")

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
		"--config-dir", t.TempDir(),
		"session",
		"--provider", liveProviderOpenAI,
		"--model", liveVoiceToolGroundingModel,
		"--api-key", apiKey,
		"--record", capturePath,
		"--record-dir", recordDir,
		"--audio-in", inputPath,
		"--audio-out", audioPath,
		"--max-duration", liveVoiceToolGroundingTimeout.String(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), liveVoiceToolGroundingTimeout+15*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("live %s run %d returned an error: %v; stdout=%q stderr=%q", testCase.Name, runNumber, err, stdout.String(), stderr.String())
	}
	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load live %s run %d capture: %v; stdout=%q stderr=%q", testCase.Name, runNumber, err, stdout.String(), stderr.String())
	}
	return liveVoiceToolGroundingRun{capture: capture, capturePath: capturePath, recordDir: recordDir, audioPath: audioPath}
}

func validateLiveVoiceToolGroundingObservation(observation liveVoiceToolGroundingObservation, testCase liveVoiceToolGroundingCase) error {
	if err := validateLiveVoiceToolGroundingContract(observation, testCase); err != nil {
		return err
	}
	return validateLiveVoiceToolGroundingCase(observation, testCase)
}

// validateLiveVoiceToolGroundingContract checks the case-independent session,
// tool, and reply ordering contract.
func validateLiveVoiceToolGroundingContract(observation liveVoiceToolGroundingObservation, testCase liveVoiceToolGroundingCase) error {
	if observation.Provider != liveProviderOpenAI || observation.Model != liveVoiceToolGroundingModel {
		return fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.Provider, observation.Model, liveVoiceToolGroundingModel)
	}
	if strings.TrimSpace(observation.Instructions) == "" {
		return fmt.Errorf("session instructions are empty")
	}
	if strings.Count(observation.Instructions, "Tool-grounding requirements:") != 1 {
		return fmt.Errorf("grounding policy count=%d, want exactly one", strings.Count(observation.Instructions, "Tool-grounding requirements:"))
	}
	if strings.Contains(observation.Instructions, "No tools are currently registered") {
		return fmt.Errorf("instructions contradict the advertised tools")
	}
	if !strings.Contains(strings.ToLower(observation.Instructions), "corresponding tool result") || !strings.Contains(strings.ToLower(observation.Instructions), "non-zero command exits") {
		return fmt.Errorf("instructions omit the result/error grounding contract")
	}
	if !hasExactlyDefaultLiveTools(observation.AdvertisedTools) {
		return fmt.Errorf("advertised tools=%v, want the default tool set", observation.AdvertisedTools)
	}
	if observation.ToolName != testCase.ExpectedTool {
		return fmt.Errorf("tool call name=%q, want %q", observation.ToolName, testCase.ExpectedTool)
	}
	if observation.ResponseCreatesAfterTool < 1 {
		return fmt.Errorf("no response.create followed the delivered function_call_output")
	}
	if observation.SpokenReplyIndex <= observation.FunctionOutputIndex || strings.TrimSpace(observation.SpokenReply) == "" {
		return fmt.Errorf("spoken reply is absent or precedes the delivered function_call_output")
	}
	if observation.TerminalIndex <= observation.SpokenReplyIndex {
		return fmt.Errorf("terminal response.done is absent or precedes the spoken reply")
	}
	if observation.TerminalStatus == rtStatusFailed || observation.TerminalStatus == rtStatusCancelled || observation.TerminalStatus == rtStatusIncomplete {
		return fmt.Errorf("terminal response status=%q", observation.TerminalStatus)
	}
	if observation.AudioBytesAfterTool == 0 {
		return fmt.Errorf("no spoken output audio arrived after the delivered function_call_output")
	}
	if strings.TrimSpace(observation.FunctionCallOutput) == "" {
		return fmt.Errorf("function_call_output is empty")
	}
	return nil
}

// liveVoiceToolGroundingArguments are the read_file and exec arguments.
type liveVoiceToolGroundingArguments struct {
	Path    string `json:"path"`
	Command string `json:"command"`
}

// validateLiveVoiceToolGroundingCase checks the case-specific tool arguments,
// tool result, and honest spoken reply.
func validateLiveVoiceToolGroundingCase(observation liveVoiceToolGroundingObservation, testCase liveVoiceToolGroundingCase) error {
	var arguments liveVoiceToolGroundingArguments
	if err := json.Unmarshal([]byte(observation.ToolArguments), &arguments); err != nil {
		return fmt.Errorf("decode %s arguments: %w", testCase.ExpectedTool, err)
	}
	result := strings.ToLower(observation.FunctionCallOutput)
	reply := strings.ToLower(observation.SpokenReply)
	switch testCase.Name {
	case "missing-file":
		return validateLiveVoiceMissingFileCase(observation, arguments, result, reply)
	case "exit-42":
		return validateLiveVoiceExit42Case(observation, arguments, result, reply)
	case "date-control":
		return validateLiveVoiceDateControlCase(observation, arguments, result, reply)
	}
	return nil
}

func validateLiveVoiceMissingFileCase(observation liveVoiceToolGroundingObservation, arguments liveVoiceToolGroundingArguments, result, reply string) error {
	if !isLiveVoiceToolGroundingMissingPath(arguments.Path) {
		return fmt.Errorf("read_file path=%q, want the designated missing path", arguments.Path)
	}
	if !strings.Contains(result, arguments.Path) || !containsLiveVoiceGroundingTerm(result, "no such file", "does not exist", "not exist", "missing") {
		return fmt.Errorf("read_file result=%q does not contain the missing-file error", observation.FunctionCallOutput)
	}
	if !containsLiveVoiceGroundingTerm(reply, "file", "read") || !containsLiveVoiceGroundingTerm(reply, "does not exist", "doesn't exist", "no such", "missing", "not found", "could not", "couldn't", "unable") {
		return fmt.Errorf("spoken missing-file reply=%q is not an honest failure", observation.SpokenReply)
	}
	return nil
}

func validateLiveVoiceExit42Case(observation liveVoiceToolGroundingObservation, arguments liveVoiceToolGroundingArguments, result, reply string) error {
	if !strings.Contains(strings.ToLower(arguments.Command), "exit 42") {
		return fmt.Errorf("exec command=%q does not contain exit 42", arguments.Command)
	}
	if !strings.Contains(result, "42") || !containsLiveVoiceGroundingTerm(result, "exit code", "exit status") {
		return fmt.Errorf("exec result=%q does not contain exit status 42", observation.FunctionCallOutput)
	}
	if !strings.Contains(reply, "42") || !containsLiveVoiceGroundingTerm(reply, "exit", "status", "code", "non-zero", "nonzero") {
		return fmt.Errorf("spoken exit-42 reply=%q does not report the returned failure", observation.SpokenReply)
	}
	if containsLiveVoiceGroundingTerm(reply, "successfully", "succeeded", "successful") {
		return fmt.Errorf("spoken exit-42 reply=%q claims success", observation.SpokenReply)
	}
	return nil
}

func validateLiveVoiceDateControlCase(observation liveVoiceToolGroundingObservation, arguments liveVoiceToolGroundingArguments, result, reply string) error {
	if !strings.Contains(strings.ToLower(arguments.Command), "date") {
		return fmt.Errorf("date control command=%q does not run date", arguments.Command)
	}
	date := liveVoiceToolGroundingDate.FindString(observation.FunctionCallOutput)
	if date == "" {
		return fmt.Errorf("date result=%q has no YYYY-MM-DD value", observation.FunctionCallOutput)
	}
	if !containsLiveVoiceGroundingDate(observation.SpokenReply, date) {
		return fmt.Errorf("spoken date reply=%q omits returned date %q", observation.SpokenReply, date)
	}
	return nil
}

func hasExactlyDefaultLiveTools(got []string) bool {
	want := make(map[string]struct{}, len(config.DefaultToolIDs))
	for _, name := range config.DefaultToolIDs {
		want[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(got))
	for _, name := range got {
		seen[name] = struct{}{}
	}
	if len(seen) != len(want) {
		return false
	}
	for name := range want {
		if _, ok := seen[name]; !ok {
			return false
		}
	}
	return true
}

func containsLiveVoiceGroundingTerm(value string, terms ...string) bool {
	value = strings.NewReplacer("’", "'", "‘", "'").Replace(strings.ToLower(value))
	for _, term := range terms {
		if strings.Contains(value, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func containsLiveVoiceGroundingDate(reply, isoDate string) bool {
	if strings.Contains(reply, isoDate) {
		return true
	}
	parsed, err := time.Parse("2006-01-02", isoDate)
	if err != nil {
		return false
	}
	for _, formatted := range []string{
		parsed.Format("January 2, 2006"),
		parsed.Format("January 02, 2006"),
		parsed.Format("Jan 2, 2006"),
		parsed.Format("01/02/2006"),
		parsed.Format("2006/01/02"),
	} {
		if strings.Contains(strings.ToLower(reply), strings.ToLower(formatted)) {
			return true
		}
	}
	ordinal := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(parsed.Format("January 2")) + `(st|nd|rd|th)?[,]?\s+` + parsed.Format("2006") + `\b`)
	return ordinal.MatchString(reply)
}

func validateLiveVoiceToolGroundingArtifacts(recordDir, audioPath string) error {
	for _, relative := range []string{
		"manifest.json",
		"client.transcript.jsonl",
		"agent.transcript.jsonl",
		"session-log.jsonl",
		"audio/in-000.pcm",
		"audio/out-000.pcm",
	} {
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

type liveVoiceToolGroundingEvidence struct {
	Probe                string   `json:"probe"`
	Run                  int      `json:"run"`
	Request              string   `json:"request"`
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

func liveVoiceToolGroundingEvidenceFromObservation(observation liveVoiceToolGroundingObservation, testCase liveVoiceToolGroundingCase, runNumber int, run liveVoiceToolGroundingRun) liveVoiceToolGroundingEvidence {
	digest := sha256.Sum256([]byte(observation.Instructions))
	return liveVoiceToolGroundingEvidence{
		Probe:                testCase.Name,
		Run:                  runNumber,
		Request:              testCase.Request,
		Provider:             observation.Provider,
		Model:                observation.Model,
		GroundingInstruction: true,
		InstructionSHA256:    hex.EncodeToString(digest[:]),
		AdvertisedTools:      append([]string(nil), observation.AdvertisedTools...),
		ToolName:             observation.ToolName,
		ToolCallID:           observation.ToolCallID,
		ToolArguments:        observation.ToolArguments,
		FunctionCallOutput:   observation.FunctionCallOutput,
		SpokenReply:          observation.SpokenReply,
		TerminalStatus:       observation.TerminalStatus,
		OutputAudioBytes:     observation.AudioBytesAfterTool,
		CapturePath:          run.capturePath,
		RecordDir:            run.recordDir,
		AudioPath:            run.audioPath,
		Pass:                 true,
	}
}

func assertMissingVoiceToolGroundingPath(t *testing.T) {
	t.Helper()
	for _, path := range []string{liveVoiceToolGroundingMissingPath, "/tmp/definitely-missing-file.txt"} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%s exists; refusing to alter customer state", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
}

func isLiveVoiceToolGroundingMissingPath(path string) bool {
	return path == liveVoiceToolGroundingMissingPath || path == "/tmp/definitely-missing-file.txt"
}

func liveVoiceToolGroundingInput(t *testing.T, testCase liveVoiceToolGroundingCase) string {
	t.Helper()
	if directory := strings.TrimSpace(os.Getenv(liveVoiceToolGroundingInputDir)); directory != "" {
		path := filepath.Join(directory, testCase.AudioName)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			t.Fatalf("live input %s is missing or empty: %s (%v)", testCase.Name, path, err)
		}
		return path
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("live input %s requires macOS say/afconvert or %s", testCase.AudioName, liveVoiceToolGroundingInputDir)
	}
	for _, command := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("live input generation requires %s or %s", command, liveVoiceToolGroundingInputDir)
		}
	}

	directory := t.TempDir()
	aiffPath := filepath.Join(directory, testCase.Name+".aiff")
	wavPath := filepath.Join(directory, testCase.AudioName)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "say", "-o", aiffPath, testCase.Request).CombinedOutput(); err != nil {
		t.Fatalf("generate %s spoken input: %v: %s", testCase.Name, err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.CommandContext(ctx, "afconvert", "-f", "WAVE", "-d", "LEI16@16000", aiffPath, wavPath).CombinedOutput(); err != nil {
		t.Fatalf("convert %s spoken input to WAV: %v: %s", testCase.Name, err, strings.TrimSpace(string(output)))
	}
	return wavPath
}

func liveVoiceToolGroundingArtifactRoot(t *testing.T) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(liveVoiceToolGroundingArtifactDir))
	if parent == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create live grounding artifact parent: %v", err)
	}
	root, err := os.MkdirTemp(parent, "s2s-voice-tool-grounding-")
	if err != nil {
		t.Fatalf("create live grounding artifact directory: %v", err)
	}
	return root
}
