package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	providersessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const testProviderVoice = "marin"

func TestProviderSessionRuntimeService_RecordPolicyAndBuild(t *testing.T) {
	live := &stubRuntimeDialer{id: "live"}
	recorder := &stubRecordingDialer{stubRuntimeDialer: stubRuntimeDialer{id: "record"}}
	service := providersessionwire.NewService(providersession.Dependencies{
		NewDefaultDialer: func(string) transport.Dialer { return live },
		NewRecordingDialer: func(transport.Dialer, string, string) providersession.RecordingDialer {
			return recorder
		},
	})
	tool := messages.ToolDefinition{Name: "lookup"}
	plan, err := service.PlanRecord(context.Background(), providersession.RecordRequest{
		Provider: "openai", Model: "gpt-realtime", APIKey: "redacted-test-key", RecordPath: "capture.json",
		Voice: testProviderVoice, AudioInputAvailable: true, ToolDefinitions: []messages.ToolDefinition{tool},
		ObserveDialer: func(d transport.Dialer) transport.Dialer { return d },
	})
	if err != nil {
		t.Fatalf("PlanRecord: %v", err)
	}
	if plan.Dialer != recorder || plan.Build.Dialer != recorder {
		t.Fatal("record plan did not preserve the owned recorder")
	}
	if !plan.CloseAfterOpen || plan.WaitForClose || plan.RequireSessionUpdated {
		t.Fatalf("record lifecycle = close=%t wait=%t ack=%t", plan.CloseAfterOpen, plan.WaitForClose, plan.RequireSessionUpdated)
	}
	if plan.TurnDetection == nil || plan.TurnDetection.Type != "semantic_vad" {
		t.Fatalf("turn detection = %#v, want semantic_vad", plan.TurnDetection)
	}
	if !plan.Build.InputAudioTranscription.Enabled || plan.Build.InputAudioTranscription.Model != models.DefaultInputAudioTranscriptionModel {
		t.Fatalf("transcription = %#v, want the OpenAI live default", plan.Build.InputAudioTranscription)
	}
	inferencer, err := service.BuildOpenAI(context.Background(), plan.Build)
	if err != nil {
		t.Fatalf("BuildOpenAI: %v", err)
	}
	request, ok := inferencer.(*inference.SessionGatewayInferencer)
	if !ok {
		t.Fatalf("inferencer type = %T, want *inference.SessionGatewayInferencer", inferencer)
	}
	if got := request.Request().Config; got.Model != "gpt-realtime" || got.Voice != testProviderVoice || len(got.Tools) != 1 || got.InputAudioTranscription == nil {
		t.Fatalf("built session config = %#v", got)
	}
}

func TestProviderSessionRuntimeService_ReplayUsesCapturedHandshakeAndBarePrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openai.session.json")
	initial := `{"type":"session.update","session":{"model":"captured-model","tools":[]}}`
	writeSessionCapture(t, path, gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "metadata-model"},
		Records: []gwtesting.CapturedSessionEvent{
			{Sequence: 1, Direction: gwtesting.DirectionClientToServer, Type: "session.update", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(initial)},
			{Sequence: 2, Direction: gwtesting.DirectionClientToServer, Type: "conversation.item.create", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"captured prompt"}]}}`)},
			{Sequence: 3, Direction: gwtesting.DirectionClientToServer, Type: "response.create", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"response.create"}`)},
			{Sequence: 4, Direction: gwtesting.DirectionServerToClient, Type: "session.closed", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"session.closed"}`)},
		},
	})
	conn := &replayHandshakeRecordingConn{}
	raw := &providerSessionRuntimeReplayDialer{replayHandshakeRecordingDialer: replayHandshakeRecordingDialer{conn: conn}, model: "dialer-model", done: make(chan struct{})}
	service := providersessionwire.NewService(providersession.Dependencies{NewReplayDialer: func(string, string) (providersession.ReplayDialer, error) { return raw, nil }})
	plan, err := service.PlanReplay(context.Background(), providersession.ReplayRequest{Provider: "openai", ReplayPath: path, ToolDefinitions: []messages.ToolDefinition{{Name: "lookup"}}})
	if err != nil {
		t.Fatalf("PlanReplay: %v", err)
	}
	if plan.Prompt != "captured prompt" || !plan.PromptProvided || !plan.ReplayComplete || !plan.WaitForClose {
		t.Fatalf("replay plan prompt/lifecycle = (%q, %t, %t, %t)", plan.Prompt, plan.PromptProvided, plan.ReplayComplete, plan.WaitForClose)
	}
	if plan.Model != "captured-model" || !json.Valid(plan.InitialSessionUpdate) {
		t.Fatalf("replay identity = model %q payload %s", plan.Model, plan.InitialSessionUpdate)
	}
	capturedPayload := append([]byte(nil), plan.InitialSessionUpdate...)
	connection, err := plan.Dialer.Dial("ignored", nil)
	if err != nil {
		t.Fatalf("replay dial: %v", err)
	}
	if err := connection.WriteMessage(1, []byte(`{"type":"session.update","session":{"model":"caller-mutation"}}`)); err != nil {
		t.Fatalf("replay handshake: %v", err)
	}
	if len(conn.writes) != 1 || string(conn.writes[0]) != string(capturedPayload) {
		t.Fatalf("replay handshake writes = %q, want captured payload", conn.writes)
	}
}

func TestProviderSessionRuntimeService_ReplayPreservesCausalCredentialHandshakeAndBareAudio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openai-audio.session.json")
	initial := `{"type":"session.update","session":{"model":"captured-audio-model"}}`
	pcm := []byte{1, 2, 3, 4}
	writeSessionCapture(t, path, gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "metadata-model"},
		Records: []gwtesting.CapturedSessionEvent{
			{Sequence: 1, Direction: gwtesting.DirectionClientToServer, Type: "session.update", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(initial)},
			{Sequence: 2, Direction: gwtesting.DirectionClientToServer, Type: "input_audio_buffer.append", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"input_audio_buffer.append","audio":"` + codec.EncodeBase64(pcm) + `"}`)},
			{Sequence: 3, Direction: gwtesting.DirectionClientToServer, Type: "input_audio_buffer.commit", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"input_audio_buffer.commit"}`)},
			{Sequence: 4, Direction: gwtesting.DirectionClientToServer, Type: "response.create", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"response.create"}`)},
		},
	})
	raw := &providerSessionRuntimeReplayDialer{
		replayHandshakeRecordingDialer: replayHandshakeRecordingDialer{conn: &replayHandshakeRecordingConn{}},
		model:                          "dialer-model", done: make(chan struct{}),
	}
	var capturedBuild providersession.BuildRequest
	service := providersessionwire.NewService(providersession.Dependencies{
		NewReplayDialer: func(string, string) (providersession.ReplayDialer, error) { return raw, nil },
		NewInferencer: func(build providersession.BuildRequest) (messages.SessionInferencer, error) {
			capturedBuild = build
			return nil, nil
		},
	})

	plan, err := service.PlanReplay(context.Background(), providersession.ReplayRequest{Provider: "openai", ReplayPath: path})
	if err != nil {
		t.Fatalf("PlanReplay: %v", err)
	}
	if capturedBuild.APIKey != "replay" {
		t.Fatalf("replay build API key = %q, want synthetic replay credential", capturedBuild.APIKey)
	}
	var gotInitial, wantInitial bytes.Buffer
	if err := json.Compact(&gotInitial, capturedBuild.InitialSessionUpdate); err != nil {
		t.Fatalf("compact replay build initial session.update: %v", err)
	}
	if err := json.Compact(&wantInitial, []byte(initial)); err != nil {
		t.Fatalf("compact captured initial session.update: %v", err)
	}
	if gotInitial.String() != wantInitial.String() {
		t.Fatalf("replay build initial session.update = %s, want captured payload %s", capturedBuild.InitialSessionUpdate, initial)
	}
	if !capturedBuild.ClientOwnsAudioTurnBoundaries {
		t.Fatal("bare audio replay did not transfer turn-boundary ownership to the scheduled plan")
	}
	if len(plan.AudioInputs) != 1 || string(plan.AudioInputs[0].PCM) != string(pcm) || !plan.AudioInputs[0].EndOfTurn {
		t.Fatalf("bare audio replay inputs = %#v, want one complete captured PCM turn", plan.AudioInputs)
	}
}

func TestProviderSessionRuntimeService_ReplayRejectsAsymmetricRatesBeforeDialer(t *testing.T) {
	path := writeReplayAudioRateCapture(t, "openai", 16000, 24000)
	called := false
	service := providersessionwire.NewService(providersession.Dependencies{NewReplayDialer: func(string, string) (providersession.ReplayDialer, error) {
		called = true
		return nil, errors.New("unexpected replay construction")
	}})
	_, err := service.PlanReplay(context.Background(), providersession.ReplayRequest{Provider: "openai", ReplayPath: path})
	if !errors.Is(err, providersession.ErrAudioSampleRateConflict) {
		t.Fatalf("PlanReplay error = %v, want ErrAudioSampleRateConflict", err)
	}
	if called {
		t.Fatal("replay dialer was constructed before rate validation")
	}
}

type providerSessionRuntimeReplayDialer struct {
	replayHandshakeRecordingDialer
	model string
	done  chan struct{}
}

func (d *providerSessionRuntimeReplayDialer) Done() <-chan struct{} { return d.done }
func (d *providerSessionRuntimeReplayDialer) Err() error            { return nil }
func (d *providerSessionRuntimeReplayDialer) Model() string         { return d.model }

var _ providersession.ReplayDialer = (*providerSessionRuntimeReplayDialer)(nil)
