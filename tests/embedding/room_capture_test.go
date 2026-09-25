package embedding_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	captureReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	runtimeRoomReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestExternalRoomRejectsMissingProviderTrace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheduler := clock.NewDeterministic(time.Unix(1_700_000_000, 0), time.Millisecond)
	live := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		Clock:     scheduler.Now,
		Scheduler: scheduler,
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return newEmbeddedLiveProvider(), nil
		},
	})
	host := roomswire.NewService(roomswire.Dependencies{
		Clock: scheduler, Live: live, Replay: runtimeRoomReplayWire.NewService(captureReplayWire.NewService()),
		Evidence: roomevidencewire.NewService(), Latency: roomevidencewire.NewLatencyService(),
	})
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{MaxDuration: time.Second}}
	for _, id := range []string{"alice", "bob"} {
		manifest.Participants = append(manifest.Participants, rooms.Participant{ID: id, SystemPrompt: "agent", OpeningPrompt: "start", Provider: "fixture", Model: "fixture", APIKeyEnv: "UNRESOLVED_TEST_SELECTOR", Tools: []string{}})
	}
	output := t.TempDir()
	ready := make(chan struct{}, 2)
	finished := make(chan rooms.RoomResult, 1)
	go func() {
		result, err := host.Run(ctx, nil, rooms.RoomRunOptions{Manifest: manifest, OutputDir: output, OnParticipantReady: func(rooms.RoomParticipantReady) { ready <- struct{}{} }})
		if err != nil {
			t.Errorf("run room: %v", err)
		}
		finished <- result
	}()
	for range manifest.Participants {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("room participants were not admitted")
		}
	}
	scheduler.AdvanceBy(time.Second)
	select {
	case result := <-finished:
		if result.RecordingStatus == nil || result.RecordingStatus.State != "partial" {
			t.Fatalf("missing capture status=%+v, want partial", result.RecordingStatus)
		}
	case <-ctx.Done():
		t.Fatal("room did not finalize")
	}
	if _, err := host.LoadReplayPlan(output); !errors.Is(err, rooms.ErrReplayBundleIncomplete) {
		t.Fatalf("replay admission=%v, want incomplete evidence", err)
	}
	for _, participant := range manifest.Participants {
		path := filepath.Join(output, "participants", participant.ID, "capture.json")
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing provider trace was fabricated at %s: %v", path, err)
		}
	}
}

const (
	redactionEnvRef       = "ROOM_REDACTION_ENV_KEY"
	redactionConfigRef    = "ROOM_REDACTION_CONFIG_KEY"
	redactionEnvSecret    = "sk-env-participant-secret-0123456789"
	redactionConfigSecret = "sk-config-participant-secret-9876543210"
	redactedEvidence      = "[REDACTED]"
)

// The room resolves the credentials its participants use (an environment
// reference and the host config fallback) and hands them to the evidence
// owner, so no artifact in the bundle, including the run result manifest,
// carries an API key a provider echoed back in output, events or errors.
func TestExternalRoomRedactsParticipantCredentialsFromEvidence(t *testing.T) {
	secrets := map[string]string{"alpha": redactionEnvSecret, "beta": redactionConfigSecret}
	host := roomswire.NewService(roomswire.Dependencies{Live: &leakingRoomLive{secrets: secrets}, Clock: clock.Real{}, Evidence: roomevidencewire.NewService()})
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{Interactive: true}, Participants: []rooms.Participant{
		{ID: "alpha", Kind: rooms.ParticipantKindAgent, SystemPrompt: "alpha", OpeningPrompt: "start", Provider: "fixture", Model: "fixture-model", APIKeyEnv: redactionEnvRef, Tools: []string{}},
		{ID: "beta", Kind: rooms.ParticipantKindAgent, SystemPrompt: "beta", OpeningPrompt: "start", Provider: "fixture", Model: "fixture-model", APIKeyEnv: redactionConfigRef, Tools: []string{}},
	}}
	output := filepath.Join(t.TempDir(), "bundle")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := host.Run(ctx, nil, rooms.RoomRunOptions{
		Manifest: manifest, OutputDir: output,
		CredentialLookup: func(name string) (string, bool) {
			return redactionEnvSecret, name == redactionEnvRef
		},
		ConfigCredential: func(name string) (string, error) {
			if name == redactionConfigRef {
				return redactionConfigSecret, nil
			}
			return "", errors.New("no configured credential")
		},
	})
	if result.TerminationReason == "" {
		t.Fatalf("room result = %+v / %v, want a terminal result", result, err)
	}
	if !assertNoSecretsInEvidence(t, output, secrets) {
		t.Fatal("no evidence artifact recorded a redacted credential; the leaking output never reached evidence")
	}
	manifestJSON, err := os.ReadFile(filepath.Join(output, roomevidence.ManifestPath))
	if err != nil {
		t.Fatalf("read result manifest: %v", err)
	}
	if !bytes.Contains(manifestJSON, []byte(redactedEvidence)) {
		t.Fatalf("result manifest did not record the redacted participant errors: %s", manifestJSON)
	}
}

// assertNoSecretsInEvidence fails for every artifact containing a secret and
// reports whether any artifact recorded a redaction marker.
func assertNoSecretsInEvidence(t *testing.T, output string, secrets map[string]string) bool {
	t.Helper()
	redacted := false
	err := filepath.WalkDir(output, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for id, secret := range secrets {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("evidence artifact %s leaks %s's credential", path, id)
			}
		}
		redacted = redacted || bytes.Contains(data, []byte(redactedEvidence))
		return nil
	})
	if err != nil {
		t.Fatalf("walk evidence: %v", err)
	}
	return redacted
}

// leakingRoomLive opens participants that echo their credential in output,
// in a terminal error event, and in their session failure.
type leakingRoomLive struct{ secrets map[string]string }

func (l *leakingRoomLive) OpenLive(_ context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	return &leakingRoomHandle{id: request.ParticipantID, secret: l.secrets[request.ParticipantID], events: make(chan session.LiveEvent, 4), done: make(chan struct{})}, nil
}

type leakingRoomHandle struct {
	id, secret string
	events     chan session.LiveEvent
	done       chan struct{}
	finish     sync.Once
	close      sync.Once
}

func (h *leakingRoomHandle) Media() audio.MediaEndpoints      { return audio.MediaEndpoints{} }
func (h *leakingRoomHandle) Events() <-chan session.LiveEvent { return h.events }
func (h *leakingRoomHandle) Start(context.Context) error {
	h.events <- session.LiveEvent{Kind: string(session.LiveEventText), SessionID: h.id, ParticipantID: h.id, Text: "my key is " + h.secret}
	h.events <- session.LiveEvent{Kind: string(session.LiveEventError), SessionID: h.id, ParticipantID: h.id, Error: fmt.Errorf("provider rejected key %s", h.secret), Critical: true}
	h.finish.Do(func() { close(h.done) })
	return nil
}
func (*leakingRoomHandle) Send(context.Context, session.LiveControl) error { return nil }
func (h *leakingRoomHandle) Cancel(error)                                  { h.finish.Do(func() { close(h.done) }) }
func (h *leakingRoomHandle) Wait() error {
	<-h.done
	return fmt.Errorf("authentication failed for %s", h.secret)
}
func (h *leakingRoomHandle) Close() error {
	h.close.Do(func() { close(h.events) })
	return nil
}
