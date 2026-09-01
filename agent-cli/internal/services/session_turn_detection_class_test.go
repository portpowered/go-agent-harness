package services

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// turnDetectionWireCaptureConn is a minimal transport.Conn that records every
// outbound frame verbatim and never produces an inbound message on its own,
// so ConnectSession's real provider construction can be exercised without a
// scripted server. It is intentionally simpler than the scripted fixtures
// used elsewhere in this file: this table only needs the first outbound
// frame, the actual session.update a live provider sends on the wire.
type turnDetectionWireCaptureConn struct {
	mu     sync.Mutex
	writes [][]byte
	closed chan struct{}
	once   sync.Once
}

func newTurnDetectionWireCaptureConn() *turnDetectionWireCaptureConn {
	return &turnDetectionWireCaptureConn{closed: make(chan struct{})}
}

func (c *turnDetectionWireCaptureConn) ReadMessage() (int, []byte, error) {
	<-c.closed
	return 0, nil, errors.New("turn detection capture connection closed")
}

func (c *turnDetectionWireCaptureConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (c *turnDetectionWireCaptureConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *turnDetectionWireCaptureConn) firstWrite() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.writes) == 0 {
		return nil
	}
	return c.writes[0]
}

type turnDetectionWireCaptureDialer struct {
	conn *turnDetectionWireCaptureConn
}

func newTurnDetectionWireCaptureDialer() *turnDetectionWireCaptureDialer {
	return &turnDetectionWireCaptureDialer{conn: newTurnDetectionWireCaptureConn()}
}

func (d *turnDetectionWireCaptureDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

var _ transport.Dialer = (*turnDetectionWireCaptureDialer)(nil)

// firstWriteTurnDetection extracts the turn_detection field from a captured
// initial session.update, whichever shape the provider used (GA nested
// audio.input.turn_detection, or the legacy/Grok flat session.turn_detection).
// present is false only when the key is entirely absent from the payload;
// isNull distinguishes an explicit JSON null from a real policy object, since
// this lane's whole point is that the two are not interchangeable.
func firstWriteTurnDetection(t *testing.T, raw []byte) (present, isNull bool) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("no outbound frame was captured")
	}
	var envelope struct {
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode session.update envelope: %v raw=%s", err, raw)
	}
	var session map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Session, &session); err != nil {
		t.Fatalf("decode session config: %v raw=%s", err, envelope.Session)
	}
	if raw, ok := session["turn_detection"]; ok {
		return true, string(raw) == "null"
	}
	rawAudio, ok := session["audio"]
	if !ok {
		return false, false
	}
	var audio map[string]json.RawMessage
	if err := json.Unmarshal(rawAudio, &audio); err != nil {
		t.Fatalf("decode audio config: %v raw=%s", err, rawAudio)
	}
	rawInput, ok := audio["input"]
	if !ok {
		return false, false
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(rawInput, &input); err != nil {
		t.Fatalf("decode audio.input config: %v raw=%s", err, rawInput)
	}
	raw, ok = input["turn_detection"]
	if !ok {
		return false, false
	}
	return true, string(raw) == "null"
}

func turnDetectionClassLoadedConfig(configPath string) *config.Config {
	return &config.Config{
		ConfigPath: configPath,
		Model: config.ModelConfig{
			Provider: config.ProviderOpenAI,
			OpenAI:   &config.OpenAIConfig{Model: openAIRealtimeDefaultModel, APIKey: "test-key"},
		},
	}
}

// writeMinimalOpenAIReplayCapture builds the smallest capture on disk that
// loadReplaySessionConfiguration and gwtesting.NewReplayWebSocketDialer will
// both accept: one client-to-server session.update record. session.update is
// the only frame the resulting plan's ConnectSession sends before this test
// closes the session, so nothing else needs to be scripted.
func writeMinimalOpenAIReplayCapture(t *testing.T, path string, sessionUpdatePayload []byte) {
	t.Helper()
	writeSessionCapture(t, path, gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: sessionProviderOpenAI, Model: openAIRealtimeDefaultModel},
		Session:  gwtesting.SessionMetadata{FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic},
		Records: []gwtesting.CapturedSessionEvent{
			{
				Sequence:    1,
				Direction:   gwtesting.DirectionClientToServer,
				Type:        "session.update",
				PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
				Payload:     sessionUpdatePayload,
			},
		},
	})
}

// TestSessionInvocationShapesResolveTurnDetectionNotOnlyOnBarePath is the
// mandatory class-level regression guard: ResolveBareSessionOptions has
// always resolved a real server_vad policy, but every other invocation shape
// reached provider construction with a nil TurnDetection because nothing
// else ever resolved one -- the third confirmed instance of "a capability
// wired only on the bare path" (see #356 for RTCDeviceBinding and the
// playback-diagnostics lane for SessionDiagnosticSink). This table drives the
// real provider construction for every shape that owns its own
// SessionRunOptions and inspects the actual bytes ConnectSession sends, not
// an intermediate SessionRunOptions/SessionConfig field.
//
// --audio-in, --audio-in-turn, and --audio-in-turn-barge are deliberately
// asserted as explicit-null, not non-nil: those three set
// ClientOwnsAudioTurnBoundaries, and OpenAI's server_vad unconditionally
// auto-commits the input buffer at speech-stop regardless of
// create_response, colliding with that contract's own explicit commit. The
// CLI integration test
// TestSessionCommand_LiveScheduledAudioServerVADCreateResponseFalseNegativeControl
// proves this collision end to end (input_audio_buffer_commit_empty). Sending
// a real policy for those three shapes would regress that proven-correct
// behavior, so they keep the deliberate suppression; this test still proves
// it is a resolved, explicit decision (isNull with present=true), not a
// forgotten/never-reached option.
func TestSessionInvocationShapesResolveTurnDetectionNotOnlyOnBarePath(t *testing.T) {
	type shapeResult struct {
		present bool
		isNull  bool
	}

	run := func(t *testing.T, build func(t *testing.T, dialer transport.Dialer) messages.SessionInferencer) shapeResult {
		t.Helper()
		dialer := newTurnDetectionWireCaptureDialer()
		inferencer := build(t, dialer)
		session, err := inferencer.ConnectSession(context.Background())
		if err != nil {
			t.Fatalf("ConnectSession: %v", err)
		}
		defer func() { _ = session.Close() }()
		present, isNull := firstWriteTurnDetection(t, dialer.conn.firstWrite())
		return shapeResult{present: present, isNull: isNull}
	}

	t.Run("bare", func(t *testing.T) {
		got := run(t, func(t *testing.T, dialer transport.Dialer) messages.SessionInferencer {
			configPath := filepath.Join(t.TempDir(), config.ConfigFileName)
			resolved, err := ResolveBareSessionOptions(SessionRunOptions{
				LoadedConfig:    turnDetectionClassLoadedConfig(configPath),
				WebSocketDialer: dialer,
			})
			if err != nil {
				t.Fatalf("ResolveBareSessionOptions: %v", err)
			}
			plan, err := planSessionRuntimeWithFactory(resolved, defaultSessionRuntimeFactory)
			if err != nil {
				t.Fatalf("planSessionRuntimeWithFactory: %v", err)
			}
			return plan.inferencer
		})
		if !got.present || got.isNull {
			t.Fatalf("bare turn_detection = present:%t isNull:%t, want present and non-null", got.present, got.isNull)
		}
	})

	t.Run("record only", func(t *testing.T) {
		got := run(t, func(t *testing.T, dialer transport.Dialer) messages.SessionInferencer {
			configPath := filepath.Join(t.TempDir(), config.ConfigFileName)
			plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
				LoadedConfig:    turnDetectionClassLoadedConfig(configPath),
				RecordPath:      filepath.Join(t.TempDir(), "capture.json"),
				WebSocketDialer: dialer,
			}, defaultSessionRuntimeFactory)
			if err != nil {
				t.Fatalf("planSessionRuntimeWithFactory: %v", err)
			}
			return plan.inferencer
		})
		if !got.present || got.isNull {
			t.Fatalf("record-only turn_detection = present:%t isNull:%t, want present and non-null", got.present, got.isNull)
		}
	})

	t.Run("replay", func(t *testing.T) {
		// Replay's outbound wire is not opts.WebSocketDialer: the initial
		// handshake is the capture's own bytes, substituted verbatim for
		// whatever the local construction would have sent (see
		// newReplayInitialSessionUpdateDialer), over the capture-backed
		// gwtesting.ReplayWebSocketDialer this plan owns internally. That
		// substitution conn strictly validates the substituted payload
		// against this exact same captured record, so a mismatch here would
		// surface as a ConnectSession error rather than a silent drop -- this
		// row proves a real, resolved, non-null captured policy survives the
		// replay path end to end instead of being silently discarded.
		replayPayload := []byte(`{"type":"session.update","session":{"type":"realtime","model":"` + openAIRealtimeDefaultModel + `","audio":{"input":{"turn_detection":{"type":"server_vad"}}}}}`)
		present, isNull := firstWriteTurnDetection(t, replayPayload)
		if !present || isNull {
			t.Fatalf("replay fixture turn_detection = present:%t isNull:%t, want present and non-null", present, isNull)
		}
		capturePath := filepath.Join(t.TempDir(), "replay.session.json")
		writeMinimalOpenAIReplayCapture(t, capturePath, replayPayload)
		plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
			ReplayPath: capturePath,
		}, defaultSessionRuntimeFactory)
		if err != nil {
			t.Fatalf("planSessionRuntimeWithFactory: %v", err)
		}
		session, err := plan.inferencer.ConnectSession(context.Background())
		if err != nil {
			t.Fatalf("replay ConnectSession rejected the non-null captured turn_detection payload: %v", err)
		}
		defer func() { _ = session.Close() }()
	})

	t.Run("audio-in", func(t *testing.T) {
		got := run(t, func(t *testing.T, dialer transport.Dialer) messages.SessionInferencer {
			configPath := filepath.Join(t.TempDir(), config.ConfigFileName)
			plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
				LoadedConfig:                  turnDetectionClassLoadedConfig(configPath),
				WebSocketDialer:               dialer,
				ClientOwnsAudioTurnBoundaries: true,
			}, defaultSessionRuntimeFactory)
			if err != nil {
				t.Fatalf("planSessionRuntimeWithFactory: %v", err)
			}
			return plan.inferencer
		})
		if !got.present || !got.isNull {
			t.Fatalf("audio-in turn_detection = present:%t isNull:%t, want present and explicitly null (see test doc comment)", got.present, got.isNull)
		}
	})

	t.Run("audio-in-turn", func(t *testing.T) {
		got := run(t, func(t *testing.T, dialer transport.Dialer) messages.SessionInferencer {
			configPath := filepath.Join(t.TempDir(), config.ConfigFileName)
			plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
				LoadedConfig:                  turnDetectionClassLoadedConfig(configPath),
				RecordPath:                    filepath.Join(t.TempDir(), "capture.json"),
				WebSocketDialer:               dialer,
				ClientOwnsAudioTurnBoundaries: true,
				AudioInputs:                   []ScheduledAudioInput{{PCM: []byte{1, 2, 3, 4}, EndOfTurn: true}},
			}, defaultSessionRuntimeFactory)
			if err != nil {
				t.Fatalf("planSessionRuntimeWithFactory: %v", err)
			}
			return plan.inferencer
		})
		if !got.present || !got.isNull {
			t.Fatalf("audio-in-turn turn_detection = present:%t isNull:%t, want present and explicitly null (see test doc comment)", got.present, got.isNull)
		}
	})

	t.Run("audio-in-turn-barge", func(t *testing.T) {
		got := run(t, func(t *testing.T, dialer transport.Dialer) messages.SessionInferencer {
			configPath := filepath.Join(t.TempDir(), config.ConfigFileName)
			plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
				LoadedConfig:                  turnDetectionClassLoadedConfig(configPath),
				RecordPath:                    filepath.Join(t.TempDir(), "capture.json"),
				WebSocketDialer:               dialer,
				ClientOwnsAudioTurnBoundaries: true,
				AudioInTurnBarge:              true,
				AudioInputs: []ScheduledAudioInput{
					{PCM: []byte{1, 2, 3, 4}, EndOfTurn: true},
					{PCM: []byte{5, 6, 7, 8}, EndOfTurn: true, AfterCompletedTurns: 1},
				},
			}, defaultSessionRuntimeFactory)
			if err != nil {
				t.Fatalf("planSessionRuntimeWithFactory: %v", err)
			}
			return plan.inferencer
		})
		if !got.present || !got.isNull {
			t.Fatalf("audio-in-turn-barge turn_detection = present:%t isNull:%t, want present and explicitly null (see test doc comment)", got.present, got.isNull)
		}
	})

	t.Run("room participant", func(t *testing.T) {
		got := run(t, func(t *testing.T, dialer transport.Dialer) messages.SessionInferencer {
			inferencer, err := defaultRoomSessionFactory(
				room.Participant{ID: "speaker", SystemPrompt: "room participant system prompt"},
				SessionRunOptions{
					Provider:        sessionProviderOpenAI,
					Model:           openAIRealtimeDefaultModel,
					APIKey:          "test-key",
					WebSocketDialer: dialer,
				},
			)
			if err != nil {
				t.Fatalf("defaultRoomSessionFactory: %v", err)
			}
			return inferencer
		})
		if !got.present || got.isNull {
			t.Fatalf("room participant turn_detection = present:%t isNull:%t, want present and non-null", got.present, got.isNull)
		}
	})

	t.Run("self-play", func(t *testing.T) {
		got := run(t, func(t *testing.T, dialer transport.Dialer) messages.SessionInferencer {
			inferencer, err := defaultSelfPlaySessionFactory(SessionRunOptions{
				Provider:        sessionProviderOpenAI,
				Model:           openAIRealtimeDefaultModel,
				ModelProvided:   true,
				APIKey:          "test-key",
				WebSocketDialer: dialer,
			}, "self-play instructions")
			if err != nil {
				t.Fatalf("defaultSelfPlaySessionFactory: %v", err)
			}
			return inferencer
		})
		if !got.present || got.isNull {
			t.Fatalf("self-play turn_detection = present:%t isNull:%t, want present and non-null", got.present, got.isNull)
		}
	})
}
