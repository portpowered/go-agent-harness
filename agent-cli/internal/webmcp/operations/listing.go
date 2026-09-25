package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/doctor"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const jsonNull = "null"

// Browsers lists the discovered browsers, narrowed to browserID when it is
// set. Endpoint addresses are redacted.
func Browsers(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig, browserID string) (BrowsersData, error) {
	candidates, err := direct.DiscoverBrowsers(ctx, broker, browser, browserID)
	if err != nil {
		return BrowsersData{}, err
	}
	rows := make([]Browser, 0, len(candidates))
	for _, candidate := range candidates {
		endpoint := doctor.EndpointForCandidate(candidate)
		rows = append(rows, Browser{
			ID:           string(candidate.ID),
			Source:       string(candidate.Source),
			Product:      normalize.BoundedText(candidate.Product, MaxTextLength),
			Protocol:     normalize.BoundedText(candidate.Protocol, maxProtocolText),
			Scope:        endpoint.Scope,
			Endpoint:     endpoint.Address,
			HarnessOwned: candidate.HarnessOwned,
		})
	}
	return BrowsersData{Browsers: rows}, nil
}

// TabFilter narrows a tabs listing.
type TabFilter struct {
	OriginContains string
	EligibleOnly   bool
}

func (f TabFilter) keep(target webmcp.Target) bool {
	if f.OriginContains != "" && !strings.Contains(normalize.RedactedOrigin(target.Origin), f.OriginContains) {
		return false
	}
	return !f.EligibleOnly || target.Eligible
}

// selectedTab marks the currently selected target in a listing.
type selectedTab struct {
	page      webmcp.PageContext
	toolCount *int
}

func (s selectedTab) mark(row *Tab) {
	if s.page.Key.BrowserID != webmcp.BrowserID(row.BrowserID) || s.page.Key.TargetID != webmcp.TargetID(row.TargetID) {
		return
	}
	row.Selected = true
	row.Attached = s.page.Connected
	row.Generation = s.page.Generation
	if s.toolCount != nil {
		count := *s.toolCount
		row.ToolCount = &count
	}
}

// Tabs lists the page targets of the configured browsers, sorted by browser
// and target ID, marking the selected target with its catalog size.
func Tabs(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig, filter TabFilter) (TabsData, error) {
	candidates, err := direct.DiscoverBrowsers(ctx, broker, browser, browser.Selection.Browser)
	if err != nil {
		return TabsData{}, err
	}
	selected := currentSelection(ctx, broker)
	rows := make([]Tab, 0)
	for _, candidate := range candidates {
		candidateRows, err := candidateTabs(ctx, broker, candidate, filter, selected)
		if err != nil {
			return TabsData{}, err
		}
		rows = append(rows, candidateRows...)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].BrowserID != rows[j].BrowserID {
			return rows[i].BrowserID < rows[j].BrowserID
		}
		return rows[i].TargetID < rows[j].TargetID
	})
	return TabsData{Tabs: rows}, nil
}

func currentSelection(ctx context.Context, broker webmcp.Broker) selectedTab {
	// The selection marker is best effort: a listing never fails because the
	// broker has no current selection.
	page, err := SelectedContext(ctx, broker, false)
	selected := selectedTab{page: page}
	if err != nil {
		return selected
	}
	if page.Key.BrowserID == "" || page.Key.TargetID == "" {
		return selected
	}
	if catalog, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: false}); err == nil {
		count := len(catalog.Tools)
		selected.toolCount = &count
	}
	return selected
}

func candidateTabs(ctx context.Context, broker webmcp.Broker, candidate webmcp.BrowserCandidate, filter TabFilter, selected selectedTab) ([]Tab, error) {
	rawTargets, err := broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
	if err != nil {
		return nil, err
	}
	rows := make([]Tab, 0)
	for _, target := range direct.PageTargetCandidates(rawTargets) {
		if target.BrowserID == "" {
			target.BrowserID = candidate.ID
		}
		if !filter.keep(target) {
			continue
		}
		row := tabFromTarget(target)
		selected.mark(&row)
		rows = append(rows, row)
	}
	return rows, nil
}

// ToolQuery narrows a tools listing.
type ToolQuery struct {
	Refresh        bool
	NameContains   string
	IncludeSchemas bool
	FrameID        string
}

// Tools ensures the selection and lists its catalog, sorted by frame and
// name.
func Tools(ctx context.Context, broker webmcp.Broker, selector Selector, query ToolQuery) (ToolsData, error) {
	page, err := EnsureSelection(ctx, broker, selector)
	if err != nil {
		return ToolsData{}, err
	}
	snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{
		Refresh:        query.Refresh,
		NameContains:   query.NameContains,
		IncludeSchemas: query.IncludeSchemas,
		FrameID:        webmcp.FrameID(query.FrameID),
	})
	if err != nil {
		return ToolsData{}, err
	}
	return toolsData(page, snapshot, query.IncludeSchemas), nil
}

func contextData(page webmcp.PageContext) Context {
	return Context{
		BrowserID:  string(page.Key.BrowserID),
		TargetID:   string(page.Key.TargetID),
		Title:      normalize.BoundedText(page.Title, MaxTextLength),
		URL:        redactedPageURL(page.URL),
		Origin:     normalize.RedactedOrigin(page.Origin),
		Generation: page.Generation,
		Connected:  page.Connected,
		Ready:      page.Ready,
	}
}

func contextDataWithCatalog(page webmcp.PageContext, snapshot webmcp.ToolCatalogSnapshot) Context {
	data := contextData(page)
	data.CatalogGeneration = snapshot.Generation
	data.ToolCount = len(snapshot.Tools)
	data.CatalogReady = snapshot.Context.Ready && snapshot.Context.Connected
	if data.BrowserID == "" {
		data.BrowserID = string(snapshot.Context.Key.BrowserID)
	}
	if data.TargetID == "" {
		data.TargetID = string(snapshot.Context.Key.TargetID)
	}
	if data.Generation == 0 {
		data.Generation = snapshot.Context.Generation
	}
	if data.Origin == "" {
		data.Origin = normalize.RedactedOrigin(snapshot.Context.Origin)
	}
	return data
}

func tabFromTarget(target webmcp.Target) Tab {
	typeName := target.Type
	if typeName == "" {
		typeName = targetTypePage
	}
	return Tab{
		BrowserID:         string(target.BrowserID),
		TargetID:          string(target.ID),
		Type:              normalize.BoundedText(typeName, MaxLabelText),
		Title:             normalize.BoundedText(target.Title, MaxTextLength),
		Origin:            normalize.RedactedOrigin(target.Origin),
		Eligible:          target.Eligible,
		EligibilityReason: normalize.BoundedText(target.EligibilityReason, MaxTextLength),
		Attached:          target.Attached,
	}
}

func toolsData(page webmcp.PageContext, snapshot webmcp.ToolCatalogSnapshot, includeSchemas bool) ToolsData {
	contextValue := snapshot.Context
	if contextValue.Key.BrowserID == "" {
		contextValue = page
	}
	tools := make([]Tool, 0, len(snapshot.Tools))
	for _, descriptor := range snapshot.Tools {
		tools = append(tools, toolFromDescriptor(descriptor, includeSchemas))
	}
	sort.SliceStable(tools, func(i, j int) bool {
		if tools[i].Frame.ID != tools[j].Frame.ID {
			return tools[i].Frame.ID < tools[j].Frame.ID
		}
		return tools[i].Name < tools[j].Name
	})
	return ToolsData{
		BrowserID:  string(contextValue.Key.BrowserID),
		TargetID:   string(contextValue.Key.TargetID),
		Generation: snapshot.Generation,
		Tools:      tools,
	}
}

func toolFromDescriptor(descriptor webmcp.ToolDescriptor, includeSchemas bool) Tool {
	schema := json.RawMessage(nil)
	if includeSchemas {
		schema = append(json.RawMessage(nil), descriptor.InputSchema...)
		if len(bytes.TrimSpace(schema)) == 0 || !json.Valid(schema) {
			schema = json.RawMessage(jsonNull)
		}
	}
	annotations := make(map[string]any)
	if descriptor.Annotations.ReadOnly != nil {
		annotations["read_only"] = *descriptor.Annotations.ReadOnly
	}
	if descriptor.Annotations.UntrustedContent != nil {
		annotations["untrusted_content"] = *descriptor.Annotations.UntrustedContent
	}
	if descriptor.Annotations.AutoSubmit != nil {
		annotations["autosubmit"] = *descriptor.Annotations.AutoSubmit
	}
	return Tool{
		Ref:         string(descriptor.Ref),
		Name:        normalize.BoundedText(descriptor.Name, MaxTextLength),
		Description: normalize.BoundedText(descriptor.Description, maxDescriptionText),
		InputSchema: schema,
		Annotations: annotations,
		Frame:       Frame{ID: string(descriptor.FrameID), Origin: normalize.RedactedOrigin(descriptor.Origin)},
		Generation:  descriptor.Generation,
	}
}

// redactedPageURL drops credentials, query, and fragment data and bounds
// the result.
func redactedPageURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		if index := strings.IndexAny(raw, "?#"); index >= 0 {
			raw = raw[:index]
		}
		return normalize.BoundedText(raw, maxURLText)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return normalize.BoundedText(parsed.String(), maxURLText)
}
