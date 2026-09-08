package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestFileStoreFactoryIsInertAndPreservesMedia(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "not-created")
	store, err := NewFactory().Open(public.FileStoreOptions{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("opening store touched directory: %v", err)
	}
	want := []public.Message{{Role: messages.RoleUser, ContentParts: []messages.ContentPart{
		messages.AudioPart{MediaType: "audio/pcm", Bytes: []byte{1, 0, 2, 0, 3, 0}},
	}, ToolCalls: []messages.ToolCall{}}}
	if err := store.Save(t.Context(), "audio", want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), "audio")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("media round trip: got %+v, err %v", got, err)
	}
	infos, err := store.List(t.Context(), public.SessionListOptions{Filter: "AUDIO", Limit: 1})
	if err != nil || len(infos) != 1 || infos[0].ID != "audio" {
		t.Fatalf("metadata: %+v, %v", infos, err)
	}
	if err := store.Delete(t.Context(), "audio"); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load(t.Context(), "audio")
	if err != nil || got != nil {
		t.Fatalf("deleted history: %+v, %v", got, err)
	}
}

func TestFileStoreAdapterPreservesTraceLineage(t *testing.T) {
	store, err := NewFactory().Open(public.FileStoreOptions{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := public.TraceRecord{
		TraceID: "trace", Status: public.TraceStatusInterrupted,
		Config:           public.TraceConfig{MaxIterations: 5, StopWord: "done", Prompt: "work"},
		CurrentIteration: 2,
		Iterations:       []public.IterationTrace{{Iteration: 2, SessionID: "session", SubAgentSessionIDs: []string{"child"}, Status: public.IterationStatusInterrupted}},
	}
	if err := store.SaveTrace(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadTrace(t.Context(), want.TraceID)
	if err != nil || !reflect.DeepEqual(got, &want) {
		t.Fatalf("trace round trip: %+v, %v", got, err)
	}
	infos, err := store.ListTraces(t.Context())
	if err != nil || len(infos) != 1 || infos[0].ID != want.TraceID {
		t.Fatalf("trace listing: %+v, %v", infos, err)
	}
}

func TestFileStoreRejectsCanceledOperationsBeforeIO(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "uncreated")
	store, err := NewFactory().Open(public.FileStoreOptions{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	operations := map[string]func() error{
		"load":       func() error { _, err := store.Load(ctx, "id"); return err },
		"save":       func() error { return store.Save(ctx, "id", nil) },
		"latest":     func() error { _, err := store.Latest(ctx); return err },
		"session-id": func() error { _, err := store.NewSessionID(ctx); return err },
		"trace-id":   func() error { _, err := store.NewTraceID(ctx); return err },
		"delete":     func() error { return store.Delete(ctx, "id") },
		"list":       func() error { _, err := store.List(ctx, public.SessionListOptions{}); return err },
		"traces":     func() error { _, err := store.ListTraces(ctx); return err },
		"load-trace": func() error { _, err := store.LoadTrace(ctx, "id"); return err },
		"save-trace": func() error { return store.SaveTrace(ctx, public.TraceRecord{}) },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}

	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("canceled operations created files: %v", err)
	}
	latest, err := store.Latest(t.Context())
	if err != nil || latest != "" {
		t.Fatalf("canceled save became latest session %q: %v", latest, err)
	}
}
