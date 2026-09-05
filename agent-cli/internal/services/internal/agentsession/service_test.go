package agentsession

import (
	"context"
	"io"
	"testing"

	runtimecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"
	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestServiceRejectsCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(Dependencies{}).Run(ctx, discardWriter{}, public.Request{}); err == nil {
		t.Fatal("canceled request succeeded")
	}
}

func TestServiceUsesInjectedRuntime(t *testing.T) {
	runtime := &recordingRuntime{}
	service := New(Dependencies{Clock: clock.Real{}, Runtime: runtime})
	request := public.Request{Prompt: "hello"}
	if err := service.Run(context.Background(), discardWriter{}, request); err != nil {
		t.Fatal(err)
	}
	if !runtime.called || runtime.request.Prompt != request.Prompt {
		t.Fatalf("runtime did not receive request: called=%v request=%+v", runtime.called, runtime.request)
	}
}

type recordingRuntime struct {
	called  bool
	request public.Request
}

var _ runtimecontract.Runtime = (*recordingRuntime)(nil)

func (r *recordingRuntime) Run(_ context.Context, _ io.Writer, request public.Request) error {
	r.called = true
	r.request = request
	return nil
}

type discardWriter struct{}

func (discardWriter) Write([]byte) (int, error) { return 0, nil }
