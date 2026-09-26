// Package mocktool is the fixture-controlled tool executor shared by the
// mock-tool-agent binary and in-process integration scenarios, so both run
// the same executor behind the same composition.
package mocktool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	// FixtureEnvironment names the fixture file for the mock-tool-agent binary.
	FixtureEnvironment = "YUI_E2E_TOOL_MOCK_FIXTURE"
	// DisableHoldToneEnvironment set to "1" disables the gap hold tone.
	DisableHoldToneEnvironment = "YUI_E2E_DISABLE_HOLD_TONE"

	observationFileMode os.FileMode = 0o600
)

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

// FixtureExecutor answers the fixture's tool calls in order, appending each
// accepted call to the fixture's observation file.
type FixtureExecutor struct {
	mu      sync.Mutex
	fixture fixture
	called  int
}

// LoadFixtureExecutor reads a fixture file.
func LoadFixtureExecutor(path string) (*FixtureExecutor, error) {
	if path == "" {
		return nil, fmt.Errorf("%s is required", FixtureEnvironment)
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
	return &FixtureExecutor{fixture: configured}, nil
}

// Ports are the composition replacements of a mock-tool agent.
func Ports(executor *FixtureExecutor) []wire.PortSwap {
	return []wire.PortSwap{wire.NewPortSwap(wire.PortToolExecutor, messages.ToolExecutor(executor))}
}

// ConfigureHoldTone disables the independently synthesized gap cue, so
// exact provider-PCM scenarios observe only provider audio.
func ConfigureHoldTone(agentCLI *cli.AgentCLI, disable bool) {
	if !disable {
		return
	}
	config := audio.DefaultHoldToneConfig()
	config.GapThreshold = time.Hour
	agentCLI.SetSessionHoldToneConfig(config)
}

// Execute answers the next expected call.
func (e *FixtureExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
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
	if err := e.observe(call); err != nil {
		return messages.ToolCallResponse{}, err
	}
	if want.Error != "" {
		return messages.ToolCallResponse{}, errors.New(want.Error)
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: want.Output}, nil
}

func (e *FixtureExecutor) observe(call messages.ToolCall) error {
	observation, err := json.Marshal(map[string]string{
		"id": call.ID, "name": call.Name, "arguments": call.Arguments,
	})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(e.fixture.Observations, os.O_CREATE|os.O_WRONLY|os.O_APPEND, observationFileMode)
	if err != nil {
		return fmt.Errorf("open tool mock observation: %w", err)
	}
	_, writeErr := file.Write(append(observation, '\n'))
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write tool mock observation: %w", err)
	}
	return nil
}

// Verify requires every expected call to have been made.
func (e *FixtureExecutor) Verify() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.called != len(e.fixture.Calls) {
		return fmt.Errorf("observed %d tool calls, want %d", e.called, len(e.fixture.Calls))
	}
	return nil
}
