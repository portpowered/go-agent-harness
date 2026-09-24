package wire

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestNewServiceAllocatesSessionIDWithoutStartingProvider(t *testing.T) {
	service := NewService(Dependencies{})
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	id, err := service.NewSessionID(context.Background(), session.Request{})
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if id == "" {
		t.Fatal("NewSessionID returned an empty identifier")
	}
}

func TestInstructionServiceResolvesLiteralPrompt(t *testing.T) {
	const prompt = "Answer from the supplied context."
	result, err := NewInstructionService().Resolve(context.Background(), session.InstructionRequest{Prompt: prompt})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Instructions != prompt {
		t.Fatalf("instructions = %q, want %q", result.Instructions, prompt)
	}
}

func TestFileStoreFactoryOpensManagedStore(t *testing.T) {
	store, err := NewFileStoreFactory().Open(session.FileStoreOptions{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if store == nil {
		t.Fatal("Open returned nil managed store")
	}
	traces, err := store.ListTraces(context.Background())
	if err != nil {
		t.Fatalf("ListTraces: %v", err)
	}
	if len(traces) != 0 {
		t.Fatalf("initial traces = %v, want empty store", traces)
	}
}

func TestDuplexLoopFactoryRejectsMissingInferencer(t *testing.T) {
	_, err := NewDuplexLoopFactory().Build(context.Background(), nil, sessionduration.DuplexLoopOptions{})
	if err == nil || !strings.Contains(err.Error(), "inferencer is required") {
		t.Fatalf("Build error = %v, want missing-inferencer failure", err)
	}
}

func TestLiveServiceRejectsMissingInferencerFactory(t *testing.T) {
	_, err := NewLiveService(LiveDependencies{}).OpenLive(context.Background(), session.LiveRequest{})
	if err == nil || !strings.Contains(err.Error(), "live inferencer factory is required") {
		t.Fatalf("OpenLive error = %v, want missing-factory failure", err)
	}
}
