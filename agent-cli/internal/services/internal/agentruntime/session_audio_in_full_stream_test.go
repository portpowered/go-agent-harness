package agentruntime_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// fullStreamServer is a scripted OpenAI Realtime websocket double that counts
// every client-to-server append, commit, and response.create on the wire and
// answers end-of-turn with a complete terminal response.
type fullStreamServer struct {
	mu     sync.Mutex
	writes []string

	responseRequested chan struct{}
	events            chan string
	closed            chan struct{}
	once              sync.Once
}

func newFullStreamServer() *fullStreamServer {
	return &fullStreamServer{
		responseRequested: make(chan struct{}),
		events:            make(chan string, 64),
		closed:            make(chan struct{}),
	}
}

func (s *fullStreamServer) writesSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *fullStreamServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.once.Do(func() {
		go func() {
			s.events <- `{"type":"session.created","session":{"id":"sess_full_stream","model":"gpt-realtime-2.1-mini"}}`
			select {
			case <-s.responseRequested:
			case <-s.closed:
				return
			}
			s.events <- `{"type":"response.created","response":{"id":"resp_1"}}`
			s.events <- `{"type":"response.output_audio_transcript.done","transcript":"Hi there."}`
			s.events <- `{"type":"response.output_audio.delta","delta":"c3Bva2VuLXJlc3BvbnNlIQ=="}`
			s.events <- `{"type":"response.output_audio.done"}`
			s.events <- `{"type":"response.done","response":{"id":"resp_1","status":"completed"}}`
			s.events <- `{"type":"session.closed","session_id":"sess_full_stream","reason":"done"}`
		}()
	})
	return &fullStreamConn{server: s}, nil
}

type fullStreamConn struct{ server *fullStreamServer }

func (c *fullStreamConn) ReadMessage() (int, []byte, error) {
	select {
	case event := <-c.server.events:
		return 1, []byte(event), nil
	case <-c.server.closed:
		return 0, nil, errors.New("connection closed")
	}
}

func (c *fullStreamConn) WriteMessage(_ int, payload []byte) error {
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
		select {
		case c.server.responseRequested <- struct{}{}:
		default:
		}
	}
	return nil
}

func (c *fullStreamConn) Close() error {
	s := c.server
	s.mu.Lock()
	done := s.closed
	s.mu.Unlock()
	select {
	case <-done:
	default:
		close(done)
	}
	return nil
}

// fixtureAudioWAVPath locates a committed corpus WAV fixture under
// go-agent-loop/testdata/audio without embedding its bytes in this package.
func fixtureAudioWAVPath(t *testing.T, name string) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(sourcePath), "..", "..", "..", "..", "..", "go-agent-loop", "testdata", "audio", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture %s not found: %v", name, err)
	}
	return path
}

// fixtureWAVSampleCount parses the data-chunk sample count from a WAV header
// so expected append counts derive from the fixture itself.
func fixtureWAVSampleCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if len(data) < 44 || string(data[36:40]) != "data" {
		t.Fatalf("fixture %s lacks a canonical 44-byte header", path)
	}
	dataBytes := binary.LittleEndian.Uint32(data[40:44])
	return int(dataBytes) / 2
}

// TestFullFixtureStreamsEveryAppendBeforeEndOfTurn proves the session
// audio-in pump delivers EVERY frame of a real committed fixture WAV to the
// provider wire before exactly one end-of-turn signal, with no silent
// mid-file truncation. Expected append counts are derived from the fixtures'
// WAV headers: ceil(sampleCount / audio.FrameSize).
func TestFullFixtureStreamsEveryAppendBeforeEndOfTurn(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{name: "utt_short_16k", file: "utt_short_16k.wav"},
		{name: "utt_long_16k", file: "utt_long_16k.wav"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wavPath := fixtureAudioWAVPath(t, tc.file)
			samples := fixtureWAVSampleCount(t, wavPath)
			wantAppends := (samples + 479) / 480
			if wantAppends <= 1 {
				t.Fatalf("fixture sample count %d yields degenerate expectation %d", samples, wantAppends)
			}

			server := newFullStreamServer()
			runErr := make(chan error, 1)
			go func() {
				runErr <- agentruntime.RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
					context.Background(),
					os.Stdout,
					agentruntime.SessionRunOptions{
						RecordPath:      filepath.Join(t.TempDir(), "capture.json"),
						Provider:        "openai",
						Model:           "gpt-realtime-2.1-mini",
						APIKey:          "test-key",
						ConfigDir:       t.TempDir(),
						WebSocketDialer: server,
					},
					filepath.Join(t.TempDir(), "response.wav"),
					60*time.Second,
					agentruntime.SessionTextSeed{},
					agentruntime.SessionAudioInput{
						Path:    wavPath,
						Present: true,
					},
					"",
				)
			}()

			select {
			case err := <-runErr:
				if err != nil {
					t.Fatalf("audio-in session error = %v", err)
				}
			case <-time.After(90 * time.Second):
				t.Fatal("timed out waiting for audio-in session")
			}

			appends, commits, creates := 0, 0, 0
			createAfterCommit := false
			commitSeen := false
			for _, writeType := range server.writesSnapshot() {
				switch writeType {
				case "input_audio_buffer.append":
					if !commitSeen {
						appends++
					}
				case "input_audio_buffer.commit":
					commits++
					commitSeen = true
				case "response.create":
					creates++
					if commitSeen {
						createAfterCommit = true
					}
				}
			}
			if appends != wantAppends {
				t.Fatalf("wire saw %d input_audio_buffer.append events before commit, want exactly %d (ceil(%d/480)) from fixture %s", appends, wantAppends, samples, tc.file)
			}
			if commits != 1 {
				t.Fatalf("wire saw %d input_audio_buffer.commit events, want exactly 1", commits)
			}
			if creates != 1 || !createAfterCommit {
				t.Fatalf("wire saw %d response.create events (after commit=%v), want exactly 1 after commit", creates, createAfterCommit)
			}
		})
	}
}
