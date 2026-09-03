package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
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
}

// TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio reproduces the
// complete response topology of test45 and test46 at process boundaries. Each
// trace has nine model responses, four audio responses, seven mock tool calls,
// an audio-only response immediately followed by fresh model audio, and a
// five-tool continuation chain before the longest final utterance.
//
// The shipped session command talks to a real local WebSocket provider and a
// separately built fixture-controlled tool executor. Playback crosses the
// audio-device-server HTTP boundary while its callback clock advances. The
// assertion sees only network protocol observations, process-owned tool
// observations, and device-rendered PCM; it does not inspect a session queue,
// sink generation, or any other playback implementation state.
func TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio(t *testing.T) {
	cases := []remoteToolAudioCase{
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
	for _, testCase := range cases {
		for _, delivery := range []struct {
			name             string
			deltaDelay       time.Duration
			toolDelay        time.Duration
			callbackInterval time.Duration
			promptBytes      int
			toolResultBytes  int
			inputFrames      int
		}{
			{name: "provider_burst", toolDelay: 3 * time.Millisecond, callbackInterval: 30 * time.Millisecond},
			{name: "captured_cadence", deltaDelay: 50 * time.Millisecond, toolDelay: 25 * time.Millisecond, callbackInterval: 30 * time.Millisecond},
			{name: "slow_device", toolDelay: 3 * time.Millisecond, callbackInterval: 45 * time.Millisecond},
			{name: "large_text_and_tool_results", toolDelay: 3 * time.Millisecond, callbackInterval: 30 * time.Millisecond, promptBytes: 64 << 10, toolResultBytes: 64 << 10},
			{name: "long_prior_input_61s", toolDelay: 3 * time.Millisecond, callbackInterval: 30 * time.Millisecond, inputFrames: 2048},
		} {
			if testCase.healthyControl && delivery.name != "provider_burst" {
				continue
			}
			t.Run(testCase.name+"/"+delivery.name, func(t *testing.T) {
				runRemoteToolAudioScenario(t, testCase, delivery.deltaDelay, delivery.toolDelay, delivery.callbackInterval, delivery.promptBytes, delivery.toolResultBytes, delivery.inputFrames)
			})
		}
	}
}

// TestAgentBinaryTest45HighRateToolAudioRegression makes the race acceptance
// criterion explicit. Twenty fresh agent, provider, tool, and device processes
// replay the full test45 topology; one missing sample fails its trial.
func TestAgentBinaryTest45HighRateToolAudioRegression(t *testing.T) {
	testCase := remoteToolAudioCase{
		name:            "test45_high_rate",
		responseSamples: []int{38400, 0, 66000, 66000, 0, 0, 0, 0, 96000},
		toolResponses:   map[int]bool{0: true, 1: true, 3: true, 4: true, 5: true, 6: true, 7: true},
	}
	for trial := 0; trial < 20; trial++ {
		t.Run(fmt.Sprintf("trial_%02d", trial+1), func(t *testing.T) {
			runRemoteToolAudioScenario(t, testCase, 0, 0, time.Millisecond, 0, 0, 0)
		})
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
	calls := remoteToolAudioCalls(testCase, toolResultBytes)
	prompt := strings.Repeat("p", promptBytes)
	provider := newRemoteToolAudioProvider(t, responses, testCase.toolResponses, calls, deltaDelay, prompt, inputFrames*audio.FrameSize*3/2)
	defer provider.Close()
	endpoint, stopDevice := startAudioDeviceServerBinary(t, true)
	defer stopDevice()

	observationPath := filepath.Join(t.TempDir(), "tool-observations.jsonl")
	fixturePath := writeRemoteToolFixture(t, observationPath, calls, toolDelay)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, mockToolAgentBinaryPath,
		"--config-dir", t.TempDir(),
		"session",
		"--provider", "openai",
		"--model", "gpt-realtime-2.1",
		"--api-key", "hermetic-key",
		"--base-url", provider.WebSocketURL(),
		"--audio-device-server", endpoint,
		"--audio-out-device=",
		"--wait-for-close",
		"--max-duration", "30s",
	)
	if prompt != "" {
		command.Args = append(command.Args, prompt)
	}
	command.Env = append(os.Environ(),
		"YUI_E2E_TOOL_MOCK_FIXTURE="+fixturePath,
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start mock-tool agent binary: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if inputFrames > 0 {
		primeRemoteToolAudioInput(t, ctx, endpoint, provider, inputFrames)
	}

	clockCtx, stopClock := context.WithCancel(ctx)
	clockDone := make(chan error, 1)
	go driveRemoteToolAudioClock(clockCtx, endpoint, provider.firstAudioSent, callbackInterval, clockDone)

	select {
	case <-provider.allResponsesSent:
	case err := <-done:
		t.Fatalf("agent exited before the complete %s topology: %v; stdout=%q stderr=%q provider=%+v", testCase.name, err, stdout.String(), stderr.String(), provider.Snapshot())
	case <-ctx.Done():
		t.Fatalf("timed out receiving the complete %s topology: %v; provider=%+v stderr=%q", testCase.name, ctx.Err(), provider.Snapshot(), stderr.String())
	}

	stopClock()
	select {
	case err := <-clockDone:
		if err != nil {
			t.Fatalf("advance remote playback callback: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timed out stopping remote playback clock: %v", ctx.Err())
	}
	snapshot := waitForRemoteToolAudio(t, ctx, endpoint, want, callbackInterval)
	got := nonzeroRemoteToolAudio(snapshot.RenderedSamples)
	if err := verifyRemoteToolAudio(got, want); err != nil {
		t.Fatalf("%s process-boundary audio verification: %v (playback=%+v provider=%+v)", testCase.name, err, snapshot.Playback, provider.Snapshot())
	}
	if snapshot.Playback.DroppedSamples != 0 || snapshot.Playback.OverflowEvents != 0 || snapshot.Playback.DiscardedSamples != 0 || snapshot.Playback.DiscardEvents != 0 {
		t.Fatalf("%s remote playback reported loss: %+v", testCase.name, snapshot.Playback)
	}

	provider.ReleaseClose()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("mock-tool agent exited: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("agent did not close after verified device playback: %v; stderr=%q", ctx.Err(), stderr.String())
	}
	observed := provider.Snapshot()
	wantInitialRequest := 0
	if prompt != "" {
		wantInitialRequest = 1
	}
	wantInputHistory := 0
	if inputFrames > 0 {
		wantInputHistory = 1
	}
	if observed.protocolError != "" || observed.responsesSent != len(responses) || observed.responseCreates != len(calls) || observed.toolResults != len(calls) || observed.initialRequests != wantInitialRequest || observed.inputHistories != wantInputHistory {
		t.Fatalf("provider edge observation = %+v, want responses=%d continuations=%d", observed, len(responses), len(calls))
	}
	assertRemoteToolObservations(t, observationPath, calls)
}

func primeRemoteToolAudioInput(t *testing.T, ctx context.Context, endpoint string, provider *remoteToolAudioProvider, frames int) {
	t.Helper()
	select {
	case <-provider.sessionReady:
	case <-ctx.Done():
		t.Fatalf("provider did not become ready for input-history prelude: %v", ctx.Err())
	}
	samples := remoteToolAudioPCM(frames*audio.FrameSize, 400)
	if err := audio.InjectRemoteDeviceServerCapture(ctx, endpoint, samples); err != nil {
		t.Fatalf("inject prior input history: %v", err)
	}
	for advanced := 0; advanced < frames; {
		batch := 8
		if remaining := frames - advanced; remaining < batch {
			batch = remaining
		}
		if err := audio.AdvanceRemoteDeviceServer(ctx, endpoint, batch); err != nil {
			t.Fatalf("advance prior input callback: %v", err)
		}
		advanced += batch
		wantSeen := advanced * audio.FrameSize * 3 / 2
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
}

func driveRemoteToolAudioClock(ctx context.Context, endpoint string, start <-chan struct{}, interval time.Duration, result chan<- error) {
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
			if err := audio.AdvanceRemoteDeviceServer(ctx, endpoint, 1); err != nil {
				if ctx.Err() != nil {
					result <- nil
					return
				}
				result <- err
				return
			}
		}
	}
}

func waitForRemoteToolAudio(t *testing.T, ctx context.Context, endpoint string, want []int16, callbackInterval time.Duration) audio.DeviceServerSnapshot {
	t.Helper()
	// The provider having written all WebSocket events does not mean the agent's
	// media pump has consumed them yet. Keep the external callback clock alive
	// until every expected sample reaches the device or the unique tail of the
	// final response proves that all earlier FIFO audio has crossed the device
	// edge. A fixed callback budget or wall-clock quiescence can race ahead of a
	// coverage/race-instrumented agent and mistake audio still in transit for
	// dropped audio.
	markerSamples := audio.FrameSize
	if markerSamples > len(want) {
		markerSamples = len(want)
	}
	finalMarker := want[len(want)-markerSamples:]
	const callbacksPerSnapshot = 32
	ticker := time.NewTicker(callbackInterval)
	defer ticker.Stop()
	callbacksSinceSnapshot := callbacksPerSnapshot
	for {
		if callbacksSinceSnapshot >= callbacksPerSnapshot {
			snapshot, err := audio.ReadRemoteDeviceServerSnapshot(ctx, endpoint)
			if err != nil {
				t.Fatalf("read remote device evidence: %v", err)
			}
			got := nonzeroRemoteToolAudio(snapshot.RenderedSamples)
			if len(got) >= len(want) || remoteToolAudioHasSuffix(got, finalMarker) {
				return snapshot
			}
			callbacksSinceSnapshot = 0
		}
		select {
		case <-ticker.C:
			// The background callback driver is stopped before this loop. Advance
			// and snapshot sequentially so a large evidence response cannot race
			// another HTTP request against the deterministic device server.
			if err := audio.AdvanceRemoteDeviceServer(ctx, endpoint, 1); err != nil {
				t.Fatalf("advance remote playback while awaiting final marker: %v", err)
			}
			callbacksSinceSnapshot++
		case <-ctx.Done():
			t.Fatal("remote playback did not reach final PCM marker before the scenario deadline")
		}
	}
}

func remoteToolAudioHasSuffix(samples, suffix []int16) bool {
	return len(suffix) > 0 && len(samples) >= len(suffix) && reflect.DeepEqual(samples[len(samples)-len(suffix):], suffix)
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

func newRemoteToolAudioProvider(t *testing.T, responses [][]int16, toolAt map[int]bool, calls []remoteToolCallFixture, deltaDelay time.Duration, expectedPrompt string, expectedInputSamples int) *remoteToolAudioProvider {
	t.Helper()
	provider := &remoteToolAudioProvider{
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
	provider.server = httptest.NewServer(http.HandlerFunc(provider.handle))
	return provider
}

func (p *remoteToolAudioProvider) WebSocketURL() string {
	return strings.Replace(p.server.URL, "http://", "ws://", 1)
}

func (p *remoteToolAudioProvider) ReleaseClose() { p.releaseOnce.Do(func() { close(p.releaseClose) }) }

func (p *remoteToolAudioProvider) Close() {
	p.ReleaseClose()
	p.server.Close()
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
	if request.Header.Get("Authorization") != "Bearer hermetic-key" {
		p.fail("missing hermetic authorization")
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	connection, err := p.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		p.fail("upgrade websocket: " + err.Error())
		return
	}
	defer connection.Close()
	closeWriterDone := make(chan struct{})
	go func() {
		select {
		case <-p.releaseClose:
			_ = p.send(connection, map[string]string{"type": "session.closed", "session_id": "sess-tool-audio-e2e", "reason": "fixture_complete"})
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
		switch event.Type {
		case "session.update":
			p.mu.Lock()
			alreadyStarted := p.started
			p.started = true
			p.mu.Unlock()
			if alreadyStarted {
				continue
			}
			if err := p.send(connection, map[string]any{"type": "session.created", "session": map[string]string{"id": "sess-tool-audio-e2e", "model": "gpt-realtime-2.1"}}); err != nil {
				return
			}
			p.sessionReadyOnce.Do(func() { close(p.sessionReady) })
			if p.expectedPrompt == "" && p.expectedInputSamples == 0 {
				if err := p.sendReadyResponses(connection); err != nil {
					return
				}
			}
		case "input_audio_buffer.append":
			decoded, decodeErr := base64.StdEncoding.DecodeString(event.Audio)
			if decodeErr != nil || len(decoded)%2 != 0 {
				p.fail("decode prior input audio")
				return
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
				if err := p.sendReadyResponses(connection); err != nil {
					return
				}
			}
		case "conversation.item.create":
			if event.Item.Type == "message" {
				if len(event.Item.Content) != 1 || event.Item.Content[0].Text != p.expectedPrompt {
					p.fail("large preamble did not reach the provider intact")
					return
				}
				p.mu.Lock()
				p.userPromptSeen = true
				p.mu.Unlock()
				continue
			}
			if event.Item.Type != "function_call_output" {
				continue
			}
			if err := p.acceptToolResult(event.Item.CallID, event.Item.Output); err != nil {
				p.fail(err.Error())
				return
			}
		case "response.create":
			p.mu.Lock()
			if p.nextResponse == 0 && p.expectedPrompt != "" {
				if !p.userPromptSeen {
					p.mu.Unlock()
					p.fail("initial response.create preceded the large preamble")
					return
				}
				p.initialRequests++
				p.mu.Unlock()
				if err := p.sendReadyResponses(connection); err != nil {
					return
				}
				continue
			}
			if p.pendingCall < 0 || !p.pendingResult {
				p.mu.Unlock()
				p.fail("response.create arrived without its correlated mock tool result")
				return
			}
			p.responseCreates++
			p.pendingCall = -1
			p.pendingResult = false
			p.mu.Unlock()
			if err := p.sendReadyResponses(connection); err != nil {
				return
			}
		}
	}
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
	if err := p.send(connection, map[string]any{"type": "response.created", "response": map[string]string{"id": responseID}}); err != nil {
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
			"type": "response.output_item.added", "response_id": responseID, "output_index": 1,
			"item": map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name},
		}); err != nil {
			return err
		}
		if err := p.send(connection, map[string]any{
			"type": "response.function_call_arguments.done", "response_id": responseID,
			"call_id": call.ID, "name": call.Name, "arguments": call.Arguments,
		}); err != nil {
			return err
		}
	}
	return p.send(connection, map[string]any{"type": "response.done", "response": map[string]string{"id": responseID, "status": "completed"}})
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
		"type": "response.output_audio.delta", "response_id": responseID, "item_id": itemID, "content_index": 0,
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
	var expected []int16
	for _, response := range responses {
		if len(response) == 0 {
			continue
		}
		converted, err := wavio.Resample(response, wavio.Rate24kHz, audio.SampleRate)
		if err != nil {
			t.Fatalf("resample reference audio: %v", err)
		}
		expected = append(expected, converted...)
	}
	return nonzeroRemoteToolAudio(expected)
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
		return fmt.Errorf("remote device rendered %d/%d nonzero samples (lost %d, %.1f%% retained)", len(got), len(want), len(want)-len(got), 100*float64(len(got))/float64(len(want)))
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

// TestToolAudioDeviceVerifierRejectsEdgeDamage red-teams the device-edge
// oracle itself. These controls never touch playback internals: they mutate
// only the PCM a faulty external device would report. The healthy E2E test is
// meaningful only if every realistic loss, duplication, reorder, and sample
// corruption below is rejected.
func TestToolAudioDeviceVerifierRejectsEdgeDamage(t *testing.T) {
	want := remoteToolAudioPCM(20*audio.FrameSize, 1200)
	controls := []struct {
		name   string
		mutate func([]int16) []int16
	}{
		{name: "retain_10_percent", mutate: retainRemoteToolAudio(10)},
		{name: "retain_25_percent", mutate: retainRemoteToolAudio(25)},
		{name: "retain_49_percent", mutate: retainRemoteToolAudio(49)},
		{name: "retain_50_percent", mutate: retainRemoteToolAudio(50)},
		{name: "retain_75_percent", mutate: retainRemoteToolAudio(75)},
		{name: "retain_90_percent", mutate: retainRemoteToolAudio(90)},
		{name: "retain_99_percent", mutate: retainRemoteToolAudio(99)},
		{name: "drop_first_sample", mutate: dropRemoteToolAudioSpan(0, 1)},
		{name: "drop_last_sample", mutate: dropRemoteToolAudioSpan(len(want)-1, 1)},
		{name: "drop_first_frame", mutate: dropRemoteToolAudioSpan(0, audio.FrameSize)},
		{name: "drop_middle_frame", mutate: dropRemoteToolAudioSpan(10*audio.FrameSize, audio.FrameSize)},
		{name: "drop_last_frame", mutate: dropRemoteToolAudioSpan(19*audio.FrameSize, audio.FrameSize)},
		{name: "duplicate_first_frame", mutate: duplicateRemoteToolAudioSpan(0, audio.FrameSize)},
		{name: "duplicate_middle_frame", mutate: duplicateRemoteToolAudioSpan(10*audio.FrameSize, audio.FrameSize)},
		{name: "duplicate_last_frame", mutate: duplicateRemoteToolAudioSpan(19*audio.FrameSize, audio.FrameSize)},
		{name: "swap_first_frames", mutate: swapRemoteToolAudioFrames(0, 1)},
		{name: "swap_middle_frames", mutate: swapRemoteToolAudioFrames(9, 10)},
		{name: "corrupt_first_sample", mutate: corruptRemoteToolAudioSample(0)},
		{name: "corrupt_middle_sample", mutate: corruptRemoteToolAudioSample(len(want) / 2)},
		{name: "corrupt_last_sample", mutate: corruptRemoteToolAudioSample(len(want) - 1)},
	}
	for _, control := range controls {
		t.Run(control.name, func(t *testing.T) {
			got := control.mutate(append([]int16(nil), want...))
			if err := verifyRemoteToolAudio(got, want); err == nil {
				t.Fatal("device-edge verifier accepted intentionally damaged PCM")
			}
		})
	}
}

func TestToolAudioFinalMarkerCannotBeImpersonatedByKnownTruncations(t *testing.T) {
	responses := make([][]int16, 9)
	for index, count := range []int{38400, 0, 66000, 66000, 0, 0, 0, 0, 96000} {
		if count > 0 {
			responses[index] = remoteToolAudioPCM(count, int16(900+index*2200))
		}
	}
	want := remoteToolAudioExpected(t, responses)
	marker := want[len(want)-audio.FrameSize:]
	for _, lost := range []int{1, audio.FrameSize, 6400, 40160, len(want) / 2} {
		t.Run(fmt.Sprintf("lost_%d", lost), func(t *testing.T) {
			truncated := want[:len(want)-lost]
			if remoteToolAudioHasSuffix(truncated, marker) {
				t.Fatalf("truncated device PCM falsely matched the final marker after losing %d samples", lost)
			}
		})
	}
}

func retainRemoteToolAudio(percent int) func([]int16) []int16 {
	return func(samples []int16) []int16 { return samples[:len(samples)*percent/100] }
}

func dropRemoteToolAudioSpan(start, count int) func([]int16) []int16 {
	return func(samples []int16) []int16 {
		return append(samples[:start:start], samples[start+count:]...)
	}
}

func duplicateRemoteToolAudioSpan(start, count int) func([]int16) []int16 {
	return func(samples []int16) []int16 {
		result := make([]int16, 0, len(samples)+count)
		result = append(result, samples[:start+count]...)
		result = append(result, samples[start:start+count]...)
		return append(result, samples[start+count:]...)
	}
}

func swapRemoteToolAudioFrames(first, second int) func([]int16) []int16 {
	return func(samples []int16) []int16 {
		firstStart := first * audio.FrameSize
		secondStart := second * audio.FrameSize
		firstFrame := append([]int16(nil), samples[firstStart:firstStart+audio.FrameSize]...)
		copy(samples[firstStart:firstStart+audio.FrameSize], samples[secondStart:secondStart+audio.FrameSize])
		copy(samples[secondStart:secondStart+audio.FrameSize], firstFrame)
		return samples
	}
}

func corruptRemoteToolAudioSample(index int) func([]int16) []int16 {
	return func(samples []int16) []int16 {
		samples[index] = -samples[index]
		return samples
	}
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
