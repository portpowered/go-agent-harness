package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// DiscoveryService is the narrow neutral seam required by the three Lane B
// tools. *discovery.Service implements it, while a deterministic fake can
// implement it without opening a browser or importing a protocol package.
type DiscoveryService interface {
	DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error)
	ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error)
	Select(context.Context, discovery.TargetSelectionRequest) (discovery.Selection, error)
	Selected() (discovery.Selection, bool)
	RefreshSelection(context.Context) (discovery.Selection, error)
}

// BrowserLookup is an optional read-only extension implemented by
// discovery.Service. It lets get_context include the browser product even
// when the selection was made through another neutral composition layer.
type BrowserLookup interface {
	Browser(string) (discovery.BrowserCandidate, bool)
}

// ToolSetOptions controls composition details that are not part of the
// model-facing schemas. PendingCount and PolicySummary are injected because
// discovery/selection intentionally does not own invocation or policy state.
type ToolSetOptions struct {
	// Enabled explicitly controls whether execution is admitted. When nil,
	// execution is enabled if a discovery service was supplied.
	Enabled *bool
	// Disabled is a convenient explicit off switch for composition tests.
	Disabled bool
	// PolicySummary is copied before it is returned in a context result. It
	// must contain safe, non-secret policy facts only.
	PolicySummary map[string]any
	// PendingCount supplies the current number of queued/in-flight browser
	// operations. A nil function reports zero.
	PendingCount func() int
}

// Options composes the stable tools with the neutral discovery service.
type Options struct {
	Service   DiscoveryService
	Discovery DiscoveryService
	Inputs    discovery.ConnectionInputs

	Enabled        *bool
	Disabled       bool
	PolicySummary  map[string]any
	PendingCount   func() int
	ToolSetOptions ToolSetOptions
}

// LaneBToolSet contains the three stable Lane B tools and their correlated
// textual executor. Dynamic page tools are deliberately not projected here.
type LaneBToolSet struct {
	mu          sync.Mutex
	service     DiscoveryService
	inputs      discovery.ConnectionInputs
	enabled     bool
	definitions []ToolDefinition
	tools       []cliTools.Tool
	executor    *LaneBExecutor
	browsers    map[string]discovery.BrowserCandidate
	policy      map[string]any
	pending     func() int
}

// New constructs a tool set from an options object. A supplied service makes
// the set enabled by default; callers may explicitly disable execution while
// retaining definitions for a preflight or disabled-mode test.
func New(options Options) *LaneBToolSet {
	service := options.Service
	if service == nil {
		service = options.Discovery
	}
	setOptions := options.ToolSetOptions
	if options.Enabled != nil {
		setOptions.Enabled = options.Enabled
	}
	if options.Disabled {
		setOptions.Disabled = true
	}
	if options.PolicySummary != nil {
		setOptions.PolicySummary = options.PolicySummary
	}
	if options.PendingCount != nil {
		setOptions.PendingCount = options.PendingCount
	}
	enabled := service != nil
	if setOptions.Enabled != nil {
		enabled = *setOptions.Enabled
	}
	if setOptions.Disabled {
		enabled = false
	}
	policy := laneBCloneMap(setOptions.PolicySummary)
	if policy == nil {
		policy = map[string]any{
			"origin_policy":      "configured",
			"remote_cdp_allowed": options.Inputs.AllowRemoteCDP,
			"selection":          "exact",
		}
	}
	set := &LaneBToolSet{
		service:     service,
		inputs:      options.Inputs,
		enabled:     enabled,
		definitions: StableToolDefinitions(),
		browsers:    make(map[string]discovery.BrowserCandidate),
		policy:      policy,
		pending:     setOptions.PendingCount,
	}
	set.tools = make([]cliTools.Tool, 0, len(set.definitions))
	for _, definition := range set.definitions {
		set.tools = append(set.tools, &laneBTool{set: set, definition: definition})
	}
	set.executor = &LaneBExecutor{set: set}
	return set
}

// NewWithService is the fake-friendly constructor variant accepting the
// narrow interface rather than requiring a concrete discovery.Service.
func NewWithService(service DiscoveryService, inputs discovery.ConnectionInputs, options ...ToolSetOptions) *LaneBToolSet {
	setOptions := firstToolSetOptions(options)
	return New(Options{Service: service, Inputs: inputs, ToolSetOptions: setOptions})
}

// NewLaneBToolSet constructs a set backed by the neutral Lane B discovery seam.
func NewLaneBToolSet(service DiscoveryService, inputs discovery.ConnectionInputs, options ...ToolSetOptions) *LaneBToolSet {
	return NewWithService(service, inputs, options...)
}

// NewLaneBBrokerToolSet is a descriptive constructor alias.
func NewLaneBBrokerToolSet(service DiscoveryService, inputs discovery.ConnectionInputs, options ...ToolSetOptions) *LaneBToolSet {
	return NewLaneBToolSet(service, inputs, options...)
}

// NewLaneBExecutor constructs the correlated messages.ToolExecutor directly.
func NewLaneBExecutor(service DiscoveryService, inputs discovery.ConnectionInputs, options ...ToolSetOptions) *LaneBExecutor {
	return NewLaneBToolSet(service, inputs, options...).Executor()
}

// Tools returns the three CLI-compatible tools in frozen order.
func (s *LaneBToolSet) Tools() []cliTools.Tool {
	if s == nil {
		return nil
	}
	return append([]cliTools.Tool(nil), s.tools...)
}

// Definitions returns the flattened go-agent-loop representation.
func (s *LaneBToolSet) Definitions() []messages.ToolDefinition {
	if s == nil {
		return nil
	}
	return AgentLoopDefinitions()
}

// AgentLoopDefinitions is a descriptive alias for Definitions.
func (s *LaneBToolSet) AgentLoopDefinitions() []messages.ToolDefinition { return s.Definitions() }

// DefinitionSchemas returns complete closed schemas for the model/provider
// boundary. It never includes a dynamic page schema.
func (s *LaneBToolSet) DefinitionSchemas() []map[string]any { return StableToolSchemas() }

// FunctionDefinitions is a descriptive alias for DefinitionSchemas.
func (s *LaneBToolSet) FunctionDefinitions() []map[string]any { return s.DefinitionSchemas() }

// Registry creates an isolated existing-CLI registry containing only these
// three tools. It does not mutate a process-wide static registry.
func (s *LaneBToolSet) Registry() (*cliTools.ToolRegistry, error) {
	registry := cliTools.NewEmptyToolRegistry()
	if s == nil {
		return registry, nil
	}
	for _, tool := range s.tools {
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// NewRegistry is a descriptive alias for Registry.
func (s *LaneBToolSet) NewRegistry() (*cliTools.ToolRegistry, error) { return s.Registry() }

// Executor returns the correlated textual executor.
func (s *LaneBToolSet) Executor() *LaneBExecutor {
	if s == nil {
		return &LaneBExecutor{}
	}
	return s.executor
}

// Service returns the injected neutral discovery seam.
func (s *LaneBToolSet) Service() DiscoveryService {
	if s == nil {
		return nil
	}
	return s.service
}

// Execute runs a named tool through the existing CLI Tool message contract.
func (s *LaneBToolSet) Execute(ctx context.Context, name string, args map[string]any) ([]messages.Message, error) {
	if s == nil {
		return nil, errors.New("nil webmcp tool set")
	}
	spec, ok := s.spec(name)
	if !ok {
		encoded, encodeErr := laneBInvalidEnvelope(name, nil, []ToolResultIssue{{Path: "/name", Code: "unknown_tool"}})
		if encodeErr != nil {
			return nil, encodeErr
		}
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(encoded))}, nil
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		encoded, encodeErr := laneBInvalidEnvelope(name, nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}})
		if encodeErr != nil {
			return nil, encodeErr
		}
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(encoded))}, nil
	}
	values, issues := laneBDecodeArguments(raw, spec)
	issues = append(issues, validateDecodedArguments(spec.definition.Name, values)...)
	if len(issues) > 0 {
		encoded, encodeErr := laneBInvalidEnvelope(name, values, issues)
		if encodeErr != nil {
			return nil, encodeErr
		}
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(encoded))}, nil
	}
	encoded, err := s.executeValidated(ctx, spec, values)
	if err != nil {
		return nil, err
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(encoded))}, nil
}

type laneBTool struct {
	set        *LaneBToolSet
	definition ToolDefinition
}

func (t *laneBTool) Name() string               { return t.definition.Name }
func (t *laneBTool) Description() string        { return t.definition.Description }
func (t *laneBTool) Parameters() map[string]any { return laneBCloneMap(t.definition.Parameters) }

func (t *laneBTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	if t == nil || t.set == nil {
		encoded, err := laneBDisabledEnvelope()
		if err != nil {
			return nil, err
		}
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(encoded))}, nil
	}
	return t.set.Execute(ctx, t.definition.Name, args)
}

// LaneBExecutor adapts the tool set to messages.ToolExecutor. It always returns
// one textual correlated result for a valid invocation or a model-input
// failure, so classified browser errors do not terminate the agent loop.
type LaneBExecutor struct{ set *LaneBToolSet }

var _ messages.ToolExecutor = (*LaneBExecutor)(nil)
var _ cliTools.Tool = (*laneBTool)(nil)

func (e *LaneBExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	response := messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}
	if e == nil || e.set == nil {
		encoded, err := laneBDisabledEnvelope()
		if err != nil {
			return response, err
		}
		response.Content = string(encoded)
		return response, nil
	}
	spec, ok := e.set.spec(call.Name)
	if !ok {
		encoded, err := laneBInvalidEnvelope("", nil, []ToolResultIssue{{Path: "/name", Code: "unknown_tool"}})
		if err != nil {
			return response, err
		}
		response.Content = string(encoded)
		return response, nil
	}
	values, issues := laneBDecodeArguments([]byte(call.Arguments), spec)
	issues = append(issues, validateDecodedArguments(spec.definition.Name, values)...)
	if len(issues) > 0 {
		encoded, err := laneBInvalidEnvelope(call.Name, values, issues)
		if err != nil {
			return response, err
		}
		response.Content = string(encoded)
		return response, nil
	}
	encoded, err := e.set.executeValidated(ctx, spec, values)
	if err != nil {
		return response, err
	}
	response.Content = string(encoded)
	return response, nil
}
