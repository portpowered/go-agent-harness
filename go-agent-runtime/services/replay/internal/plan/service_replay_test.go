package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestLoadSessionConfigurationPreservesAdmittedHandshakeAndCopiesMetadata(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord(replaySessionUpdate, `{"type":"session.update","session":{"model":"  captured-model  ","tools":[{"name":" exec "},{"name":""}],"audio":{"input":{"format":{"rate":24000}},"output":{"format":{"rate":24000}}}}}`),
	)
	service := New()
	configuration, err := service.LoadSessionConfiguration(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Model() != "captured-model" || configuration.InputAudioSampleRate() != 24000 || configuration.OutputAudioSampleRate() != 24000 {
		t.Fatalf("configuration metadata = model %q rates %d/%d", configuration.Model(), configuration.InputAudioSampleRate(), configuration.OutputAudioSampleRate())
	}
	if !configuration.InitialToolsKnown() || !slices.Equal(configuration.InitialToolNames(), []string{"exec"}) {
		t.Fatalf("configuration tools = %v known=%t", configuration.InitialToolNames(), configuration.InitialToolsKnown())
	}
	originalPayload := configuration.Payload()
	mutatedPayload := configuration.Payload()
	mutatedPayload[0] = 'x'
	mutatedNames := configuration.InitialToolNames()
	mutatedNames[0] = "mutated"
	if !bytes.Equal(configuration.Payload(), originalPayload) || configuration.InitialToolNames()[0] != "exec" {
		t.Fatal("configuration accessors exposed mutable admission state")
	}
	if len(originalPayload) == 0 {
		t.Fatal("configuration omitted the captured handshake payload")
	}
}

func TestLoadSessionConfigurationRejectsConflictingAndMalformedRates(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    error
		phrase  string
	}{
		{
			name:    "conflicting duplex rates",
			payload: `{"type":"session.update","session":{"audio":{"input":{"format":{"rate":16000}},"output":{"format":{"rate":24000}}}}}`,
			want:    replay.ErrSessionAudioSampleRateConflict,
		},
		{
			name:    "malformed GA rate",
			payload: `{"type":"session.update","session":{"audio":{"input":{"format":{"rate":"bad"}}}}}`,
			phrase:  "rate must be an integer",
		},
		{
			name:    "malformed legacy rate",
			payload: `{"type":"session.update","session":{"input_audio_format":[]}}`,
			phrase:  "decode input audio format",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writePlanCapture(t, clientRecord(replaySessionUpdate, test.payload))
			_, err := New().LoadSessionConfiguration(t.Context(), path)
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("configuration error = %v, want capture path", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("configuration error = %v, want %v", err, test.want)
			}
			if test.phrase != "" && !strings.Contains(err.Error(), test.phrase) {
				t.Fatalf("configuration error = %v, want %q", err, test.phrase)
			}
		})
	}
}

func TestInspectCaptureRequiresRealtimeHandshake(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord(replayCreateItem, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
	)
	if _, err := New().InspectCapture(t.Context(), path); err == nil || !strings.Contains(err.Error(), "missing initial outbound session.update configuration") {
		t.Fatalf("InspectCapture error = %v, want missing handshake admission", err)
	}
}

func TestLoadCapturedActionsPreservesTextAndRawAudioTurnBoundaries(t *testing.T) {
	textPath := writePlanCapture(t,
		clientRecord(replaySessionUpdate, `{"type":"session.update","session":{"model":"fixture"}}`),
		clientRecord(replayCreateItem, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"captured prompt"}]}}`),
		clientRecord(replayResponseCreate, `{"type":"response.create"}`),
	)
	prompt, err := New().LoadCapturedTextPrompt(t.Context(), textPath)
	if err != nil || prompt == nil || prompt.Text != "captured prompt" {
		t.Fatalf("captured prompt = %#v, error=%v", prompt, err)
	}

	audioPath := writePlanCapture(t,
		clientRecord(replaySessionUpdate, `{"type":"session.update","session":{"model":"fixture"}}`),
		clientRecord(replayAppend, `{"type":"input_audio_buffer.append","audio":"AQACAA=="}`),
		clientRecord(replayCommit, `{"type":"input_audio_buffer.commit"}`),
		clientRecord(replayResponseCreate, `{"type":"response.create"}`),
		clientRecord(replayAppend, `{"type":"input_audio_buffer.append","audio":"//79AA=="}`),
		clientRecord(replayCommit, `{"type":"input_audio_buffer.commit"}`),
		clientRecord(replayResponseCreate, `{"type":"response.create"}`),
	)
	turns, err := New().LoadCapturedAudioTurns(t.Context(), audioPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || !bytes.Equal(turns[0].PCM, []byte{1, 0, 2, 0}) || !bytes.Equal(turns[1].PCM, []byte{255, 254, 253, 0}) {
		t.Fatalf("captured audio turns = %#v", turns)
	}
	if turns[0].AfterCompletedTurns != 0 || turns[1].AfterCompletedTurns != 1 || !turns[0].EndOfTurn || !turns[1].EndOfTurn {
		t.Fatalf("captured audio scheduling = %#v", turns)
	}
}

func TestLoadLivePlanRejectsOddPCMWhileRawAdapterPreservesBytes(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord(replaySessionUpdate, `{"type":"session.update","session":{"model":"fixture"}}`),
		clientRecord(replayAppend, `{"type":"input_audio_buffer.append","audio":"AQ=="}`),
		clientRecord(replayCommit, `{"type":"input_audio_buffer.commit"}`),
		clientRecord(replayResponseCreate, `{"type":"response.create"}`),
	)
	if _, err := New().LoadLivePlan(t.Context(), path); err == nil || !strings.Contains(err.Error(), "odd byte length") {
		t.Fatalf("LoadLivePlan error = %v, want PCM alignment failure", err)
	}
	if _, err := New().LoadCapturedAudioTurns(t.Context(), path); err == nil || !strings.Contains(err.Error(), "odd byte length") {
		t.Fatalf("LoadCapturedAudioTurns error = %v, want PCM alignment failure", err)
	}
	turns, err := New().LoadCapturedAudioTurnsRaw(t.Context(), path)
	if err != nil || len(turns) != 1 || !bytes.Equal(turns[0].PCM, []byte{1}) {
		t.Fatalf("LoadCapturedAudioTurnsRaw = %#v, error = %v", turns, err)
	}
}

func TestWrapInitialSessionUpdateDialerReplacesOnlyFirstPayload(t *testing.T) {
	path := writePlanCapture(t, clientRecord(replaySessionUpdate, `{"type":"session.update","session":{"model":"captured"}}`))
	configuration, err := New().LoadSessionConfiguration(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	innerConn := &planTestConn{}
	wrapped := New().WrapInitialSessionUpdateDialer(&planTestDialer{conn: innerConn}, configuration, false)
	conn, err := wrapped.Dial("wss://fixture.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update","session":{"model":"generated"}}`)); err != nil {
		t.Fatal(err)
	}
	later := []byte(`{"type":"response.create"}`)
	if err := conn.WriteMessage(1, later); err != nil {
		t.Fatal(err)
	}
	if len(innerConn.writes) != 2 || !bytes.Equal(innerConn.writes[0], configuration.Payload()) || !bytes.Equal(innerConn.writes[1], later) {
		t.Fatalf("wrapped writes = %q", innerConn.writes)
	}
}

func TestReplayCapturePropagatesSinkAndContextOutcomes(t *testing.T) {
	path := writeStreamPlanCapture(t, streamPlanRecord(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")}))
	sinkErr := errors.New("renderer stopped")
	if err := New().ReplayCapture(t.Context(), path, func(messages.StreamMessage) error { return sinkErr }); !errors.Is(err, sinkErr) {
		t.Fatalf("ReplayCapture sink error = %v, want %v", err, sinkErr)
	}
	cause := errors.New("host stopped")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if err := New().ReplayCapture(ctx, path, func(messages.StreamMessage) error { return nil }); !errors.Is(err, cause) {
		t.Fatalf("ReplayCapture context error = %v, want %v", err, cause)
	}
	if err := New().ReplayCapture(t.Context(), path, nil); !errors.Is(err, replay.ErrReplaySinkRequired) {
		t.Fatalf("ReplayCapture nil sink error = %v", err)
	}
}

func TestReplayLifecycleQueriesUseAdmittedCapture(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord(replaySessionUpdate, `{"type":"session.update","session":{"model":"fixture"}}`),
		serverRecord("session.closed", `{"type":"session.closed"}`),
		serverRecord("response.done", `{"type":"response.done"}`),
	)
	duration, err := New().ReplayDuration(t.Context(), path, "RECORDED")
	if err != nil || duration != 3002*time.Millisecond {
		t.Fatalf("ReplayDuration = %v, error=%v, want 3.002s", duration, err)
	}
	hasEvent, err := New().HasEvent(t.Context(), path, "session.closed")
	if err != nil || !hasEvent {
		t.Fatalf("HasEvent = %t, error=%v", hasEvent, err)
	}
	if hasEvent, err = New().HasEvent(t.Context(), path, "missing"); err != nil || hasEvent {
		t.Fatalf("missing HasEvent = %t, error=%v", hasEvent, err)
	}
}

func writeStreamPlanCapture(t *testing.T, records ...gatewaytesting.CapturedSessionEvent) string {
	t.Helper()
	for index := range records {
		records[index].Sequence = index + 1
		records[index].TimestampMs = int64(index)
	}
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "fixture", Model: "fixture"},
		Records:  records,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "stream-capture.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func streamPlanRecord(message messages.StreamMessage) gatewaytesting.CapturedSessionEvent {
	payload, err := gatewaytesting.MarshalStreamMessage(message)
	if err != nil {
		panic(err)
	}
	return gatewaytesting.CapturedSessionEvent{
		Direction:   gatewaytesting.DirectionServerToClient,
		Type:        string(message.Type),
		PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage,
		Payload:     payload,
	}
}

type planTestDialer struct {
	conn *planTestConn
}

func (d *planTestDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

type planTestConn struct {
	writes [][]byte
}

func (c *planTestConn) ReadMessage() (int, []byte, error) { return 0, nil, io.EOF }

func (c *planTestConn) WriteMessage(_ int, payload []byte) error {
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (*planTestConn) Close() error { return nil }
