// Package operations owns the business rules behind the direct WebMCP
// commands: exact target resolution against the persisted selection,
// stale-selection and browser-loss classification, selection and
// activation, bounded listings, invocation with its dispatch receipt and
// interrupt reconciliation, exact-target cancellation, and the bounded watch
// stream. Command wiring, flag parsing, and output rendering stay in the CLI
// transport; every result here is a redacted, bounded data model.
package operations

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

// Display bounds for direct-command result text fields.
const (
	// MaxTextLength bounds titles, names, IDs, and reasons.
	MaxTextLength = 160
	// MaxLabelText bounds short labels such as a target type.
	MaxLabelText = 40

	maxProtocolText    = 80
	maxDescriptionText = 500
	maxURLText         = 240
)

// Selector is one command's target request. BrowserFlagChanged and
// TabFlagChanged report whether the command line explicitly named the browser
// or the tab; either one suppresses the persisted selection, which is never
// used as a hint for a different target.
type Selector struct {
	Browser            config.BrowserConfig
	BrowserFlagChanged bool
	TabFlagChanged     bool
	// LoadSelection reads the persisted selection. A nil loader is treated as
	// an unreadable persisted selection when one would be consulted.
	LoadSelection func() (selectionstore.Selection, error)
}

// Resolution is an exact, policy-checked target. Stored is the persisted
// selection the resolution was validated against, when one was used.
type Resolution struct {
	Candidate webmcp.BrowserCandidate
	Target    webmcp.Target
	Stored    *selectionstore.Selection
}

func (r Resolution) selector() webmcp.TargetSelector {
	return webmcp.TargetSelector{BrowserID: r.Candidate.ID, TargetID: r.Target.ID}
}

// Browser is the safe browser listing shape. Endpoint addresses are
// redacted before they are copied into this result.
type Browser struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Product      string `json:"product"`
	Protocol     string `json:"protocol"`
	Scope        string `json:"scope"`
	Endpoint     string `json:"endpoint,omitempty"`
	HarnessOwned bool   `json:"harness_owned"`
}

// BrowsersData is the browsers listing result.
type BrowsersData struct {
	Browsers []Browser `json:"browsers"`
}

// Tab contains only bounded display metadata and normalized origin
// information. URL query/fragment data is never returned.
type Tab struct {
	BrowserID         string `json:"browser_id"`
	TargetID          string `json:"target_id"`
	Type              string `json:"type"`
	Title             string `json:"title"`
	Origin            string `json:"origin"`
	Eligible          bool   `json:"eligible"`
	EligibilityReason string `json:"eligibility_reason,omitempty"`
	Attached          bool   `json:"attached"`
	Selected          bool   `json:"selected"`
	Generation        uint64 `json:"generation,omitempty"`
	ToolCount         *int   `json:"tool_count,omitempty"`
}

// TabsData is the tabs listing result.
type TabsData struct {
	Tabs []Tab `json:"tabs"`
}

// Context is the selected page context with its catalog summary.
type Context struct {
	BrowserID         string `json:"browser_id"`
	TargetID          string `json:"target_id"`
	Title             string `json:"title"`
	URL               string `json:"url,omitempty"`
	Origin            string `json:"origin"`
	Generation        uint64 `json:"generation"`
	Connected         bool   `json:"connected"`
	Ready             bool   `json:"ready"`
	CatalogReady      bool   `json:"catalog_ready"`
	CatalogGeneration uint64 `json:"catalog_generation"`
	ToolCount         int    `json:"tool_count"`
}

// Frame identifies the frame that exposes a tool.
type Frame struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
}

// Tool is one bounded catalog entry.
type Tool struct {
	Ref         string          `json:"ref"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Annotations map[string]any  `json:"annotations"`
	Frame       Frame           `json:"frame"`
	Generation  uint64          `json:"generation"`
}

// ToolsData is the tools listing result.
type ToolsData struct {
	BrowserID  string `json:"browser_id"`
	TargetID   string `json:"target_id"`
	Generation uint64 `json:"generation"`
	Tools      []Tool `json:"tools"`
}

// Invocation is one terminal invocation result.
type Invocation struct {
	InvocationID string          `json:"invocation_id"`
	ToolRef      string          `json:"tool_ref"`
	Status       string          `json:"status"`
	Output       json.RawMessage `json:"output"`
}

// CancelData is the confirmed cancellation result.
type CancelData struct {
	InvocationID string `json:"invocation_id"`
	Status       string `json:"status"`
	Phase        string `json:"phase,omitempty"`
	Outcome      string `json:"outcome,omitempty"`
}

// Event is one bounded watch event.
type Event struct {
	Version      string `json:"version"`
	Type         string `json:"type"`
	Sequence     uint64 `json:"sequence"`
	BrowserID    string `json:"browser_id,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	Generation   uint64 `json:"generation,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	ToolRef      string `json:"tool_ref,omitempty"`
	State        string `json:"state,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// WatchData is the watch result: a terminal status and the observed events.
type WatchData struct {
	Status string  `json:"status"`
	Events []Event `json:"events"`
}

// Watch statuses.
const (
	WatchStatusEnded    = "ended"
	WatchStatusCanceled = "canceled"
	WatchStatusOnce     = "one_event"
	WatchStatusFailed   = "failed"
)
