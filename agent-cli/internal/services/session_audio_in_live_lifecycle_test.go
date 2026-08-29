package services_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	functional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type audioInLifecycleServer struct {
	mu             sync.Mutex
	writes         []string
	sessionUpdates []json.RawMessage

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

func (s *audioInLifecycleServer) sessionUpdateSnapshot() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessionUpdates) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), s.sessionUpdates[0]...)
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
		Type    string          `json:"type"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	c.server.mu.Lock()
	c.server.writes = append(c.server.writes, envelope.Type)
	if envelope.Type == "session.update" && len(envelope.Session) > 0 {
		c.server.sessionUpdates = append(c.server.sessionUpdates, append(json.RawMessage(nil), envelope.Session...))
	}
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

// scheduledAudioLifecycleServer is a live-shaped OpenAI transport that emits
// one response for each response.create and deliberately never sends a
// captured session.closed event. The session runner must close locally only
// after the final scheduled response.
type scheduledAudioLifecycleServer struct {
	mu                      sync.Mutex
	writes                  []string
	sessionUpdates          []json.RawMessage
	responses               chan int
	events                  chan string
	closed                  chan struct{}
	closeOnce               sync.Once
	sessionCreated          chan struct{}
	sessionUpdatedRelease   chan struct{}
	bargeIn                 bool
	holdBargeResponseOutput bool
	bargeResponseTurn       int
	firstResponseCancel     chan struct{}
	firstCancelOnce         sync.Once
	nextTurn                int
}

func newScheduledAudioLifecycleServer() *scheduledAudioLifecycleServer {
	return &scheduledAudioLifecycleServer{
		responses:      make(chan int, 8),
		events:         make(chan string, 32),
		closed:         make(chan struct{}),
		sessionCreated: make(chan struct{}),
	}
}

func newDelayedScheduledAudioLifecycleServer() *scheduledAudioLifecycleServer {
	server := newScheduledAudioLifecycleServer()
	server.sessionUpdatedRelease = make(chan struct{})
	return server
}

func newBargeInScheduledAudioLifecycleServer() *scheduledAudioLifecycleServer {
	server := newScheduledAudioLifecycleServer()
	// An unbuffered event path makes the active response boundary observable
	// before the fixture waits for the cancellation owned by ModelRunner.
	server.events = make(chan string)
	server.bargeIn = true
	server.bargeResponseTurn = 1
	server.firstResponseCancel = make(chan struct{})
	return server
}

func newPromptBargeInScheduledAudioLifecycleServer() *scheduledAudioLifecycleServer {
	server := newBargeInScheduledAudioLifecycleServer()
	server.bargeResponseTurn = 2
	server.holdBargeResponseOutput = true
	return server
}

func (s *scheduledAudioLifecycleServer) writesSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *scheduledAudioLifecycleServer) sessionUpdateSnapshot() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessionUpdates) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), s.sessionUpdates[0]...)
}

func (s *scheduledAudioLifecycleServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	go func() {
		s.events <- `{"type":"session.created","session":{"id":"sess_scheduled","model":"gpt-realtime"}}`
		closeOnce(s.sessionCreated)
		if s.sessionUpdatedRelease != nil {
			select {
			case <-s.sessionUpdatedRelease:
			case <-s.closed:
				return
			}
		}
		s.events <- `{"type":"session.updated","session":{"id":"sess_scheduled","model":"gpt-realtime"}}`
		for {
			select {
			case turn := <-s.responses:
				if s.bargeIn && turn == s.bargeResponseTurn {
					s.events <- `{"type":"input_audio_buffer.speech_started"}`
					s.events <- `{"type":"input_audio_buffer.speech_stopped"}`
					s.events <- `{"type":"response.created","response":{"id":"resp_` + string(rune('0'+turn)) + `"}}`
					if !s.holdBargeResponseOutput {
						s.events <- `{"type":"response.output_audio.delta","delta":"AQID","format":"pcm16"}`
					}
					select {
					case <-s.firstResponseCancel:
						// This delta is deliberately stale. ModelRunner must suppress
						// it after its accepted RESPONSE.CANCEL boundary.
						s.events <- `{"type":"response.output_audio.delta","delta":"Y2FuY2VsLXN0YWxl","format":"pcm16"}`
						s.events <- `{"type":"response.done","response":{"id":"resp_` + string(rune('0'+turn)) + `","status":"cancelled"}}`
					case <-s.closed:
						return
					}
					continue
				}
				audio := base64AudioForTurn(turn)
				s.events <- `{"type":"input_audio_buffer.speech_started"}`
				s.events <- `{"type":"input_audio_buffer.speech_stopped"}`
				s.events <- `{"type":"response.created","response":{"id":"resp_` + string(rune('0'+turn)) + `"}}`
				s.events <- `{"type":"response.output_audio.delta","delta":"` + audio + `","format":"pcm16"}`
				s.events <- `{"type":"response.output_audio.done"}`
				s.events <- `{"type":"response.done","response":{"status":"completed"}}`
			case <-s.closed:
				return
			}
		}
	}()
	return &scheduledAudioLifecycleConn{server: s}, nil
}

func (s *scheduledAudioLifecycleServer) releaseSessionUpdated() {
	if s == nil || s.sessionUpdatedRelease == nil {
		return
	}
	closeOnce(s.sessionUpdatedRelease)
}

func base64AudioForTurn(turn int) string {
	return base64.StdEncoding.EncodeToString([]byte{byte(turn), 0, byte(turn + 10), 0})
}

type scheduledAudioLifecycleConn struct {
	server *scheduledAudioLifecycleServer
}

func (c *scheduledAudioLifecycleConn) ReadMessage() (int, []byte, error) {
	select {
	case event := <-c.server.events:
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(event), &envelope); err == nil {
			c.server.mu.Lock()
			c.server.writes = append(c.server.writes, "IN:"+envelope.Type)
			c.server.mu.Unlock()
		}
		return 1, []byte(event), nil
	case <-c.server.closed:
		return 0, nil, errors.New("connection closed")
	}
}

func (c *scheduledAudioLifecycleConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type    string          `json:"type"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	c.server.mu.Lock()
	c.server.writes = append(c.server.writes, envelope.Type)
	if envelope.Type == "session.update" && len(envelope.Session) > 0 {
		c.server.sessionUpdates = append(c.server.sessionUpdates, append(json.RawMessage(nil), envelope.Session...))
	}
	if envelope.Type == "response.cancel" && c.server.bargeIn {
		c.server.firstCancelOnce.Do(func() { close(c.server.firstResponseCancel) })
	}
	if envelope.Type == "response.create" {
		c.server.nextTurn++
		turn := c.server.nextTurn
		c.server.mu.Unlock()
		c.server.responses <- turn
		return nil
	}
	c.server.mu.Unlock()
	return nil
}

func (c *scheduledAudioLifecycleConn) Close() error {
	c.server.closeOnce.Do(func() { close(c.server.closed) })
	return nil
}

// scheduledEmptyToolLifecycleServer is the exact three-turn live-shaped
// fixture for the empty-tool-result regression. The second scheduled response
// requests a directory listing, returns no assistant audio, and only permits
// the third scheduled response after the tool result's explicit continuation
// reaches a terminal response.done.
type scheduledEmptyToolLifecycleServer struct {
	mu             sync.Mutex
	writes         []string
	clientMessages [][]byte
	sessionUpdates []json.RawMessage
	responses      chan int
	events         chan string
	closed         chan struct{}
	shutdownOnce   sync.Once
	startOnce      sync.Once
	nextResponse   int
}

func newScheduledEmptyToolLifecycleServer() *scheduledEmptyToolLifecycleServer {
	return &scheduledEmptyToolLifecycleServer{
		responses: make(chan int, 8),
		events:    make(chan string, 64),
		closed:    make(chan struct{}),
	}
}

func (s *scheduledEmptyToolLifecycleServer) writesSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *scheduledEmptyToolLifecycleServer) clientMessagesSnapshot() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages := make([][]byte, len(s.clientMessages))
	for index, payload := range s.clientMessages {
		messages[index] = append([]byte(nil), payload...)
	}
	return messages
}

func (s *scheduledEmptyToolLifecycleServer) shutdown() {
	s.shutdownOnce.Do(func() { close(s.closed) })
}

func (s *scheduledEmptyToolLifecycleServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.startOnce.Do(func() {
		go s.serve()
	})
	return &scheduledEmptyToolLifecycleConn{server: s}, nil
}

func (s *scheduledEmptyToolLifecycleServer) serve() {
	if !s.sendEvent(`{"type":"session.created","session":{"id":"sess_empty_tool","model":"gpt-realtime"}}`) {
		return
	}
	if !s.sendEvent(`{"type":"session.updated","session":{"id":"sess_empty_tool","model":"gpt-realtime"}}`) {
		return
	}
	for {
		select {
		case responseNumber := <-s.responses:
			if responseNumber == 2 {
				if !s.sendEvent(`{"type":"input_audio_buffer.speech_started"}`) ||
					!s.sendEvent(`{"type":"input_audio_buffer.speech_stopped"}`) ||
					!s.sendEvent(`{"type":"response.created","response":{"id":"resp_tool"}}`) ||
					!s.sendEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"item_empty_directory","call_id":"call_empty_directory","name":"list_directory","arguments":""}}`) ||
					!s.sendEvent(`{"type":"response.function_call_arguments.done","call_id":"call_empty_directory","name":"list_directory","arguments":"{}"}`) ||
					!s.sendEvent(`{"type":"response.done","response":{"status":"completed"}}`) {
					return
				}
				continue
			}
			if !s.sendEvent(`{"type":"input_audio_buffer.speech_started"}`) ||
				!s.sendEvent(`{"type":"input_audio_buffer.speech_stopped"}`) ||
				!s.sendEvent(`{"type":"response.created","response":{"id":"resp_`+string(rune('0'+responseNumber))+`"}}`) ||
				!s.sendEvent(`{"type":"response.output_audio.delta","delta":"`+base64AudioForTurn(responseNumber)+`","format":"pcm16"}`) ||
				!s.sendEvent(`{"type":"response.output_audio.done"}`) ||
				!s.sendEvent(`{"type":"response.done","response":{"status":"completed"}}`) {
				return
			}
		case <-s.closed:
			return
		}
	}
}

func (s *scheduledEmptyToolLifecycleServer) sendEvent(event string) bool {
	select {
	case s.events <- event:
		return true
	case <-s.closed:
		return false
	}
}

type scheduledEmptyToolLifecycleConn struct {
	server *scheduledEmptyToolLifecycleServer
}

func (c *scheduledEmptyToolLifecycleConn) ReadMessage() (int, []byte, error) {
	select {
	case event := <-c.server.events:
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(event), &envelope); err == nil {
			c.server.mu.Lock()
			c.server.writes = append(c.server.writes, "IN:"+envelope.Type)
			c.server.mu.Unlock()
		}
		return 1, []byte(event), nil
	case <-c.server.closed:
		return 0, nil, errors.New("connection closed")
	}
}

func (c *scheduledEmptyToolLifecycleConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type    string          `json:"type"`
		Session json.RawMessage `json:"session"`
		Item    struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	c.server.mu.Lock()
	c.server.writes = append(c.server.writes, envelope.Type)
	c.server.clientMessages = append(c.server.clientMessages, append([]byte(nil), payload...))
	if envelope.Type == "session.update" && len(envelope.Session) > 0 {
		c.server.sessionUpdates = append(c.server.sessionUpdates, append(json.RawMessage(nil), envelope.Session...))
	}
	if envelope.Type == "response.create" {
		c.server.nextResponse++
		responseNumber := c.server.nextResponse
		c.server.mu.Unlock()
		select {
		case c.server.responses <- responseNumber:
			return nil
		case <-c.server.closed:
			return errors.New("connection closed")
		}
	}
	c.server.mu.Unlock()
	return nil
}

func (c *scheduledEmptyToolLifecycleConn) Close() error {
	c.server.shutdown()
	return nil
}

type emptyDirectoryToolExecutor struct {
	mu    sync.Mutex
	calls []messages.ToolCall
}

func (e *emptyDirectoryToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, nil
}

func (e *emptyDirectoryToolExecutor) callsSnapshot() []messages.ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...)
}

type scheduledTurnDiagnosticSink struct {
	mu      sync.Mutex
	records []services.SessionDiagnosticRecord
}

func (s *scheduledTurnDiagnosticSink) RecordSessionDiagnostic(record services.SessionDiagnosticRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
}

func (s *scheduledTurnDiagnosticSink) recordsSnapshot() []services.SessionDiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]services.SessionDiagnosticRecord(nil), s.records...)
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
