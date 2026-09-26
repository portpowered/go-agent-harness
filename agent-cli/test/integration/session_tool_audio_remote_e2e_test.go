package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/clitest"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	remoteToolAudioDeltaSamples = 9600 // 25,600 base64 bytes, matching test45/test46.
	remoteToolAudioResult       = `{"ok":true,"source":"mock-tool-edge"}`
)

var remoteToolAudioNames = []string{
	"webmcp_list_tabs",
	"webmcp_select_tab",
	"webmcp_list_tools",
	"list_decks",
	"select_deck",
	"webmcp_list_tools",
	"get_state",
}

type remoteToolAudioCase struct {
	name            string
	responseSamples []int
	toolResponses   map[int]bool
	healthyControl  bool
	deviceWAV       bool
	naturalClose    bool
	providerClose   bool
	timingEvidence  bool
	holdToneControl bool
	drainInterval   time.Duration // see remoteToolAudioCase.drainCadence
	inProcess       bool          // see startRemoteToolAudioTopology
}

// naturalCloseBaseline is a plain response that ends the finite session
// while its PCM is still queued for the device.
func naturalCloseBaseline() remoteToolAudioCase {
	return remoteToolAudioCase{name: "natural_close_baseline", responseSamples: []int{38400}, naturalClose: true, deviceWAV: true}
}

// TestAgentBinaryNaturalCloseDrainsRemoteDevicePCM is the real-process proof
// of the natural-close drain: the shipped binary returns from a finite session
// only after its queued PCM crossed the audio-device-server HTTP boundary.
func TestAgentBinaryNaturalCloseDrainsRemoteDevicePCM(t *testing.T) {
	runRemoteToolAudioScenario(t, naturalCloseBaseline(), 0, 3*time.Millisecond, 30*time.Millisecond, 32, 0, 0)
}

// TestNaturalCloseDrainsDevicePCM reproduces the live provider timing
// contract: response.done makes a finite session return while the provider's
// faster-than-realtime PCM is still queued for a 16 kHz output device. Both the
// plain response and the speech/tool/speech continuation must remain alive
// until every accepted sample reaches the device edge. It runs in-process on
// a virtual clock (see startRemoteToolAudioTopology).
func TestNaturalCloseDrainsDevicePCM(t *testing.T) {
	for _, testCase := range []remoteToolAudioCase{
		naturalCloseBaseline(),
		{
			name:            "provider_close_tool_continuation",
			responseSamples: []int{38400, 66000},
			toolResponses:   map[int]bool{0: true},
			providerClose:   true,
			deviceWAV:       true,
			inProcess:       true,
		},
	} {
		testCase.inProcess = true
		clitest.Subtest(t, testCase.name, func(t *testing.T) {
			promptBytes := 0
			if testCase.naturalClose {
				promptBytes = 32
			}
			runRemoteToolAudioScenario(t, testCase, 0, 3*time.Millisecond, 30*time.Millisecond, promptBytes, 0, 0)
		})
	}
}

// TestAgentBinaryAudioOutRecordsRemoteDevicePCM pins --audio-out as a
// secondary observation of the selected playback device. The provider emits
// 24 kHz audio on both sides of a real mock tool call, while the remote device
// accepts 16 kHz PCM. The finished WAV must therefore carry the negotiated
// device rate and exactly the samples observed at the remote device edge.
func TestAgentBinaryAudioOutRecordsRemoteDevicePCM(t *testing.T) {
	runRemoteToolAudioScenario(t, remoteToolAudioCase{
		name:            "device_wav_tool_continuation",
		responseSamples: []int{38400, 66000},
		toolResponses:   map[int]bool{0: true},
		deviceWAV:       true,
	}, 0, 3*time.Millisecond, 30*time.Millisecond, 0, 0, 0)
}

// TestAgentBinarySerialToolTimingAtProcessEdges distills the six-call browser
// chain observed in test7.json. The compiled CLI crosses a real WebSocket, a
// fixture-controlled executor process, and the remote device HTTP boundary.
// It proves that harness scheduling does not manufacture the multi-second
// pauses seen in the live recording and that burst audio remains queued while
// the serial tool chain completes.
func TestAgentBinarySerialToolTimingAtProcessEdges(t *testing.T) {
	runRemoteToolAudioScenario(t, remoteToolAudioCase{
		name:            "test7_serial_tool_timing",
		responseSamples: []int{48000, 0, 0, 0, 0, 0, 24000},
		toolResponses:   map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true},
		deviceWAV:       true,
		timingEvidence:  true,
	}, 0, 3*time.Millisecond, 30*time.Millisecond, 32, 0, 0)
}

// remoteToolAudioContinuationCases reproduce the complete response topology of
// test45 and test46. Each trace has nine model responses, four audio
// responses, seven mock tool calls, an audio-only response immediately
// followed by fresh model audio, and a five-tool continuation chain before the
// longest final utterance; test47/test48 are matched healthy controls.
func remoteToolAudioContinuationCases() []remoteToolAudioCase {
	return []remoteToolAudioCase{
		{
			name:            "test45",
			responseSamples: []int{38400, 0, 66000, 66000, 0, 0, 0, 0, 96000},
			toolResponses:   map[int]bool{0: true, 1: true, 3: true, 4: true, 5: true, 6: true, 7: true},
		},
		{
			name:            "test46",
			responseSamples: []int{46800, 0, 48000, 55200, 0, 0, 0, 0, 111600},
			toolResponses:   map[int]bool{0: true, 1: true, 3: true, 4: true, 5: true, 6: true, 7: true},
		},
		{
			// Responses 9-14 are the test47 segment with the same long
			// audio-plus-tool, tool-only continuation chain, and final audio.
			name:            "test47_matched_healthy_control",
			responseSamples: []int{50400, 0, 0, 0, 0, 96000},
			toolResponses:   map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true},
			healthyControl:  true,
		},
		{
			// Responses 8-13 are the equivalent healthy test48 chain.
			name:            "test48_matched_healthy_control",
			responseSamples: []int{82800, 0, 0, 0, 0, 98400},
			toolResponses:   map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true},
			healthyControl:  true,
		},
	}
}

// TestToolContinuationPreservesDeviceAudio runs every continuation topology
// against every delivery in-process on a virtual clock: the shipped session
// command talks real WebSocket to the provider over in-memory pipes, calls
// the fixture tool executor, and plays through the simulated duplex device
// the audio-device-server binary serves. captured_cadence and slow_device
// (deviceCadence) keep the device clock at device cadence for the whole run,
// so nearly all playback, including every response boundary at the device
// edge, happens while the queue drains; the others drain on
// remoteToolAudioDrainInterval. The assertion sees only protocol
// observations, tool observations and device-rendered PCM, never session
// queue or sink implementation state.
func TestToolContinuationPreservesDeviceAudio(t *testing.T) {
	for _, testCase := range remoteToolAudioContinuationCases() {
		for _, delivery := range remoteToolAudioDeliveries() {
			if testCase.healthyControl && delivery.name != "provider_burst" {
				continue
			}
			t.Run(testCase.name+"/"+delivery.name, func(t *testing.T) {
				t.Parallel() // each scenario is CPU-bound in its own bubble
				clitest.Test(t, func(t *testing.T) {
					scenario := testCase
					scenario.inProcess = true
					runRemoteToolAudioContinuation(t, scenario, delivery)
				})
			})
		}
	}
}

// TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio repeats the
// continuation matrix with fresh process triples: the shipped binary (with
// the fixture tool executor), a real local WebSocket provider, and the
// audio-device-server binary whose manual callback clock the test advances
// over HTTP. Its device-cadence deliveries drain in real time (11-22 s each).
// Pull requests keep test45/captured_cadence as the representative real-time
// continuation across the three processes; the rest of the matrix runs with
// YUI_AUDIO_STRESS=1 (make test-audio-device-server-integration and the
// nightly audio stress workflow).
func TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio(t *testing.T) {
	scenarioSlots := make(chan struct{}, remoteToolAudioScenarioSlots)
	for _, testCase := range remoteToolAudioContinuationCases() {
		for _, delivery := range remoteToolAudioDeliveries() {
			if testCase.healthyControl && delivery.name != "provider_burst" {
				continue
			}
			t.Run(testCase.name+"/"+delivery.name, func(t *testing.T) {
				if testCase.name != "test45" || delivery.name != "captured_cadence" {
					requireRemoteToolAudioStress(t)
				}
				// Bound real process/device pairs so callback clocks retain CPU under the full package.
				t.Parallel()
				scenarioSlots <- struct{}{}
				defer func() { <-scenarioSlots }()
				runRemoteToolAudioContinuation(t, testCase, delivery)
			})
		}
	}
}

func runRemoteToolAudioContinuation(t *testing.T, scenario remoteToolAudioCase, delivery remoteToolAudioDelivery) {
	t.Helper()
	if !delivery.deviceCadence {
		scenario.drainInterval = remoteToolAudioDrainInterval
	}
	runRemoteToolAudioScenario(t, scenario, delivery.deltaDelay, delivery.toolDelay, delivery.callbackInterval, delivery.promptBytes, delivery.toolResultBytes, delivery.inputFrames)
}

func TestAgentBinaryTest45HighRateToolAudioRegression(t *testing.T) {
	requireRemoteToolAudioStress(t)
	slots := make(chan struct{}, 2)
	testCase := remoteToolAudioCase{
		name:            "test45_high_rate",
		responseSamples: []int{38400, 0, 66000, 66000, 0, 0, 0, 0, 96000},
		toolResponses:   map[int]bool{0: true, 1: true, 3: true, 4: true, 5: true, 6: true, 7: true},
	}
	for trial := 0; trial < 20; trial++ {
		t.Run(fmt.Sprintf("trial_%02d", trial+1), func(t *testing.T) {
			t.Parallel()
			slots <- struct{}{}
			defer func() { <-slots }()
			runRemoteToolAudioScenario(t, testCase, 0, 0, time.Millisecond, 0, 0, 0)
		})
	}
}
func TestAgentBinaryTest46HighRateToolAudioRegression(t *testing.T) {
	requireRemoteToolAudioStress(t)
	slots := make(chan struct{}, 2)
	testCase := remoteToolAudioCase{
		name:            "test46_high_rate",
		responseSamples: []int{46800, 0, 48000, 55200, 0, 0, 0, 0, 111600},
		toolResponses:   map[int]bool{0: true, 1: true, 3: true, 4: true, 5: true, 6: true, 7: true},
	}
	for trial := 0; trial < 20; trial++ {
		t.Run(fmt.Sprintf("trial_%02d", trial+1), func(t *testing.T) {
			t.Parallel()
			slots <- struct{}{}
			defer func() { <-slots }()
			runRemoteToolAudioScenario(t, testCase, 0, 0, time.Millisecond, 0, 0, 0)
		})
	}
}

func requireRemoteToolAudioStress(t *testing.T) {
	t.Helper()
	if os.Getenv("YUI_AUDIO_STRESS") != "1" {
		t.Skip("set YUI_AUDIO_STRESS=1 to run fresh-process high-rate audio stress")
	}
}

func runRemoteToolAudioScenario(t *testing.T, testCase remoteToolAudioCase, deltaDelay, toolDelay, callbackInterval time.Duration, promptBytes, toolResultBytes, inputFrames int) {
	t.Helper()
	responses := make([][]int16, len(testCase.responseSamples))
	for index, count := range testCase.responseSamples {
		if count > 0 {
			responses[index] = remoteToolAudioPCM(count, int16(900+index*2200))
		}
	}
	want := remoteToolAudioExpected(t, responses)
	if testCase.deviceWAV {
		want = trimRemoteToolAudioEdgeSilence(remoteToolAudioExpectedPCM(t, responses))
	}
	calls := remoteToolAudioCalls(testCase, toolResultBytes)
	prompt := strings.Repeat("p", promptBytes)
	provider := newRemoteToolAudioProvider(responses, testCase.toolResponses, calls, deltaDelay, prompt, inputFrames*audio.FrameSize*3/2)
	defer provider.Close()
	device, startAgent := startRemoteToolAudioTopology(t, testCase, provider)

	paths := newRemoteToolAudioPaths(t, testCase, calls, toolDelay)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := startAgent(ctx, remoteToolAudioArgs(testCase, inputFrames > 0, prompt, paths), paths)
	done, stdout, stderr := agent.done, agent.stdout, agent.stderr
	if inputFrames > 0 {
		primeRemoteToolAudioInput(t, ctx, device, provider, inputFrames)
	}

	clockCtx, stopClock := context.WithCancel(ctx)
	clockDone := make(chan error, 1)
	go driveRemoteToolAudioClock(clockCtx, device, provider.firstAudioSent, callbackInterval, &stdout.callbackAdvances, clockDone)
	awaitRemoteToolAudioTopology(t, ctx, testCase, provider, done, stdout, stderr)
	stopClock()
	select {
	case err := <-clockDone:
		if err != nil {
			t.Fatalf("advance remote playback callback: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timed out stopping remote playback clock: %v", ctx.Err())
	}
	var snapshot devicegw.DeviceServerSnapshot
	if testCase.naturalClose || testCase.providerClose {
		var snapshotErr error
		snapshot, snapshotErr = device.Snapshot(ctx)
		if snapshotErr != nil {
			t.Fatalf("read naturally closed remote device evidence: %v", snapshotErr)
		}
	} else {
		snapshot = requireRemoteToolAudio(t, ctx, device, nonzeroRemoteToolAudio(want), testCase.drainCadence(callbackInterval), &stdout.callbackAdvances, provider, len(calls), want, done, stderr)
	}
	got := nonzeroRemoteToolAudio(snapshot.RenderedSamples)
	if testCase.deviceWAV {
		// This fixture deliberately makes the continuation available while the
		// first response still has ample queued audio. Preserve the exact interior
		// zeros produced by filter startup; any additional silence is a cut. Longer multi-tool stress fixtures may contain
		// legitimate provider/tool latency after their queue naturally empties.
		got = trimRemoteToolAudioEdgeSilence(snapshot.RenderedSamples)
	}
	assertRemoteToolAudioScenario(t, testCase, got, want, snapshot, provider)
	if snapshot.Playback.DroppedSamples != 0 || snapshot.Playback.OverflowEvents != 0 || snapshot.Playback.DiscardedSamples != 0 || snapshot.Playback.DiscardEvents != 0 {
		t.Fatalf("%s remote playback reported loss: %+v", testCase.name, snapshot.Playback)
	}
	if !testCase.naturalClose && !testCase.providerClose {
		provider.ReleaseClose()
		awaitRemoteToolAudioExit(t, ctx, done, stdout, stderr, "agent did not close after verified device playback", "mock-tool agent exited")
	}
	assertRemoteToolAudioProviderEdge(t, provider.Snapshot(), len(responses), len(calls), prompt != "", inputFrames > 0)
	if paths.audioOut != "" {
		assertRemoteToolAudioDeviceWAV(t, paths.audioOut, testCase.deviceWAV, want)
	}
	if len(calls) > 0 {
		assertRemoteToolObservations(t, paths.observation, calls)
	}
	if paths.capture != "" {
		assertRemoteToolTimingEvidence(t, paths.capture, len(calls))
	}
}

func assertRemoteToolTimingEvidence(t *testing.T, capturePath string, wantCalls int) {
	t.Helper()
	report, err := runtimeReplayWire.NewService().AnalyzeTiming(t.Context(), capturePath)
	if err != nil {
		t.Fatalf("analyze process-edge timing capture: %v", err)
	}
	if report.Summary.ToolCallCount != wantCalls || report.Summary.UnfinishedToolCallCount != 0 {
		t.Fatalf("process-edge tool topology = %+v, want %d completed calls", report.Summary, wantCalls)
	}
	for _, check := range []struct {
		name string
		got  int64
		max  int64
	}{
		{name: "executor", got: report.Summary.ToolExecutionMS.MaxMS, max: 500},
		{name: "result scheduling", got: report.Summary.ToolResultToRequestMS.MaxMS, max: 100},
		{name: "provider admission", got: report.Summary.ToolRequestToCreatedMS.MaxMS, max: 500},
		{name: "fixture response", got: report.Summary.ToolCreatedToFirstOutputMS.MaxMS, max: 500},
	} {
		if check.got > check.max {
			t.Errorf("process-edge %s latency = %dms, want <= %dms; summary=%+v", check.name, check.got, check.max, report.Summary)
		}
	}
	if report.Summary.EstimatedAudibleGapMS.Count != 0 {
		t.Errorf("serial tool fixture introduced an estimated audible gap: %+v", report.Summary.EstimatedAudibleGapMS)
	}
	if report.Summary.MaxEstimatedQueueDelayMS < 1000 {
		t.Errorf("fixture did not exercise burst audio queued across tools: max queue delay=%dms", report.Summary.MaxEstimatedQueueDelayMS)
	}
}

// Timeout diagnostics stay on the scenario path so a failure preserves the
// bounded device, provider, child, and callback evidence needed to repair it.
// Keep this evidence beside the scenario's deadline and cleanup logic.
func remoteToolAudioFailureEvidence(ctx context.Context, device remoteToolAudioDevice, provider *remoteToolAudioProvider, expectedToolCalls int, want []int16, done <-chan error, stderr *remoteToolAudioBuffer, callbackAdvances *atomic.Uint64) string {
	childStatus := "still running at timeout"
	childExited := false
	select {
	case err := <-done:
		childExited = true
		childStatus = fmt.Sprintf("exited: %v", err)
	default:
	}
	diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
	defer cancel()
	snapshot, snapshotErr := device.Snapshot(diagnosticCtx)
	got := nonzeroRemoteToolAudio(snapshot.RenderedSamples)
	markerSamples := audio.FrameSize
	if markerSamples > len(want) {
		markerSamples = len(want)
	}
	finalMarker := want[len(want)-markerSamples:]
	lastTrace := remoteToolAudioTraceTail(snapshot.Trace, "")
	lastRenderTrace := remoteToolAudioTraceTail(snapshot.Trace, "render")
	lastCaptureTrace := remoteToolAudioTraceTail(snapshot.Trace, "capture")
	stderrText := "<child still running>"
	if childExited {
		stderrText = stderr.String()
		if len(stderrText) > 4096 {
			stderrText = stderrText[len(stderrText)-4096:]
		}
	}
	return fmt.Sprintf("remote timeout evidence: snapshot_error=%v rendered_pcm=%d nonzero_pcm=%d expected_pcm=%d final_marker=%t playback={queued:%d dropped:%d overflow:%d discarded:%d discard_events:%d callbacks:%d rendered:%d} capture={queued:%d captured:%d dropped:%d} trace_last=%s trace_render=%s trace_capture=%s expected_tool_calls=%d provider=%+v child=%s stderr=%q callback_advances=%d", snapshotErr, len(snapshot.RenderedSamples), len(got), len(want), remoteToolAudioHasSuffix(got, finalMarker), snapshot.Playback.QueuedSamples, snapshot.Playback.DroppedSamples, snapshot.Playback.OverflowEvents, snapshot.Playback.DiscardedSamples, snapshot.Playback.DiscardEvents, snapshot.Playback.CallbackCount, snapshot.Playback.RenderedSamples, snapshot.Capture.QueuedSamples, snapshot.Capture.CapturedSamples, snapshot.Capture.DroppedSamples, lastTrace, lastRenderTrace, lastCaptureTrace, expectedToolCalls, provider.Snapshot(), childStatus, stderrText, callbackAdvances.Load())
}

// trimRemoteToolAudioEdgeSilence removes only callbacks before playback began
// and after it completed. Silence inside the model PCM remains observable: it
// is the audible discontinuity that sample-count-only checks used to erase.
func trimRemoteToolAudioEdgeSilence(samples []int16) []int16 {
	for len(samples) > 0 && samples[0] == 0 {
		samples = samples[1:]
	}
	for len(samples) > 0 && samples[len(samples)-1] == 0 {
		samples = samples[:len(samples)-1]
	}
	return samples
}

type remoteToolCallFixture struct {
	ID        string
	Name      string
	Arguments string
	Output    string
}

func remoteToolAudioCalls(testCase remoteToolAudioCase, resultBytes int) []remoteToolCallFixture {
	calls := make([]remoteToolCallFixture, 0, len(remoteToolAudioNames))
	toolNumber := 0
	for response := range testCase.responseSamples {
		if !testCase.toolResponses[response] {
			continue
		}
		output := remoteToolAudioResult
		if resultBytes > 0 {
			output = `{"data":"` + strings.Repeat("x", resultBytes) + `"}`
		}
		calls = append(calls, remoteToolCallFixture{
			ID:        fmt.Sprintf("call-%s-%d", testCase.name, toolNumber),
			Name:      remoteToolAudioNames[toolNumber],
			Arguments: fmt.Sprintf(`{"step":%d,"trace":%q}`, toolNumber, testCase.name),
			Output:    output,
		})
		toolNumber++
	}
	return calls
}

type remoteToolAudioProvider struct {
	server   *httptest.Server
	upgrader websocket.Upgrader

	responses            [][]int16
	toolAt               map[int]bool
	calls                []remoteToolCallFixture
	deltaDelay           time.Duration
	expectedPrompt       string
	expectedInputSamples int
	releaseClose         chan struct{}
	releaseOnce          sync.Once
	allSentOnce          sync.Once

	allResponsesSent chan struct{}
	firstAudioSent   chan struct{}
	sessionReady     chan struct{}
	sessionReadyOnce sync.Once
	firstAudioOnce   sync.Once

	mu               sync.Mutex
	started          bool
	nextResponse     int
	pendingCall      int
	pendingResult    bool
	responsesSent    int
	responseCreates  int
	initialRequests  int
	toolResults      int
	userPromptSeen   bool
	inputSamplesSeen int
	inputHistories   int
	protocolError    string
}

type remoteToolAudioProviderSnapshot struct {
	responsesSent    int
	responseCreates  int
	initialRequests  int
	inputHistories   int
	inputSamplesSeen int
	toolResults      int
	protocolError    string
}

func newRemoteToolAudioProvider(responses [][]int16, toolAt map[int]bool, calls []remoteToolCallFixture, deltaDelay time.Duration, expectedPrompt string, expectedInputSamples int) *remoteToolAudioProvider {
	return &remoteToolAudioProvider{
		upgrader:             websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		responses:            responses,
		toolAt:               toolAt,
		calls:                calls,
		deltaDelay:           deltaDelay,
		expectedPrompt:       expectedPrompt,
		expectedInputSamples: expectedInputSamples,
		releaseClose:         make(chan struct{}),
		allResponsesSent:     make(chan struct{}),
		firstAudioSent:       make(chan struct{}),
		sessionReady:         make(chan struct{}),
		pendingCall:          -1,
	}
}

func (p *remoteToolAudioProvider) WebSocketURL() string {
	return strings.Replace(p.server.URL, "http://", "ws://", 1)
}

func (p *remoteToolAudioProvider) ReleaseClose() { p.releaseOnce.Do(func() { close(p.releaseClose) }) }

func (p *remoteToolAudioProvider) Close() {
	p.ReleaseClose()
	if p.server != nil {
		p.server.Close()
	}
}

func (p *remoteToolAudioProvider) Snapshot() remoteToolAudioProviderSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return remoteToolAudioProviderSnapshot{
		responsesSent: p.responsesSent, responseCreates: p.responseCreates,
		initialRequests: p.initialRequests, inputHistories: p.inputHistories, inputSamplesSeen: p.inputSamplesSeen,
		toolResults: p.toolResults, protocolError: p.protocolError,
	}
}

func (p *remoteToolAudioProvider) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != rtAuthorizationHeader {
		p.fail("missing hermetic authorization")
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	connection, err := p.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		p.fail("upgrade websocket: " + err.Error())
		return
	}
	defer discardCloseError(connection)
	closeWriterDone := make(chan struct{})
	go func() {
		select {
		case <-p.releaseClose:
			_ = p.send(connection, map[string]string{"type": rtEventSessionClosed, "session_id": "sess-tool-audio-e2e", "reason": "fixture_complete"}) //nolint:errcheck // send records write failures through p.fail
		case <-closeWriterDone:
		}
	}()
	defer close(closeWriterDone)

	for {
		_, payload, err := connection.ReadMessage()
		if err != nil {
			return
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type    string `json:"type"`
				CallID  string `json:"call_id"`
				Output  string `json:"output"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
			Audio string `json:"audio"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			p.fail("decode client event: " + err.Error())
			return
		}
		var keepReading bool
		switch event.Type {
		case rtEventSessionUpdate:
			keepReading = p.handleSessionUpdate(connection)
		case rtEventInputAudioAppend:
			keepReading = p.handleInputAudio(connection, event.Audio)
		case rtEventConversationItemCreate:
			keepReading = p.handleItem(event.Item.Type, event.Item.CallID, event.Item.Output, event.Item.Content)
		case rtEventResponseCreate:
			keepReading = p.handleResponseCreate(connection)
		default:
			keepReading = true
		}
		if !keepReading {
			return
		}
	}
}

// handleSessionUpdate opens the session once and, without a prompt or input
// history to wait for, starts the scripted responses.
func (p *remoteToolAudioProvider) handleSessionUpdate(connection *websocket.Conn) bool {
	p.mu.Lock()
	alreadyStarted := p.started
	p.started = true
	p.mu.Unlock()
	if alreadyStarted {
		return true
	}
	if err := p.send(connection, map[string]any{"type": rtEventSessionCreated, "session": map[string]string{"id": "sess-tool-audio-e2e", "model": "gpt-realtime-2.1"}}); err != nil {
		return false
	}
	p.sessionReadyOnce.Do(func() { close(p.sessionReady) })
	if p.expectedPrompt == "" && p.expectedInputSamples == 0 {
		return p.sendReadyResponses(connection) == nil
	}
	return true
}

// handleInputAudio counts prior input history and starts the responses once
// the expected input has arrived.
func (p *remoteToolAudioProvider) handleInputAudio(connection *websocket.Conn, encoded string) bool {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded)%2 != 0 {
		p.fail("decode prior input audio")
		return false
	}
	p.mu.Lock()
	p.inputSamplesSeen += len(decoded) / 2
	ready := p.expectedInputSamples > 0 && p.inputSamplesSeen >= p.expectedInputSamples && p.nextResponse == 0
	if ready {
		p.inputHistories++
		p.expectedInputSamples = 0
	}
	p.mu.Unlock()
	if ready {
		return p.sendReadyResponses(connection) == nil
	}
	return true
}

// handleItem accepts the large preamble message and correlated tool results.
func (p *remoteToolAudioProvider) handleItem(itemType, callID, output string, content []struct {
	Text string `json:"text"`
}) bool {
	if itemType == rtItemMessage {
		if len(content) != 1 || content[0].Text != p.expectedPrompt {
			p.fail("large preamble did not reach the provider intact")
			return false
		}
		p.mu.Lock()
		p.userPromptSeen = true
		p.mu.Unlock()
		return true
	}
	if itemType != rtItemFunctionCallOutput {
		return true
	}
	if err := p.acceptToolResult(callID, output); err != nil {
		p.fail(err.Error())
		return false
	}
	return true
}

// handleResponseCreate answers the initial prompt request or a continuation
// that follows exactly one correlated tool result.
func (p *remoteToolAudioProvider) handleResponseCreate(connection *websocket.Conn) bool {
	p.mu.Lock()
	if p.nextResponse == 0 && p.expectedPrompt != "" {
		if !p.userPromptSeen {
			p.mu.Unlock()
			p.fail("initial response.create preceded the large preamble")
			return false
		}
		p.initialRequests++
		p.mu.Unlock()
		return p.sendReadyResponses(connection) == nil
	}
	if p.pendingCall < 0 || !p.pendingResult {
		p.mu.Unlock()
		p.fail("response.create arrived without its correlated mock tool result")
		return false
	}
	p.responseCreates++
	p.pendingCall = -1
	p.pendingResult = false
	p.mu.Unlock()
	return p.sendReadyResponses(connection) == nil
}

func (p *remoteToolAudioProvider) acceptToolResult(callID, output string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pendingCall < 0 || p.pendingCall >= len(p.calls) {
		return fmt.Errorf("unexpected function_call_output for %q", callID)
	}
	want := p.calls[p.pendingCall]
	if callID != want.ID || output != want.Output {
		return fmt.Errorf("function result = {%q %q}, want {%q %q}", callID, output, want.ID, want.Output)
	}
	p.pendingResult = true
	p.toolResults++
	return nil
}

// sendReadyResponses emits one response at a time until a tool result is
// required. An audio-only response is immediately followed by the next
// server-created response, exercising the second real failure mode: fresh
// agent audio must queue behind existing audible audio rather than replace it.
func (p *remoteToolAudioProvider) sendReadyResponses(connection *websocket.Conn) error {
	for {
		p.mu.Lock()
		index := p.nextResponse
		if index >= len(p.responses) {
			p.mu.Unlock()
			p.allSentOnce.Do(func() { close(p.allResponsesSent) })
			return nil
		}
		p.nextResponse++
		p.responsesSent++
		tool := p.toolAt[index]
		callIndex := -1
		if tool {
			callIndex = 0
			for response := 0; response < index; response++ {
				if p.toolAt[response] {
					callIndex++
				}
			}
			p.pendingCall = callIndex
			p.pendingResult = false
		}
		p.mu.Unlock()
		if err := p.sendResponse(connection, index, callIndex); err != nil {
			return err
		}
		if tool {
			return nil
		}
	}
}

func (p *remoteToolAudioProvider) sendResponse(connection *websocket.Conn, index, callIndex int) error {
	responseID := fmt.Sprintf("resp-tool-audio-%d", index)
	itemID := fmt.Sprintf("item-tool-audio-%d", index)
	if err := p.send(connection, map[string]any{"type": rtEventResponseCreated, "response": map[string]string{"id": responseID}}); err != nil {
		return err
	}
	for offset := 0; offset < len(p.responses[index]); offset += remoteToolAudioDeltaSamples {
		end := offset + remoteToolAudioDeltaSamples
		if end > len(p.responses[index]) {
			end = len(p.responses[index])
		}
		if err := p.send(connection, remoteToolAudioDelta(responseID, itemID, p.responses[index][offset:end])); err != nil {
			return err
		}
		p.firstAudioOnce.Do(func() { close(p.firstAudioSent) })
		if p.deltaDelay > 0 {
			time.Sleep(p.deltaDelay)
		}
	}
	if len(p.responses[index]) > 0 {
		if err := p.send(connection, map[string]string{"type": "response.output_audio.done", "response_id": responseID, "item_id": itemID}); err != nil {
			return err
		}
	}
	if callIndex >= 0 {
		call := p.calls[callIndex]
		if err := p.send(connection, map[string]any{
			"type": rtEventOutputItemAdded, "response_id": responseID, "output_index": 1,
			"item": map[string]string{"type": rtItemFunctionCall, "call_id": call.ID, "name": call.Name},
		}); err != nil {
			return err
		}
		if err := p.send(connection, map[string]any{
			"type": rtEventFunctionCallArgumentsDone, "response_id": responseID,
			"call_id": call.ID, "name": call.Name, "arguments": call.Arguments,
		}); err != nil {
			return err
		}
	}
	return p.send(connection, map[string]any{"type": rtEventResponseDone, "response": map[string]string{"id": responseID, "status": rtStatusCompleted}})
}

func (p *remoteToolAudioProvider) send(connection *websocket.Conn, event any) error {
	if err := connection.WriteJSON(event); err != nil {
		p.fail("write server event: " + err.Error())
		return err
	}
	return nil
}

func (p *remoteToolAudioProvider) fail(message string) {
	p.mu.Lock()
	if p.protocolError == "" {
		p.protocolError = message
	}
	p.mu.Unlock()
}

func writeRemoteToolFixture(t *testing.T, observationPath string, calls []remoteToolCallFixture, delay time.Duration) string {
	t.Helper()
	configured := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		configured = append(configured, map[string]any{
			"name": call.Name, "arguments": call.Arguments, "output": call.Output,
			"delay_ms": delay.Milliseconds(),
		})
	}
	data, err := json.Marshal(map[string]any{"observations": observationPath, "calls": configured})
	if err != nil {
		t.Fatalf("marshal tool mock fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tool-mock.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write tool mock fixture: %v", err)
	}
	return path
}

func remoteToolAudioDelta(responseID, itemID string, samples []int16) map[string]any {
	return map[string]any{
		"type": rtEventOutputAudioDelta, "response_id": responseID, "item_id": itemID, "content_index": 0,
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(samples)),
	}
}

func remoteToolAudioPCM(samples int, seed int16) []int16 {
	pcm := make([]int16, samples)
	state := uint32(uint16(seed)) ^ 0x9e3779b9
	for index := range pcm {
		// Use a deterministic, positive pseudo-random signal rather than a short
		// periodic ramp. The final device-edge marker must not occur at an earlier
		// offset: otherwise a truncated response whose loss is an exact multiple
		// of the ramp period can impersonate the completed FIFO tail.
		state = state*1664525 + 1013904223
		pcm[index] = seed + int16(state%1021)
	}
	return pcm
}

func remoteToolAudioExpected(t *testing.T, responses [][]int16) []int16 {
	t.Helper()
	return nonzeroRemoteToolAudio(remoteToolAudioExpectedPCM(t, responses))
}

func remoteToolAudioExpectedPCM(t *testing.T, responses [][]int16) []int16 {
	t.Helper()
	var expected []int16
	for _, response := range responses {
		if len(response) == 0 {
			continue
		}
		converter, err := wavio.NewPCM16Resampler(wavio.Rate24kHz, audio.SampleRate)
		if err != nil {
			t.Fatal(err)
		}
		converted, err := converter.Process(response, true)
		if err != nil {
			t.Fatalf("resample reference audio: %v", err)
		}
		expected = append(expected, converted...)
	}
	return expected
}

func nonzeroRemoteToolAudio(samples []int16) []int16 {
	filtered := make([]int16, 0, len(samples))
	for _, sample := range samples {
		if sample != 0 {
			filtered = append(filtered, sample)
		}
	}
	return filtered
}

func verifyRemoteToolAudio(got, want []int16) error {
	if len(got) != len(want) {
		return fmt.Errorf("remote device rendered %d/%d compared samples (lost %d, %.1f%% retained)", len(got), len(want), len(want)-len(got), 100*float64(len(got))/float64(len(want)))
	}
	if !reflect.DeepEqual(got, want) {
		for index := range want {
			if got[index] != want[index] {
				return fmt.Errorf("PCM differs at device sample %d: got %d want %d", index, got[index], want[index])
			}
		}
	}
	return nil
}

func assertRemoteToolObservations(t *testing.T, path string, calls []remoteToolCallFixture) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tool mock observations: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(calls) {
		t.Fatalf("tool mock observations = %d, want %d", len(lines), len(calls))
	}
	for index, line := range lines {
		var observed map[string]string
		if err := json.Unmarshal([]byte(line), &observed); err != nil {
			t.Fatalf("decode tool mock observation %d: %v", index, err)
		}
		want := calls[index]
		if observed["id"] != want.ID || observed["name"] != want.Name || observed["arguments"] != want.Arguments {
			t.Fatalf("tool mock observation %d = %v, want %+v", index, observed, want)
		}
	}
}
