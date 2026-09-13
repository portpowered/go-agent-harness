package selfplay

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionFactoryFuncRejectsNilAndDelegates(t *testing.T) {
	t.Parallel()

	if _, err := (SessionFactoryFunc)(nil).NewSession(context.Background(), SessionRequest{}); err == nil {
		t.Fatal("nil session factory should fail closed")
	}

	wantErr := errors.New("factory failure")
	ctx := context.Background()
	request := SessionRequest{Provider: SelfPlayDefaultProvider, Model: SelfPlayDefaultModel, Prompt: SelfPlayOpeningSeed}
	called := false
	factory := SessionFactoryFunc(func(gotCtx context.Context, gotRequest SessionRequest) (messages.SessionInferencer, error) {
		called = true
		if gotCtx != ctx {
			t.Fatalf("context = %p, want %p", gotCtx, ctx)
		}
		if gotRequest != request {
			t.Fatalf("request = %#v, want %#v", gotRequest, request)
		}
		return nil, wantErr
	})

	if _, err := factory.NewSession(ctx, request); !errors.Is(err, wantErr) {
		t.Fatalf("factory error = %v, want %v", err, wantErr)
	}
	if !called {
		t.Fatal("session factory callback was not invoked")
	}
}

func TestSessionRunnerFuncRejectsNilAndDelegates(t *testing.T) {
	t.Parallel()

	if err := (SessionRunnerFunc)(nil).Run(context.Background(), nil, SessionRunOptions{}); err == nil {
		t.Fatal("nil session runner should fail closed")
	}

	wantErr := errors.New("runner failure")
	ctx := context.Background()
	options := SessionRunOptions{Prompt: SelfPlayOpeningSeed, Provider: SelfPlayDefaultProvider, Model: SelfPlayDefaultModel}
	called := false
	runner := SessionRunnerFunc(func(gotCtx context.Context, session messages.SessionInferencer, gotOptions SessionRunOptions) error {
		called = true
		if gotCtx != ctx {
			t.Fatalf("context = %p, want %p", gotCtx, ctx)
		}
		if session != nil {
			t.Fatalf("session = %#v, want nil", session)
		}
		if gotOptions.Prompt != options.Prompt || gotOptions.Provider != options.Provider || gotOptions.Model != options.Model {
			t.Fatalf("options = %#v, want %#v", gotOptions, options)
		}
		return wantErr
	})

	if err := runner.Run(ctx, nil, options); !errors.Is(err, wantErr) {
		t.Fatalf("runner error = %v, want %v", err, wantErr)
	}
	if !called {
		t.Fatal("session runner callback was not invoked")
	}
}

func TestRunFuncRejectsNilAndDelegates(t *testing.T) {
	t.Parallel()

	if err := (RunFunc)(nil).Run(context.Background(), io.Discard, RunOptions{}); err == nil {
		t.Fatal("nil self-play service should fail closed")
	}

	wantErr := errors.New("service failure")
	ctx := context.Background()
	options := RunOptions{Provider: SelfPlayDefaultProvider, Model: SelfPlayDefaultModel}
	called := false
	service := RunFunc(func(gotCtx context.Context, out io.Writer, gotOptions RunOptions) error {
		called = true
		if gotCtx != ctx {
			t.Fatalf("context = %p, want %p", gotCtx, ctx)
		}
		if out != io.Discard {
			t.Fatalf("writer = %T, want io.Discard", out)
		}
		if gotOptions.Provider != options.Provider || gotOptions.Model != options.Model {
			t.Fatalf("options = %#v, want %#v", gotOptions, options)
		}
		return wantErr
	})

	if err := service.Run(ctx, io.Discard, options); !errors.Is(err, wantErr) {
		t.Fatalf("service error = %v, want %v", err, wantErr)
	}
	if !called {
		t.Fatal("service callback was not invoked")
	}
}
