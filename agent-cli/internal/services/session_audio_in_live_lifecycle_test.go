package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	functional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// audioInLifecycleServer is an in-process OpenAI Realtime websocket server
// double used to exercise the live record runtime lifecycle hermetically.
type audioInLifecycleServer struct {
	mu     sync.Mutex
	writes []string

	responseRequested chan struct{}
	silentAfterCommit bool
	closeStarted      chan struct{}
	once              sync.Once
	events            chan string
	closed            chan struct{}
}

func newScriptedRealtimeServer(silentAfterCommit bool) *audioInLifecycleServer {
	return &audioInLifecycleServer{
		responseRequested: make(chan struct{}),
		closeStarted:      make(chan struct{}),
		events:            make(chan string, 32),
		closed:            make(chan struct{}),
		silentAfterCommit: silentAfterCommit,
	}
}

func (s *audioInLifecycleServer) writesSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *audioInLifecycleServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.once.Do(func() {
		go func() {
			s.events <- `{"type":"session.created","session":{"id":"sess_scripted","model":"gpt-realtime-2.1-mini"}}`
			for {
				select {
				case <-s.responseRequested:
					if s.silentAfterCommit {
						<-s.closed
						return
					}
					s.events <- `{"type":"response.created","response":{"id":"resp_1"}}`
					s.events <- `{"type":"response.output_audio_transcript.done","transcript":"Hi there."}`
					s.events <- `{"type":"response.output_audio.delta","delta":"c3Bva2VuIHJlc3BvbnNlcw=="}`
					s.events <- `{"type":"response.output_audio.done"}`
					s.events <- `{"type":"response.done","response":{"id":"resp_1","status":"completed"}}`
					s.events <- `{"type":"session.closed","session_id":"sess_scripted","reason":"done"}`
					return
				case <-s.closed:
					return
				}
			}
		}()
	})
	return &audioInLifecycleConn{server: s}, nil
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

type audioInLifecycleConn struct {
	server *audioInLifecycleServer
}

func (c *audioInLifecycleConn) ReadMessage() (int, []byte, error) {
	select {
	case event := <-c.server.events:
		c.server.mu.Lock()
		c.server.writes = append(c.server.writes, "IN:"+event[:min(40, len(event))])
		c.server.mu.Unlock()
		return 1, []byte(event), nil
	case <-c.server.closed:
		return 0, nil, errors.New("connection closed")
	}
}

func (c *audioInLifecycleConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	c.server.mu.Lock()
	c.server.writes = append(c.server.writes, envelope.Type)
	c.server.mu.Unlock()
	if envelope.Type == "response.create" {
		closeOnce(c.server.responseRequested)
	}
	if envelope.Type == "session.close" || envelope.Type == "close_session" {
		closeOnce(c.server.closeStarted)
	}
	return nil
}

func (c *audioInLifecycleConn) Close() error {
	closeOnce(c.server.closed)
	return nil
}

func liveAudioInRunOptions(t *testing.T, dialer *audioInLifecycleServer, recordPath string) services.SessionRunOptions {
	t.Helper()
	return services.SessionRunOptions{
		RecordPath:      recordPath,
		Provider:        "openai",
		Model:           "gpt-realtime-2.1-mini",
		APIKey:          "test-key",
		ConfigDir:       t.TempDir(),
		WebSocketDialer: dialer,
	}
}

// TestLiveRecordRuntimeAudioInCompletesRoundTrip drives the real OpenAI
// live-record session plan (not replay) through a scripted websocket server:
// audio frames stream, end-of-turn commit + response.create reach the wire,
// and the session stays open until the terminal response.done arrives even
// though the local audio source was exhausted long before.
func TestLiveRecordRuntimeAudioInCompletesRoundTrip(t *testing.T) {
	server := newScriptedRealtimeServer(false)
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	outputPath := filepath.Join(t.TempDir(), "response.wav")

	runErr := make(chan error, 1)
	go func() {
		runErr <- services.RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
			context.Background(),
			os.Stdout,
			liveAudioInRunOptions(t, server, recordPath),
			outputPath,
			15*time.Second,
			services.SessionTextSeed{},
			services.SessionAudioInput{
				Path:    committedSessionAudioInputWAVPath(t),
				Present: true,
			},
			"",
		)
	}()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("live-mode audio-in session error = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for live-mode audio-in session")
	}

	writes := server.writesSnapshot()
	commitIndex, createIndex := -1, -1
	for index, writeType := range writes {
		switch writeType {
		case "input_audio_buffer.commit":
			commitIndex = index
		case "response.create":
			createIndex = index
		}
	}
	if commitIndex < 0 || createIndex < 0 {
		t.Fatalf("wire capture missing commit/response.create: %v", writes)
	}
	if commitIndex > createIndex {
		t.Fatalf("commit must precede response.create: %v", writes)
	}
	appends := 0
	for _, writeType := range writes[:commitIndex] {
		if writeType == "input_audio_buffer.append" {
			appends++
		}
	}
	if appends == 0 {
		t.Fatalf("no input_audio_buffer.append preceded the commit: %v", writes)
	}
	info, statErr := os.Stat(outputPath)
	if statErr != nil {
		t.Fatalf("stat recorded response audio: %v (writes=%v)", statErr, server.writesSnapshot())
	}
	if info.Size() <= 44 {
		t.Fatalf("recorded response audio = %d bytes; want non-empty assistant audio", info.Size())
	}
}

// TestLiveRecordRuntimeAudioInCancellationDuringAwaitSurfacesError proves a
// cancellation while the session waits for the model response after
// end-of-turn surfaces as an explicit error instead of a clean exit.
func TestLiveRecordRuntimeAudioInCancellationDuringAwaitSurfacesError(t *testing.T) {
	server := newScriptedRealtimeServer(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
			ctx,
			os.Stdout,
			liveAudioInRunOptions(t, server, filepath.Join(t.TempDir(), "capture.json")),
			filepath.Join(t.TempDir(), "response.wav"),
			0,
			services.SessionTextSeed{},
			services.SessionAudioInput{
				Path:    committedSessionAudioInputWAVPath(t),
				Present: true,
			},
			"",
		)
	}()

	deadline := time.After(10 * time.Second)
	for {
		writes := server.writesSnapshot()
		hasCreate := false
		for _, writeType := range writes {
			if writeType == "response.create" {
				hasCreate = true
			}
		}
		if hasCreate {
			break
		}
		select {
		case <-deadline:
			t.Fatal("end-of-turn response.create never reached the wire")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancellation during await window exited cleanly; want explicit error")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "awaiting") {
			t.Fatalf("cancellation error = %v; want context.Canceled or awaiting-response failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled session did not terminate")
	}
}

// TestRunSessionWithAudioInputEndOfTurnLostSurfacesError proves that a
// cancellation racing the end-of-turn send reports the lost signal instead of
// silently exiting successfully.
func TestRunSessionWithAudioInputEndOfTurnLostSurfacesError(t *testing.T) {
	source := newGatedAudioSource(1)
	baseInferencer := functional.NewMockSessionInferencer()
	t.Cleanup(baseInferencer.Close)
	endOfTurnInvoked := make(chan struct{})
	input := services.SessionAudioInput{
		Path:    "gated.raw",
		Present: true,
		Source:  source,
		SendAudioInput: func(_ context.Context, _ []byte) error {
			close(source.gates[0])
			return nil
		},
		SendEndOfTurn: func(ctx context.Context) error {
			close(endOfTurnInvoked)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithAudioInput(ctx, os.Stdout, services.SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: baseInferencer,
		}, input)
	}()

	select {
	case <-endOfTurnInvoked:
	case <-time.After(3 * time.Second):
		t.Fatal("end-of-turn hook never invoked")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, services.ErrSessionAudioInputEndOfTurnLost) {
			t.Fatalf("error = %v; want ErrSessionAudioInputEndOfTurnLost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not report the lost end-of-turn signal")
	}
}
