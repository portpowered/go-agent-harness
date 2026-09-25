package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"
	rooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestRoomRunCommandParsesManifestOutputAndStreamOptions(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	outputDir := filepath.Join(t.TempDir(), "evidence")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(t.TempDir(), "config")
	command := newTestRoomRunCommand(globalFlags, nil)

	var got rooms.RoomRunOptions
	command.SetRunner(func(_ context.Context, _ io.Writer, options rooms.RoomRunOptions) (rooms.RoomResult, error) {
		got = options
		if options.EventSink == nil {
			return rooms.RoomResult{}, nil
		}
		return rooms.RoomResult{}, nil
	})

	var output bytes.Buffer
	cmd := command.Generate()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{
		"--manifest", manifestPath,
		"--out", outputDir,
		"--stream", "127.0.0.1:0",
	})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute room run: %v", err)
	}
	if got.Manifest.SchemaVersion != 1 || len(got.Manifest.Participants) != 2 {
		t.Fatalf("manifest options = %+v", got.Manifest)
	}
	if got.OutputDir != outputDir {
		t.Fatalf("output directory = %q, want %q", got.OutputDir, outputDir)
	}
	if got.ConfigDir != globalFlags.ConfigDir() {
		t.Fatalf("config directory = %q, want %q", got.ConfigDir, globalFlags.ConfigDir())
	}
	if got.EventSink == nil {
		t.Fatal("stream broker is nil when --stream is configured")
	}
	if !strings.Contains(output.String(), "room stream listening: http://") {
		t.Fatalf("output = %q, want stream address", output.String())
	}
	if strings.Contains(output.String(), "alice-secret") || strings.Contains(output.String(), "bob-secret") {
		t.Fatalf("output leaked a credential: %q", output.String())
	}
}

func TestRoomRunCommandUsesDocumentedDefaultOutputAndBoundedProgress(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	var got rooms.RoomRunOptions
	command.SetRunner(func(_ context.Context, _ io.Writer, options rooms.RoomRunOptions) (rooms.RoomResult, error) {
		got = options
		options.OnDiagnostic("alice", rooms.RoomDiagnosticRecord{
			Event:  serviceSession.SessionDiagnosticEventTurn,
			Fields: map[string]string{"turn_index": "2"},
		})
		options.OnDiagnostic("alice", rooms.RoomDiagnosticRecord{
			Event:  serviceSession.SessionDiagnosticEventToolCall,
			Fields: map[string]string{"tool_name": "read_file"},
		})
		return rooms.RoomResult{
			TerminationReason: rooms.RoomTerminationMaxTurnsReached,
			Participants: map[string]rooms.RoomParticipantResult{
				"alice": {ID: "alice", ParticipantID: "alice", TerminationReason: rooms.ParticipantTerminationEnded, TurnsCompleted: 2},
			},
		}, nil
	})

	var output bytes.Buffer
	cmd := command.Generate()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--manifest", manifestPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute room run: %v", err)
	}
	if got.OutputDir != DefaultRoomOutputDir {
		t.Fatalf("default output directory = %q, want %q", got.OutputDir, DefaultRoomOutputDir)
	}
	for _, want := range []string{
		"room starting: participants=2 output=room-run",
		`participant "alice": session_turn_completed turn=2`,
		`participant "alice": session_tool_call_unexecutable`,
		"room stopped: reason=max_turns_reached participants=1 active=0",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "AUDIO.DELTA") || strings.Contains(output.String(), "raw") {
		t.Fatalf("bounded room output contains raw-delta text: %q", output.String())
	}
}

func TestRoomRunCommandEmitsRunningAfterAllParticipantsAreReady(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(_ context.Context, _ io.Writer, options rooms.RoomRunOptions) (rooms.RoomResult, error) {
		for _, participant := range options.Manifest.Participants {
			options.OnParticipantReady(rooms.RoomParticipantReady{
				ParticipantID: participant.ID,
				Kind:          participant.Kind,
				InputDevice:   participant.InputDevice,
				OutputDevice:  participant.OutputDevice,
				Provider:      participant.Provider,
				Model:         participant.Model,
			})
		}
		return rooms.RoomResult{
			TerminationReason: rooms.RoomTerminationStopped,
			Participants: map[string]rooms.RoomParticipantResult{
				"alice": {ID: "alice", ParticipantID: "alice", TerminationReason: rooms.ParticipantTerminationEnded},
				"bob":   {ID: "bob", ParticipantID: "bob", TerminationReason: rooms.ParticipantTerminationEnded},
			},
		}, nil
	})

	var output bytes.Buffer
	cmd := command.Generate()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--manifest", manifestPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute room run: %v", err)
	}
	text := output.String()
	lastReady := strings.LastIndex(text, `participant "bob" ready:`)
	running := strings.Index(text, "room running: participants=2")
	if lastReady < 0 || running < 0 || running <= lastReady {
		t.Fatalf("room output = %q, want running marker after both ready markers", text)
	}
	if !strings.Contains(text, "room stopped: reason=stopped participants=2 active=0") {
		t.Fatalf("room output = %q, want zero-active terminal marker", text)
	}
}

func TestWriteRoomResultIncludesLivenessClassification(t *testing.T) {
	var output bytes.Buffer
	writeRoomResult(&roomCommandOutput{writer: &output}, rooms.RoomResult{
		TerminationReason: rooms.RoomTerminationFailed,
		Participants: map[string]rooms.RoomParticipantResult{
			"silent": {
				ID:                "silent",
				ParticipantID:     "silent",
				TerminationReason: rooms.ParticipantTerminationError,
				Classification:    servicetest.SessionSilentProviderTimeoutClassification,
			},
		},
	})
	if !strings.Contains(output.String(), `participant "silent": error turns=0 connected=false classification=`+servicetest.SessionSilentProviderTimeoutClassification) {
		t.Fatalf("room terminal output = %q, want typed participant classification", output.String())
	}
}

// TestRoomRunCommandExampleFlagPrintsValidManifestAndExitsZero is the
// regression guard for the "no schema reference or example" defect: a
// first-time user should not have to reverse-engineer the manifest shape one
// validation error at a time. `--example` must print a real manifest that
// room.ParseManifest accepts unmodified, and must never touch the runner or
// require any of the command's other flags.
func TestRoomRunCommandExampleFlagPrintsValidManifestAndExitsZero(t *testing.T) {
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	var calls atomic.Int32
	command.SetRunner(func(context.Context, io.Writer, rooms.RoomRunOptions) (rooms.RoomResult, error) {
		calls.Add(1)
		return rooms.RoomResult{}, nil
	})
	var output bytes.Buffer
	cmd := command.Generate()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--example"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute room run --example: %v, want exit 0", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("runner calls = %d, want zero: --example must not run anything", calls.Load())
	}
	manifest, err := room.ParseManifest(output.Bytes(), room.ValidationOptions{
		LookupCredential: func(name string) (string, bool) { return "x", name == "OPENAI_API_KEY" },
	})
	if err != nil {
		t.Fatalf("--example output did not parse as a valid room manifest: %v\noutput:\n%s", err, output.String())
	}
	if len(manifest.Participants) < 2 {
		t.Fatalf("--example manifest participants = %d, want at least 2", len(manifest.Participants))
	}
}

func TestRoomRunCommandRejectsInvalidManifestBeforeRunner(t *testing.T) {
	secret := "manifest-secret-value"
	t.Setenv("ROOM_ALICE_KEY", secret)
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"room":{"max_turns":1},"participants":[]}`), 0o600); err != nil {
		t.Fatalf("write invalid manifest: %v", err)
	}
	var calls atomic.Int32
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(context.Context, io.Writer, rooms.RoomRunOptions) (rooms.RoomResult, error) {
		calls.Add(1)
		return rooms.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--manifest", path, "--out", filepath.Join(t.TempDir(), "out")})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "participants") {
		t.Fatalf("error = %v, want participant validation error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("manifest error leaked credential: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("runner calls = %d, want zero", calls.Load())
	}
}

// TestRoomRunCommandRejectsAllAgentRoomWithNoOpenerAndExitsNonZero is the
// core regression guard for the silent-all-agent-room defect: a room with
// only agent participants and no opening_prompt has nobody to speak first,
// so a live run would idle in silence until max_duration expired and still
// report success (0 turns, 0 audio, reason=max_duration_reached, exit 0).
// The command must instead fail loudly before the runner (and therefore
// before any provider is dialed), and `agent room run`'s process exit code
// must be non-zero — cmd/agent's main.go calls os.Exit(1) whenever
// rootCmd.Execute() returns a non-nil error, so asserting a non-nil error
// here is the exit-code assertion.
func TestRoomRunCommandRejectsAllAgentRoomWithNoOpenerAndExitsNonZero(t *testing.T) {
	t.Setenv("ROOM_ALICE_KEY", "alice-secret")
	t.Setenv("ROOM_BOB_KEY", "bob-secret")
	path := filepath.Join(t.TempDir(), "no-opener.json")
	data := []byte(`{
  "schema_version": 1,
  "room": {"max_turns": 2},
  "participants": [
    {"id": "alice", "system_prompt": "Alice", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_ALICE_KEY", "tools": []},
    {"id": "bob", "system_prompt": "Bob", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_BOB_KEY", "tools": []}
  ]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write no-opener manifest: %v", err)
	}
	var calls atomic.Int32
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(context.Context, io.Writer, rooms.RoomRunOptions) (rooms.RoomResult, error) {
		calls.Add(1)
		return rooms.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--manifest", path, "--out", filepath.Join(t.TempDir(), "out")})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("error = <nil>, want a non-nil error (and therefore a non-zero process exit) for an all-agent room with no designated opener")
	}
	if !strings.Contains(err.Error(), "opening_prompt") {
		t.Fatalf("error = %v, want actionable guidance naming opening_prompt", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("runner calls = %d, want zero: a room that can never speak must never reach the runner (and never dial a provider)", calls.Load())
	}
}

// TestRoomRunCommandAcceptsRoomWithDesignatedOpener guards against
// over-triggering the new check: an ordinary two-agent room where one
// participant sets opening_prompt must still start and succeed normally.
func TestRoomRunCommandAcceptsRoomWithDesignatedOpener(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	var calls atomic.Int32
	command.SetRunner(func(_ context.Context, _ io.Writer, options rooms.RoomRunOptions) (rooms.RoomResult, error) {
		calls.Add(1)
		if len(options.Manifest.Participants) == 0 || options.Manifest.Participants[0].OpeningPrompt == "" {
			t.Fatalf("expected the manifest's opening participant to be preserved: %+v", options.Manifest.Participants)
		}
		return rooms.RoomResult{
			TerminationReason: rooms.RoomTerminationMaxTurnsReached,
			Participants: map[string]rooms.RoomParticipantResult{
				"alice": {ID: "alice", ParticipantID: "alice", TerminationReason: rooms.ParticipantTerminationEnded, TurnsCompleted: 2},
				"bob":   {ID: "bob", ParticipantID: "bob", TerminationReason: rooms.ParticipantTerminationEnded, TurnsCompleted: 2},
			},
		}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--manifest", manifestPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute normal room with a designated opener: %v, want exit 0", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("runner calls = %d, want exactly one", calls.Load())
	}
}

func TestRoomRunCommandReplayAdmissionBypassesLiveLaunchSeams(t *testing.T) {
	registry := newBareRoomCLIRegistry(t)
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), registry)
	var runnerCalls atomic.Int32
	command.SetRunner(func(context.Context, io.Writer, rooms.RoomRunOptions) (rooms.RoomResult, error) {
		runnerCalls.Add(1)
		return rooms.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--replay", filepath.Join(t.TempDir(), "missing-room-bundle")})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, rooms.ErrReplayBundleIncomplete) {
		t.Fatalf("replay admission error = %v, want incomplete bundle error", err)
	}
	if runnerCalls.Load() != 0 || registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("replay admission caused live startup work: runner=%d defaults=%d opens=%d", runnerCalls.Load(), registry.defaultCalls, registry.openCalls)
	}
}

func TestRoomRunCommandRejectsReplaySourceCompetition(t *testing.T) {
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), newBareRoomCLIRegistry(t))
	var runnerCalls atomic.Int32
	command.SetRunner(func(context.Context, io.Writer, rooms.RoomRunOptions) (rooms.RoomResult, error) {
		runnerCalls.Add(1)
		return rooms.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--replay", filepath.Join(t.TempDir(), "bundle"), "--config", filepath.Join(t.TempDir(), "room.json")})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, rooms.ErrReplaySourceConflict) {
		t.Fatalf("source competition error = %v, want replay source conflict", err)
	}
	if runnerCalls.Load() != 0 {
		t.Fatalf("runner calls = %d, want zero", runnerCalls.Load())
	}
}

func TestRoomRunCommandRejectsUnsafeOutputBeforeRunner(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	outputDir := filepath.Join(t.TempDir(), "occupied")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "existing.txt"), []byte("occupied"), 0o600); err != nil {
		t.Fatalf("seed output directory: %v", err)
	}
	var calls atomic.Int32
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(context.Context, io.Writer, rooms.RoomRunOptions) (rooms.RoomResult, error) {
		calls.Add(1)
		return rooms.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--manifest", manifestPath, "--out", outputDir})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("error = %v, want empty-output validation error", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("runner calls = %d, want zero", calls.Load())
	}
}

func TestRoomRunCommandRejectsMalformedAndOccupiedStreamBeforeRunner(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{name: "malformed", address: "not-an-address", want: "listen room stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
			command.SetRunner(func(context.Context, io.Writer, rooms.RoomRunOptions) (rooms.RoomResult, error) {
				calls.Add(1)
				return rooms.RoomResult{}, nil
			})
			cmd := command.Generate()
			cmd.SetArgs([]string{"--manifest", manifestPath, "--out", filepath.Join(t.TempDir(), "out"), "--stream", tt.address})
			err := cmd.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if calls.Load() != 0 {
				t.Fatalf("runner calls = %d, want zero", calls.Load())
			}
		})
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve stream address: %v", err)
	}
	defer closeTestResource(t, listener)
	var calls atomic.Int32
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(context.Context, io.Writer, rooms.RoomRunOptions) (rooms.RoomResult, error) {
		calls.Add(1)
		return rooms.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--manifest", manifestPath, "--out", filepath.Join(t.TempDir(), "out"), "--stream", listener.Addr().String()})
	err = cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen room stream") {
		t.Fatalf("occupied stream error = %v, want listen error", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("occupied stream runner calls = %d, want zero", calls.Load())
	}
}

func TestRoomRunCommandRedactsCredentialsFromEventStreamOutputAndError(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	output, stream := &bytes.Buffer{}, (*http.Response)(nil)
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(ctx context.Context, _ io.Writer, options rooms.RoomRunOptions) (rooms.RoomResult, error) {
		response, err := http.Get(strings.Fields(strings.SplitN(output.String(), "room stream listening: ", 2)[1])[0])
		if err != nil {
			t.Fatalf("connect event stream: %v", err)
		}
		stream = response
		t.Cleanup(func() { closeTestResource(t, response.Body) })
		publishErr := errors.Join(options.EventSink.Publish(ctx, "alice", session.LiveEvent{Kind: "browser.invocation", State: "key alice-secret"}),
			options.EventSink.Publish(ctx, "bob", session.LiveEvent{Kind: "browser.failed", Reason: "auth bob-secret rejected"}))
		options.OnParticipantTerminated(rooms.RoomParticipantResult{ParticipantID: "alice", TerminationReason: "error alice-secret"})
		return rooms.RoomResult{TerminationReason: "failed bob-secret"}, errors.Join(publishErr, errors.New("provider rejected alice-secret"))
	})
	cmd := command.Generate()
	cmd.SetOut(output)
	cmd.SetArgs([]string{"--manifest", manifestPath, "--out", filepath.Join(t.TempDir(), "out"), "--stream", "127.0.0.1:0"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || strings.Contains(err.Error(), "alice-secret") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("room error = %v, want the credential redacted", err)
	}
	body, readErr := io.ReadAll(stream.Body)
	if readErr != nil || !strings.Contains(string(body), `"state":"key [REDACTED]"`) || !strings.Contains(string(body), "auth [REDACTED] rejected") {
		t.Fatalf("event stream = %q (%v), want redacted participant events", body, readErr)
	}
	if text := output.String() + string(body); strings.Contains(text, "alice-secret") || strings.Contains(text, "bob-secret") {
		t.Fatalf("stdout or /events leaked a credential: %q", text)
	}
}

func TestRoomRunCommandPropagatesCallerCancellationToRunner(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	runnerStarted := make(chan struct{})
	runnerCanceled := make(chan struct{})
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(ctx context.Context, _ io.Writer, _ rooms.RoomRunOptions) (rooms.RoomResult, error) {
		close(runnerStarted)
		<-ctx.Done()
		close(runnerCanceled)
		return rooms.RoomResult{TerminationReason: rooms.RoomTerminationStopped}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--manifest", manifestPath, "--out", filepath.Join(t.TempDir(), "out")})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(parent) }()
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("room runner did not start")
	}
	cancel()
	select {
	case <-runnerCanceled:
	case <-time.After(time.Second):
		t.Fatal("room runner did not receive caller cancellation")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("room command cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("room command did not return after cancellation")
	}
}

func writeRoomCLIManifest(t *testing.T) string {
	t.Helper()
	t.Setenv("ROOM_ALICE_KEY", "alice-secret")
	t.Setenv("ROOM_BOB_KEY", "bob-secret")
	path := filepath.Join(t.TempDir(), "room.json")
	data := []byte(`{
  "schema_version": 1,
  "room": {"max_turns": 2},
  "participants": [
    {"id": "alice", "system_prompt": "Alice", "opening_prompt": "Start the room.", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_ALICE_KEY", "tools": []},
    {"id": "bob", "system_prompt": "Bob", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_BOB_KEY", "tools": []}
  ]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write room manifest: %v", err)
	}
	return path
}

// TestRoomRunCommandSurfacesAllParticipantsFailedAsNonZeroExit covers
// Instance 3 of the exit-code-must-mean-the-work-happened defect family: the
// rooms service classifies a run in which every participant failed as a typed
// room error, and the CLI must exit non-zero while still printing the result.
func TestRoomRunCommandSurfacesAllParticipantsFailedAsNonZeroExit(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(_ context.Context, _ io.Writer, _ rooms.RoomRunOptions) (rooms.RoomResult, error) {
		return rooms.RoomResult{
				TerminationReason: rooms.RoomTerminationStopped,
				Participants: map[string]rooms.RoomParticipantResult{
					roomTestAliceID: {ID: roomTestAliceID, ParticipantID: roomTestAliceID, TerminationReason: rooms.ParticipantTerminationError, Error: "provider dial failed"},
					"bob":           {ID: "bob", ParticipantID: "bob", TerminationReason: rooms.ParticipantTerminationError, Error: "provider dial failed"},
				},
			}, &rooms.AllParticipantsFailedError{Participants: []rooms.ParticipantFailureDetail{
				{ParticipantID: roomTestAliceID, Error: "provider dial failed"}, {ParticipantID: "bob", Error: "provider dial failed"},
			}}
	})
	var output bytes.Buffer
	cmd := command.Generate()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--manifest", manifestPath})
	err := cmd.ExecuteContext(context.Background())
	if !errors.Is(err, rooms.ErrAllParticipantsFailed) {
		t.Fatalf("room run with every participant failing returned %v; want a named non-zero failure; output=%q", err, output.String())
	}
	if !strings.Contains(output.String(), `participant "alice": error`) {
		t.Fatalf("output = %q, want the failed participant results", output.String())
	}
	for _, want := range []string{roomTestAliceID, "bob", "all 2 participant"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestRoomRunCommandPartialParticipantFailureStillExitsZero asserts no
// over-triggering, and that #321 fault isolation is preserved at the CLI
// boundary: one participant failing while the other survives must still exit
// 0, exactly as before this fix.
func TestRoomRunCommandPartialParticipantFailureStillExitsZero(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(_ context.Context, _ io.Writer, _ rooms.RoomRunOptions) (rooms.RoomResult, error) {
		return rooms.RoomResult{
			TerminationReason: rooms.RoomTerminationMaxTurnsReached,
			Participants: map[string]rooms.RoomParticipantResult{
				"alice": {ID: "alice", ParticipantID: "alice", TerminationReason: rooms.ParticipantTerminationError, Error: "provider dial failed"},
				"bob":   {ID: "bob", ParticipantID: "bob", TerminationReason: rooms.ParticipantTerminationEnded, TurnsCompleted: 2},
			},
		}, nil
	})

	var output bytes.Buffer
	cmd := command.Generate()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--manifest", manifestPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("room run with a surviving peer returned an error: %v\noutput=%q", err, output.String())
	}
}

// TestRoomRunCommandAllParticipantsSucceedStillExitsZero asserts no
// over-triggering for the ordinary success case: every participant ending
// normally must still exit 0.
func TestRoomRunCommandAllParticipantsSucceedStillExitsZero(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	command := newTestRoomRunCommand(flags.NewGlobalFlags(), nil)
	command.SetRunner(func(_ context.Context, _ io.Writer, _ rooms.RoomRunOptions) (rooms.RoomResult, error) {
		return rooms.RoomResult{
			TerminationReason: rooms.RoomTerminationMaxTurnsReached,
			Participants: map[string]rooms.RoomParticipantResult{
				"alice": {ID: "alice", ParticipantID: "alice", TerminationReason: rooms.ParticipantTerminationEnded, TurnsCompleted: 2},
				"bob":   {ID: "bob", ParticipantID: "bob", TerminationReason: rooms.ParticipantTerminationEnded, TurnsCompleted: 2},
			},
		}, nil
	})

	var output bytes.Buffer
	cmd := command.Generate()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--manifest", manifestPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("room run with every participant succeeding returned an error: %v\noutput=%q", err, output.String())
	}
}
