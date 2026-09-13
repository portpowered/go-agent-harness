package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type fakeInferencer struct{}

func (fakeInferencer) ConnectSession(context.Context) (messages.Session, error) { return nil, nil }

func testScope(t *testing.T) roomplanning.FilesystemScope {
	t.Helper()
	return roomplanning.FilesystemScope{PrimaryRoot: t.TempDir()}
}

func testManifest() rooms.Manifest {
	return rooms.Manifest{Participants: []rooms.Participant{
		{ID: "alpha", Kind: rooms.ParticipantKindAgent, Provider: "openai", Model: "model", APIKeyEnv: "ALPHA_KEY"},
		{ID: "beta", Kind: rooms.ParticipantKindAgent, Provider: "openai", Model: "model", APIKeyEnv: "BETA_KEY"},
	}}
}

func TestPlanKeepsCredentialOutOfReturnedPlansAndOrdersParticipants(t *testing.T) {
	secret := "room-secret-do-not-leak"
	var seen []string
	result, err := New().Plan(context.Background(), roomplanning.Options{
		Manifest: testManifest(), Filesystem: ptr(testScope(t)),
		LookupCredential: func(name string) (string, bool) {
			if name == "ALPHA_KEY" {
				return secret, true
			}
			return "", false
		},
		SessionFactory: func(request roomplanning.LiveSessionRequest) (messages.SessionInferencer, error) {
			seen = append(seen, request.Participant.ID+":"+request.Credential)
			return fakeInferencer{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := []string{seen[0], seen[1]}; got[0] != "alpha:"+secret || got[1] != "beta:" {
		t.Fatalf("factory credentials = %q", got)
	}
	serialized := fmt.Sprintf("%#v", result)
	if strings.Contains(serialized, secret) {
		t.Fatalf("returned plan leaked credential: %s", serialized)
	}
	if len(result.Plans) != 2 || result.Plans[0].Participant.ID != "alpha" || result.Plans[1].Participant.ID != "beta" {
		t.Fatalf("plans = %#v, want manifest order", result.Plans)
	}
}

func TestPlanReplayNeverConsultsLiveSeams(t *testing.T) {
	var replayCalls atomic.Int32
	replay := rooms.RoomReplayPlan{Participants: []rooms.RoomReplayParticipant{
		{ID: "alpha", Kind: rooms.ParticipantKindAgent, Provider: "openai", Model: "model", CapturePath: "/tmp/alpha.capture"},
		{ID: "beta", Kind: rooms.ParticipantKindHuman},
	}}
	result, err := New().Plan(context.Background(), roomplanning.Options{
		Manifest: testManifest(), ReplayPlan: &replay, Filesystem: ptr(testScope(t)),
		LookupCredential: func(string) (string, bool) { t.Fatal("replay consulted credentials"); return "", false },
		SessionFactory: func(roomplanning.LiveSessionRequest) (messages.SessionInferencer, error) {
			t.Fatal("replay called live factory")
			return nil, nil
		},
		ToolFactory: func(rooms.Participant) (roomplanning.ToolCapabilities, error) {
			t.Fatal("replay called tool factory")
			return roomplanning.ToolCapabilities{}, nil
		},
		BrowserFactory: func(rooms.Participant, roomplanning.ToolCapabilities) (roomplanning.BrowserCapabilities, error) {
			t.Fatal("replay called browser factory")
			return roomplanning.BrowserCapabilities{}, nil
		},
		ReplayPlanner: func(_ context.Context, request roomplanning.ReplayRequest) (roomplanning.ReplaySession, error) {
			replayCalls.Add(1)
			return roomplanning.ReplaySession{Inferencer: fakeInferencer{}, Done: make(chan struct{}), DoneErr: func() error { return nil }}, nil
		},
	})
	if err != nil {
		t.Fatalf("Plan replay: %v", err)
	}
	if replayCalls.Load() != 1 || len(result.Plans) != 2 || !result.Plans[0].Replay || !result.Plans[1].Replay {
		t.Fatalf("replay calls/plans = %d/%#v", replayCalls.Load(), result.Plans)
	}
	if result.Plans[0].Options.ReplayPath != "/tmp/alpha.capture" || result.Plans[0].Options.RoomReplay != true {
		t.Fatalf("replay options = %#v", result.Plans[0].Options)
	}
}

func TestPlanRejectsToolSurfaceBeforeSessionConstruction(t *testing.T) {
	var sessions atomic.Int32
	_, err := New().Plan(context.Background(), roomplanning.Options{
		Manifest: rooms.Manifest{Participants: []rooms.Participant{
			{ID: "alpha", Kind: rooms.ParticipantKindAgent, Tools: []string{"wanted"}},
		}},
		Filesystem: ptr(testScope(t)),
		ToolFactory: func(rooms.Participant) (roomplanning.ToolCapabilities, error) {
			return roomplanning.ToolCapabilities{Executor: &messages.DefaultToolExecutor{}, Definitions: []messages.ToolDefinition{{Name: "other"}}}, nil
		},
		SessionFactory: func(roomplanning.LiveSessionRequest) (messages.SessionInferencer, error) {
			sessions.Add(1)
			return fakeInferencer{}, nil
		},
	})
	if !errors.Is(err, roomplanning.ErrParticipantToolMatch) {
		t.Fatalf("error = %v, want tool mismatch", err)
	}
	if sessions.Load() != 0 {
		t.Fatalf("session factory calls = %d, want zero", sessions.Load())
	}
}

func ptr[T any](value T) *T { return &value }

type testCoordinator struct {
	done     chan struct{}
	progress chan struct{}
	active   bool
	stopping bool
	roomErr  error
}

func (c *testCoordinator) Done() <-chan struct{}               { return c.done }
func (c *testCoordinator) Progress() <-chan struct{}           { return c.progress }
func (c *testCoordinator) IsActive(string) bool                { return c.active }
func (c *testCoordinator) IsStopping() bool                    { return c.stopping }
func (c *testCoordinator) Stop(roomplanning.TerminationReason) { c.stopping = true }
func (c *testCoordinator) FailParticipant(string, error)       { c.active = false }
func (c *testCoordinator) Fail(err error)                      { c.roomErr = err; c.stopping = true }
func (c *testCoordinator) RoomError() error                    { return c.roomErr }

type testCleanup struct{ done chan time.Time }

func (c *testCleanup) Start()                 {}
func (c *testCleanup) Done() <-chan time.Time { return c.done }

func TestAwaitWaitsForReadinessAfterConnection(t *testing.T) {
	coordinator := &testCoordinator{done: make(chan struct{}), progress: make(chan struct{}, 1), active: true}
	cleanup := &testCleanup{done: make(chan time.Time)}
	outcomes := make(chan roomplanning.ConnectionOutcome, 1)
	tracker := &struct{}{}
	var opened atomic.Bool
	connected := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- New().Await(ctx, roomplanning.AwaitOptions{
			Participants: []roomplanning.AdmissionParticipant{{ID: "alpha", Kind: rooms.ParticipantKindAgent, Tracker: tracker, MarkConnected: func(error) { close(connected) }, Snapshot: func() roomplanning.LifecycleSnapshot { return roomplanning.LifecycleSnapshot{Opened: opened.Load()} }}},
			Outcomes:     outcomes, Coordinator: coordinator, Cleanup: cleanup, Timer: make(chan time.Time), TimerFactory: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
		})
	}()
	outcomes <- roomplanning.ConnectionOutcome{Tracker: tracker}
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("connection outcome was not observed")
	}
	select {
	case err := <-finished:
		t.Fatalf("admission returned before readiness: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	opened.Store(true)
	coordinator.progress <- struct{}{}
	if err := <-finished; err != nil {
		t.Fatalf("Await: %v", err)
	}
}

func TestAwaitIgnoresTypedNilTracker(t *testing.T) {
	var tracker *struct{}
	cleanupDone := make(chan time.Time)
	close(cleanupDone)
	coordinator := &testCoordinator{done: make(chan struct{}), progress: make(chan struct{}, 1), active: true}
	err := New().Await(context.Background(), roomplanning.AwaitOptions{
		Participants: []roomplanning.AdmissionParticipant{{
			ID: "human", Kind: rooms.ParticipantKindHuman, Tracker: tracker,
			Snapshot: func() roomplanning.LifecycleSnapshot { return roomplanning.LifecycleSnapshot{DeviceReady: true} },
		}},
		Outcomes:    make(chan roomplanning.ConnectionOutcome),
		Coordinator: coordinator,
		Cleanup:     &testCleanup{done: cleanupDone},
		Timer:       make(chan time.Time),
	})
	if err != nil {
		t.Fatalf("Await typed-nil tracker: %v", err)
	}
}

func TestAwaitReportsUntrackedParticipantWorkDuringCleanup(t *testing.T) {
	cleanupDone := make(chan time.Time)
	close(cleanupDone)
	coordinator := &testCoordinator{done: make(chan struct{}), progress: make(chan struct{}, 1), active: true}
	tracker := &struct{}{}
	err := New().Await(context.Background(), roomplanning.AwaitOptions{
		Participants: []roomplanning.AdmissionParticipant{
			{ID: "agent", Kind: rooms.ParticipantKindAgent, Tracker: tracker},
			{ID: "human", Kind: rooms.ParticipantKindHuman, OutstandingWork: func() []string { return []string{"participant \"human\" phase devices"} }},
		},
		Outcomes:    make(chan roomplanning.ConnectionOutcome),
		Coordinator: coordinator,
		Cleanup:     &testCleanup{done: cleanupDone},
		Timer:       make(chan time.Time),
	})
	if err == nil || !strings.Contains(err.Error(), `participant "human" phase devices`) || !strings.Contains(err.Error(), `participant "agent" phase connect`) {
		t.Fatalf("Await cleanup error = %v, want tracked and untracked work", err)
	}
}
