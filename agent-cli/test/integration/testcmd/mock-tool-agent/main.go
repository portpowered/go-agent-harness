package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const fixtureEnvironment = "YUI_E2E_TOOL_MOCK_FIXTURE"

type fixture struct {
	Observations string             `json:"observations"`
	Calls        []expectedToolCall `json:"calls"`
}

type expectedToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Output    string `json:"output"`
	DelayMS   int64  `json:"delay_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type fixtureExecutor struct {
	mu      sync.Mutex
	fixture fixture
	called  int
}

func main() {
	executor, err := loadFixtureExecutor(os.Getenv(fixtureEnvironment))
	if err != nil {
		fatal(err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortToolExecutor, messages.ToolExecutor(executor)),
	)
	if err != nil {
		fatal(err)
	}
	if err := agentCLI.Generate().Execute(); err != nil {
		os.Exit(1)
	}
	if err := executor.verify(); err != nil {
		fatal(err)
	}
}

func loadFixtureExecutor(path string) (*fixtureExecutor, error) {
	if path == "" {
		return nil, fmt.Errorf("%s is required", fixtureEnvironment)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tool mock fixture: %w", err)
	}
	var configured fixture
	if err := json.Unmarshal(data, &configured); err != nil {
		return nil, fmt.Errorf("decode tool mock fixture: %w", err)
	}
	if configured.Observations == "" || len(configured.Calls) == 0 {
		return nil, errors.New("tool mock fixture requires observations and at least one call")
	}
	return &fixtureExecutor{fixture: configured}, nil
}

func (e *fixtureExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	if e.called >= len(e.fixture.Calls) {
		e.mu.Unlock()
		return messages.ToolCallResponse{}, fmt.Errorf("unexpected tool call %q after %d expectations", call.Name, e.called)
	}
	want := e.fixture.Calls[e.called]
	if call.Name != want.Name || call.Arguments != want.Arguments {
		e.mu.Unlock()
		return messages.ToolCallResponse{}, fmt.Errorf("tool call %d = %q %s, want %q %s", e.called, call.Name, call.Arguments, want.Name, want.Arguments)
	}
	e.called++
	e.mu.Unlock()
	if want.DelayMS > 0 {
		timer := time.NewTimer(time.Duration(want.DelayMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return messages.ToolCallResponse{}, ctx.Err()
		}
	}
	observation, err := json.Marshal(map[string]string{
		"id": call.ID, "name": call.Name, "arguments": call.Arguments,
	})
	if err != nil {
		return messages.ToolCallResponse{}, err
	}
	file, err := os.OpenFile(e.fixture.Observations, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return messages.ToolCallResponse{}, fmt.Errorf("open tool mock observation: %w", err)
	}
	_, writeErr := file.Write(append(observation, '\n'))
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return messages.ToolCallResponse{}, fmt.Errorf("write tool mock observation: %w", err)
	}
	if want.Error != "" {
		return messages.ToolCallResponse{}, errors.New(want.Error)
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: want.Output}, nil
}

func (e *fixtureExecutor) verify() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.called != len(e.fixture.Calls) {
		return fmt.Errorf("observed %d tool calls, want %d", e.called, len(e.fixture.Calls))
	}
	return nil
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "mock-tool-agent:", err)
	os.Exit(1)
}
