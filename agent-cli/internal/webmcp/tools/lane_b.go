package tools

import (
	"context"
	"sync"

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
	set.executor = &LaneBExecutor{set: set}
	return set
}

// Executor returns the correlated textual executor.
func (s *LaneBToolSet) Executor() *LaneBExecutor {
	if s == nil {
		return &LaneBExecutor{}
	}
	return s.executor
}

// LaneBExecutor adapts the tool set to messages.ToolExecutor. It always returns
// one textual correlated result for a valid invocation or a model-input
// failure, so classified browser errors do not terminate the agent loop.
type LaneBExecutor struct{ set *LaneBToolSet }

var _ messages.ToolExecutor = (*LaneBExecutor)(nil)

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
