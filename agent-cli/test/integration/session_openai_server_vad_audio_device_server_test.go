package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/clitest"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/test/integration/testcmd/mocktool"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	serverVADInterruptedAudioFixture = "audio/openai-server-vad-interrupted-output.base64"
	serverVADInterruptedAudioBytes   = 1440
	serverVADInterruptedAudioSHA256  = "bf9f2ae334b63a8f5e4f6868cebfbe3d64a07a34bfefcbef1c0cfac4806c78d6"
)

// TestAgentBinaryOpenAIServerVADBargeInUsesRemoteAudioDevice starts both
// shipped binaries as child processes. The agent sees only an OpenAI replay
// socket and the public --audio-device-server flag; assertions inspect the
// provider wire contract and device-server evidence rather than Go internals.
func TestAgentBinaryOpenAIServerVADBargeInUsesRemoteAudioDevice(t *testing.T) {
	endpoint, stopServer := startAudioDeviceServerBinary(t, true)
	defer stopServer()

	delta := loadServerVADInterruptedAudio(t)
	capturePath := filepath.Join(t.TempDir(), "openai-server-vad-barge-in-before-first-callback.session.json")
	writeServerVADBargeInCapture(t, capturePath, delta)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, agentBinaryPath,
		"session",
		"--replay", capturePath,
		"--prompt", "replay server VAD barge in",
		"--audio-device-server", endpoint,
		"--audio-out-device=",
	)
	command.Stdout = io.Discard
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("run agent binary with remote audio-device server: %v; stderr=%q", err, stderr.String())
	}

	snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(ctx, endpoint)
	if err != nil {
		t.Fatalf("read cross-process device evidence: %v", err)
	}
	if len(snapshot.RenderedSamples) != 0 {
		t.Fatalf("rendered samples before first callback = %d, want zero", len(snapshot.RenderedSamples))
	}
	if snapshot.Playback.QueuedSamples != 0 || snapshot.Playback.DroppedSamples != 0 {
		t.Fatalf("playback queue after server VAD = %+v", snapshot.Playback)
	}
}

func TestAudioDeviceServerBinaryDefaultClockRunsWithoutController(t *testing.T) {
	endpoint, stopServer := startAudioDeviceServerBinary(t, false)
	defer stopServer()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(ctx, endpoint)
		if err != nil {
			t.Fatalf("read realtime device snapshot: %v", err)
		}
		if snapshot.Playback.CallbackCount > 0 && snapshot.Capture.CompletedFrames > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("default device clock did not advance: playback=%+v capture=%+v", snapshot.Playback, snapshot.Capture)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func startAudioDeviceServerBinary(t *testing.T, manualClock bool) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	arguments := []string{"--listen", "127.0.0.1:0", "--sample-rate", "16000", "--render-quantum", "480", "--capture-quantum", "480"}
	if manualClock {
		arguments = append(arguments, "--manual-clock")
	}
	command := exec.CommandContext(ctx, audioDeviceServerBinaryPath, arguments...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("open audio-device server stdout: %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("start audio-device server: %v", err)
	}
	ready := make(chan []byte, 1)
	go func() { ready <- readAnnouncementLine(stdout) }()
	var line []byte
	select {
	case line = <-ready:
	case <-time.After(30 * time.Second): // absorbs a fresh binary's first exec on a loaded host
		cancelAndReap(cancel, command)
		t.Fatalf("audio-device server did not become ready; stderr=%q", stderr.String())
	}
	var announcement struct {
		Endpoint string `json:"endpoint"`
		Input    string `json:"input_device"`
		Output   string `json:"output_device"`
	}
	if err := json.Unmarshal(line, &announcement); err != nil {
		cancelAndReap(cancel, command)
		t.Fatalf("decode audio-device server ready line %q: %v; stderr=%q", line, err, stderr.String())
	}
	if announcement.Endpoint == "" || announcement.Input != "simulated-duplex:input" || announcement.Output != "simulated-duplex:output" {
		cancelAndReap(cancel, command)
		t.Fatalf("audio-device server announcement = %+v", announcement)
	}
	return announcement.Endpoint, func() {
		cancel()
		if err := command.Wait(); err != nil && ctx.Err() == nil {
			t.Errorf("wait for audio-device server: %v; stderr=%q", err, stderr.String())
		}
	}
}

func loadServerVADInterruptedAudio(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(locateCLIFixture(t, serverVADInterruptedAudioFixture))
	if err != nil {
		t.Fatalf("read server-VAD audio fixture: %v", err)
	}
	encoded := strings.TrimSpace(string(data))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode server-VAD audio fixture: %v", err)
	}
	if len(decoded) != serverVADInterruptedAudioBytes {
		t.Fatalf("server-VAD fixture bytes = %d, want %d", len(decoded), serverVADInterruptedAudioBytes)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(decoded)); got != serverVADInterruptedAudioSHA256 {
		t.Fatalf("server-VAD fixture SHA-256 = %s, want %s", got, serverVADInterruptedAudioSHA256)
	}
	return encoded
}

func writeServerVADBargeInCapture(t *testing.T, path, delta string) {
	t.Helper()
	sequence := 0
	records := make([]gwtesting.CapturedSessionEvent, 0, 10)
	add := func(direction gwtesting.SessionEventDirection, payload any) {
		sequence++
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal server-VAD replay event %d: %v", sequence, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("decode server-VAD replay event type %d: %v", sequence, err)
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: int64(sequence), Type: envelope.Type,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: data,
		})
	}
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": rtEventSessionUpdate, "session": map[string]any{
			"model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": rtEventSessionCreated, "session": map[string]any{
			"id": "sess-server-vad-binary", "type": "realtime", "model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": rtEventConversationItemCreate, "item": map[string]any{
			"type": rtItemMessage, "role": rtRoleUser, "content": []map[string]any{{"type": "input_text", "text": "replay server VAD barge in"}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{"type": rtEventResponseCreate})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": rtEventResponseCreated, "response": map[string]any{"id": "resp-server-vad-binary", "status": "in_progress"},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": rtEventOutputAudioDelta, "response_id": "resp-server-vad-binary", "item_id": "item-server-vad-binary",
		"output_index": 0, "content_index": 0, "delta": delta,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "input_audio_buffer.speech_started", "audio_start_ms": 7156, "item_id": "item-user-server-vad",
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.truncate", "item_id": "item-server-vad-binary", "content_index": 0, "audio_end_ms": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-server-vad-binary", "item_id": "item-server-vad-binary",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": rtEventResponseDone, "response": map[string]any{
			"id": "resp-server-vad-binary", "status": rtStatusCancelled, "status_details": map[string]any{"type": rtStatusCancelled},
		},
	})
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1-mini"},
		Session:  gwtesting.SessionMetadata{ID: "sess-server-vad-binary", StartedAtUTC: "2026-09-01T23:11:01.000000Z"},
		Records:  records,
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal server-VAD replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write server-VAD replay capture: %v", err)
	}
}

// readAnnouncementLine reads the server's one-line readiness announcement. A
// read failure yields the read error text, which the caller's decode step
// rejects and reports together with the captured stderr.
func readAnnouncementLine(stdout io.Reader) []byte {
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		return []byte(fmt.Sprintf("%s (read error: %v)", line, err))
	}
	return line
}

// cancelAndReap stops a server that failed readiness and reaps the process.
// The cancellation kills it, so the wait's kill-signal error is expected.
func cancelAndReap(cancel context.CancelFunc, command *exec.Cmd) {
	cancel()
	if err := command.Wait(); err != nil {
		return
	}
}

// remoteToolAudioDevice is the explicitly clocked duplex device a scenario
// renders through: the audio-device-server binary over loopback HTTP, or the
// same simulated registry used in-process.
type remoteToolAudioDevice interface {
	Advance(ctx context.Context, callbacks int) error
	InjectCapture(ctx context.Context, samples []int16) error
	Snapshot(ctx context.Context) (devicegw.DeviceServerSnapshot, error)
}

// remoteDeviceServer drives an audio-device-server process.
type remoteDeviceServer struct{ endpoint string }

func (d remoteDeviceServer) Advance(ctx context.Context, callbacks int) error {
	return devicegw.AdvanceRemoteDeviceServer(ctx, d.endpoint, callbacks)
}

func (d remoteDeviceServer) InjectCapture(ctx context.Context, samples []int16) error {
	return devicegw.InjectRemoteDeviceServerCapture(ctx, d.endpoint, samples)
}

func (d remoteDeviceServer) Snapshot(ctx context.Context) (devicegw.DeviceServerSnapshot, error) {
	return devicegw.ReadRemoteDeviceServerSnapshot(ctx, d.endpoint)
}

// inProcessDuplexDevice is the manually clocked simulated registry that the
// audio-device-server binary serves, opened directly by an in-process agent.
type inProcessDuplexDevice struct {
	registry *devicegw.SimulatedDuplexRegistry
}

func newInProcessDuplexDevice(t *testing.T) inProcessDuplexDevice {
	t.Helper()
	clock := devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}}
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{Render: clock, Capture: clock})
	if err != nil {
		t.Fatalf("create simulated duplex device: %v", err)
	}
	return inProcessDuplexDevice{registry: registry}
}

func (d inProcessDuplexDevice) Advance(_ context.Context, callbacks int) error {
	return d.registry.Advance(callbacks) //nolint:contextcheck // the simulated registry advances synchronously in memory; only the HTTP device server takes the context.
}

func (d inProcessDuplexDevice) InjectCapture(_ context.Context, samples []int16) error {
	d.registry.InjectNearEnd(samples)
	return nil
}

func (d inProcessDuplexDevice) Snapshot(context.Context) (devicegw.DeviceServerSnapshot, error) {
	return devicegw.DeviceServerSnapshot{
		Playback: d.registry.PlaybackStats(), Capture: d.registry.CaptureStats(),
		RenderedSamples: d.registry.RenderedSamples(), CapturedSamples: d.registry.CapturedSamples(), Trace: d.registry.Trace(),
	}, nil
}

// remoteToolAudioAgent is one running agent session of a scenario; done
// reports its exit like exec.Cmd.Wait.
type remoteToolAudioAgent struct {
	done           <-chan error
	stdout, stderr *remoteToolAudioBuffer
}

// startRemoteToolAudioTopology starts the scenario's device, serves its
// provider, and returns how to start the agent against them. An in-process
// topology must run inside a clitest bubble: the provider speaks real
// WebSocket over in-memory pipes and the agent opens the simulated device
// directly, so the device clock and every provider and tool delay are
// virtual.
func startRemoteToolAudioTopology(t *testing.T, testCase remoteToolAudioCase, provider *remoteToolAudioProvider) (remoteToolAudioDevice, func(context.Context, []string, remoteToolAudioPaths) remoteToolAudioAgent) {
	t.Helper()
	if !testCase.inProcess {
		provider.serveHTTP()
		endpoint, stopDevice := startAudioDeviceServerBinary(t, true)
		t.Cleanup(stopDevice)
		return remoteDeviceServer{endpoint: endpoint}, func(ctx context.Context, arguments []string, paths remoteToolAudioPaths) remoteToolAudioAgent {
			arguments = append([]string{"--audio-device-server", endpoint, "--base-url", provider.WebSocketURL()}, arguments...)
			return startRemoteToolAudioProcess(t, ctx, testCase, arguments, paths)
		}
	}
	listener := clitest.NewPipeListener()
	clitest.Serve(t, listener, http.HandlerFunc(provider.handle))
	device := newInProcessDuplexDevice(t)
	return device, func(_ context.Context, arguments []string, paths remoteToolAudioPaths) remoteToolAudioAgent {
		arguments = append([]string{"--base-url", "ws://provider.pipe"}, arguments...)
		return startRemoteToolAudioInProcess(t, testCase, arguments, paths, listener, device) //nolint:contextcheck // composed like cmd/agent's main, which has no context; the command runs under the bubble's test context.
	}
}

func startRemoteToolAudioProcess(t *testing.T, ctx context.Context, testCase remoteToolAudioCase, arguments []string, paths remoteToolAudioPaths) remoteToolAudioAgent {
	t.Helper()
	binaryPath := agentBinaryPath
	if paths.fixture != "" {
		binaryPath = mockToolAgentBinaryPath
	}
	command := exec.CommandContext(ctx, binaryPath, remoteToolAudioSessionArgs(paths.configDir, arguments)...)
	command.Env = remoteToolAudioEnvironment(os.Environ(), paths.fixture, testCase.holdToneControl)
	agent := remoteToolAudioAgent{stdout: &remoteToolAudioBuffer{}, stderr: &remoteToolAudioBuffer{}}
	command.Stdout, command.Stderr = agent.stdout, agent.stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start agent binary: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	agent.done = done
	return agent
}

// startRemoteToolAudioInProcess runs the session command the binaries run,
// composed like the mock-tool agent when the scenario has tools.
func startRemoteToolAudioInProcess(t *testing.T, testCase remoteToolAudioCase, arguments []string, paths remoteToolAudioPaths, listener *clitest.PipeListener, device inProcessDuplexDevice) remoteToolAudioAgent {
	t.Helper()
	ports := []wire.PortSwap{
		wire.NewPortSwap(wire.PortTransportDialer, clitest.WebSocketDialer(listener)),
		wire.NewPortSwap(wire.PortDeviceRegistry, wire.DeviceRegistry(device.registry)),
	}
	var executor *mocktool.FixtureExecutor
	var configure func(*cli.AgentCLI)
	if paths.fixture != "" {
		var err error
		if executor, err = mocktool.LoadFixtureExecutor(paths.fixture); err != nil {
			t.Fatalf("load tool mock fixture: %v", err)
		}
		ports = append(ports, mocktool.Ports(executor)...)
		configure = func(agentCLI *cli.AgentCLI) { mocktool.ConfigureHoldTone(agentCLI, !testCase.holdToneControl) }
	}
	agent := remoteToolAudioAgent{stdout: &remoteToolAudioBuffer{}, stderr: &remoteToolAudioBuffer{}}
	process := clitest.Start(t, clitest.Invocation{
		Args: remoteToolAudioSessionArgs(paths.configDir, arguments), Ports: ports, Configure: configure,
		// The mock-tool-agent binary composes with the mock initializer's
		// relaxed validation; match it, and stay strict like cmd/agent otherwise.
		RelaxModelValidation: executor != nil,
		Stdout:               agent.stdout, Stderr: agent.stderr,
	})
	done := make(chan error, 1)
	go func() {
		result := process.Wait()
		switch {
		case result.ExitCode != 0:
			done <- fmt.Errorf("exit status %d", result.ExitCode)
		case executor != nil:
			done <- executor.Verify()
		default:
			done <- nil
		}
	}()
	agent.done = done
	return agent
}

func primeRemoteToolAudioInput(t *testing.T, ctx context.Context, device remoteToolAudioDevice, provider *remoteToolAudioProvider, frames int) {
	t.Helper()
	select {
	case <-provider.sessionReady:
	case <-ctx.Done():
		t.Fatalf("provider did not become ready for input-history prelude: %v", ctx.Err())
	}
	samples := remoteToolAudioPCM(frames*audio.FrameSize, 400)
	if err := device.InjectCapture(ctx, samples); err != nil {
		t.Fatalf("inject prior input history: %v", err)
	}
	for advanced := 0; advanced < frames; {
		batch := 8
		if remaining := frames - advanced; remaining < batch {
			batch = remaining
		}
		if err := device.Advance(ctx, batch); err != nil {
			t.Fatalf("advance prior input callback: %v", err)
		}
		advanced += batch
		// The final fractional phase needs the next capture sample.
		// Do not wait for an EOF tail while this capture stream is live.
		wantSeen := advanced*audio.FrameSize*3/2 - 1
		deadline := time.NewTimer(time.Second)
		for provider.Snapshot().inputSamplesSeen < wantSeen {
			select {
			case <-deadline.C:
				t.Fatalf("provider received %d/%d prior input samples after %d callbacks", provider.Snapshot().inputSamplesSeen, wantSeen, advanced)
			case <-ctx.Done():
				deadline.Stop()
				t.Fatalf("prior input history cancelled: %v", ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
		deadline.Stop()
	}
	// Release the final fractional resampling phase with a silence callback.
	// The stream remains open; no artificial EOF/turn boundary is introduced.
	if err := device.Advance(ctx, 1); err != nil {
		t.Fatalf("advance capture lookahead: %v", err)
	}
}

func driveRemoteToolAudioClock(ctx context.Context, device remoteToolAudioDevice, start <-chan struct{}, interval time.Duration, callbackAdvances *atomic.Uint64, result chan<- error) {
	select {
	case <-start:
	case <-ctx.Done():
		result <- nil
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result <- nil
			return
		case <-ticker.C:
			if err := device.Advance(ctx, 1); err != nil {
				if ctx.Err() != nil {
					result <- nil
					return
				}
				result <- err
				return
			}
			callbackAdvances.Add(1)
		}
	}
}
