package agent

import (
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// RunData holds the runtime data for an agent execution.
type RunData struct {
	SessionID      string
	SessionManager *session.Storage
	Loop           *agentloop.AgentLoop
	Recorder       *testing.RecordRoundTripper
	Models         *config.ModelsConfig
	LoggerCloser   io.Closer // closed when the run finishes so the log file handle is released
	ConfigDir      string    // config directory for skills and context
}

// CloseLogger releases the log file handle if the run used file logging. Safe to call if LoggerCloser is nil.
func (r *RunData) CloseLogger() {
	if r != nil && r.LoggerCloser != nil {
		_ = r.LoggerCloser.Close()
		r.LoggerCloser = nil
	}
}

// Executor constructs and executes agent loops based on configuration.
type Executor struct {
	executor             messages.ToolExecutor
	toolDefs             []messages.ToolDefinition
	inferencerOverride   messages.Inferencer
	relaxModelValidation bool
}

// NewExecutor creates a new Executor with the given dependencies.
func NewExecutor(executor messages.ToolExecutor, toolDefs []messages.ToolDefinition, inferencerOverride messages.Inferencer, relaxModelValidation ...bool) *Executor {
	relax := false
	if len(relaxModelValidation) > 0 {
		relax = relaxModelValidation[0]
	}
	return &Executor{
		executor:             executor,
		toolDefs:             toolDefs,
		inferencerOverride:   inferencerOverride,
		relaxModelValidation: relax,
	}
}
