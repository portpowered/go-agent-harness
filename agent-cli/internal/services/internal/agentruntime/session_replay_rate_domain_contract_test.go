package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	devices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	deviceswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	c106ProviderRate = 24000
	c106PublicRate   = 16000
	c106ProviderPCM  = 2400
	c106PublicPCM    = 1600
)

var (
	errC106OracleRateMismatch   = errors.New("C106 cross-rate oracle rejected sample rate")
	errC106OracleOddPCM         = errors.New("C106 cross-rate oracle rejected odd PCM16 byte count")
	errC106OracleDuration       = errors.New("C106 cross-rate oracle rejected duration")
	errC106OracleSampleMismatch = errors.New("C106 cross-rate oracle rejected sample mismatch")
	errC106OracleUnsupportedPCM = errors.New("C106 cross-rate oracle rejected PCM16 shape")
)

type c106PublicSessionResult struct {
	providerPCM      []byte
	publicPCM        []byte
	recordedMediaPCM []byte
	outputRate       int
	outputSamples    int
	replayDone       bool
	replayErr        error
	finalized        bool
	finalizeErr      error
}

type c106EventCollector struct {
	mu       sync.Mutex
	messages []messages.StreamMessage
}

func (c *c106EventCollector) Publish(_ context.Context, event session.LiveEvent) error {
	if c == nil || event.Message == nil {
		return nil
	}
	c.mu.Lock()
	c.messages = append(c.messages, *event.Message)
	c.mu.Unlock()
	return nil
}

func (c *c106EventCollector) audioPCM() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var pcm []byte
	for _, message := range c.messages {
		if message.Type != messages.StreamTypeAudioDelta {
			continue
		}
		value, ok := message.Value.(*messages.AudioDeltaValue)
		if ok && value != nil {
			pcm = append(pcm, value.Content...)
		}
	}
	return pcm
}

type c106Recorder struct {
	mu            sync.Mutex
	messagePCM    []byte
	agentMediaPCM []byte
	finalized     bool
	finalizeErr   error
}

func (r *c106Recorder) RecordMessage(_ context.Context, record session.LiveRecord) error {
	if r == nil || record.Message.Type != messages.StreamTypeAudioDelta {
		return nil
	}
	value, ok := record.Message.Value.(*messages.AudioDeltaValue)
	if !ok || value == nil {
		return nil
	}
	r.mu.Lock()
	r.messagePCM = append(r.messagePCM, value.Content...)
	r.mu.Unlock()
	return nil
}

func (r *c106Recorder) RecordAudio(_ context.Context, record session.LiveAudioRecord) error {
	if r == nil || record.Direction != session.LiveRecordAgent || record.Admission != session.LiveAudioMediaBridged {
		return nil
	}
	r.mu.Lock()
	r.agentMediaPCM = append(r.agentMediaPCM, c106PCM16LEBytes(record.Frame.Samples)...)
	r.mu.Unlock()
	return nil
}

func (*c106Recorder) RecordEvent(context.Context, session.LiveEvent) error { return nil }

func (r *c106Recorder) Finalize(_ context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.finalized = true
	r.finalizeErr = runErr
	r.mu.Unlock()
	return nil
}

func (r *c106Recorder) snapshot() (messagePCM, agentMediaPCM []byte, finalized bool, finalizeErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.messagePCM...), append([]byte(nil), r.agentMediaPCM...), r.finalized, r.finalizeErr
}

func c106PCM16LEBytes(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

func c106ProviderSamples() []int16 {
	samples := make([]int16, c106ProviderPCM)
	for index := range samples {
		samples[index] = int16((index*1973)%60000 - 30000)
	}
	return samples
}

func c106BuildReplayFixture(t *testing.T) string {
	t.Helper()
	basePath := filepath.Join("..", "..", "..", "..", "test", "integration", "testdata", "openai_realtime_smoke.session.json")
	capture, err := gwtesting.LoadSessionCapture(basePath)
	if err != nil {
		t.Fatalf("load canonical realtime smoke capture: %v", err)
	}
	if len(capture.Records) < 10 {
		t.Fatalf("canonical realtime smoke capture has %d records; want at least 10", len(capture.Records))
	}

	records := make([]gwtesting.CapturedSessionEvent, 0, 13)
	records = append(records, capture.Records[0], capture.Records[1], capture.Records[2], capture.Records[3])
	records[0].Payload = json.RawMessage(`{"session":{"model":"gpt-realtime","type":"realtime","output_modalities":["audio"],"audio":{"input":{"format":{"type":"audio/pcm","rate":24000}},"output":{"format":{"type":"audio/pcm","rate":24000}}}},"type":"session.update"}`)
	records = append(records, capture.Records[4], capture.Records[5], capture.Records[6], capture.Records[7])

	appendServerEvent := func(eventType string, payload json.RawMessage) {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionServerToClient,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     payload,
		})
	}

	deltaPayload, err := json.Marshal(map[string]string{
		"type":  "response.output_audio.delta",
		"delta": base64.StdEncoding.EncodeToString(c106PCM16LEBytes(c106ProviderSamples())),
	})
	if err != nil {
		t.Fatalf("marshal C106 audio delta: %v", err)
	}
	appendServerEvent("response.output_audio.delta", deltaPayload)
	appendServerEvent("response.output_audio.done", json.RawMessage(`{"type":"response.output_audio.done"}`))
	appendServerEvent("response.done", json.RawMessage(`{"type":"response.done","response":{"id":"resp_c106","status":"completed"}}`))
	appendServerEvent("session.closed", json.RawMessage(`{"type":"session.closed","session_id":"sess_c106_rate_domain","reason":"fixture_complete"}`))

	capture.Version = gwtesting.SessionCaptureVersion
	capture.Session.ID = "sess_c106_rate_domain"
	capture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	capture.Records = records
	encoded, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal C106 replay fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "c106-rate-domain.session.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write C106 replay fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("validate C106 replay fixture: %v", err)
	}
	return path
}

func c106RunPublicSession(t *testing.T, recording bool) c106PublicSessionResult {
	t.Helper()
	fixturePath := c106BuildReplayFixture(t)
	dialer, err := gwtesting.NewReplayWebSocketDialer(fixturePath)
	if err != nil {
		t.Fatalf("open C106 replay dialer: %v", err)
	}
	inferencer, err := buildOpenAIRealtimeSessionInferencerWithInputAudioTranscription(
		config.OpenAIConfig{APIKey: "c106-replay-key", Model: "gpt-realtime"},
		"",
		dialer,
		models.InputAudioTranscriptionConfig{},
	)
	if err != nil {
		t.Fatalf("build C106 replay inferencer: %v", err)
	}
	outputConfigurer, ok := inferencer.(sessionAudioOutputConfigurer)
	if !ok {
		t.Fatalf("C106 replay inferencer %T does not expose the audio output contract", inferencer)
	}
	inputConfigurer, ok := inferencer.(sessionAudioInputConfigurer)
	if !ok {
		t.Fatalf("C106 replay inferencer %T does not expose the audio input contract", inferencer)
	}
	outputConfigurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(c106ProviderRate))
	inputConfigurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(c106ProviderRate))

	outputPath := filepath.Join(t.TempDir(), "public-16k.wav")
	sink, err := audio.NewFileSinkAtSampleRate(outputPath, nil, c106PublicRate)
	if err != nil {
		t.Fatalf("open C106 public sink: %v", err)
	}
	collector := &c106EventCollector{}
	var recorder *c106Recorder
	if recording {
		recorder = &c106Recorder{}
	}
	liveService := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return inferencer, nil
		},
		Clock:         func() time.Time { return time.Now() },
		Scheduler:     clock.Real{},
		EventCapacity: 64,
	})
	runner, ok := liveService.(session.LiveRunner)
	if !ok {
		t.Fatalf("C106 live service %T does not implement LiveRunner", liveService)
	}
	request := session.LiveRequest{
		SessionID:             "sess_c106_rate_domain",
		ParticipantID:         "participant_c106",
		Provider:              "openai",
		Model:                 "gpt-realtime",
		OpeningPrompt:         "run the openai smoke replay",
		OpeningPromptPresent:  true,
		InputAudioSampleRate:  c106ProviderRate,
		OutputAudioSampleRate: c106ProviderRate,
		OutputAudioContinuous: true,
		Replay: session.LiveReplayPolicy{
			InputCapturePath: fixturePath,
			Timing:           session.LiveReplayTimingFast,
		},
		MaxDuration: 5 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := runner.RunLive(ctx, session.LiveRunOptions{
		Request: request,
		Devices: deviceswire.NewFileService(),
		DeviceRequest: devices.Request{
			PlaybackEnabled: true,
			SampleRate:      c106ProviderRate,
			Channels:        1,
			FileOutput: &devices.FileOutput{
				Sink:       sink,
				SampleRate: c106PublicRate,
				Continuous: true,
			},
		},
		Events:   collector,
		Recorder: recorder,
	})
	if runErr != nil {
		t.Fatalf("C106 public session replay recording=%t: %v; replay=%v", recording, runErr, dialer.Err())
	}
	select {
	case <-dialer.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("C106 replay did not reach its bounded terminal cleanup")
	}
	if replayErr := dialer.Err(); replayErr != nil {
		t.Fatalf("C106 replay divergence or incomplete capture: %v", replayErr)
	}
	publicWAV, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read C106 public WAV: %v", err)
	}
	outputRate, publicSamples, err := wavio.Read(bytes.NewReader(publicWAV))
	if err != nil {
		t.Fatalf("parse C106 public WAV: %v", err)
	}
	if recorder == nil {
		return c106PublicSessionResult{
			providerPCM:   collector.audioPCM(),
			publicPCM:     c106PCM16LEBytes(publicSamples),
			outputRate:    outputRate,
			outputSamples: len(publicSamples),
			replayDone:    true,
		}
	}
	messagePCM, mediaPCM, finalized, finalizeErr := recorder.snapshot()
	if !finalized {
		t.Fatal("C106 recording-on run did not finalize its recorder")
	}
	if finalizeErr != nil {
		t.Fatalf("C106 recording-on recorder finalized with run error: %v", finalizeErr)
	}
	if !bytes.Equal(messagePCM, collector.audioPCM()) {
		t.Fatalf("C106 recording message audio differs from public event audio: message=%d event=%d bytes", len(messagePCM), len(collector.audioPCM()))
	}
	return c106PublicSessionResult{
		providerPCM:      collector.audioPCM(),
		publicPCM:        c106PCM16LEBytes(publicSamples),
		recordedMediaPCM: mediaPCM,
		outputRate:       outputRate,
		outputSamples:    len(publicSamples),
		replayDone:       true,
		finalized:        finalized,
		finalizeErr:      finalizeErr,
	}
}

func c106CompareCrossRatePCM(providerRate int, providerPCM []byte, publicRate int, publicPCM []byte) error {
	if providerRate != c106ProviderRate || publicRate != c106PublicRate {
		return fmt.Errorf("%w: provider=%d public=%d", errC106OracleRateMismatch, providerRate, publicRate)
	}
	if len(providerPCM)%2 != 0 || len(publicPCM)%2 != 0 {
		return fmt.Errorf("%w: provider_bytes=%d public_bytes=%d", errC106OracleOddPCM, len(providerPCM), len(publicPCM))
	}
	providerSamples, err := codec.DecodePCM16(providerPCM)
	if err != nil {
		return fmt.Errorf("%w: provider: %v", errC106OracleUnsupportedPCM, err)
	}
	publicSamples, err := codec.DecodePCM16(publicPCM)
	if err != nil {
		return fmt.Errorf("%w: public: %v", errC106OracleUnsupportedPCM, err)
	}
	resampler, err := wavio.NewPCM16Resampler(providerRate, publicRate)
	if err != nil {
		return fmt.Errorf("%w: create canonical resampler: %v", errC106OracleUnsupportedPCM, err)
	}
	converted, err := resampler.Process(providerSamples, true)
	if err != nil {
		return fmt.Errorf("%w: process canonical resampler: %v", errC106OracleUnsupportedPCM, err)
	}
	if uint64(len(providerSamples))*uint64(publicRate) != uint64(len(publicSamples))*uint64(providerRate) {
		return fmt.Errorf("%w: provider_samples=%d public_samples=%d", errC106OracleDuration, len(providerSamples), len(publicSamples))
	}
	if len(converted) != len(publicSamples) {
		return fmt.Errorf("%w: converted_samples=%d public_samples=%d", errC106OracleDuration, len(converted), len(publicSamples))
	}
	for index := range converted {
		if converted[index] != publicSamples[index] {
			return fmt.Errorf("%w: first_mismatch_sample=%d converted=%d public=%d", errC106OracleSampleMismatch, index, converted[index], publicSamples[index])
		}
	}
	return nil
}

func TestSessionReplayRateDomainContractYUI24kTo16k(t *testing.T) {
	off := c106RunPublicSession(t, false)
	on := c106RunPublicSession(t, true)
	if off.outputRate != c106PublicRate || on.outputRate != c106PublicRate {
		t.Fatalf("public sink rates = %d/%d, want %d/%d", off.outputRate, on.outputRate, c106PublicRate, c106PublicRate)
	}
	if off.outputSamples != c106PublicPCM || on.outputSamples != c106PublicPCM {
		t.Fatalf("public sample counts = %d/%d, want %d/%d", off.outputSamples, on.outputSamples, c106PublicPCM, c106PublicPCM)
	}
	if len(off.providerPCM) != c106ProviderPCM*2 || len(on.providerPCM) != c106ProviderPCM*2 {
		t.Fatalf("provider PCM byte counts = %d/%d, want %d", len(off.providerPCM), len(on.providerPCM), c106ProviderPCM*2)
	}
	if len(off.publicPCM) != c106PublicPCM*2 || len(on.publicPCM) != c106PublicPCM*2 {
		t.Fatalf("public PCM byte counts = %d/%d, want %d", len(off.publicPCM), len(on.publicPCM), c106PublicPCM*2)
	}
	if !bytes.Equal(off.providerPCM, on.providerPCM) {
		t.Fatal("provider PCM changed between recording-off and recording-on runs")
	}
	if !bytes.Equal(off.publicPCM, on.publicPCM) {
		t.Fatal("public PCM changed between recording-off and recording-on runs")
	}
	if !bytes.Equal(on.recordedMediaPCM, on.providerPCM) {
		t.Fatalf("recording-on provider media bytes = %d, want exact provider capture %d", len(on.recordedMediaPCM), len(on.providerPCM))
	}
	if err := c106CompareCrossRatePCM(c106ProviderRate, on.providerPCM, c106PublicRate, on.publicPCM); err != nil {
		t.Fatalf("strict canonical 24 kHz to 16 kHz oracle: %v", err)
	}
	t.Logf("C106 cross-rate PASS provider=%dHz/%d samples/%d bytes sha256=%s public=%dHz/%d samples/%d bytes sha256=%s", c106ProviderRate, c106ProviderPCM, len(on.providerPCM), c106SHA256(on.providerPCM), c106PublicRate, c106PublicPCM, len(on.publicPCM), c106SHA256(on.publicPCM))
}

func TestSessionReplayRateDomainContractRejectsWrongRateOddBytesAndMutation(t *testing.T) {
	providerPCM := c106PCM16LEBytes(c106ProviderSamples())
	resampler, err := wavio.NewPCM16Resampler(c106ProviderRate, c106PublicRate)
	if err != nil {
		t.Fatalf("create C106 oracle fixture resampler: %v", err)
	}
	publicSamples, err := resampler.Process(c106ProviderSamples(), true)
	if err != nil {
		t.Fatalf("resample C106 oracle fixture: %v", err)
	}
	publicPCM := c106PCM16LEBytes(publicSamples)
	if err := c106CompareCrossRatePCM(c106ProviderRate, providerPCM, c106PublicRate, publicPCM); err != nil {
		t.Fatalf("canonical fixture should pass strict oracle: %v", err)
	}
	if err := c106CompareCrossRatePCM(c106ProviderRate, providerPCM, c106ProviderRate, publicPCM); !errors.Is(err, errC106OracleRateMismatch) {
		t.Fatalf("wrong public rate error = %v, want stable rate rejection", err)
	}
	oddPCM := append(append([]byte(nil), publicPCM...), 0x7f)
	if err := c106CompareCrossRatePCM(c106ProviderRate, providerPCM, c106PublicRate, oddPCM); !errors.Is(err, errC106OracleOddPCM) {
		t.Fatalf("odd public PCM error = %v, want stable shape rejection", err)
	}
	mutatedPCM := append([]byte(nil), publicPCM...)
	binary.LittleEndian.PutUint16(mutatedPCM[20:], binary.LittleEndian.Uint16(mutatedPCM[20:])+1)
	firstMutation := c106CompareCrossRatePCM(c106ProviderRate, providerPCM, c106PublicRate, mutatedPCM)
	secondMutation := c106CompareCrossRatePCM(c106ProviderRate, providerPCM, c106PublicRate, mutatedPCM)
	if !errors.Is(firstMutation, errC106OracleSampleMismatch) || firstMutation.Error() != secondMutation.Error() {
		t.Fatalf("one-sample mutation errors are not stable fail-closed diagnostics: first=%v second=%v", firstMutation, secondMutation)
	}

	fixturePath := c106BuildReplayFixture(t)
	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read C106 fixture for integrity negative control: %v", err)
	}
	mutatedFixture := bytes.Replace(fixtureBytes, []byte("run the openai smoke replay"), []byte("mutated C106 replay prompt"), 1)
	if bytes.Equal(mutatedFixture, fixtureBytes) {
		t.Fatal("integrity negative control did not locate a replay-relevant payload")
	}
	mutatedFixturePath := filepath.Join(t.TempDir(), "c106-rate-domain-mutated.session.json")
	if err := os.WriteFile(mutatedFixturePath, mutatedFixture, 0o600); err != nil {
		t.Fatalf("write mutated C106 fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(mutatedFixturePath); !errors.Is(err, gwtesting.ErrSessionCaptureIntegrity) {
		t.Fatalf("mutated replay fixture error = %v, want integrity rejection before use", err)
	}
}

func c106SHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

type c106NoMediaSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
}

func (s *c106NoMediaSession) Send(context.Context, messages.StreamMessage) bool { return true }
func (s *c106NoMediaSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *c106NoMediaSession) Done() <-chan struct{} { return s.done }
func (s *c106NoMediaSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}
func (*c106NoMediaSession) RTCMedia() audio.MediaEndpoints { return audio.MediaEndpoints{} }

type c106NoMediaInferencer struct{ pcm []byte }

func (i *c106NoMediaInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s := &c106NoMediaSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		done:    make(chan struct{}),
	}
	responseID := "c106-same-rate-response"
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("c106", "same-rate")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioDeltaValue(i.pcm)},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("c106", "fixture_complete")},
	}
	for _, event := range events {
		if !s.receive.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	return s, nil
}

func c106RunSameRateObserver(t *testing.T, recording bool) (pcm []byte, recorder *c106Recorder) {
	t.Helper()
	pcm = c106PCM16LEBytes(make([]int16, 64))
	for index := 0; index < len(pcm)/2; index++ {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(index*257-8000))
	}
	collector := &c106EventCollector{}
	if recording {
		recorder = &c106Recorder{}
	}
	liveService := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return &c106NoMediaInferencer{pcm: pcm}, nil
		},
		Clock:     func() time.Time { return time.Now() },
		Scheduler: clock.Real{},
	})
	runner, ok := liveService.(session.LiveRunner)
	if !ok {
		t.Fatalf("C106 same-rate live service %T does not implement LiveRunner", liveService)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := runner.RunLive(ctx, session.LiveRunOptions{
		Request: session.LiveRequest{
			SessionID:             "sess_c106_same_rate",
			ParticipantID:         "participant_c106",
			Provider:              "c106",
			Model:                 "same-rate",
			OpeningPrompt:         "same-rate",
			OpeningPromptPresent:  true,
			InputAudioSampleRate:  c106PublicRate,
			OutputAudioSampleRate: c106PublicRate,
			OutputAudioContinuous: true,
		},
		Events:   collector,
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("C106 same-rate observer run recording=%t: %v", recording, err)
	}
	select {
	case <-time.After(20 * time.Millisecond):
	case <-context.Background().Done():
	}
	return collector.audioPCM(), recorder
}

func TestSessionReplayRateDomainContractC68SameRateAndObserverOmission(t *testing.T) {
	offPCM, _ := c106RunSameRateObserver(t, false)
	onPCM, recorder := c106RunSameRateObserver(t, true)
	if len(offPCM) != 128 || len(onPCM) != 128 {
		t.Fatalf("same-rate public PCM byte counts = %d/%d, want 128", len(offPCM), len(onPCM))
	}
	if !bytes.Equal(offPCM, onPCM) || c106SHA256(offPCM) != c106SHA256(onPCM) {
		t.Fatalf("same-rate public PCM changed across recording modes: off=%s on=%s", c106SHA256(offPCM), c106SHA256(onPCM))
	}
	if recorder == nil {
		t.Fatal("recording-on same-rate run did not allocate recorder")
	}
	messagePCM, mediaPCM, finalized, finalizeErr := recorder.snapshot()
	if !bytes.Equal(messagePCM, onPCM) {
		t.Fatalf("same-rate recorded message PCM = %d bytes, want exact public 128 bytes", len(messagePCM))
	}
	if len(mediaPCM) != 0 {
		t.Fatalf("same-rate recording media PCM = %d bytes, want C68 observer omission 0", len(mediaPCM))
	}
	if !finalized || finalizeErr != nil {
		t.Fatalf("same-rate observer recorder finalization = finalized:%t err:%v", finalized, finalizeErr)
	}
	t.Logf("C106 same-rate PASS provider/public=%d bytes sha256=%s; RECORDING_OBSERVER_OMISSION_UNRESOLVED accepted_audio=0 audio_bytes=0", len(onPCM), c106SHA256(onPCM))
}
