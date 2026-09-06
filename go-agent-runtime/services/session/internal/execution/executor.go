package agent

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	looplogging "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// Storage is the private persistence port used by session execution. The
// concrete file-backed implementation is one possible adapter; embedders can
// supply their own store through the public session resolver.
type Storage interface {
	Load(string) ([]messages.Message, error)
	Latest() (string, error)
	NewSessionID() string
	Save(string, []messages.Message) error
	WorkspaceDir() string
	LoadTrace(string) (*session.TraceRecord, error)
	SaveTrace(session.TraceRecord) error
	NewTraceID() string
}

func newSessionID(storage Storage) (string, error) {
	if storage == nil {
		return "", fmt.Errorf("session storage is required")
	}
	if aware, ok := storage.(interface{ NewSessionIDWithError() (string, error) }); ok {
		id, err := aware.NewSessionIDWithError()
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", fmt.Errorf("session storage returned an empty session ID")
		}
		return id, nil
	}
	id := storage.NewSessionID()
	if id == "" {
		return "", fmt.Errorf("session storage returned an empty session ID")
	}
	return id, nil
}

func newTraceID(storage Storage) (string, error) {
	if storage == nil {
		return "", fmt.Errorf("session storage is required")
	}
	if aware, ok := storage.(interface{ NewTraceIDWithError() (string, error) }); ok {
		id, err := aware.NewTraceIDWithError()
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", fmt.Errorf("session storage returned an empty trace ID")
		}
		return id, nil
	}
	id := storage.NewTraceID()
	if id == "" {
		return "", fmt.Errorf("session storage returned an empty trace ID")
	}
	return id, nil
}

// RuntimeResolution contains values resolved by the service host before an
// invocation starts. It is private to the session service composition package;
// no implementation type crosses the public runtime boundary.
type RuntimeResolution struct {
	// Resolved marks the invocation as having crossed the host admission
	// boundary. A resolved invocation must consume only the value contracts
	// below; in particular it must never fall back to config-file discovery.
	Resolved        bool
	Provider        ProviderConfig
	ModelCatalog    ModelCatalog
	ModelPolicy     ModelPolicy
	ProviderService providers.Service
	Storage         Storage
	WorkspaceDir    string
	AllowPaths      []string
	SkillRoots      []tools.SkillRoot
	Logger          looplogging.Logger
	// PromptResolved distinguishes an explicitly resolved empty prompt from
	// the legacy request shape that asks the CLI runtime to inspect AGENTS.md.
	PromptResolved bool
}

// RunData holds the runtime data for an agent execution.
type RunData struct {
	SessionID string
	Loop      *agentloop.AgentLoop
	Capture   providers.CaptureWriter

	// sessionManager and models are implementation details of the session
	// service. Keeping them private prevents the embeddable package from
	// exposing host config/storage implementations through a returned handle.
	sessionManager   Storage
	modelCatalog     ModelCatalog
	producedMessages []messages.Message
}

// WorkspaceDir returns the workspace selected for this invocation. The
// storage implementation remains owned by the runtime and is not exposed to
// callers.
func (r *RunData) WorkspaceDir() string {
	if r == nil || r.sessionManager == nil {
		return ""
	}
	return r.sessionManager.WorkspaceDir()
}

// Executor constructs and executes agent loops based on configuration.
type Executor struct {
	toolService             tools.Service
	executor                messages.ToolExecutor
	toolDefs                []messages.ToolDefinition
	inferencerOverride      messages.Inferencer
	relaxModelValidation    bool
	resolved                bool
	resolvedProvider        ProviderConfig
	resolvedCatalog         ModelCatalog
	resolvedModelPolicy     ModelPolicy
	resolvedProviderService providers.Service
	resolvedStorage         Storage
	resolvedWorkspace       string
	resolvedAllowPaths      []string
	resolvedSkillRoots      []tools.SkillRoot
	resolvedPrompt          bool
	logger                  looplogging.Logger
}

// NewExecutor creates a new Executor with the given dependencies.
func NewExecutor(executor messages.ToolExecutor, toolDefs []messages.ToolDefinition, inferencerOverride messages.Inferencer, relaxModelValidation ...bool) *Executor {
	return newExecutor(nil, executor, toolDefs, inferencerOverride, nil, relaxModelValidation...)
}

// NewExecutorWithToolService constructs an executor whose default tool
// surface is resolved by the reusable tools service. The legacy constructor
// remains available for tests and hosts that inject a complete executor.
func NewExecutorWithToolService(toolService tools.Service, executor messages.ToolExecutor, toolDefs []messages.ToolDefinition, inferencerOverride messages.Inferencer, relaxModelValidation ...bool) *Executor {
	return newExecutor(toolService, executor, toolDefs, inferencerOverride, nil, relaxModelValidation...)
}

// NewExecutorWithToolServiceAndLogger constructs an executor with the host's
// loop logger. Logger ownership stays at the composition edge; a nil logger
// is handled by the loop's no-op default.
func NewExecutorWithToolServiceAndLogger(toolService tools.Service, executor messages.ToolExecutor, toolDefs []messages.ToolDefinition, inferencerOverride messages.Inferencer, logger looplogging.Logger, relaxModelValidation ...bool) *Executor {
	return newExecutor(toolService, executor, toolDefs, inferencerOverride, logger, relaxModelValidation...)
}

func newExecutor(toolService tools.Service, executor messages.ToolExecutor, toolDefs []messages.ToolDefinition, inferencerOverride messages.Inferencer, logger looplogging.Logger, relaxModelValidation ...bool) *Executor {
	relax := false
	if len(relaxModelValidation) > 0 {
		relax = relaxModelValidation[0]
	}
	return &Executor{
		toolService:          toolService,
		executor:             executor,
		toolDefs:             toolDefs,
		inferencerOverride:   inferencerOverride,
		relaxModelValidation: relax,
		logger:               logger,
	}
}

// WithResolution returns an invocation-scoped executor view. The base
// executor remains immutable so concurrent sessions can resolve different
// providers, stores, and workspace policies safely.
func (e *Executor) WithResolution(resolution RuntimeResolution) *Executor {
	if e == nil {
		return nil
	}
	clone := *e
	clone.resolved = resolution.Resolved
	clone.resolvedProvider = cloneProviderConfig(resolution.Provider)
	clone.resolvedCatalog = cloneModelCatalog(resolution.ModelCatalog)
	clone.resolvedModelPolicy = resolution.ModelPolicy
	clone.resolvedProviderService = resolution.ProviderService
	clone.resolvedStorage = resolution.Storage
	clone.resolvedWorkspace = resolution.WorkspaceDir
	clone.resolvedAllowPaths = append([]string(nil), resolution.AllowPaths...)
	clone.resolvedSkillRoots = append([]tools.SkillRoot(nil), resolution.SkillRoots...)
	clone.resolvedPrompt = resolution.PromptResolved
	if resolution.Logger != nil {
		clone.logger = resolution.Logger
	}
	return &clone
}

func cloneProviderConfig(provider ProviderConfig) ProviderConfig {
	if provider.Fal != nil {
		fal := *provider.Fal
		provider.Fal = &fal
	}
	return provider
}

func cloneModelCatalog(catalog ModelCatalog) ModelCatalog {
	if len(catalog.Models) == 0 {
		return catalog
	}
	clone := ModelCatalog{Models: make([]ModelInfo, len(catalog.Models))}
	for i, model := range catalog.Models {
		clone.Models[i] = ModelInfo{
			Name:                    model.Name,
			Aliases:                 append([]string(nil), model.Aliases...),
			Providers:               append([]string(nil), model.Providers...),
			InputModalities:         append([]string(nil), model.InputModalities...),
			OutputModalities:        append([]string(nil), model.OutputModalities...),
			SupportedInputMimeTypes: append([]string(nil), model.SupportedInputMimeTypes...),
		}
	}
	return clone
}
