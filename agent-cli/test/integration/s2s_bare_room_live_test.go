//go:build live

package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

const (
	bareRoomLiveProbeOptInEnv        = "AGENT_HARNESS_LIVE_BARE_ROOM"
	bareRoomLiveProbeStartupTimeout  = 10 * time.Second
	bareRoomLiveProbeTeardownTimeout = 10 * time.Second
	bareRoomLiveProbeKillTimeout     = 2 * time.Second
)

// TestLiveBareRoomRunDefaultDevices is the one bounded, credential-gated
// process probe for the flagship zero-flag room. It deliberately supplies no
// room configuration, provider/model/device selection, recording destination,
// or scripted microphone input. The hermetic room tests own correctness; this
// test only confirms that the shipped binary reaches the same readiness shape
// on the host's current default devices and then exits cleanly on SIGINT.
func TestLiveBareRoomRunDefaultDevices(t *testing.T) {
	if os.Getenv(bareRoomLiveProbeOptInEnv) != "1" {
		t.Skipf("SKIP: %s!=1; bare-room live probe is explicit opt-in", bareRoomLiveProbeOptInEnv)
	}
	apiKey := strings.TrimSpace(os.Getenv(services.DefaultRoomCredentialEnv))
	if apiKey == "" {
		t.Skipf("BLOCKED: %s is not set; bare-room live probe has no credential", services.DefaultRoomCredentialEnv)
	}

	registry := audio.NewHostDeviceRegistry()
	input, err := registry.Default(audio.DirectionInput)
	if err != nil {
		t.Skipf("BLOCKED: host default input is unavailable: %v", err)
	}
	output, err := registry.Default(audio.DirectionOutput)
	if err != nil {
		t.Skipf("BLOCKED: host default output is unavailable: %v", err)
	}

	configDir := t.TempDir()
	process, err := startBareRoomLiveProbe(t, configDir, apiKey)
	if err != nil {
		t.Fatalf("start bare-room live probe: %v", err)
	}
	defer process.terminate()

	readiness, err := process.awaitRunning(input.ID, output.ID)
	if err != nil {
		process.terminate()
		t.Fatalf("bare-room live startup probe failed: %v\n%s", err, process.diagnostics())
	}

	signalAt := time.Now()
	if err := process.command.Process.Signal(os.Interrupt); err != nil {
		process.terminate()
		t.Fatalf("send SIGINT to bare-room live probe: %v\n%s", err, process.diagnostics())
	}
	teardownStarted := time.Now()
	if err := process.awaitExit(bareRoomLiveProbeTeardownTimeout); err != nil {
		process.terminate()
		t.Fatalf("bare-room live teardown probe failed: %v\n%s", err, process.diagnostics())
	}
	teardownDuration := time.Since(teardownStarted)
	if process.waitErr != nil {
		t.Fatalf("bare-room live probe exited unsuccessfully after SIGINT: %v\n%s", process.waitErr, process.diagnostics())
	}
	if signalAt.Before(readiness.observedAt) {
		t.Fatalf("bare-room live probe sent SIGINT before readiness: signal=%s readiness=%s", signalAt, readiness.observedAt)
	}

	if err := validateBareRoomLiveOutput(process.linesSeen, readiness); err != nil {
		t.Fatalf("bare-room live output contract: %v\n%s", err, process.diagnostics())
	}
	if err := validateBareRoomLiveArtifacts(readiness.outputDir, apiKey, configDir, readiness.customerInput, readiness.customerOutput); err != nil {
		t.Fatalf("bare-room live artifact contract: %v", err)
	}

	t.Logf("bare-room live probe: command=agent --config-dir <temp> room run; startup=%s; customer=input:%s output:%s; agent=provider:openai model:%s; SIGINT=sent; teardown=%s; result=stopped active=0; artifacts=%s",
		readiness.startupDuration.Round(time.Millisecond), input.ID, output.ID, readiness.agentModel,
		teardownDuration.Round(time.Millisecond), readiness.outputDir)
}

type bareRoomLiveReadiness struct {
	customerInput   string
	customerOutput  string
	agentModel      string
	outputDir       string
	startupDuration time.Duration
	observedAt      time.Time
}

type bareRoomLiveReadyParticipant struct {
	id       string
	kind     string
	input    string
	output   string
	provider string
	model    string
}

type bareRoomLiveProcess struct {
	command    *exec.Cmd
	lines      <-chan string
	scanDone   <-chan error
	stderrDone <-chan string
	wait       <-chan error

	linesSeen  []string
	waitErr    error
	waited     bool
	linesOpen  bool
	scanErr    error
	stderr     string
	stderrRead bool
}

func startBareRoomLiveProbe(t *testing.T, configDir, apiKey string) (*bareRoomLiveProcess, error) {
	t.Helper()
	command := exec.Command(buildAgentBinary(t), bareRoomLiveProbeArgs(configDir)...)
	command.Dir = agentCLIRoot(t)
	command.Env = bareRoomLiveProbeEnvironment(apiKey)

	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capture stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, err
	}

	lines := make(chan string, 64)
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- redactBareRoomLiveText(scanner.Text(), apiKey)
		}
		close(lines)
		scanDone <- scanner.Err()
	}()

	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- redactBareRoomLiveText(string(data), apiKey)
	}()

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	return &bareRoomLiveProcess{
		command:    command,
		lines:      lines,
		scanDone:   scanDone,
		stderrDone: stderrDone,
		wait:       wait,
		linesOpen:  true,
	}, nil
}

func bareRoomLiveProbeArgs(configDir string) []string {
	// --config-dir is a global isolation flag. The room command receives no
	// config, participant, provider, model, device, stream, or recording flag.
	return []string{"--config-dir", configDir, "room", "run"}
}

func bareRoomLiveProbeEnvironment(apiKey string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok {
			switch name {
			case services.DefaultRoomCredentialEnv, "AGENT_MODEL__OPENAI__MODEL", "AGENT_MODEL__OPENAI__BASE_URL", "AGENT_MODEL__PROVIDER":
				continue
			}
		}
		environment = append(environment, entry)
	}
	return append(environment, services.DefaultRoomCredentialEnv+"="+apiKey)
}

func (p *bareRoomLiveProcess) awaitRunning(inputID, outputID audio.DeviceID) (bareRoomLiveReadiness, error) {
	var readiness bareRoomLiveReadiness
	startedAt := time.Now()
	ready := make(map[string]bareRoomLiveReadyParticipant, 2)
	readyEvents := 0
	deadline := time.NewTimer(bareRoomLiveProbeStartupTimeout)
	defer deadline.Stop()
	for {
		select {
		case line, open := <-p.lines:
			if !open {
				p.linesOpen = false
				p.scanErr = <-p.scanDone
				return readiness, errors.New("stdout closed before room running")
			}
			p.linesSeen = append(p.linesSeen, line)
			if fields, ok := bareRoomLineFields(line, "room starting: "); ok {
				readiness.outputDir = fields["output"]
			}
			if participant, ok := parseBareRoomLiveReadyParticipant(line); ok {
				ready[participant.id] = participant
				readyEvents++
			}
			if fields, ok := bareRoomLineFields(line, "room running: "); ok {
				if fields["participants"] != "2" {
					return readiness, fmt.Errorf("room running reported participants=%q, want 2", fields["participants"])
				}
				if readyEvents != 2 || len(ready) != 2 {
					return readiness, fmt.Errorf("participant readiness events=%d identities=%d, want exactly two", readyEvents, len(ready))
				}
				customer := ready[services.DefaultRoomCustomerID]
				agent := ready[services.DefaultRoomAgentID]
				if customer.kind != "human" || customer.input != string(inputID) || customer.output != string(outputID) {
					return readiness, fmt.Errorf("customer readiness=%+v, want human input=%q output=%q", customer, inputID, outputID)
				}
				if agent.kind != "agent" || agent.provider != "openai" || agent.model != services.DefaultOpenAIRealtimeModel || agent.input != "" || agent.output != "" {
					return readiness, fmt.Errorf("agent readiness=%+v, want openai model=%q without devices", agent, services.DefaultOpenAIRealtimeModel)
				}
				readiness.customerInput = customer.input
				readiness.customerOutput = customer.output
				readiness.agentModel = agent.model
				readiness.startupDuration = time.Since(startedAt)
				readiness.observedAt = time.Now()
				return readiness, nil
			}
		case waitErr := <-p.wait:
			p.waitErr = waitErr
			p.waited = true
			return readiness, fmt.Errorf("process exited before room running: %w", waitErr)
		case <-deadline.C:
			return readiness, fmt.Errorf("room running was not observed within %s", bareRoomLiveProbeStartupTimeout)
		}
	}
}

func (p *bareRoomLiveProcess) awaitExit(timeout time.Duration) error {
	if p == nil {
		return errors.New("bare-room live process is nil")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for p.linesOpen || !p.waited {
		select {
		case line, open := <-p.lines:
			if !open {
				p.linesOpen = false
				p.scanErr = <-p.scanDone
				continue
			}
			p.linesSeen = append(p.linesSeen, line)
		case waitErr := <-p.wait:
			p.waitErr = waitErr
			p.waited = true
		case <-deadline.C:
			return fmt.Errorf("process did not exit within %s", timeout)
		}
	}
	if !p.stderrRead {
		p.stderr = <-p.stderrDone
		p.stderrRead = true
	}
	if p.scanErr != nil {
		return fmt.Errorf("read stdout: %w", p.scanErr)
	}
	return nil
}

func (p *bareRoomLiveProcess) terminate() {
	if p == nil {
		return
	}
	if !p.waited && p.command != nil && p.command.Process != nil {
		_ = p.command.Process.Kill()
		select {
		case p.waitErr = <-p.wait:
			p.waited = true
		case <-time.After(bareRoomLiveProbeKillTimeout):
			return
		}
	}
	for p.linesOpen {
		select {
		case line, open := <-p.lines:
			if !open {
				p.linesOpen = false
				p.scanErr = <-p.scanDone
				continue
			}
			p.linesSeen = append(p.linesSeen, line)
		case <-time.After(bareRoomLiveProbeKillTimeout):
			return
		}
	}
	if !p.stderrRead {
		select {
		case p.stderr = <-p.stderrDone:
			p.stderrRead = true
		case <-time.After(bareRoomLiveProbeKillTimeout):
		}
	}
}

func (p *bareRoomLiveProcess) diagnostics() string {
	if p == nil {
		return "no process diagnostics"
	}
	if !p.stderrRead {
		select {
		case p.stderr = <-p.stderrDone:
			p.stderrRead = true
		default:
		}
	}
	stdout := strings.TrimSpace(strings.Join(p.linesSeen, "\n"))
	stderr := strings.TrimSpace(p.stderr)
	if stdout == "" {
		stdout = "<empty>"
	}
	if stderr == "" {
		stderr = "<empty>"
	}
	return fmt.Sprintf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
}

func redactBareRoomLiveText(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "<redacted>")
}

func parseBareRoomLiveReadyParticipant(line string) (bareRoomLiveReadyParticipant, bool) {
	const prefix = `participant "`
	if !strings.HasPrefix(line, prefix) {
		return bareRoomLiveReadyParticipant{}, false
	}
	remainder := line[len(prefix):]
	quote := strings.IndexByte(remainder, '"')
	if quote <= 0 {
		return bareRoomLiveReadyParticipant{}, false
	}
	id := remainder[:quote]
	fields, ok := bareRoomLineFields(remainder[quote+1:], " ready: ")
	if !ok {
		return bareRoomLiveReadyParticipant{}, false
	}
	return bareRoomLiveReadyParticipant{
		id:       id,
		kind:     fields["kind"],
		input:    fields["input"],
		output:   fields["output"],
		provider: fields["provider"],
		model:    fields["model"],
	}, true
}

func bareRoomLineFields(line, prefix string) (map[string]string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return nil, false
	}
	fields := make(map[string]string)
	for _, token := range strings.Fields(strings.TrimPrefix(line, prefix)) {
		key, value, ok := strings.Cut(token, "=")
		if ok {
			fields[key] = value
		}
	}
	return fields, true
}

func validateBareRoomLiveOutput(lines []string, readiness bareRoomLiveReadiness) error {
	if readiness.outputDir == "" {
		return errors.New("room starting line omitted output directory")
	}
	if readiness.customerInput == "" || readiness.customerOutput == "" || readiness.agentModel == "" {
		return fmt.Errorf("readiness metadata is incomplete: %+v", readiness)
	}
	runningCount := 0
	stoppedCount := 0
	for _, line := range lines {
		if line == "room running: participants=2" {
			runningCount++
		}
		if line == "room stopped: reason=stopped participants=2 active=0" {
			stoppedCount++
		}
	}
	if runningCount != 1 {
		return fmt.Errorf("room running observations=%d, want exactly one", runningCount)
	}
	if stoppedCount != 1 {
		return fmt.Errorf("clean stopped observations=%d, want exactly one", stoppedCount)
	}
	return nil
}

type bareRoomLiveEvidenceManifest struct {
	Finalized         bool                                       `json:"finalized"`
	TerminationReason string                                     `json:"termination_reason"`
	Reason            string                                     `json:"reason"`
	Participants      map[string]bareRoomLiveEvidenceParticipant `json:"participants"`
	TurnCounts        map[string]int                             `json:"turn_counts"`
	Artifacts         map[string]string                          `json:"artifacts"`
	Error             string                                     `json:"error"`
}

type bareRoomLiveEvidenceParticipant struct {
	Kind      string `json:"kind"`
	Connected bool   `json:"connected"`
	Input     string `json:"input_device"`
	Output    string `json:"output_device"`
}

func validateBareRoomLiveArtifacts(outputDir, secret, configDir, customerInput, customerOutput string) error {
	relative, err := filepath.Rel(configDir, outputDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("run directory %q is not under config directory %q", outputDir, configDir)
	}
	if !strings.HasPrefix(filepath.Base(outputDir), "room-run-") {
		return fmt.Errorf("run directory %q does not use the fresh room-run prefix", outputDir)
	}

	manifestPath := filepath.Join(outputDir, services.RoomEvidenceManifestPath)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read terminal manifest: %w", err)
	}
	if bytes.Contains(manifestData, []byte(secret)) {
		return errors.New("terminal manifest contains the provider credential")
	}
	var manifest bareRoomLiveEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("decode terminal manifest: %w", err)
	}
	if !manifest.Finalized || manifest.TerminationReason != string(services.RoomTerminationStopped) || manifest.Reason != string(services.RoomTerminationStopped) || manifest.Error != "" {
		return fmt.Errorf("terminal manifest = %+v, want finalized stopped result without error", manifest)
	}
	wantKinds := map[string]string{services.DefaultRoomCustomerID: "human", services.DefaultRoomAgentID: "agent"}
	if len(manifest.Participants) != len(wantKinds) || len(manifest.TurnCounts) != len(wantKinds) {
		return fmt.Errorf("manifest participants=%d turns=%d, want two each", len(manifest.Participants), len(manifest.TurnCounts))
	}
	for id, kind := range wantKinds {
		participant, ok := manifest.Participants[id]
		if !ok || participant.Kind != kind || !participant.Connected {
			return fmt.Errorf("manifest participant %q = %+v, want connected %s", id, participant, kind)
		}
		if id == services.DefaultRoomCustomerID && (participant.Input != customerInput || participant.Output != customerOutput) {
			return fmt.Errorf("manifest customer devices=%q/%q, want %q/%q", participant.Input, participant.Output, customerInput, customerOutput)
		}
		if manifest.TurnCounts[id] != 0 {
			return fmt.Errorf("manifest participant %q turn count=%d, want zero scripted turns", id, manifest.TurnCounts[id])
		}
	}

	wantArtifacts := []string{
		"customer.wav", "customer.diagnostics", "customer.deltas",
		"agent.wav", "agent.diagnostics", "agent.deltas",
	}
	for _, key := range wantArtifacts {
		relativePath, ok := manifest.Artifacts[key]
		if !ok || relativePath == "" {
			return fmt.Errorf("terminal manifest omitted artifact %q", key)
		}
		artifactPath := filepath.Join(outputDir, filepath.FromSlash(relativePath))
		artifactRelative, err := filepath.Rel(outputDir, artifactPath)
		if err != nil || artifactRelative == ".." || strings.HasPrefix(artifactRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(artifactRelative) {
			return fmt.Errorf("artifact %q escapes run directory", key)
		}
		data, err := os.ReadFile(artifactPath)
		if err != nil {
			return fmt.Errorf("read artifact %q: %w", key, err)
		}
		if bytes.Contains(data, []byte(secret)) {
			return fmt.Errorf("artifact %q contains the provider credential", key)
		}
		if strings.HasSuffix(key, ".wav") {
			if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
				return fmt.Errorf("artifact %q is not a readable WAV", key)
			}
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			if line := bytes.TrimSpace(scanner.Bytes()); len(line) > 0 && !json.Valid(line) {
				return fmt.Errorf("artifact %q contains invalid JSONL", key)
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read artifact %q: %w", key, err)
		}
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("list finalized run directory: %w", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			return fmt.Errorf("temporary artifact %q remained after teardown", entry.Name())
		}
	}
	return nil
}
