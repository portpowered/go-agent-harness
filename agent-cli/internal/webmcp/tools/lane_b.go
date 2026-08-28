package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var laneBNormalizedIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

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

type laneBToolSpec struct{ definition ToolDefinition }

func (s *LaneBToolSet) spec(name string) (laneBToolSpec, bool) {
	if s == nil {
		return laneBToolSpec{}, false
	}
	for _, definition := range s.definitions {
		if definition.Name == name {
			return laneBToolSpec{definition: definition}, true
		}
	}
	return laneBToolSpec{}, false
}

func laneBDecodeArguments(raw []byte, spec laneBToolSpec) (map[string]any, []ToolResultIssue) {
	object, issues := laneBDecodeJSONObject(raw)
	if len(issues) > 0 && object == nil {
		return nil, issues
	}
	properties, _ := spec.definition.Parameters["properties"].(map[string]any)
	propertyNames := schemaOrder(spec.definition.Name)
	allowed := make(map[string]map[string]any, len(properties))
	for name, value := range properties {
		property, _ := value.(map[string]any)
		allowed[name] = property
	}
	unknown := make([]string, 0)
	for name := range object {
		if _, ok := allowed[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "unknown_property"})
	}
	result := make(map[string]any, len(properties))
	for _, name := range propertyNames {
		property, exists := allowed[name]
		if !exists {
			continue
		}
		rawValue, present := object[name]
		if !present {
			if requiredPropertyName(spec.definition.Parameters, name) {
				issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "required"})
				continue
			}
			result[name] = property["default"]
			continue
		}
		if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "invalid_type"})
			continue
		}
		valueType, _ := property["type"].(string)
		switch valueType {
		case "string":
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "invalid_type"})
				continue
			}
			result[name] = value
		case "boolean":
			var value bool
			if err := json.Unmarshal(rawValue, &value); err != nil {
				issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "invalid_type"})
				continue
			}
			result[name] = value
		default:
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "unsupported_type"})
		}
	}
	return result, issues
}

func laneBDecodeJSONObject(raw []byte) (map[string]json.RawMessage, []ToolResultIssue) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, []ToolResultIssue{{Path: "/", Code: "object_required"}}
	}
	object := make(map[string]json.RawMessage)
	var issues []ToolResultIssue
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, []ToolResultIssue{{Path: laneBPointerPath(key), Code: "invalid_json"}}
		}
		if _, exists := object[key]; exists {
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath(key), Code: "duplicate_property"})
			continue
		}
		object[key] = append(json.RawMessage(nil), value...)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, []ToolResultIssue{{Path: "/", Code: "multiple_json_values"}}
		}
		return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	return object, issues
}

func requiredPropertyName(schema map[string]any, name string) bool {
	required, _ := schema["required"].([]string)
	for _, value := range required {
		if value == name {
			return true
		}
	}
	return false
}

func validateDecodedArguments(name string, values map[string]any) []ToolResultIssue {
	var issues []ToolResultIssue
	checkID := func(field string) {
		value, present := values[field]
		if !present {
			return
		}
		text, isString := value.(string)
		if !isString {
			return
		}
		if !laneBNormalizedIDPattern.MatchString(text) {
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath(field), Code: "invalid_identifier"})
		}
	}
	switch name {
	case SelectTabToolName:
		checkID("browser_id")
		checkID("target_id")
	case ListTabsToolName:
		if value, present := values["browser_id"].(string); present && value != "" && !laneBNormalizedIDPattern.MatchString(value) {
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath("browser_id"), Code: "invalid_identifier"})
		}
	}
	return issues
}

func (s *LaneBToolSet) executeValidated(ctx context.Context, spec laneBToolSpec, args map[string]any) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.service == nil || !s.enabled {
		return laneBDisabledEnvelope()
	}
	switch spec.definition.Name {
	case GetContextToolName:
		return s.executeGetContext(ctx, laneBBoolValue(args, "refresh"))
	case ListTabsToolName:
		return s.executeListTabs(ctx, listOptionsFrom(args))
	case SelectTabToolName:
		return s.executeSelectTab(ctx, laneBStringValue(args, "browser_id"), laneBStringValue(args, "target_id"), laneBBoolValue(args, "activate"))
	default:
		return laneBInvalidEnvelope(spec.definition.Name, args, []ToolResultIssue{{Path: "/name", Code: "unknown_tool"}})
	}
}

func (s *LaneBToolSet) executeGetContext(ctx context.Context, refresh bool) ([]byte, error) {
	var (
		selected discovery.Selection
		ok       bool
		err      error
	)
	if refresh {
		selected, err = s.service.RefreshSelection(ctx)
		ok = selected.BrowserID != "" || selected.TargetID != ""
	} else {
		selected, ok = s.service.Selected()
	}
	if err != nil {
		return laneBBrokerFailure(err, ErrorStaleSelection, map[string]any{"phase": "context"})
	}
	if !ok || selected.BrowserID == "" || selected.TargetID == "" {
		return laneBBrokerFailure(noSelectionError(), ErrorNoEligibleTab, map[string]any{"candidate_count": 0})
	}
	page := selected.Context()
	if !page.Connected {
		return laneBBrokerFailure(disconnectedError(selected.BrowserID, selected.TargetID, "context"), ErrorBrowserDisconnected, nil)
	}
	if !page.Ready {
		return laneBBrokerFailure(unsupportedError(selected.BrowserID, selected.TargetID), ErrorUnsupportedWebMCP, nil)
	}
	return EncodeToolResult(s.laneBContextData(selected), nil)
}

func (s *LaneBToolSet) executeListTabs(ctx context.Context, options listOptions) ([]byte, error) {
	candidates, err := s.service.DiscoverAll(ctx, s.inputs)
	if err != nil {
		return laneBBrokerFailure(err, ErrorEndpointNotFound, nil)
	}
	candidates = append([]discovery.BrowserCandidate(nil), candidates...)
	s.rememberBrowsers(candidates)
	if options.BrowserID != "" {
		found := false
		for _, candidate := range candidates {
			if candidate.ID == options.BrowserID {
				found = true
				candidates = []discovery.BrowserCandidate{candidate}
				break
			}
		}
		if !found {
			return laneBBrokerFailure(noEligibleError(options.BrowserID, options, 0), ErrorNoEligibleTab, nil)
		}
	}
	if options.BrowserID == "" && len(candidates) > 1 {
		return laneBBrokerFailure(ambiguousBrowserError(candidates), ErrorAmbiguousBrowser, nil)
	}
	if len(candidates) == 0 {
		return laneBBrokerFailure(noEligibleError(options.BrowserID, options, 0), ErrorNoEligibleTab, nil)
	}

	allTargets := make([]discovery.Target, 0)
	candidateCount := 0
	for _, browser := range candidates {
		snapshot, listErr := s.service.ListTargetSnapshot(ctx, browser, discovery.TargetListOptions{
			BrowserID:            browser.ID,
			OriginContains:       options.OriginContains,
			EligibleOnly:         discovery.Bool(options.EligibleOnly),
			IncludeZeroToolPages: options.IncludeZeroToolPages,
		})
		candidateCount += snapshot.CandidateCount
		if listErr != nil {
			if discoveryCode(listErr) == ErrorNoEligibleTab {
				continue
			}
			return laneBBrokerFailure(listErr, ErrorNoEligibleTab, map[string]any{"browser_id": safeID(browser.ID), "candidate_count": snapshot.CandidateCount})
		}
		filtered := laneBFilterTargets(snapshot.Targets, options)
		allTargets = append(allTargets, filtered...)
		if snapshot.CandidateCount > 0 && len(filtered) == 0 {
			// The service normally returns no_eligible_tab for this case; keep
			// this guard for neutral fakes that return an empty success.
			continue
		}
	}
	sort.Slice(allTargets, func(i, j int) bool {
		if allTargets[i].BrowserID != allTargets[j].BrowserID {
			return allTargets[i].BrowserID < allTargets[j].BrowserID
		}
		return allTargets[i].ID < allTargets[j].ID
	})
	if len(allTargets) == 0 {
		return laneBBrokerFailure(noEligibleError(options.BrowserID, options, candidateCount), ErrorNoEligibleTab, nil)
	}
	data := listTabsData{
		Browsers:       browserChoices(candidates),
		Targets:        targetChoices(allTargets),
		CandidateCount: candidateCount,
		EligibleCount:  countEligible(allTargets),
		Filters:        safeListOptions(options),
	}
	return EncodeToolResult(data, nil)
}

func (s *LaneBToolSet) executeSelectTab(ctx context.Context, browserID, targetID string, activate bool) ([]byte, error) {
	candidates, err := s.service.DiscoverAll(ctx, s.inputs)
	if err != nil {
		return laneBBrokerFailure(err, ErrorEndpointNotFound, map[string]any{"phase": "selection_discovery"})
	}
	var browser discovery.BrowserCandidate
	for _, candidate := range candidates {
		if candidate.ID == browserID {
			browser = candidate
			break
		}
	}
	if browser.ID == "" {
		return laneBBrokerFailure(noEligibleError(browserID, listOptions{BrowserID: browserID}, len(candidates)), ErrorNoEligibleTab, nil)
	}
	s.rememberBrowser(browser)
	selected, selectErr := s.service.Select(ctx, discovery.TargetSelectionRequest{
		Browser:   browser,
		BrowserID: browserID,
		TargetID:  targetID,
		Activate:  activate,
		Reason:    "model_request",
	})
	if selectErr != nil {
		return laneBBrokerFailure(selectErr, ErrorNoEligibleTab, map[string]any{
			"browser_id": safeID(browserID),
			"target_id":  safeID(targetID),
			"phase":      "select",
		})
	}
	return EncodeToolResult(s.laneBContextData(selected), nil)
}

type listOptions struct {
	BrowserID            string `json:"browser_id"`
	OriginContains       string `json:"origin_contains"`
	EligibleOnly         bool   `json:"eligible_only"`
	IncludeZeroToolPages bool   `json:"include_zero_tool_pages"`
}

func listOptionsFrom(values map[string]any) listOptions {
	return listOptions{
		BrowserID:            laneBStringValue(values, "browser_id"),
		OriginContains:       laneBStringValue(values, "origin_contains"),
		EligibleOnly:         laneBBoolValueDefault(values, "eligible_only", true),
		IncludeZeroToolPages: laneBBoolValueDefault(values, "include_zero_tool_pages", false),
	}
}

func safeListOptions(options listOptions) listOptions {
	options.BrowserID = safeID(options.BrowserID)
	options.OriginContains = safeOriginFilter(options.OriginContains)
	return options
}

type laneBContextData struct {
	BrowserID      string         `json:"browser_id"`
	BrowserProduct string         `json:"browser_product"`
	TargetID       string         `json:"target_id"`
	Title          string         `json:"title"`
	URL            string         `json:"url"`
	Origin         string         `json:"origin"`
	Generation     uint64         `json:"generation"`
	Connected      bool           `json:"connected"`
	Ready          bool           `json:"ready"`
	CatalogReady   bool           `json:"catalog_ready"`
	ToolCount      int            `json:"tool_count"`
	ToolCountKnown bool           `json:"tool_count_known"`
	PendingCount   int            `json:"pending_count"`
	PolicySummary  map[string]any `json:"policy_summary"`
}

func (s *LaneBToolSet) laneBContextData(selected discovery.Selection) laneBContextData {
	page := selected.Context()
	pageURL, pageOrigin := safePageMetadata(selected.URL, selected.Origin)
	browserProduct := "unknown"
	if browser, ok := s.browser(selected.BrowserID); ok && browser.Product != "" {
		browserProduct = browser.Product
	}
	toolCount := selected.Target.ToolCount
	toolCountKnown := selected.Target.ToolCountKnown
	pending := 0
	if s.pending != nil {
		pending = s.pending()
		if pending < 0 {
			pending = 0
		}
	}
	return laneBContextData{
		BrowserID:      safeID(selected.BrowserID),
		BrowserProduct: safeLabel(browserProduct, 128),
		TargetID:       safeID(selected.TargetID),
		Title:          boundedOutputLabel(selected.Title, 512),
		URL:            pageURL,
		Origin:         pageOrigin,
		Generation:     selected.Generation,
		Connected:      page.Connected,
		Ready:          page.Ready,
		CatalogReady:   page.Ready && selected.Target.WebMCP,
		ToolCount:      toolCount,
		ToolCountKnown: toolCountKnown,
		PendingCount:   pending,
		PolicySummary:  laneBCloneMap(s.policy),
	}
}

type browserChoice struct {
	BrowserID string `json:"browser_id"`
	Product   string `json:"product"`
	Protocol  string `json:"protocol"`
}

type targetChoice struct {
	BrowserID         string `json:"browser_id"`
	TargetID          string `json:"target_id"`
	Type              string `json:"type"`
	Title             string `json:"title"`
	URL               string `json:"url"`
	Origin            string `json:"origin"`
	Generation        uint64 `json:"generation"`
	WebMCP            bool   `json:"webmcp"`
	ToolCount         int    `json:"tool_count"`
	ToolCountKnown    bool   `json:"tool_count_known"`
	Eligible          bool   `json:"eligible"`
	EligibilityReason string `json:"eligibility_reason,omitempty"`
}

type listTabsData struct {
	Browsers       []browserChoice `json:"browsers"`
	Targets        []targetChoice  `json:"targets"`
	CandidateCount int             `json:"candidate_count"`
	EligibleCount  int             `json:"eligible_count"`
	Filters        listOptions     `json:"filters"`
}

func browserChoices(candidates []discovery.BrowserCandidate) []browserChoice {
	ordered := append([]discovery.BrowserCandidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	result := make([]browserChoice, 0, len(ordered))
	for _, candidate := range ordered {
		result = append(result, browserChoice{
			BrowserID: candidate.ID,
			Product:   safeLabel(candidate.Product, 128),
			Protocol:  safeLabel(candidate.Protocol, 32),
		})
	}
	return result
}

func targetChoices(targets []discovery.Target) []targetChoice {
	result := make([]targetChoice, 0, len(targets))
	for _, target := range targets {
		pageURL, pageOrigin := safePageMetadata(target.URL, target.Origin)
		result = append(result, targetChoice{
			BrowserID:         safeID(target.BrowserID),
			TargetID:          safeID(target.ID),
			Type:              boundedOutputLabel(target.Type, 32),
			Title:             boundedOutputLabel(target.Title, 512),
			URL:               pageURL,
			Origin:            pageOrigin,
			Generation:        target.Generation,
			WebMCP:            target.WebMCP,
			ToolCount:         target.ToolCount,
			ToolCountKnown:    target.ToolCountKnown,
			Eligible:          target.Eligible,
			EligibilityReason: safeLabel(target.EligibilityReason, 64),
		})
	}
	return result
}

func laneBFilterTargets(targets []discovery.Target, options listOptions) []discovery.Target {
	result := make([]discovery.Target, 0, len(targets))
	for _, target := range targets {
		if options.OriginContains != "" && !strings.Contains(target.Origin, options.OriginContains) {
			continue
		}
		if options.EligibleOnly {
			if !target.Eligible {
				continue
			}
			if !options.IncludeZeroToolPages && target.ToolCountKnown && target.ToolCount == 0 {
				continue
			}
		}
		result = append(result, target)
	}
	return result
}

func countEligible(targets []discovery.Target) int {
	count := 0
	for _, target := range targets {
		if target.Eligible {
			count++
		}
	}
	return count
}

func (s *LaneBToolSet) rememberBrowser(browser discovery.BrowserCandidate) {
	if browser.ID == "" {
		return
	}
	s.mu.Lock()
	if s.browsers == nil {
		s.browsers = make(map[string]discovery.BrowserCandidate)
	}
	s.browsers[browser.ID] = browser
	s.mu.Unlock()
}

func (s *LaneBToolSet) rememberBrowsers(browsers []discovery.BrowserCandidate) {
	for _, browser := range browsers {
		s.rememberBrowser(browser)
	}
}

func (s *LaneBToolSet) browser(browserID string) (discovery.BrowserCandidate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if browser, ok := s.browsers[strings.TrimSpace(browserID)]; ok {
		return browser, true
	}
	if lookup, ok := s.service.(BrowserLookup); ok {
		return lookup.Browser(browserID)
	}
	return discovery.BrowserCandidate{}, false
}

func laneBInvalidEnvelope(toolName string, values map[string]any, issues []ToolResultIssue) ([]byte, error) {
	toolRef := ""
	if values != nil {
		// Only an opaque-looking value could be returned; Lane B has no
		// tool_ref input, so this remains empty for all three tools.
		if value, ok := values["tool_ref"].(string); ok && laneBNormalizedIDPattern.MatchString(value) {
			toolRef = value
		}
	}
	inputSchema := objectSchema()
	for _, definition := range StableToolDefinitions() {
		if definition.Name == toolName {
			inputSchema = laneBCloneMap(definition.Parameters)
			break
		}
	}
	return EncodeToolResult(nil, &ToolResultError{
		Code:      string(ErrorInvalidToolInput),
		Message:   "The broker tool input is invalid.",
		Retryable: true,
		Details: map[string]any{
			"tool":         boundedOutputLabel(toolName, 64),
			"tool_ref":     toolRef,
			"input_schema": inputSchema,
			"issues":       issues,
		},
	})
}

func laneBDisabledEnvelope() ([]byte, error) {
	return EncodeToolResult(nil, &ToolResultError{
		Code:      string(ErrorWebMCPDisabled),
		Message:   "Browser tools are not enabled.",
		Retryable: true,
		Details:   map[string]any{"activation": "browser-tools"},
	})
}

func laneBBrokerFailure(err error, fallback ErrorCode, details map[string]any) ([]byte, error) {
	resultError := resultErrorFor(err, fallback, details)
	return EncodeToolResult(nil, &resultError)
}

func resultErrorFor(err error, fallback ErrorCode, fallbackDetails map[string]any) ToolResultError {
	if fallbackDetails == nil {
		fallbackDetails = map[string]any{}
	}
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil {
		code := ErrorCode(discoveryErr.Code)
		if !IsKnownErrorCode(code) {
			code = fallback
		}
		message := discoveryErr.Message
		if message == "" {
			message = defaultErrorMessage(code)
		}
		detailValues := safeDiscoveryDetails(code, discoveryErr.Details)
		if len(detailValues) == 0 {
			detailValues = safeDetails(fallbackDetails)
		}
		return ToolResultError{Code: string(code), Message: safeMessage(message, code), Retryable: discoveryErr.Retryable, Details: detailValues}
	}
	if discovery.IsBrowserDisconnected(err) {
		return ToolResultError{Code: string(ErrorBrowserDisconnected), Message: defaultErrorMessage(ErrorBrowserDisconnected), Retryable: false, Details: safeDetails(fallbackDetails)}
	}
	if errors.Is(err, context.Canceled) {
		fallback = ErrorEndpointUnreachable
	}
	if !IsKnownErrorCode(fallback) {
		fallback = ErrorNoEligibleTab
	}
	return ToolResultError{Code: string(fallback), Message: defaultErrorMessage(fallback), Retryable: retryable(fallback), Details: safeDetails(fallbackDetails)}
}

func discoveryCode(err error) ErrorCode {
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil {
		return ErrorCode(discoveryErr.Code)
	}
	return ""
}

func safeDiscoveryDetails(code ErrorCode, details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	result := make(map[string]any)
	copyField := func(key string, value any) {
		if value != nil {
			result[key] = value
		}
	}
	switch code {
	case ErrorEndpointNotFound:
		copyField("endpoint_kind", safeLabel(details["endpoint_kind"], 32))
		copyField("source", safeLabel(details["source"], 64))
	case ErrorEndpointUnreachable:
		copyField("endpoint_kind", safeLabel(details["endpoint_kind"], 32))
		copyField("address_class", safeLabel(details["address_class"], 32))
		copyField("phase", safeLabel(details["phase"], 32))
	case ErrorRemoteEndpointDenied:
		copyField("endpoint_kind", safeLabel(details["endpoint_kind"], 32))
		copyField("network_class", safeLabel(details["network_class"], 32))
		copyField("required_flag", safeLabel(details["required_flag"], 64))
	case ErrorBrowserProtocol:
		copyField("phase", safeLabel(details["phase"], 32))
		copyField("protocol", safeLabel(details["protocol"], 32))
		copyField("reason_code", safeLabel(details["reason_code"], 64))
	case ErrorUnsupportedWebMCP:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("target_id", safeIDValue(details["target_id"]))
		copyField("required_capability", safeLabel(details["required_capability"], 32))
	case ErrorNoEligibleTab:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		if filters, ok := details["filters"].(map[string]any); ok {
			result["filters"] = safeFilterDetails(filters)
		}
		copyField("candidate_count", nonNegativeInt(details["candidate_count"]))
	case ErrorAmbiguousBrowser:
		copyField("candidate_browser_ids", safeIDList(details["candidate_browser_ids"]))
	case ErrorAmbiguousTab:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("candidate_target_ids", safeIDList(details["candidate_target_ids"]))
	case ErrorStaleSelection:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("target_id", safeIDValue(details["target_id"]))
		copyField("selected_generation", nonNegativeUint(details["selected_generation"]))
		copyField("reason", safeLabel(details["reason"], 64))
	case ErrorTargetAttachFailed, ErrorTargetDetached:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("target_id", safeIDValue(details["target_id"]))
		copyField("phase", safeLabel(details["phase"], 32))
		copyField("reason_code", safeLabel(details["reason_code"], 64))
		copyField("generation", nonNegativeUint(details["generation"]))
		copyField("reason", safeLabel(details["reason"], 64))
	case ErrorBrowserDisconnected:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("target_id", safeIDValue(details["target_id"]))
		copyField("phase", safeLabel(details["phase"], 32))
		result["reconnect_required"] = true
	}
	return result
}

func safeFilterDetails(details map[string]any) map[string]any {
	result := map[string]any{}
	if value, ok := details["eligible_only"].(bool); ok {
		result["eligible_only"] = value
	}
	if value, ok := details["include_zero_tool_pages"].(bool); ok {
		result["include_zero_tool_pages"] = value
	}
	if value, ok := details["origin_contains"].(string); ok {
		result["origin_contains"] = safeOriginFilter(value)
	}
	return result
}

func safeDetails(details map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range details {
		keyLabel := safeLabel(key, 64)
		if keyLabel == "" {
			continue
		}
		switch typed := value.(type) {
		case string:
			result[keyLabel] = safeLabel(typed, 128)
		case bool:
			result[keyLabel] = typed
		case int:
			if typed >= 0 {
				result[keyLabel] = typed
			}
		case int64:
			if typed >= 0 {
				result[keyLabel] = typed
			}
		case uint64:
			result[keyLabel] = typed
		}
	}
	return result
}

func noSelectionError() error {
	return &discovery.DiscoveryError{
		Code:      discovery.CodeNoEligibleTab,
		Message:   "no selected browser tab is available",
		Retryable: true,
		Details: map[string]any{
			"filters":         map[string]any{"selection": "current"},
			"candidate_count": 0,
		},
	}
}

func noEligibleError(browserID string, options listOptions, candidateCount int) error {
	filters := map[string]any{
		"eligible_only":           options.EligibleOnly,
		"include_zero_tool_pages": options.IncludeZeroToolPages,
	}
	if options.OriginContains != "" {
		filters["origin_contains"] = safeOriginFilter(options.OriginContains)
	}
	details := map[string]any{"filters": filters, "candidate_count": maxInt(candidateCount, 0)}
	if laneBNormalizedIDPattern.MatchString(browserID) {
		details["browser_id"] = browserID
	}
	return &discovery.DiscoveryError{
		Code:      discovery.CodeNoEligibleTab,
		Message:   "no eligible browser tab matched the requested filters",
		Retryable: true,
		Details:   details,
	}
}

func unsupportedError(browserID, targetID string) error {
	return &discovery.DiscoveryError{
		Code:      discovery.CodeUnsupportedWebMCP,
		Message:   "target does not provide WebMCP",
		Retryable: false,
		Details: map[string]any{
			"browser_id":          safeID(browserID),
			"target_id":           safeID(targetID),
			"required_capability": "webmcp",
		},
	}
}

func disconnectedError(browserID, targetID, phase string) error {
	return &discovery.DiscoveryError{
		Code:      discovery.CodeBrowserDisconnected,
		Message:   "browser connection ended; an exact reconnect is required",
		Retryable: false,
		Details: map[string]any{
			"browser_id":         safeID(browserID),
			"target_id":          safeID(targetID),
			"phase":              boundedOutputLabel(phase, 32),
			"reconnect_required": true,
		},
	}
}

func retryable(code ErrorCode) bool {
	switch code {
	case ErrorWebMCPDisabled, ErrorEndpointUnreachable, ErrorNoEligibleTab, ErrorAmbiguousBrowser, ErrorAmbiguousTab, ErrorStaleSelection, ErrorInvalidToolInput:
		return true
	default:
		return false
	}
}

func defaultErrorMessage(code ErrorCode) string {
	switch code {
	case ErrorWebMCPDisabled:
		return "Browser tools are not enabled."
	case ErrorEndpointNotFound:
		return "No authorized browser endpoint was found."
	case ErrorEndpointUnreachable:
		return "The browser endpoint could not be reached."
	case ErrorRemoteEndpointDenied:
		return "The remote browser endpoint is not permitted."
	case ErrorBrowserProtocol:
		return "The browser endpoint returned invalid protocol metadata."
	case ErrorUnsupportedWebMCP:
		return "The selected target does not provide WebMCP."
	case ErrorNoEligibleTab:
		return "No eligible browser tab matched the request."
	case ErrorAmbiguousBrowser:
		return "Multiple browsers matched; an exact browser ID is required."
	case ErrorAmbiguousTab:
		return "Multiple browser tabs matched; an exact target ID is required."
	case ErrorStaleSelection:
		return "The selected browser target is no longer current."
	case ErrorTargetAttachFailed:
		return "The selected browser target could not be initialized."
	case ErrorTargetDetached:
		return "The selected browser target was detached."
	case ErrorBrowserDisconnected:
		return "The browser connection ended before the operation completed."
	case ErrorInvalidToolInput:
		return "The broker tool input is invalid."
	default:
		return "The WebMCP operation could not be completed."
	}
}

func safeMessage(message string, code ErrorCode) string {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 160 || strings.ContainsAny(message, "\r\n") || strings.Contains(message, "://") || strings.ContainsAny(message, "?#") {
		return defaultErrorMessage(code)
	}
	return message
}

func safeID(value string) string {
	if laneBNormalizedIDPattern.MatchString(strings.TrimSpace(value)) {
		return strings.TrimSpace(value)
	}
	return ""
}

func safeIDValue(value any) string {
	text, _ := value.(string)
	return safeID(text)
}

func safeIDList(value any) []string {
	var result []string
	switch values := value.(type) {
	case []string:
		result = make([]string, 0, len(values))
		for _, value := range values {
			if normalized := safeID(value); normalized != "" {
				result = append(result, normalized)
			}
		}
	case []any:
		result = make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				if normalized := safeID(text); normalized != "" {
					result = append(result, normalized)
				}
			}
		}
	}
	sort.Strings(result)
	return result
}

func safeLabel(value any, max int) string {
	text, _ := value.(string)
	if strings.Contains(text, "://") || strings.ContainsAny(text, "?#@") {
		return "redacted"
	}
	return boundedOutputLabel(text, max)
}

func safeOriginFilter(value string) string {
	if strings.ContainsAny(value, "?#") || strings.Contains(value, "@") {
		return "redacted"
	}
	return boundedOutputLabel(value, 128)
}

func boundedOutputLabel(value string, max int) string {
	value = strings.TrimSpace(value)
	if max < 1 {
		return ""
	}
	if len(value) > max {
		value = value[:max]
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "redacted"
		}
	}
	return value
}

func nonNegativeInt(value any) int {
	switch typed := value.(type) {
	case int:
		if typed >= 0 {
			return typed
		}
	case int64:
		if typed >= 0 && typed <= int64(^uint(0)>>1) {
			return int(typed)
		}
	case float64:
		if typed >= 0 && typed <= float64(^uint(0)>>1) {
			return int(typed)
		}
	}
	return 0
}

func nonNegativeUint(value any) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	case float64:
		if typed >= 0 {
			return uint64(typed)
		}
	}
	return 0
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func firstToolSetOptions(options []ToolSetOptions) ToolSetOptions {
	if len(options) == 0 {
		return ToolSetOptions{}
	}
	return options[0]
}

func laneBCloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return map[string]any{}
	}
	return result
}

func laneBPointerPath(name string) string {
	return "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
}

func laneBStringValue(values map[string]any, name string) string {
	if values == nil {
		return ""
	}
	value, _ := values[name].(string)
	return value
}

func laneBBoolValue(values map[string]any, name string) bool {
	value, _ := values[name].(bool)
	return value
}

func laneBBoolValueDefault(values map[string]any, name string, fallback bool) bool {
	if values == nil {
		return fallback
	}
	value, ok := values[name].(bool)
	if !ok {
		return fallback
	}
	return value
}
