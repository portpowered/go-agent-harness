package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
)

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestRunAskPropagatesOutputWriteError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	exec := resolvedExecutorForTest(t, stubInferencer{}, nil, nil, nil)
	cfg := &Config{NoSystemInformation: true}

	_, err := exec.RunAsk(context.Background(), cfg, agentloop.NewExecuteInput("hello"), failingWriter{err: errors.New("stdout closed")})
	if err == nil {
		t.Fatal("expected output write error, got nil")
	}
	if !strings.Contains(err.Error(), "write output") {
		t.Fatalf("error = %v, want write output context", err)
	}
}

func TestRunIterativeLoopPropagatesTraceBannerWriteError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	exec := resolvedExecutorForTest(t, stubInferencer{}, nil, nil, nil)
	cfg := &Config{}

	_, err := exec.RunIterativeLoop(
		context.Background(),
		cfg,
		IterativeLoopConfig{MaxIterations: 1},
		agentloop.NewExecuteInput("hello"),
		failingWriter{err: errors.New("stdout closed")},
	)
	if err == nil {
		t.Fatal("expected trace banner write error, got nil")
	}
	if !strings.Contains(err.Error(), "write trace ID") {
		t.Fatalf("error = %v, want write trace ID context", err)
	}
}
