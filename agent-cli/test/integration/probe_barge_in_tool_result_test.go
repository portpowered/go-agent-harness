package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/clitest"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/test/integration/testnet"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// v3bFixtureDir holds the recorded barge-in-during-tool-call session fixtures
// for the s2s v3b vertical. All evidence flows through the real CLI probe run
// command in replay mode; no internal loop functions are called directly.
var v3bFixtureDir = filepath.Join("testdata")

func TestV3BBargeInDuringToolCallDeliversToolResult(t *testing.T) {
	fixture := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-delivered.session.json")
	scenarioPath := writeV3BScenario(t, "v3b-delivered", fixture, true)

	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{"probe", "run", scenarioPath, "--replay", fixture, "--json"})
	if execErr := rootCmd.ExecuteContext(context.Background()); execErr != nil {
		t.Fatalf("delivered-result scenario must pass via CLI: %v\nstderr=%s", execErr, writer.StderrString())
	}
	result := decodeSingleV3BResult(t, writer.StdoutString())
	if result["pass"] != true {
		t.Fatalf("scenario must pass: %v", result)
	}
	assertExpectationKindsPass(t, result, "tool-result-delivered", "no-orphaned-tool-result", "terminal-reason")
}

func TestV3BBargeInDuringToolCallExplicitDiscard(t *testing.T) {
	fixture := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-discarded.session.json")
	scenarioPath := writeV3BScenario(t, "v3b-discarded", fixture, true)

	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{"probe", "run", scenarioPath, "--replay", fixture, "--json"})
	if execErr := rootCmd.ExecuteContext(context.Background()); execErr != nil {
		t.Fatalf("discard scenario must exit cleanly: %v\nstderr=%s", execErr, writer.StderrString())
	}
	result := decodeSingleV3BResult(t, writer.StdoutString())
	if result["pass"] != true {
		t.Fatalf("scenario must pass: %v", result)
	}
	assertExpectationKindsPass(t, result, "tool-result-discarded", "no-orphaned-tool-result", "terminal-reason")
}

func TestV3BNegativeControlOrphanedToolResultFails(t *testing.T) {
	fixture := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-orphaned.session.json")
	scenarioPath := writeV3BScenario(t, "v3b-orphaned", fixture, false)

	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{"probe", "run", scenarioPath, "--replay", fixture, "--json"})
	execErr := rootCmd.ExecuteContext(context.Background())
	if execErr == nil {
		t.Fatalf("orphaned tool result must fail the CLI run")
	}
	if !strings.Contains(execErr.Error(), "1 of 1 probe scenarios failed") {
		t.Fatalf("failure must be reported: %v", execErr)
	}
	result := decodeSingleV3BResult(t, writer.StdoutString())
	if result["pass"] != false {
		t.Fatalf("orphaned negative control must fail: %v", result)
	}
	outcomes := mustAs[[]any](t, result["expectations"])
	failed := false
	for _, raw := range outcomes {
		outcome := mustAs[map[string]any](t, raw)
		if outcome["kind"] == "no-orphaned-tool-result" && outcome["passed"] == false {
			failed = true
			detail := fmt.Sprint(outcome["actual"])
			if !strings.Contains(detail, "call_v3b_weather") {
				t.Fatalf("failure must name the orphaned tool call: %v", outcome)
			}
		}
	}
	if !failed {
		t.Fatalf("no-orphaned-tool-result expectation must be the failing one: %v", outcomes)
	}
}

func TestV3BWrongFunctionCallOutputSubtypeFails(t *testing.T) {
	source := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-delivered.session.json")
	fixture := writeMutatedV3BFixture(t, source, rtEventConversationItemCreate, func(record map[string]any) {
		payload := mustAs[map[string]any](t, record["payload"])
		item := mustAs[map[string]any](t, payload["item"])
		item["type"] = rtItemMessage
	})
	scenarioPath := writeV3BScenario(t, "v3b-delivered-wrong-subtype", fixture, true)

	result, execErr := runV3BScenario(t, scenarioPath, fixture)
	if execErr == nil {
		t.Fatal("wrong item subtype must leave the tool result orphaned")
	}
	assertExpectationKindFails(t, result, "tool-result-delivered")
	assertExpectationKindFails(t, result, "no-orphaned-tool-result")
}

func TestV3BWrongDirectionDiscardFails(t *testing.T) {
	source := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-discarded.session.json")
	fixture := writeMutatedV3BFixture(t, source, "tool.result.discarded", func(record map[string]any) {
		record["direction"] = "server_to_client"
	})
	scenarioPath := writeV3BScenario(t, "v3b-discarded-wrong-direction", fixture, true)

	result, execErr := runV3BScenario(t, scenarioPath, fixture)
	if execErr == nil {
		t.Fatal("wrong-direction discard must leave the tool result orphaned")
	}
	assertExpectationKindFails(t, result, "tool-result-discarded")
	assertExpectationKindFails(t, result, "no-orphaned-tool-result")
}

func runV3BScenario(t *testing.T, scenarioPath, fixture string) (map[string]any, error) {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{"probe", "run", scenarioPath, "--replay", fixture, "--json"})
	execErr := rootCmd.ExecuteContext(context.Background())
	return decodeSingleV3BResult(t, writer.StdoutString()), execErr
}

func writeMutatedV3BFixture(t *testing.T, source, recordType string, mutate func(map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source fixture: %v", err)
	}
	var capture map[string]any
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatalf("decode source fixture: %v", err)
	}
	records, ok := capture["records"].([]any)
	if !ok {
		t.Fatal("source fixture records are not an array")
	}
	found := false
	for _, raw := range records {
		record, ok := raw.(map[string]any)
		if !ok || record["type"] != recordType {
			continue
		}
		mutate(record)
		found = true
		break
	}
	if !found {
		t.Fatalf("source fixture has no %q record", recordType)
	}
	mutated, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("encode mutated fixture: %v", err)
	}
	var mutatedCapture gwtesting.SessionCapture
	if err := json.Unmarshal(mutated, &mutatedCapture); err != nil {
		t.Fatalf("decode mutated capture: %v", err)
	}
	sealed, err := gwtesting.SealSessionCapture(mutatedCapture)
	if err != nil {
		t.Fatalf("seal mutated capture: %v", err)
	}
	mutated, err = json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		t.Fatalf("encode sealed mutated fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "mutated.session.json")
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		t.Fatalf("write mutated fixture: %v", err)
	}
	return path
}

// writeV3BScenario writes an on-disk scenario JSON selecting the new
// measurable expectations, exercising the CLI scenario-file loading path.
func writeV3BScenario(t *testing.T, id, fixture string, expectNoOrphan bool) string {
	t.Helper()
	expectations := `[
		{"type": "tool_result_delivered", "tool_call_id": "call_v3b_weather"},
		{"type": "no_orphaned_tool_result"},
		{"type": "terminal_reason", "value": "synthetic"}
	]`
	if strings.HasPrefix(id, "v3b-discarded") {
		expectations = `[
			{"type": "tool_result_discarded", "tool_call_id": "call_v3b_weather"},
			{"type": "no_orphaned_tool_result"},
			{"type": "terminal_reason", "value": "synthetic_failure"}
		]`
	}
	if !expectNoOrphan {
		expectations = `[{"type": "no_orphaned_tool_result"}]`
	}
	document := `{
		"id": "` + id + `",
		"name": "` + id + `",
		"description": "v3b barge-in during tool call",
		"steps": [
			{"type": "send_text", "text": "what is the weather?"},
			{"type": "close"}
		],
		"expectations": ` + expectations + `
	}`
	path := filepath.Join(t.TempDir(), id+".scenario.json")
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

func decodeSingleV3BResult(t *testing.T, stdout string) map[string]any {
	t.Helper()
	line := strings.TrimSpace(stdout)
	if line == "" {
		t.Fatalf("no JSONL result on stdout")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		t.Fatalf("decode result line %q: %v", line, err)
	}
	return result
}

func assertExpectationKindsPass(t *testing.T, result map[string]any, kinds ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, kind := range kinds {
		want[kind] = true
	}
	seen := map[string]bool{}
	for _, raw := range mustAs[[]any](t, result["expectations"]) {
		outcome := mustAs[map[string]any](t, raw)
		kind := mustAs[string](t, outcome["kind"])
		if want[kind] && outcome["passed"] != true {
			t.Fatalf("expectation %s must pass: %v", kind, outcome)
		}
		seen[kind] = true
	}
	for _, kind := range kinds {
		if !seen[kind] {
			t.Fatalf("expected outcome for kind %q missing from results: %v", kind, result)
		}
	}
}

func assertExpectationKindFails(t *testing.T, result map[string]any, want string) {
	t.Helper()
	for _, raw := range mustAs[[]any](t, result["expectations"]) {
		outcome := mustAs[map[string]any](t, raw)
		if outcome["kind"] == want {
			if outcome["passed"] != false {
				t.Fatalf("expectation %s must fail: %v", want, outcome)
			}
			return
		}
	}
	t.Fatalf("expected failed outcome for kind %q missing: %v", want, result)
}

const (
	postDoneBargeInResponseID = "resp-post-done-barge-in"
	postDoneBargeInItemID     = "item-post-done-barge-in"
	// postDoneBargeInSamples is 6 s of 24 kHz provider audio: 96000 samples,
	// 200 callbacks, at the 16 kHz device.
	postDoneBargeInSamples = 144000
	// postDoneBargeInHeard is how much of the response the device renders
	// after response.done before the user speaks: 0.5 s.
	postDoneBargeInHeard = 8000
	// postDoneBargeInDeviceSamplesPerMS is the device's 16 kHz rate per ms.
	postDoneBargeInDeviceSamplesPerMS = 16
	// postDoneBargeInSpeech is the near-end speech amplitude: well above the
	// barge-in speech level and the playback echo margin.
	postDoneBargeInSpeech = 8000
	// postDoneBargeInVADLevel is the input peak the fake provider's server
	// VAD treats as speech.
	postDoneBargeInVADLevel = 4000
	// postDoneBargeInTruncateSlackMS bounds the gap between the truncation
	// point and the audio the device rendered before playback stopped: the
	// resampler's and device queue's latency.
	postDoneBargeInTruncateSlackMS   = 120
	postDoneBargeInCallbacksPerCheck = 4
	// postDoneBargeInInterruptWait bounds the barge-in: speech onset, the
	// provider's VAD and the truncation. The response has ample audio left
	// playing meanwhile.
	postDoneBargeInInterruptWait    = 5 * time.Second
	postDoneBargeInSettleCallbacks  = 20
	postDoneBargeInCallbackInterval = 10 * time.Millisecond
)

// TestAgentBinaryPostDoneBargeInStopsRemoteDevicePlayback is the real-process
// proof of a barge-in after response.done. Provider audio arrives faster than
// real time, so the response is done while seconds are still queued for the
// device. The user speaking into the audio-device-server over its HTTP
// boundary must stop that playback and truncate the provider's item at the
// audio the device actually rendered.
func TestAgentBinaryPostDoneBargeInStopsRemoteDevicePlayback(t *testing.T) {
	provider := newPostDoneBargeInProvider()
	provider.server = testnet.NewWANSegmentServer(t, http.HandlerFunc(provider.handle))
	endpoint, stopDevice := startAudioDeviceServerBinary(t, true)
	t.Cleanup(stopDevice)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	paths := remoteToolAudioPaths{configDir: t.TempDir()}
	arguments := append([]string{"--audio-device-server", endpoint, "--base-url", provider.WebSocketURL()}, postDoneBargeInArgs()...)
	agent := startRemoteToolAudioProcess(t, ctx, remoteToolAudioCase{}, arguments, paths)
	runPostDoneBargeIn(t, ctx, remoteDeviceServer{endpoint: endpoint}, provider, agent)
}

// TestPostDoneBargeInStopsDevicePlayback runs the same scenario in-process on
// a virtual clock: real WebSocket over in-memory pipes and the simulated
// device the audio-device-server serves.
func TestPostDoneBargeInStopsDevicePlayback(t *testing.T) {
	clitest.Test(t, func(t *testing.T) {
		provider := newPostDoneBargeInProvider()
		listener := clitest.NewPipeListener()
		clitest.Serve(t, listener, http.HandlerFunc(provider.handle))
		device := newInProcessDuplexDevice(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		arguments := append([]string{"--base-url", "ws://provider.pipe"}, postDoneBargeInArgs()...)
		agent := startRemoteToolAudioInProcess(t, remoteToolAudioCase{}, arguments, remoteToolAudioPaths{configDir: t.TempDir()}, listener, device)
		runPostDoneBargeIn(t, ctx, device, provider, agent)
	})
}

// TestPostDoneBargeInClientTurnsStopsDevicePlayback is the same scenario
// with provider VAD off: the client owns turn boundaries, so no
// speech_started ever arrives and only the runner's own after-response.done
// interrupt can stop playback and truncate the item.
func TestPostDoneBargeInClientTurnsStopsDevicePlayback(t *testing.T) {
	clitest.Test(t, func(t *testing.T) {
		provider := newPostDoneBargeInProvider()
		provider.serverVAD = false
		listener := clitest.NewPipeListener()
		clitest.Serve(t, listener, http.HandlerFunc(provider.handle))
		device := newInProcessDuplexDevice(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		inferencer, err := providerswire.NewService(providerswire.Dependencies{}).BuildSession(ctx, runtimeproviders.SessionConfig{
			Provider: "openai", Model: "gpt-realtime-2.1", APIKey: "hermetic-key", RealtimeURL: "ws://provider.pipe",
			InputAudioFormat: models.AudioFormatPCM16, OutputAudioFormat: models.AudioFormatPCM16,
			InputAudioSampleRate: 24000, OutputAudioSampleRate: 24000,
			ClientOwnsAudioTurnBoundaries: true, WebSocketDialer: clitest.WebSocketDialer(listener),
		})
		if err != nil {
			t.Fatalf("build client-turn provider session: %v", err)
		}
		agent := remoteToolAudioAgent{stdout: &remoteToolAudioBuffer{}, stderr: &remoteToolAudioBuffer{}}
		process := clitest.Start(t, clitest.Invocation{
			Args: remoteToolAudioSessionArgs(t.TempDir(), append([]string{"--base-url", "ws://provider.pipe"}, postDoneBargeInArgs()...)),
			Ports: []wire.PortSwap{
				wire.NewPortSwap(wire.PortDeviceRegistry, wire.DeviceRegistry(device.registry)),
				wire.NewPortSwap(wire.PortSessionInferencer, inferencer),
			},
			Stdout: agent.stdout, Stderr: agent.stderr,
		})
		done := make(chan error, 1)
		go func() {
			if result := process.Wait(); result.ExitCode != 0 {
				done <- fmt.Errorf("exit status %d", result.ExitCode)
				return
			}
			done <- nil
		}()
		agent.done = done
		runPostDoneBargeIn(t, ctx, device, provider, agent)
		if provider.Snapshot().speechStarted {
			t.Fatal("provider VAD started speech; the client-turn scenario must not rely on it")
		}
	})
}

func postDoneBargeInArgs() []string {
	return []string{
		"--provider", "openai",
		"--model", "gpt-realtime-2.1",
		"--api-key", "hermetic-key",
		"--audio-out-device=",
		"--audio-in-device=",
		"--max-duration", "30s",
		"--wait-for-close",
		"answer at length",
	}
}

func runPostDoneBargeIn(t *testing.T, ctx context.Context, device remoteToolAudioDevice, provider *postDoneBargeInProvider, agent remoteToolAudioAgent) {
	t.Helper()
	run := &postDoneBargeInRun{t: t, ctx: ctx, device: device, provider: provider, agent: agent, clock: time.NewTicker(postDoneBargeInCallbackInterval)}
	defer run.clock.Stop()
	select {
	case <-provider.responseDone:
	case err := <-agent.done:
		run.fail("agent exited before response.done: %v", err)
	case <-ctx.Done():
		run.fail("provider response did not complete: %v", ctx.Err())
	}
	// The response is done; play part of its queued audio, then speak.
	for _, heard := run.rendered(); heard < postDoneBargeInHeard; _, heard = run.rendered() {
		run.advance(postDoneBargeInCallbacksPerCheck)
	}
	if err := device.InjectCapture(ctx, postDoneBargeInSpeechPCM()); err != nil {
		run.fail("inject near-end speech: %v", err)
	}
	run.awaitTruncation()

	// Playback stopped: further callbacks render nothing more of the response.
	run.advance(postDoneBargeInSettleCallbacks)
	stopped, heard := run.rendered()
	run.advance(postDoneBargeInSettleCallbacks)
	after, heardAfter := run.rendered()
	if heardAfter != heard || after.Playback.QueuedSamples != 0 {
		run.fail("playback continued after the barge-in: rendered %d then %d samples, queued=%d", heard, heardAfter, after.Playback.QueuedSamples)
	}
	if total := postDoneBargeInSamples * 2 / 3; heard >= total {
		run.fail("device rendered %d of %d response samples; the barge-in did not stop playback", heard, total)
	}
	if stopped.Playback.DroppedSamples != 0 || stopped.Playback.OverflowEvents != 0 {
		run.fail("device playback lost samples before the barge-in: %+v", stopped.Playback)
	}
	assertPostDoneTruncation(t, provider.Snapshot(), heard/postDoneBargeInDeviceSamplesPerMS)

	provider.ReleaseClose()
	awaitRemoteToolAudioExit(t, ctx, agent.done, agent.stdout, agent.stderr, "agent did not close after the provider closed", "agent exited")
}

// postDoneBargeInRun drives one scenario's device clock.
type postDoneBargeInRun struct {
	t        *testing.T
	ctx      context.Context
	device   remoteToolAudioDevice
	provider *postDoneBargeInProvider
	agent    remoteToolAudioAgent
	clock    *time.Ticker
}

func (r *postDoneBargeInRun) fail(format string, args ...any) {
	r.t.Helper()
	r.t.Fatalf("%s; provider=%+v stderr=%q", fmt.Sprintf(format, args...), r.provider.Snapshot(), r.agent.stderr.String())
}

// advance renders callbacks on the device clock, one per tick.
func (r *postDoneBargeInRun) advance(callbacks int) {
	r.t.Helper()
	for range callbacks {
		select {
		case <-r.clock.C:
		case <-r.ctx.Done():
			r.fail("device clock cancelled: %v", r.ctx.Err())
		}
		if err := r.device.Advance(r.ctx, 1); err != nil {
			r.fail("advance device clock: %v", err)
		}
	}
}

// rendered reads the device evidence and how much response audio it rendered.
func (r *postDoneBargeInRun) rendered() (devicegw.DeviceServerSnapshot, int) {
	r.t.Helper()
	snapshot, err := r.device.Snapshot(r.ctx)
	if err != nil {
		r.fail("read device evidence: %v", err)
	}
	return snapshot, len(nonzeroRemoteToolAudio(snapshot.RenderedSamples))
}

// awaitTruncation keeps the device clock running until the provider sees
// the interrupted item truncated.
func (r *postDoneBargeInRun) awaitTruncation() {
	r.t.Helper()
	interrupt := time.NewTimer(postDoneBargeInInterruptWait)
	defer interrupt.Stop()
	for {
		r.advance(1)
		select {
		case <-r.provider.truncated:
			return
		case err := <-r.agent.done:
			r.fail("agent exited before truncating the interrupted item: %v", err)
		case <-interrupt.C:
			r.fail("no truncation within %s of the user speaking", postDoneBargeInInterruptWait)
		default:
		}
	}
}

func assertPostDoneTruncation(t *testing.T, observed postDoneBargeInSnapshot, heardMS int) {
	t.Helper()
	if observed.protocolError != "" {
		t.Fatalf("provider protocol error: %s", observed.protocolError)
	}
	if len(observed.truncates) != 1 {
		t.Fatalf("truncations = %+v, want exactly one", observed.truncates)
	}
	truncate := observed.truncates[0]
	if truncate.ItemID != postDoneBargeInItemID || truncate.ContentIndex != 0 {
		t.Fatalf("truncation = %+v, want item %q content 0", truncate, postDoneBargeInItemID)
	}
	if gap := truncate.AudioEndMS - heardMS; truncate.AudioEndMS <= 0 || gap > postDoneBargeInTruncateSlackMS || -gap > postDoneBargeInTruncateSlackMS {
		t.Fatalf("truncation audio_end_ms = %d, want the %d ms the device rendered (within %d ms)", truncate.AudioEndMS, heardMS, postDoneBargeInTruncateSlackMS)
	}
}

// postDoneBargeInSpeechPCM is 1 s of a loud 1 kHz square wave at 16 kHz.
func postDoneBargeInSpeechPCM() []int16 {
	const halfPeriod = 8
	samples := make([]int16, 16000)
	for index := range samples {
		samples[index] = postDoneBargeInSpeech
		if index/halfPeriod%2 == 1 {
			samples[index] = -postDoneBargeInSpeech
		}
	}
	return samples
}
