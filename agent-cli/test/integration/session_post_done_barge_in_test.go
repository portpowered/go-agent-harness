package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/clitest"
	"github.com/portpowered/go-agent-harness/agent-cli/test/integration/testnet"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

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
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("%s; provider=%+v stderr=%q", fmt.Sprintf(format, args...), provider.Snapshot(), agent.stderr.String())
	}
	select {
	case <-provider.responseDone:
	case err := <-agent.done:
		fail("agent exited before response.done: %v", err)
	case <-ctx.Done():
		fail("provider response did not complete: %v", ctx.Err())
	}

	clock := time.NewTicker(postDoneBargeInCallbackInterval)
	defer clock.Stop()
	advance := func(callbacks int) {
		t.Helper()
		for range callbacks {
			select {
			case <-clock.C:
			case <-ctx.Done():
				fail("device clock cancelled: %v", ctx.Err())
			}
			if err := device.Advance(ctx, 1); err != nil {
				fail("advance device clock: %v", err)
			}
		}
	}
	rendered := func() (devicegw.DeviceServerSnapshot, int) {
		t.Helper()
		snapshot, err := device.Snapshot(ctx)
		if err != nil {
			fail("read device evidence: %v", err)
		}
		return snapshot, len(nonzeroRemoteToolAudio(snapshot.RenderedSamples))
	}

	// The response is done; play part of its queued audio.
	for _, heard := rendered(); heard < postDoneBargeInHeard; _, heard = rendered() {
		advance(postDoneBargeInCallbacksPerCheck)
	}
	if err := device.InjectCapture(ctx, postDoneBargeInSpeechPCM()); err != nil {
		fail("inject near-end speech: %v", err)
	}
	interrupt := time.NewTimer(postDoneBargeInInterruptWait)
	defer interrupt.Stop()
	for truncated := false; !truncated; {
		advance(1)
		select {
		case <-provider.truncated:
			truncated = true
		case err := <-agent.done:
			fail("agent exited before truncating the interrupted item: %v", err)
		case <-interrupt.C:
			fail("no truncation within %s of the user speaking", postDoneBargeInInterruptWait)
		default:
		}
	}

	// Playback stopped: further callbacks render nothing more of the response.
	advance(postDoneBargeInSettleCallbacks)
	stopped, heard := rendered()
	advance(postDoneBargeInSettleCallbacks)
	after, heardAfter := rendered()
	if heardAfter != heard || after.Playback.QueuedSamples != 0 {
		fail("playback continued after the barge-in: rendered %d then %d samples, queued=%d", heard, heardAfter, after.Playback.QueuedSamples)
	}
	if total := postDoneBargeInSamples * 2 / 3; heard >= total {
		fail("device rendered %d of %d response samples; the barge-in did not stop playback", heard, total)
	}
	if stopped.Playback.DroppedSamples != 0 || stopped.Playback.OverflowEvents != 0 {
		fail("device playback lost samples before the barge-in: %+v", stopped.Playback)
	}
	assertPostDoneTruncation(t, provider.Snapshot(), heard/postDoneBargeInDeviceSamplesPerMS)

	provider.ReleaseClose()
	awaitRemoteToolAudioExit(t, ctx, agent.done, agent.stdout, agent.stderr, "agent did not close after the provider closed", "agent exited")
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

type postDoneTruncate struct {
	ItemID       string `json:"item_id"`
	ContentIndex int    `json:"content_index"`
	AudioEndMS   int    `json:"audio_end_ms"`
}

type postDoneBargeInSnapshot struct {
	responseCreates int
	speechStarted   bool
	truncates       []postDoneTruncate
	protocolError   string
}

// postDoneBargeInProvider answers the prompt with one long audio response
// sent in a burst, then acts as a server-VAD provider: loud input audio
// yields input_audio_buffer.speech_started.
type postDoneBargeInProvider struct {
	server   *httptest.Server
	upgrader websocket.Upgrader
	response []int16

	responseDone, truncated, releaseClose           chan struct{}
	doneOnce, speechOnce, truncateOnce, releaseOnce sync.Once
	writeMu                                         sync.Mutex

	mu       sync.Mutex
	observed postDoneBargeInSnapshot
}

func newPostDoneBargeInProvider() *postDoneBargeInProvider {
	return &postDoneBargeInProvider{
		response:     remoteToolAudioPCM(postDoneBargeInSamples, 900),
		responseDone: make(chan struct{}),
		truncated:    make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
}

func (p *postDoneBargeInProvider) WebSocketURL() string {
	return "ws" + p.server.URL[len("http"):]
}

func (p *postDoneBargeInProvider) ReleaseClose() { p.releaseOnce.Do(func() { close(p.releaseClose) }) }

func (p *postDoneBargeInProvider) Snapshot() postDoneBargeInSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	observed := p.observed
	observed.truncates = append([]postDoneTruncate(nil), p.observed.truncates...)
	return observed
}

func (p *postDoneBargeInProvider) handle(writer http.ResponseWriter, request *http.Request) {
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
	readerDone := make(chan struct{})
	defer close(readerDone)
	go func() {
		select {
		case <-p.releaseClose:
			_ = p.send(connection, map[string]string{"type": rtEventSessionClosed, "reason": "fixture_complete"}) //nolint:errcheck // send records write failures through p.fail
		case <-readerDone:
		}
	}()
	for {
		_, payload, err := connection.ReadMessage()
		if err != nil || !p.handleEvent(connection, payload) {
			return
		}
	}
}

func (p *postDoneBargeInProvider) handleEvent(connection *websocket.Conn, payload []byte) bool {
	var event struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
		postDoneTruncate
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		p.fail("decode client event: " + err.Error())
		return false
	}
	switch event.Type {
	case rtEventSessionUpdate:
		return p.send(connection, map[string]any{"type": rtEventSessionCreated, "session": map[string]string{"id": "sess-post-done-barge-in", "model": "gpt-realtime-2.1"}}) == nil
	case rtEventResponseCreate:
		return p.handleResponseCreate(connection)
	case rtEventInputAudioAppend:
		return p.handleInputAudio(connection, event.Audio)
	case "conversation.item.truncate":
		p.mu.Lock()
		p.observed.truncates = append(p.observed.truncates, event.postDoneTruncate)
		p.mu.Unlock()
		p.truncateOnce.Do(func() { close(p.truncated) })
	}
	return true
}

// handleResponseCreate answers the prompt: the whole response, then done.
func (p *postDoneBargeInProvider) handleResponseCreate(connection *websocket.Conn) bool {
	p.mu.Lock()
	p.observed.responseCreates++
	first := p.observed.responseCreates == 1
	p.mu.Unlock()
	if !first {
		return true
	}
	if p.send(connection, map[string]any{"type": rtEventResponseCreated, "response": map[string]string{"id": postDoneBargeInResponseID}}) != nil {
		return false
	}
	for offset := 0; offset < len(p.response); offset += remoteToolAudioDeltaSamples {
		end := min(offset+remoteToolAudioDeltaSamples, len(p.response))
		if p.send(connection, remoteToolAudioDelta(postDoneBargeInResponseID, postDoneBargeInItemID, p.response[offset:end])) != nil {
			return false
		}
	}
	if p.send(connection, map[string]string{"type": "response.output_audio.done", "response_id": postDoneBargeInResponseID, "item_id": postDoneBargeInItemID}) != nil {
		return false
	}
	if p.send(connection, map[string]any{"type": rtEventResponseDone, "response": map[string]string{"id": postDoneBargeInResponseID, "status": rtStatusCompleted}}) != nil {
		return false
	}
	p.doneOnce.Do(func() { close(p.responseDone) })
	return true
}

// handleInputAudio is the server VAD: the first loud input after the
// response is done starts user speech.
func (p *postDoneBargeInProvider) handleInputAudio(connection *websocket.Conn, encoded string) bool {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded)%2 != 0 {
		p.fail("decode input audio")
		return false
	}
	loud := false
	for index := 0; index+1 < len(decoded); index += 2 {
		sample := int(int16(uint16(decoded[index]) | uint16(decoded[index+1])<<8))
		if sample > postDoneBargeInVADLevel || sample < -postDoneBargeInVADLevel {
			loud = true
			break
		}
	}
	if !loud {
		return true
	}
	started := false
	p.speechOnce.Do(func() { started = true })
	if !started {
		return true
	}
	p.mu.Lock()
	p.observed.speechStarted = true
	p.mu.Unlock()
	return p.send(connection, map[string]any{"type": "input_audio_buffer.speech_started", "audio_start_ms": 0, "item_id": "item-user-post-done"}) == nil
}

func (p *postDoneBargeInProvider) send(connection *websocket.Conn, event any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := connection.WriteJSON(event); err != nil {
		p.fail("write server event: " + err.Error())
		return err
	}
	return nil
}

func (p *postDoneBargeInProvider) fail(message string) {
	p.mu.Lock()
	if p.observed.protocolError == "" {
		p.observed.protocolError = message
	}
	p.mu.Unlock()
}
