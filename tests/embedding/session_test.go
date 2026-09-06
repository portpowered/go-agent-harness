package embedding_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

type hostInferencer struct {
	response string
	calls    atomic.Int32
	tools    atomic.Int32
	entered  chan<- string
	release  <-chan struct{}
	requests chan<- messages.InferenceRequest
}

func (model *hostInferencer) Infer(ctx context.Context, request messages.InferenceRequest) (messages.InferenceResult, error) {
	model.calls.Add(1)
	model.tools.Store(int32(len(request.Tools)))
	if model.requests != nil {
		select {
		case model.requests <- request:
		case <-ctx.Done():
			return messages.InferenceResult{}, ctx.Err()
		}
	}
	if model.entered != nil {
		select {
		case model.entered <- model.response:
		case <-ctx.Done():
			return messages.InferenceResult{}, ctx.Err()
		}
		select {
		case <-model.release:
		case <-ctx.Done():
			return messages.InferenceResult{}, ctx.Err()
		}
	}
	return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, model.response)}, nil
}

func (*hostInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, errors.New("nonstreaming consumer unexpectedly requested model streaming")
}

func newHost(model *hostInferencer) session.Service {
	return newHostWithStore(model, &memoryStore{})
}

func newHostWithStore(model *hostInferencer, store *memoryStore) session.Service {
	resolver := session.ResolverFunc(func(ctx context.Context, request session.Request) (session.Resolution, error) {
		if err := ctx.Err(); err != nil {
			return session.Resolution{}, err
		}
		return session.Resolution{Store: store, SystemPrompt: request.SystemPrompt}, nil
	})
	return sessionwire.NewService(sessionwire.Dependencies{
		Inferencer: model, RelaxValidation: true, Resolver: resolver, Store: store,
	})
}

func TestIdentifierFailurePreservesCauseAndDoesNotInfer(t *testing.T) {
	cause := errors.New("identifier source unavailable")
	model := &hostInferencer{response: "must not run"}
	host := newHostWithStore(model, &memoryStore{idError: cause})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := host.Run(ctx, session.Request{Input: agentloop.ExecuteInput{Message: "hello"}})
	if !errors.Is(err, cause) || model.calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d; want identifier failure before inference", err, model.calls.Load())
	}
}

func TestExternalHostRunsWithoutCLIConfiguration(t *testing.T) {
	model := &hostInferencer{response: "embedded response"}
	host := newHost(model)
	if model.calls.Load() != 0 {
		t.Fatal("constructing a service performed inference")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := host.Run(ctx, session.Request{Input: agentloop.ExecuteInput{Message: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != model.response || model.calls.Load() != 1 {
		t.Fatalf("result=%+v calls=%d", result, model.calls.Load())
	}
	if len(result.Messages) == 0 || result.Messages[len(result.Messages)-1].TextContent() != model.response {
		t.Fatalf("structured terminal messages missing: %+v", result.Messages)
	}
	if model.tools.Load() != 0 {
		t.Fatalf("host supplied no tools, but inference received %d", model.tools.Load())
	}
}

func TestCanceledInvocationDoesNotStartInference(t *testing.T) {
	model := &hostInferencer{response: "must not run"}
	host := newHost(model)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := host.Run(ctx, session.Request{Input: agentloop.ExecuteInput{Message: "hello"}})
	if !errors.Is(err, context.Canceled) || model.calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, model.calls.Load())
	}
}

type hostResult struct {
	want   string
	result session.Result
	err    error
}

func TestIndependentHostsCanRunConcurrently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		joined := make(chan struct{})
		go func() { workers.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("host invocations did not join after cancellation")
		}
	})
	entered := make(chan string, 2)
	release := make(chan struct{})
	results := make(chan hostResult, 2)
	for _, response := range []string{"first host", "second host"} {
		model := &hostInferencer{response: response, entered: entered, release: release}
		host := newHost(model)
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := host.Run(ctx, session.Request{Input: agentloop.ExecuteInput{Message: response}})
			results <- hostResult{want: response, result: result, err: err}
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case result := <-results:
			t.Fatalf("host returned before both models entered: %+v", result)
		case <-ctx.Done():
			t.Fatal("hosts could not enter inference independently")
		}
	}
	close(release)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil || result.result.Text != result.want {
				t.Fatalf("host result=%+v", result)
			}
		case <-ctx.Done():
			t.Fatal("hosts did not finish after inference was released")
		}
	}
}
