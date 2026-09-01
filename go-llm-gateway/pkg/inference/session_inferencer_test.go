package inference

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// mockSession is a simple messages.Session implementation for testing.
type mockSession struct {
	recvBuf *messages.TypedBuffer[messages.StreamMessage]
	sendBuf *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func newMockSession() *mockSession {
	return &mockSession{
		recvBuf: messages.NewTypedBuffer[messages.StreamMessage](64),
		sendBuf: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
}

func (s *mockSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.sendBuf.Write(ctx, msg)
}

func (s *mockSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recvBuf
}

func (s *mockSession) Done() <-chan struct{} {
	return s.done
}

func (s *mockSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// mockSessionGateway implements sessionGateway for testing.
type mockSessionGateway struct {
	mu              sync.Mutex
	capturedConfig  models.SessionConfig
	capturedConfigs []models.SessionConfig
	session         messages.Session
	err             error
}

func (m *mockSessionGateway) ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capturedConfig = config
	m.capturedConfigs = append(m.capturedConfigs, config)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.session, nil
}

// --- SessionGatewayInferencer tests ---

func TestSessionGatewayInferencer_ConnectSession(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}

	si := NewSessionGatewayInferencer(gw,
		WithSessionModel("grok-3-mini"),
		WithSessionVoice("Eve"),
		WithSessionInstructions("Be helpful"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := si.ConnectSession(ctx)
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if gw.capturedConfig.Model != "grok-3-mini" {
		t.Errorf("model: got %q, want %q", gw.capturedConfig.Model, "grok-3-mini")
	}
	if gw.capturedConfig.Voice != "Eve" {
		t.Errorf("voice: got %q, want %q", gw.capturedConfig.Voice, "Eve")
	}
	if gw.capturedConfig.Instructions != "Be helpful" {
		t.Errorf("instructions: got %q, want %q", gw.capturedConfig.Instructions, "Be helpful")
	}
	if session != sess {
		t.Fatal("ConnectSession should return the loop-owned session from the gateway unchanged")
	}
}

func TestSessionGatewayInferencer_WithSessionToolsDefensivelyCopies(t *testing.T) {
	tools := []models.ToolDefinition{{
		Name:        "read_file",
		Description: "Read a file",
		Parameters:  []models.ToolParameter{{Name: "path", Type: "string", Required: true}},
	}}
	si := NewSessionGatewayInferencer(&mockSessionGateway{session: newMockSession()}, WithSessionTools(tools))

	tools[0].Name = "mutated"
	tools[0].Parameters[0].Name = "mutated-path"

	got := si.Request().Config.Tools
	if len(got) != 1 || got[0].Name != "read_file" || len(got[0].Parameters) != 1 || got[0].Parameters[0].Name != "path" {
		t.Fatalf("session tools = %#v, want an immutable read_file definition", got)
	}
}

func TestSessionGatewayInferencer_WithSessionInputAudioTranscriptionDefensivelyCopies(t *testing.T) {
	policy := models.InputAudioTranscriptionConfig{Enabled: true, Model: models.DefaultInputAudioTranscriptionModel}
	si := NewSessionGatewayInferencer(&mockSessionGateway{session: newMockSession()}, WithSessionInputAudioTranscription(policy))

	policy.Model = "mutated-input-transcriber"
	snapshot := si.Request()
	if snapshot.Config.InputAudioTranscription == nil || !snapshot.Config.InputAudioTranscription.Enabled || snapshot.Config.InputAudioTranscription.Model != models.DefaultInputAudioTranscriptionModel {
		t.Fatalf("input transcription policy was not copied: %#v", snapshot.Config.InputAudioTranscription)
	}

	snapshot.Config.InputAudioTranscription.Model = "snapshot-mutated-input-transcriber"
	again := si.Request()
	if again.Config.InputAudioTranscription == nil || again.Config.InputAudioTranscription.Model != models.DefaultInputAudioTranscriptionModel {
		t.Fatalf("Request returned mutable input transcription state: %#v", again.Config.InputAudioTranscription)
	}
}

func TestSessionGatewayInferencer_SetSessionAudioFormatsAndRates(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}
	si := NewSessionGatewayInferencer(gw, WithSessionRequest(SessionRequest{
		Config: models.SessionConfig{Modalities: []models.SessionModality{models.SessionModalityText}},
	}))

	si.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate24000)
	// The output setter adds audio to a text-only request. The input setter then
	// exercises the existing-audio path while completing the provider contract.
	si.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate24000)

	configured := si.Request().Config
	if configured.OutputAudioFormat != models.AudioFormatPCM16 || configured.OutputAudioSampleRate != models.SampleRate24000 {
		t.Fatalf("configured output audio = %q/%d, want pcm16/24000", configured.OutputAudioFormat, configured.OutputAudioSampleRate)
	}
	if configured.InputAudioFormat != models.AudioFormatPCM16 || configured.InputAudioSampleRate != models.SampleRate24000 {
		t.Fatalf("configured input audio = %q/%d, want pcm16/24000", configured.InputAudioFormat, configured.InputAudioSampleRate)
	}
	if len(configured.Modalities) != 2 || configured.Modalities[0] != models.SessionModalityText || configured.Modalities[1] != models.SessionModalityAudio {
		t.Fatalf("configured modalities = %#v, want text and audio once", configured.Modalities)
	}

	connected, err := si.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = connected.Close() }()
	if gw.capturedConfig.InputAudioSampleRate != models.SampleRate24000 || gw.capturedConfig.OutputAudioSampleRate != models.SampleRate24000 {
		t.Fatalf("provider audio rates = %d/%d, want 24000/24000", gw.capturedConfig.InputAudioSampleRate, gw.capturedConfig.OutputAudioSampleRate)
	}
	if len(gw.capturedConfig.Modalities) != 2 || gw.capturedConfig.Modalities[1] != models.SessionModalityAudio {
		t.Fatalf("provider modalities = %#v, want audio enabled once", gw.capturedConfig.Modalities)
	}

	var nilInferencer *SessionGatewayInferencer
	nilInferencer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate24000)
	nilInferencer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate24000)
}

func TestSessionGatewayInferencer_ConnectSessionUsesFullPersistentRequest(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}
	req := SessionRequest{
		Config: models.SessionConfig{
			Model:                 "gpt-realtime",
			Modalities:            []models.SessionModality{models.SessionModalityText, models.SessionModalityAudio},
			Voice:                 "alloy",
			Instructions:          "Keep answers concise.",
			InputAudioFormat:      models.AudioFormatPCM16,
			OutputAudioFormat:     models.AudioFormatG711Ulaw,
			InputAudioSampleRate:  models.SampleRate16000,
			OutputAudioSampleRate: models.SampleRate24000,
			Tools: []models.ToolDefinition{{
				Name:        "lookup",
				Description: "Look up a value",
				Parameters: []models.ToolParameter{{
					Name:        "query",
					Type:        "string",
					Description: "query text",
					Required:    true,
				}},
			}},
			TurnDetection: &models.TurnDetectionConfig{
				Type:              "server_vad",
				Threshold:         0.6,
				PrefixPaddingMs:   120,
				SilenceDurationMs: 240,
			},
			Config: []byte(`{"vendor":"specific"}`),
		},
	}
	si := NewSessionGatewayInferencer(gw, WithSessionRequest(req))

	session, err := si.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	got := gw.capturedConfig
	if got.Model != req.Config.Model || got.Voice != req.Config.Voice || got.Instructions != req.Config.Instructions {
		t.Fatalf("basic config = %#v, want %#v", got, req.Config)
	}
	if len(got.Modalities) != 2 || got.Modalities[0] != models.SessionModalityText || got.Modalities[1] != models.SessionModalityAudio {
		t.Fatalf("modalities = %#v", got.Modalities)
	}
	if got.InputAudioFormat != models.AudioFormatPCM16 || got.OutputAudioFormat != models.AudioFormatG711Ulaw {
		t.Fatalf("audio formats = %#v/%#v", got.InputAudioFormat, got.OutputAudioFormat)
	}
	if got.InputAudioSampleRate != models.SampleRate16000 || got.OutputAudioSampleRate != models.SampleRate24000 {
		t.Fatalf("sample rates = %#v/%#v", got.InputAudioSampleRate, got.OutputAudioSampleRate)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "lookup" || len(got.Tools[0].Parameters) != 1 || got.Tools[0].Parameters[0].Name != "query" {
		t.Fatalf("tools = %#v", got.Tools)
	}
	if got.TurnDetection == nil || got.TurnDetection.Type != "server_vad" || got.TurnDetection.SilenceDurationMs != 240 {
		t.Fatalf("turn detection = %#v", got.TurnDetection)
	}
	if string(got.Config) != `{"vendor":"specific"}` {
		t.Fatalf("provider config = %s", string(got.Config))
	}
}

func TestSessionGatewayInferencer_RequestIsDefensivelyCopied(t *testing.T) {
	createResponse := true
	req := SessionRequest{
		Config: models.SessionConfig{
			Model:      "original",
			Modalities: []models.SessionModality{models.SessionModalityText},
			Tools: []models.ToolDefinition{{
				Name:       "original-tool",
				Parameters: []models.ToolParameter{{Name: "original-param"}},
			}},
			TurnDetection: &models.TurnDetectionConfig{
				Type:           "server_vad",
				CreateResponse: &createResponse,
			},
			Config: []byte(`{"mode":"original"}`),
		},
	}
	si := NewSessionGatewayInferencer(&mockSessionGateway{session: newMockSession()}, WithSessionRequest(req))

	req.Config.Model = "mutated"
	req.Config.Modalities[0] = models.SessionModalityAudio
	req.Config.Tools[0].Name = "mutated-tool"
	req.Config.Tools[0].Parameters[0].Name = "mutated-param"
	req.Config.TurnDetection.Type = "mutated"
	*req.Config.TurnDetection.CreateResponse = false
	req.Config.Config[9] = 'X'

	snapshot := si.Request()
	if snapshot.Config.Model != "original" ||
		snapshot.Config.Modalities[0] != models.SessionModalityText ||
		snapshot.Config.Tools[0].Name != "original-tool" ||
		snapshot.Config.Tools[0].Parameters[0].Name != "original-param" ||
		snapshot.Config.TurnDetection.Type != "server_vad" ||
		snapshot.Config.TurnDetection.CreateResponse == nil ||
		!*snapshot.Config.TurnDetection.CreateResponse ||
		string(snapshot.Config.Config) != `{"mode":"original"}` {
		t.Fatalf("request was not defensively copied: %#v", snapshot.Config)
	}

	snapshot.Config.Model = "snapshot-mutated"
	snapshot.Config.Modalities[0] = models.SessionModalityAudio
	snapshot.Config.Tools[0].Parameters[0].Name = "snapshot-mutated"
	snapshot.Config.TurnDetection.Type = "snapshot-mutated"
	*snapshot.Config.TurnDetection.CreateResponse = false
	snapshot.Config.Config[9] = 'Y'

	again := si.Request()
	if again.Config.Model != "original" ||
		again.Config.Modalities[0] != models.SessionModalityText ||
		again.Config.Tools[0].Parameters[0].Name != "original-param" ||
		again.Config.TurnDetection.Type != "server_vad" ||
		again.Config.TurnDetection.CreateResponse == nil ||
		!*again.Config.TurnDetection.CreateResponse ||
		string(again.Config.Config) != `{"mode":"original"}` {
		t.Fatalf("Request returned mutable internal state: %#v", again.Config)
	}
}

func TestSessionGatewayInferencer_ConnectSessionClonesTurnDetectionCreateResponse(t *testing.T) {
	createResponse := true
	sess := newMockSession()
	gateway := &mockSessionGateway{session: sess}
	si := NewSessionGatewayInferencer(gateway, WithSessionRequest(SessionRequest{
		Config: models.SessionConfig{
			Model: "gpt-realtime",
			TurnDetection: &models.TurnDetectionConfig{
				Type:           "server_vad",
				CreateResponse: &createResponse,
			},
		},
	}))

	// The caller's bool is independent from the inferencer's persistent request.
	createResponse = false
	requestSnapshot := si.Request()
	if requestSnapshot.Config.TurnDetection == nil || requestSnapshot.Config.TurnDetection.CreateResponse == nil || !*requestSnapshot.Config.TurnDetection.CreateResponse {
		t.Fatalf("Request lost the configured CreateResponse value: %#v", requestSnapshot.Config.TurnDetection)
	}

	// A public Request snapshot is also independent from future connections.
	*requestSnapshot.Config.TurnDetection.CreateResponse = false
	if sessionRequest := si.Request(); sessionRequest.Config.TurnDetection == nil || sessionRequest.Config.TurnDetection.CreateResponse == nil || !*sessionRequest.Config.TurnDetection.CreateResponse {
		t.Fatalf("Request snapshot mutated persistent CreateResponse state: %#v", sessionRequest.Config.TurnDetection)
	}

	firstSession, err := si.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("first ConnectSession: %v", err)
	}
	defer func() { _ = firstSession.Close() }()

	firstConfig := gateway.capturedConfig
	if firstConfig.TurnDetection == nil || firstConfig.TurnDetection.CreateResponse == nil || !*firstConfig.TurnDetection.CreateResponse {
		t.Fatalf("ConnectSession lost the configured CreateResponse value: %#v", firstConfig.TurnDetection)
	}

	// The gateway's captured connection request must not alias persistent state.
	*firstConfig.TurnDetection.CreateResponse = false
	secondSession, err := si.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("second ConnectSession: %v", err)
	}
	defer func() { _ = secondSession.Close() }()

	secondConfig := gateway.capturedConfig
	if secondConfig.TurnDetection == nil || secondConfig.TurnDetection.CreateResponse == nil || !*secondConfig.TurnDetection.CreateResponse {
		t.Fatalf("connection snapshot mutated persistent CreateResponse state: %#v", secondConfig.TurnDetection)
	}
}

func TestSessionGatewayInferencer_CancelledConnectDoesNotMutatePersistentRequest(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}
	si := NewSessionGatewayInferencer(gw, WithSessionRequest(SessionRequest{
		Config: models.SessionConfig{
			Model:        "gpt-realtime",
			Voice:        "alloy",
			Instructions: "persistent",
			Modalities:   []models.SessionModality{models.SessionModalityText},
			Config:       []byte(`{"vendor":"specific"}`),
		},
	}))

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := si.ConnectSession(cancelledCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ConnectSession error = %v, want context.Canceled", err)
	}

	session, err := si.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("second ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if len(gw.capturedConfigs) != 2 {
		t.Fatalf("captured configs = %d, want 2", len(gw.capturedConfigs))
	}
	first := gw.capturedConfigs[0]
	second := gw.capturedConfigs[1]
	if first.Model != second.Model || first.Voice != second.Voice || first.Instructions != second.Instructions {
		t.Fatalf("persistent request changed after cancellation: first=%#v second=%#v", first, second)
	}
	if string(first.Config) != string(second.Config) || first.Config == nil || second.Config == nil {
		t.Fatalf("provider config changed after cancellation: first=%s second=%s", first.Config, second.Config)
	}
}

func TestSessionGatewayInferencer_ConnectSessionError(t *testing.T) {
	gw := &mockSessionGateway{err: errors.New("connection refused")}
	si := NewSessionGatewayInferencer(gw, WithSessionModel("grok-3-mini"))

	_, err := si.ConnectSession(context.Background())
	if err == nil {
		t.Fatal("expected error from gateway")
	}
}

func TestSessionGatewayInferencer_ConnectSessionErrorClassification(t *testing.T) {
	gw := &mockSessionGateway{
		err: providers.NewUnsupportedRequestError("fake-session", "session", "audio", []string{"text"}, "fake-session: audio sessions are not supported"),
	}
	si := NewSessionGatewayInferencer(gw, WithSessionModel("fake-session-model"))

	_, err := si.ConnectSession(context.Background())
	if err == nil {
		t.Fatal("expected error from gateway")
	}
	if !errors.Is(err, providers.ErrUnsupportedRequest) {
		t.Fatalf("ConnectSession error classification did not preserve ErrUnsupportedRequest: %v", err)
	}

	var validationErr *providers.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("ConnectSession error did not preserve validation details: %v", err)
	}
	if validationErr.Provider != "fake-session" || validationErr.Feature != "session" || validationErr.Requested != "audio" {
		t.Fatalf("validation details = %#v", validationErr)
	}
}

func TestSessionGatewayInferencer_ReturnedSessionIsUsable(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}
	si := NewSessionGatewayInferencer(gw, WithSessionModel("grok-3-mini"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := si.ConnectSession(ctx)
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Inject an inbound StreamMessage via the mock's recvBuf.
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{0x01, 0x02}),
	}
	sess.recvBuf.Write(ctx, msg)

	// Read it from the returned session's Receive buffer.
	received, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("Read from session Receive buffer returned false")
	}
	if received.Type != messages.StreamTypeAudioDelta {
		t.Errorf("type: got %q, want %q", received.Type, messages.StreamTypeAudioDelta)
	}
}

func TestSessionGatewayInferencer_SendRouted(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}
	si := NewSessionGatewayInferencer(gw, WithSessionModel("grok-3-mini"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := si.ConnectSession(ctx)
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Send a StreamMessage via the session.
	sent := messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{0xAB, 0xCD}),
	}
	if !session.Send(ctx, sent) {
		t.Fatal("Send returned false")
	}

	// The mock session's sendBuf should have the message.
	received, ok := sess.sendBuf.ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("sendBuf read returned false")
	}
	if received.Type != messages.StreamTypeAudioDelta {
		t.Errorf("type: got %q, want %q", received.Type, messages.StreamTypeAudioDelta)
	}
}

func TestSessionGatewayInferencer_ImplementsLoopOwnedContractAtRuntime(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}
	var inferencer messages.SessionInferencer = NewSessionGatewayInferencer(gw, WithSessionModel("grok-3-mini"))

	session, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession via messages.SessionInferencer: %v", err)
	}
	defer func() { _ = session.Close() }()

	if session != sess {
		t.Fatal("ConnectSession should expose the loop-owned session contract without wrapping it in a second shared session surface")
	}
}
