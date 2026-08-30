package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// PageToolNamePrefix disambiguates a connected-catalog page tool whose name
// collides with a built-in session tool. Non-colliding page tools keep the
// page's own name so a model can call exactly what the catalog listed.
const PageToolNamePrefix = "page_"

// pageToolState guards the dynamic first-class page-tool surface. The name
// map is rebuilt on every definition snapshot and consulted (with a live
// catalog re-resolution) on every dynamic call, so executor routing follows
// toolsAdded/toolsRemoved and generation bumps without holding stale refs.
type pageToolState struct {
	mu            sync.Mutex
	reservedNames map[string]struct{}
	publishedName map[string]string // advertised session name -> catalog name
}

func (s *BrokerToolSet) pageState() *pageToolState {
	s.pageOnce.Do(func() {
		s.page = &pageToolState{
			reservedNames: make(map[string]struct{}),
			publishedName: make(map[string]string),
		}
	})
	return s.page
}

// SetReservedToolNames records the session's non-page tool names (static
// tools plus the stable broker tools). A page tool that collides with a
// reserved name is advertised with PageToolNamePrefix instead.
func (s *BrokerToolSet) SetReservedToolNames(names []string) {
	if s == nil {
		return
	}
	state := s.pageState()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.reservedNames = make(map[string]struct{}, len(names)+len(s.definitions))
	for _, name := range names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			state.reservedNames[trimmed] = struct{}{}
		}
	}
	for _, definition := range s.definitions {
		state.reservedNames[definition.Name] = struct{}{}
	}
}

// PageToolDefinitions snapshots the connected catalog as first-class session
// tool definitions: the page tool's own name (prefixed only on collision),
// its description, and its top-level input schema translated to the flat
// agent-loop parameter contract. A broker without a connected catalog yields
// no page tools and no error; the stable broker tools remain available.
func (s *BrokerToolSet) PageToolDefinitions(ctx context.Context) []messages.ToolDefinition {
	definitions, _ := s.PageToolDefinitionsWithError(ctx)
	return definitions
}

// PageToolDefinitionsWithError is the error-preserving form of
// PageToolDefinitions. Session publication uses this form so a failed catalog
// refresh cannot be mistaken for an intentional empty page surface.
func (s *BrokerToolSet) PageToolDefinitionsWithError(ctx context.Context) ([]messages.ToolDefinition, error) {
	if s == nil || s.broker == nil {
		return nil, nil
	}
	catalog, err := s.broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		return nil, err
	}
	state := s.pageState()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.publishedName = make(map[string]string, len(catalog.Tools))
	definitions := make([]messages.ToolDefinition, 0, len(catalog.Tools))
	for _, descriptor := range catalog.Tools {
		name := strings.TrimSpace(descriptor.Name)
		if name == "" {
			continue
		}
		advertised := name
		if _, reserved := state.reservedNames[advertised]; reserved {
			advertised = PageToolNamePrefix + name
		}
		if _, taken := state.publishedName[advertised]; taken {
			continue
		}
		if _, stillReserved := state.reservedNames[advertised]; stillReserved {
			continue
		}
		state.publishedName[advertised] = name
		definitions = append(definitions, pageToolDefinition(advertised, descriptor))
	}
	return definitions, nil
}

func pageToolDefinition(advertised string, descriptor webmcp.ToolDescriptor) messages.ToolDefinition {
	description := strings.TrimSpace(descriptor.Description)
	if description == "" {
		description = "Tool provided by the connected browser page."
	}
	parameters, closed := pageToolParameters(descriptor.InputSchema)
	return messages.ToolDefinition{
		Name:             advertised,
		Description:      description,
		Parameters:       parameters,
		ParametersClosed: closed,
	}
}

// pageToolParameters translates a page tool's JSON input schema into the flat
// agent-loop parameter contract. Top-level property names, types, required
// flags, and descriptions survive verbatim; nested detail (array items,
// enums, nested objects) is summarized into the parameter description so the
// model still sees the page's real expectations.
func pageToolParameters(schema json.RawMessage) ([]messages.ToolParameter, bool) {
	var parsed struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &parsed) != nil {
		return nil, true
	}
	required := make(map[string]bool, len(parsed.Required))
	for _, name := range parsed.Required {
		required[name] = true
	}
	names := make([]string, 0, len(parsed.Properties))
	for name := range parsed.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	parameters := make([]messages.ToolParameter, 0, len(names))
	for _, name := range names {
		var property struct {
			Type        string          `json:"type"`
			Description string          `json:"description"`
			Items       json.RawMessage `json:"items"`
			Enum        []any           `json:"enum"`
		}
		_ = json.Unmarshal(parsed.Properties[name], &property)
		parameterType := property.Type
		if parameterType == "" {
			parameterType = "object"
		}
		description := property.Description
		if parameterType == "array" && len(property.Items) > 0 {
			description = strings.TrimSpace(description + " Items: " + compactSchemaSummary(property.Items))
		}
		if len(property.Enum) > 0 {
			if encoded, err := json.Marshal(property.Enum); err == nil {
				description = strings.TrimSpace(description + " One of: " + string(encoded))
			}
		}
		parameters = append(parameters, messages.ToolParameter{
			Name:        name,
			Type:        parameterType,
			Description: description,
			Required:    required[name],
		})
	}
	closed := parsed.AdditionalProperties == nil || !*parsed.AdditionalProperties
	return parameters, closed
}

func compactSchemaSummary(schema json.RawMessage) string {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, []byte(schema)); err != nil {
		return string(schema)
	}
	summary := buffer.String()
	const maxSummary = 160
	if len(summary) > maxSummary {
		return summary[:maxSummary] + "..."
	}
	return summary
}

// ResolvesDynamicTools reports that this executor routes tool names beyond
// its static definitions: first-class page tools resolved against the live
// connected catalog at call time.
func (e *Executor) ResolvesDynamicTools() bool {
	return e != nil && e.set != nil && e.set.broker != nil
}

// executePageTool routes one dynamic call. The advertised name is resolved
// against the CURRENT catalog by name (published mapping first, then a live
// exact-name or prefix-stripped match), so calls stay correct across catalog
// generation bumps and tool-ref rotation. A name with no live catalog match
// returns a model-visible guidance envelope, never a hard executor error.
func (s *BrokerToolSet) executePageTool(ctx context.Context, call messages.ToolCall) ([]byte, error) {
	// The catalog step is where a cold or degraded broker pays its browser
	// setup (dial, attach, enable, catalog wait). Bound it separately from
	// the page tool's own run so an interactive deadline expiring here is
	// reported as slow browser setup, not blamed on the page tool.
	setupContext := ctx
	cancelSetup := func() {}
	if deadline, ok := ctx.Deadline(); ok {
		reserve := time.Until(deadline) / 3
		if reserve > 0 {
			setupContext, cancelSetup = context.WithDeadline(ctx, deadline.Add(-reserve))
		}
	}
	setupStarted := time.Now()
	catalog, err := s.broker.ListTools(setupContext, webmcp.ListToolsOptions{IncludeSchemas: false})
	cancelSetup()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || (setupContext.Err() != nil && ctx.Err() == nil) {
			resultError := webmcp.ToolResultError{
				Code:      string(webmcp.ErrorTargetAttachFailed),
				Retryable: true,
				Message:   fmt.Sprintf("browser setup for %q did not become ready within %s: the connection, attach, or page catalog is slow; the page tool itself never ran", call.Name, time.Since(setupStarted).Round(time.Millisecond)),
				Details: map[string]any{
					"phase":          "setup_timeout",
					"tool":           call.Name,
					"setup_duration": time.Since(setupStarted).String(),
				},
			}
			return webmcp.EncodeToolResult(nil, &resultError)
		}
		return brokerFailure(err, webmcp.ErrorStaleSelection, map[string]any{
			"phase": "page_tool_catalog",
			"tool":  call.Name,
		})
	}
	state := s.pageState()
	state.mu.Lock()
	catalogName, published := state.publishedName[call.Name]
	state.mu.Unlock()
	if !published {
		catalogName = call.Name
	}
	descriptor, found := catalogToolByName(catalog, catalogName)
	if !found && strings.HasPrefix(catalogName, PageToolNamePrefix) {
		descriptor, found = catalogToolByName(catalog, strings.TrimPrefix(catalogName, PageToolNamePrefix))
	}
	if !found {
		return pageToolGuidanceEnvelope(call.Name, catalog)
	}
	input := strings.TrimSpace(call.Arguments)
	if input == "" {
		input = "{}"
	}
	if !json.Valid([]byte(input)) {
		var schema map[string]any
		_ = json.Unmarshal(descriptor.InputSchema, &schema)
		return invalidEnvelope(schema, string(descriptor.Ref), []webmcp.ToolResultIssue{{Path: "", Code: "invalid_json"}})
	}
	return s.invokeToolRef(ctx, webmcp.InvokeRequest{
		ToolRef: descriptor.Ref,
		Input:   json.RawMessage(input),
		Reason:  "first_class_page_tool",
	})
}

func catalogToolByName(catalog webmcp.ToolCatalogSnapshot, name string) (webmcp.ToolDescriptor, bool) {
	for _, descriptor := range catalog.Tools {
		if descriptor.Name == name {
			return descriptor, true
		}
	}
	return webmcp.ToolDescriptor{}, false
}

// pageToolGuidanceEnvelope answers a call that names no live catalog entry.
// The model gets actionable guidance instead of a dead-end: the tools that DO
// exist right now, close matches to what it asked for, and the stable
// webmcp_list_tools/webmcp_invoke path.
func pageToolGuidanceEnvelope(requested string, catalog webmcp.ToolCatalogSnapshot) ([]byte, error) {
	available := make([]string, 0, len(catalog.Tools))
	for _, descriptor := range catalog.Tools {
		available = append(available, descriptor.Name)
	}
	sort.Strings(available)
	close := closeToolMatches(requested, available)
	message := fmt.Sprintf("tool %q is not in the connected page catalog", requested)
	if len(close) > 0 {
		message += fmt.Sprintf("; close matches: %s", strings.Join(close, ", "))
	}
	if len(available) > 0 {
		message += fmt.Sprintf(". Available page tools: %s. Call one directly by name, or use webmcp_list_tools and webmcp_invoke.", strings.Join(available, ", "))
	} else {
		message += ". No page tools are connected; use webmcp_list_tabs and webmcp_select_tab first, then webmcp_list_tools."
	}
	resultError := webmcp.ToolResultError{
		Code:      string(webmcp.ErrorStaleToolRef),
		Retryable: true,
		Message:   message,
		Details: map[string]any{
			"requested_tool":  requested,
			"available_tools": available,
		},
	}
	return webmcp.EncodeToolResult(nil, &resultError)
}

// closeToolMatches finds catalog names within a small edit distance or
// containing/contained-by the requested name.
func closeToolMatches(requested string, available []string) []string {
	requested = strings.ToLower(requested)
	matches := make([]string, 0, 2)
	for _, name := range available {
		lower := strings.ToLower(name)
		if strings.Contains(lower, requested) || strings.Contains(requested, lower) {
			matches = append(matches, name)
		}
	}
	return matches
}
