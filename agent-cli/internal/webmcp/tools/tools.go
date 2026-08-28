package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// BrokerToolSet contains the six stable tools and a matching executor. It is
// intentionally not registered in the process-wide static tool registry;
// callers opt into this set when browser tools are explicitly enabled.
type BrokerToolSet struct {
	broker      webmcp.Broker
	definitions []webmcp.BrokerToolDefinition
	tools       []cliTools.Tool
	executor    *Executor
}

// ToolSet is the concise name used by composition callers.
type ToolSet = BrokerToolSet

// NewBrokerToolSet creates a stable WebMCP tool set backed by broker. A nil
// broker is allowed so definitions can be composed before browser activation;
// execution then returns a classified webmcp_disabled envelope.
func NewBrokerToolSet(broker webmcp.Broker) *BrokerToolSet {
	definitions := webmcp.StableBrokerToolDefinitions()
	set := &BrokerToolSet{
		broker:      broker,
		definitions: definitions,
	}
	set.tools = make([]cliTools.Tool, 0, len(definitions))
	for _, definition := range definitions {
		set.tools = append(set.tools, &brokerTool{set: set, definition: definition})
	}
	set.executor = &Executor{set: set}
	return set
}

// NewToolSet is an alias for NewBrokerToolSet.
func NewToolSet(broker webmcp.Broker) *ToolSet {
	return NewBrokerToolSet(broker)
}

// NewWebMCPToolSet is a descriptive constructor alias.
func NewWebMCPToolSet(broker webmcp.Broker) *ToolSet {
	return NewBrokerToolSet(broker)
}

// NewExecutor creates the direct agent-loop executor for broker.
func NewExecutor(broker webmcp.Broker) *Executor {
	return NewBrokerToolSet(broker).Executor()
}

// Tools returns the six CLI-compatible tools in frozen order.
func (s *BrokerToolSet) Tools() []cliTools.Tool {
	if s == nil {
		return nil
	}
	return append([]cliTools.Tool(nil), s.tools...)
}

// Definitions returns the six provider-neutral flat definitions used by the
// current agent-loop contract. Use DefinitionSchemas when the complete
// additionalProperties/default-bearing JSON schemas are needed.
func (s *BrokerToolSet) Definitions() []messages.ToolDefinition {
	if s == nil {
		return nil
	}
	result := make([]messages.ToolDefinition, 0, len(s.definitions))
	for _, definition := range s.definitions {
		result = append(result, messages.ToolDefinition{
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  flatParameters(definition.Parameters),
		})
	}
	return result
}

// AgentLoopDefinitions is an explicit alias for Definitions.
func (s *BrokerToolSet) AgentLoopDefinitions() []messages.ToolDefinition {
	return s.Definitions()
}

// DefinitionSchemas returns fresh CLI-shaped function definitions with the
// complete scalar schemas, defaults, required lists, and closed objects.
func (s *BrokerToolSet) DefinitionSchemas() []map[string]any {
	if s == nil {
		return nil
	}
	return webmcp.StableBrokerToolSchemas()
}

// FunctionDefinitions is a descriptive alias for DefinitionSchemas.
func (s *BrokerToolSet) FunctionDefinitions() []map[string]any {
	return s.DefinitionSchemas()
}

// Registry returns an isolated CLI registry containing only the six broker
// tools. It never mutates a caller's static registry.
func (s *BrokerToolSet) Registry() (*cliTools.ToolRegistry, error) {
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

// NewRegistry is an alias for Registry.
func (s *BrokerToolSet) NewRegistry() (*cliTools.ToolRegistry, error) {
	return s.Registry()
}

// Executor returns the correlated textual executor for the tool set.
func (s *BrokerToolSet) Executor() *Executor {
	if s == nil {
		return &Executor{}
	}
	return s.executor
}

// Broker exposes the injected broker for composition diagnostics.
func (s *BrokerToolSet) Broker() webmcp.Broker {
	if s == nil {
		return nil
	}
	return s.broker
}

// StableDefinitions is a package-level convenience for callers that only
// need the provider-facing schemas.
func StableDefinitions() []map[string]any {
	return webmcp.StableBrokerToolSchemas()
}

// BrokerToolDefinitions is a package-level alias matching the domain name.
func BrokerToolDefinitions() []map[string]any {
	return StableDefinitions()
}

type brokerTool struct {
	set        *BrokerToolSet
	definition webmcp.BrokerToolDefinition
}

func (t *brokerTool) Name() string { return t.definition.Name }

func (t *brokerTool) Description() string { return t.definition.Description }

func (t *brokerTool) Parameters() map[string]any {
	return cloneMap(t.definition.Parameters)
}

// Execute keeps the existing CLI Tool contract usable for direct command
// paths. Failures are ordinary tool messages containing the same envelope as
// the direct ToolExecutor path.
func (t *brokerTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	if t == nil || t.set == nil {
		encoded, err := disabledEnvelope()
		if err != nil {
			return nil, err
		}
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(encoded))}, nil
	}
	encoded, err := t.set.executeMap(ctx, t.definition.Name, args)
	if err != nil {
		return nil, err
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(encoded))}, nil
}

// Executor adapts a broker to messages.ToolExecutor. Each call returns one
// correlated textual response and never returns rich ContentParts.
type Executor struct {
	set *BrokerToolSet
}

var _ messages.ToolExecutor = (*Executor)(nil)
var _ cliTools.Tool = (*brokerTool)(nil)

// Execute validates the broker function object before any broker method is
// called. It returns invalid input as a correlated tool result, not a Go
// error, so an ordinary model mistake cannot terminate the session.
func (e *Executor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	response := messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}
	if e == nil || e.set == nil {
		encoded, err := disabledEnvelope()
		if err != nil {
			return response, err
		}
		response.Content = string(encoded)
		return response, nil
	}

	spec, ok := e.set.spec(call.Name)
	if !ok {
		encoded, err := invalidEnvelope(unknownToolSchema(), "", []webmcp.ToolResultIssue{{Path: "/name", Code: "unknown_tool"}})
		if err != nil {
			return response, err
		}
		response.Content = string(encoded)
		return response, nil
	}
	args, issues := decodeArguments([]byte(call.Arguments), spec)
	if len(issues) > 0 {
		encoded, err := invalidEnvelope(spec.definition.Parameters, stringValue(args, "tool_ref"), issues)
		if err != nil {
			return response, err
		}
		response.Content = string(encoded)
		return response, nil
	}
	encoded, err := e.set.executeValidated(ctx, spec, args)
	if err != nil {
		return response, err
	}
	response.Content = string(encoded)
	return response, nil
}

func (s *BrokerToolSet) executeMap(ctx context.Context, name string, args map[string]any) ([]byte, error) {
	spec, ok := s.spec(name)
	if !ok {
		return invalidEnvelope(unknownToolSchema(), "", []webmcp.ToolResultIssue{{Path: "/name", Code: "unknown_tool"}})
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return invalidEnvelope(spec.definition.Parameters, stringValue(nil, "tool_ref"), []webmcp.ToolResultIssue{{Path: "/", Code: "invalid_json"}})
	}
	validated, issues := decodeArguments(raw, spec)
	if len(issues) > 0 {
		return invalidEnvelope(spec.definition.Parameters, stringValue(validated, "tool_ref"), issues)
	}
	return s.executeValidated(ctx, spec, validated)
}

func (s *BrokerToolSet) executeValidated(ctx context.Context, spec toolSpec, args map[string]any) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.broker == nil {
		return disabledEnvelope()
	}

	switch spec.definition.Name {
	case webmcp.GetContextToolName:
		selected, err := s.selected(ctx, boolValue(args, "refresh"))
		if err != nil {
			return brokerFailure(err, webmcp.ErrorStaleSelection, map[string]any{"phase": "selected"})
		}
		return webmcp.EncodeToolResult(contextDataFrom(selected), nil)

	case webmcp.ListTabsToolName:
		browserID := webmcp.BrowserID(stringValue(args, "browser_id"))
		targets, err := s.broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: browserID})
		if err != nil {
			return brokerFailure(err, webmcp.ErrorNoEligibleTab, map[string]any{"browser_id": string(browserID), "candidate_count": 0})
		}
		filtered := filterTargets(targets, stringValue(args, "origin_contains"), boolValueDefault(args, "eligible_only", true))
		return webmcp.EncodeToolResult(tabsData{Targets: targetDataList(filtered)}, nil)

	case webmcp.SelectTabToolName:
		selector := webmcp.TargetSelector{
			BrowserID: webmcp.BrowserID(stringValue(args, "browser_id")),
			TargetID:  webmcp.TargetID(stringValue(args, "target_id")),
		}
		selected, err := s.selectTarget(ctx, selector, boolValue(args, "activate"))
		if err != nil {
			return brokerFailure(err, webmcp.ErrorTargetAttachFailed, map[string]any{
				"browser_id": string(selector.BrowserID),
				"target_id":  string(selector.TargetID),
				"phase":      "select",
			})
		}
		return webmcp.EncodeToolResult(contextDataFrom(selected), nil)

	case webmcp.ListToolsToolName:
		options := webmcp.ListToolsOptions{
			Refresh:        boolValue(args, "refresh"),
			NameContains:   stringValue(args, "name_contains"),
			IncludeSchemas: boolValueDefault(args, "include_schemas", true),
			FrameID:        webmcp.FrameID(stringValue(args, "frame_id")),
		}
		catalog, err := s.broker.ListTools(ctx, options)
		if err != nil {
			return brokerFailure(err, webmcp.ErrorStaleSelection, map[string]any{"phase": "list_tools"})
		}
		return webmcp.EncodeToolResult(catalogDataFrom(catalog, options.IncludeSchemas), nil)

	case webmcp.InvokeToolName:
		request := webmcp.InvokeRequest{
			ToolRef: webmcp.ToolRef(stringValue(args, "tool_ref")),
			Input:   json.RawMessage(stringValue(args, "input_json")),
			Reason:  stringValue(args, "reason"),
		}
		result, err := s.broker.Invoke(ctx, request)
		if err != nil {
			return brokerFailure(err, webmcp.ErrorInvocationFailed, map[string]any{
				"tool_ref": string(request.ToolRef),
				"phase":    "invoke",
			})
		}
		if result.ErrorCode != "" || isFailedInvocationState(result.State) {
			return invocationFailure(result, request.ToolRef)
		}
		output, err := compactJSONOrNull(result.Output)
		if err != nil {
			return brokerFailure(err, webmcp.ErrorInvocationFailed, map[string]any{
				"invocation_id":       string(result.InvocationID),
				"tool_ref":            string(request.ToolRef),
				"phase":               "result_serialization",
				"page_error_code":     "invalid_json",
				"side_effect_unknown": true,
			})
		}
		status := string(result.State)
		if status == "" {
			status = string(webmcp.InvocationCompleted)
		}
		return webmcp.EncodeToolResult(invokeData{
			InvocationID: result.InvocationID,
			ToolRef:      request.ToolRef,
			Status:       status,
			Output:       output,
		}, nil)

	case webmcp.CancelToolName:
		request := webmcp.CancelRequest{
			InvocationID: webmcp.InvocationID(stringValue(args, "invocation_id")),
			Reason:       stringValue(args, "reason"),
		}
		if err := s.broker.Cancel(ctx, request); err != nil {
			return brokerFailure(err, webmcp.ErrorInvocationFailed, map[string]any{
				"invocation_id": string(request.InvocationID),
				"phase":         "cancel",
			})
		}
		return webmcp.EncodeToolResult(cancelData{InvocationID: request.InvocationID, Status: "cancel_requested"}, nil)
	default:
		return invalidEnvelope(unknownToolSchema(), "", []webmcp.ToolResultIssue{{Path: "/name", Code: "unknown_tool"}})
	}
}

// Optional broker extensions preserve the frozen Broker interface while
// allowing discovery/selection implementations to honor refresh and activate
// controls when they are available.
type contextRefresher interface {
	SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error)
}

type targetSelectorWithOptions interface {
	SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
}

func (s *BrokerToolSet) selected(ctx context.Context, refresh bool) (webmcp.PageContext, error) {
	if refresher, ok := s.broker.(contextRefresher); ok {
		return refresher.SelectedWithRefresh(ctx, refresh)
	}
	return s.broker.Selected(ctx)
}

func (s *BrokerToolSet) selectTarget(ctx context.Context, selector webmcp.TargetSelector, activate bool) (webmcp.PageContext, error) {
	if selectorWithOptions, ok := s.broker.(targetSelectorWithOptions); ok {
		return selectorWithOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	}
	return s.broker.Select(ctx, selector)
}

type toolSpec struct {
	definition webmcp.BrokerToolDefinition
	properties []propertySpec
}

type propertySpec struct {
	name     string
	typeName string
	required bool
	defaultV any
}

func (s *BrokerToolSet) spec(name string) (toolSpec, bool) {
	if s == nil {
		return toolSpec{}, false
	}
	for _, definition := range s.definitions {
		if definition.Name == name {
			return makeToolSpec(definition), true
		}
	}
	return toolSpec{}, false
}

func makeToolSpec(definition webmcp.BrokerToolDefinition) toolSpec {
	orders := map[string][]string{
		webmcp.GetContextToolName: {"refresh"},
		webmcp.ListTabsToolName:   {"browser_id", "origin_contains", "eligible_only", "include_zero_tool_pages"},
		webmcp.SelectTabToolName:  {"browser_id", "target_id", "activate"},
		webmcp.ListToolsToolName:  {"refresh", "name_contains", "include_schemas", "frame_id"},
		webmcp.InvokeToolName:     {"tool_ref", "input_json", "reason"},
		webmcp.CancelToolName:     {"invocation_id", "reason"},
	}
	properties := definition.Parameters["properties"].(map[string]any)
	var requiredSet map[string]bool
	if required, ok := definition.Parameters["required"].([]string); ok {
		requiredSet = make(map[string]bool, len(required))
		for _, name := range required {
			requiredSet[name] = true
		}
	}
	var specs []propertySpec
	for _, name := range orders[definition.Name] {
		schema, _ := properties[name].(map[string]any)
		valueType, _ := schema["type"].(string)
		spec := propertySpec{name: name, typeName: valueType, required: requiredSet[name]}
		if !spec.required {
			spec.defaultV = schema["default"]
		}
		specs = append(specs, spec)
	}
	return toolSpec{definition: definition, properties: specs}
}

func decodeArguments(raw []byte, spec toolSpec) (map[string]any, []webmcp.ToolResultIssue) {
	object, issues := decodeJSONObject(raw)
	if len(issues) > 0 {
		return nil, issues
	}
	allowed := make(map[string]propertySpec, len(spec.properties))
	for _, property := range spec.properties {
		allowed[property.name] = property
	}
	unknown := make([]string, 0)
	for name := range object {
		if _, ok := allowed[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		issues = append(issues, webmcp.ToolResultIssue{Path: pointerPath(name), Code: "unknown_property"})
	}

	result := make(map[string]any, len(spec.properties))
	for _, property := range spec.properties {
		rawValue, present := object[property.name]
		if !present {
			if property.required {
				issues = append(issues, webmcp.ToolResultIssue{Path: pointerPath(property.name), Code: "required"})
				continue
			}
			result[property.name] = property.defaultV
			continue
		}
		switch property.typeName {
		case "string":
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				issues = append(issues, webmcp.ToolResultIssue{Path: pointerPath(property.name), Code: "invalid_type"})
				continue
			}
			result[property.name] = value
		case "boolean":
			var value bool
			if err := json.Unmarshal(rawValue, &value); err != nil {
				issues = append(issues, webmcp.ToolResultIssue{Path: pointerPath(property.name), Code: "invalid_type"})
				continue
			}
			result[property.name] = value
		default:
			issues = append(issues, webmcp.ToolResultIssue{Path: pointerPath(property.name), Code: "unsupported_type"})
		}
	}
	return result, issues
}

func decodeJSONObject(raw []byte) (map[string]json.RawMessage, []webmcp.ToolResultIssue) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, []webmcp.ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, []webmcp.ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, []webmcp.ToolResultIssue{{Path: "/", Code: "object_required"}}
	}
	object := make(map[string]json.RawMessage)
	var issues []webmcp.ToolResultIssue
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, []webmcp.ToolResultIssue{{Path: "/", Code: "invalid_json"}}
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, []webmcp.ToolResultIssue{{Path: "/", Code: "invalid_json"}}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, []webmcp.ToolResultIssue{{Path: pointerPath(key), Code: "invalid_json"}}
		}
		if _, exists := object[key]; exists {
			issues = append(issues, webmcp.ToolResultIssue{Path: pointerPath(key), Code: "duplicate_property"})
			continue
		}
		object[key] = append(json.RawMessage(nil), value...)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, []webmcp.ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, []webmcp.ToolResultIssue{{Path: "/", Code: "multiple_json_values"}}
		}
		return nil, []webmcp.ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	return object, issues
}

func invalidEnvelope(inputSchema map[string]any, toolRef string, issues []webmcp.ToolResultIssue) ([]byte, error) {
	if inputSchema == nil {
		inputSchema = unknownToolSchema()
	}
	resultError := webmcp.ToolResultError{
		Code:      string(webmcp.ErrorInvalidToolInput),
		Message:   "The broker tool input is invalid.",
		Retryable: true,
		Details: map[string]any{
			"tool_ref":     toolRef,
			"input_schema": inputSchema,
			"issues":       issues,
		},
	}
	return webmcp.EncodeToolResult(nil, &resultError)
}

func disabledEnvelope() ([]byte, error) {
	resultError := webmcp.ToolResultError{
		Code:      string(webmcp.ErrorWebMCPDisabled),
		Message:   "Browser tools are not enabled.",
		Retryable: true,
		Details:   map[string]any{"activation": "browser-tools"},
	}
	return webmcp.EncodeToolResult(nil, &resultError)
}

func brokerFailure(err error, fallback webmcp.ErrorCode, details map[string]any) ([]byte, error) {
	resultError := webmcp.ResultErrorFor(err, fallback, details)
	return webmcp.EncodeToolResult(nil, &resultError)
}

func invocationFailure(result webmcp.InvokeResult, toolRef webmcp.ToolRef) ([]byte, error) {
	code := webmcp.ErrorCode(result.ErrorCode)
	if !webmcp.IsKnownErrorCode(code) {
		switch result.State {
		case webmcp.InvocationCanceled:
			code = webmcp.ErrorInvocationCanceled
		case webmcp.InvocationTimedOut:
			code = webmcp.ErrorInvocationTimedOut
		case webmcp.InvocationOrphaned:
			code = webmcp.ErrorInvocationOrphaned
		default:
			code = webmcp.ErrorInvocationFailed
		}
	}
	details := map[string]any{
		"invocation_id": string(result.InvocationID),
		"tool_ref":      string(toolRef),
		"phase":         "invoke",
	}
	if result.ErrorDetails != nil {
		details = cloneMap(result.ErrorDetails)
	}
	switch code {
	case webmcp.ErrorInvocationCanceled:
		if result.ErrorDetails == nil {
			details = map[string]any{"invocation_id": string(result.InvocationID), "cancel_source": "broker"}
		}
	case webmcp.ErrorInvocationTimedOut:
		if _, ok := details["timeout_ms"]; !ok {
			details["timeout_ms"] = 0
		}
		if _, ok := details["side_effect_unknown"]; !ok {
			details["side_effect_unknown"] = true
		}
	case webmcp.ErrorInvocationOrphaned:
		if result.ErrorDetails == nil {
			details["target_id"] = ""
			details["generation"] = uint64(0)
			details["terminal_observed"] = false
		}
	case webmcp.ErrorResultTooLarge:
		if result.ErrorDetails == nil {
			details["limit_bytes"] = 0
			details["observed_bytes"] = 0
		}
	case webmcp.ErrorInvocationFailed:
		if _, ok := details["page_error_code"]; !ok {
			details["page_error_code"] = result.ErrorCode
		}
		if _, ok := details["side_effect_unknown"]; !ok {
			details["side_effect_unknown"] = true
		}
	}
	resultError := webmcp.ToolResultError{
		Code:      string(code),
		Message:   invocationMessage(code),
		Retryable: false,
		Details:   details,
	}
	return webmcp.EncodeToolResult(nil, &resultError)
}

func invocationMessage(code webmcp.ErrorCode) string {
	switch code {
	case webmcp.ErrorTargetDetached:
		return "The selected browser target detached before the invocation completed."
	case webmcp.ErrorPageNavigated:
		return "The page navigated before the invocation completed."
	case webmcp.ErrorBrowserDisconnected:
		return "The browser connection ended before the invocation completed."
	case webmcp.ErrorInvocationCanceled:
		return "The browser invocation was canceled."
	case webmcp.ErrorInvocationTimedOut:
		return "The browser invocation timed out."
	case webmcp.ErrorInvocationOrphaned:
		return "The browser invocation could not be reconciled before shutdown."
	default:
		return "The browser invocation failed."
	}
}

func isFailedInvocationState(state webmcp.InvocationState) bool {
	switch state {
	case webmcp.InvocationError, webmcp.InvocationCanceled, webmcp.InvocationTimedOut, webmcp.InvocationOrphaned, webmcp.InvocationPolicyDenied:
		return true
	default:
		return false
	}
}

type contextData struct {
	BrowserID  webmcp.BrowserID `json:"browser_id"`
	TargetID   webmcp.TargetID  `json:"target_id"`
	Title      string           `json:"title"`
	URL        string           `json:"url"`
	Origin     string           `json:"origin"`
	Generation uint64           `json:"generation"`
	Connected  bool             `json:"connected"`
	Ready      bool             `json:"ready"`
}

func contextDataFrom(context webmcp.PageContext) contextData {
	return contextData{
		BrowserID:  context.Key.BrowserID,
		TargetID:   context.Key.TargetID,
		Title:      context.Title,
		URL:        context.URL,
		Origin:     context.Origin,
		Generation: context.Generation,
		Connected:  context.Connected,
		Ready:      context.Ready,
	}
}

type tabsData struct {
	Targets []targetData `json:"targets"`
}

type targetData struct {
	BrowserID         webmcp.BrowserID `json:"browser_id"`
	TargetID          webmcp.TargetID  `json:"target_id"`
	Type              string           `json:"type"`
	Title             string           `json:"title"`
	URL               string           `json:"url"`
	Origin            string           `json:"origin"`
	Attached          bool             `json:"attached"`
	Eligible          bool             `json:"eligible"`
	EligibilityReason string           `json:"eligibility_reason,omitempty"`
}

func targetDataList(targets []webmcp.Target) []targetData {
	result := make([]targetData, 0, len(targets))
	for _, target := range targets {
		result = append(result, targetData{
			BrowserID:         target.BrowserID,
			TargetID:          target.ID,
			Type:              target.Type,
			Title:             target.Title,
			URL:               target.URL,
			Origin:            target.Origin,
			Attached:          target.Attached,
			Eligible:          target.Eligible,
			EligibilityReason: target.EligibilityReason,
		})
	}
	return result
}

func filterTargets(targets []webmcp.Target, originContains string, eligibleOnly bool) []webmcp.Target {
	filtered := make([]webmcp.Target, 0, len(targets))
	for _, target := range targets {
		if eligibleOnly && !target.Eligible {
			continue
		}
		if originContains != "" && !strings.Contains(target.Origin, originContains) {
			continue
		}
		filtered = append(filtered, target)
	}
	return filtered
}

type catalogData struct {
	Generation uint64        `json:"generation"`
	Tools      []catalogTool `json:"tools"`
}

type catalogTool struct {
	Ref         webmcp.ToolRef  `json:"ref"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Annotations annotationsData `json:"annotations"`
	Frame       frameData       `json:"frame"`
	Generation  uint64          `json:"generation"`
}

type annotationsData struct {
	ReadOnly   *bool `json:"read_only,omitempty"`
	Untrusted  *bool `json:"untrusted_content,omitempty"`
	AutoSubmit *bool `json:"auto_submit,omitempty"`
}

type frameData struct {
	ID     webmcp.FrameID `json:"id"`
	Origin string         `json:"origin"`
}

func catalogDataFrom(snapshot webmcp.ToolCatalogSnapshot, includeSchemas bool) catalogData {
	result := catalogData{Generation: snapshot.Generation, Tools: make([]catalogTool, 0, len(snapshot.Tools))}
	for _, tool := range snapshot.Tools {
		entry := catalogTool{
			Ref:         tool.Ref,
			Name:        tool.Name,
			Description: tool.Description,
			Annotations: annotationsData{ReadOnly: tool.Annotations.ReadOnly, Untrusted: tool.Annotations.UntrustedContent, AutoSubmit: tool.Annotations.AutoSubmit},
			Frame:       frameData{ID: tool.FrameID, Origin: tool.Origin},
			Generation:  tool.Generation,
		}
		if includeSchemas {
			entry.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
		}
		result.Tools = append(result.Tools, entry)
	}
	return result
}

type invokeData struct {
	InvocationID webmcp.InvocationID `json:"invocation_id"`
	ToolRef      webmcp.ToolRef      `json:"tool_ref"`
	Status       string              `json:"status"`
	Output       json.RawMessage     `json:"output"`
}

type cancelData struct {
	InvocationID webmcp.InvocationID `json:"invocation_id"`
	Status       string              `json:"status"`
}

func compactJSONOrNull(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("more than one JSON value")
		}
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return nil, err
	}
	return json.RawMessage(compact.Bytes()), nil
}

func flatParameters(schema map[string]any) []messages.ToolParameter {
	properties, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	if requiredList, ok := schema["required"].([]string); ok {
		for _, name := range requiredList {
			required[name] = true
		}
	}
	orders := make([]string, 0, len(properties))
	for _, name := range []string{
		"refresh", "browser_id", "origin_contains", "eligible_only", "include_zero_tool_pages",
		"target_id", "activate", "name_contains", "include_schemas", "frame_id",
		"tool_ref", "input_json", "reason", "invocation_id",
	} {
		if _, ok := properties[name]; ok {
			orders = append(orders, name)
		}
	}
	// Keep the helper total if a future stable definition adds a property
	// before its explicit contract order is updated.
	for name := range properties {
		found := false
		for _, ordered := range orders {
			if ordered == name {
				found = true
				break
			}
		}
		if !found {
			orders = append(orders, name)
		}
	}
	result := make([]messages.ToolParameter, 0, len(orders))
	for _, name := range orders {
		property, _ := properties[name].(map[string]any)
		valueType, _ := property["type"].(string)
		description, _ := property["description"].(string)
		result = append(result, messages.ToolParameter{Name: name, Type: valueType, Description: description, Required: required[name]})
	}
	return result
}

func unknownToolSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func cloneMap(value map[string]any) map[string]any {
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

func pointerPath(name string) string {
	return "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
}

func stringValue(values map[string]any, name string) string {
	if values == nil {
		return ""
	}
	value, _ := values[name].(string)
	return value
}

func boolValue(values map[string]any, name string) bool {
	value, _ := values[name].(bool)
	return value
}

func boolValueDefault(values map[string]any, name string, fallback bool) bool {
	if values == nil {
		return fallback
	}
	value, ok := values[name].(bool)
	if !ok {
		return fallback
	}
	return value
}
