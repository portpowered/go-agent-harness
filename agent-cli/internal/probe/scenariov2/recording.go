package scenariov2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

// Browser event payload field names and values.
const (
	fieldInvocationID = "invocation_id"
	fieldToolRef      = "tool_ref"
	fieldToolName     = "tool_name"
	fieldReason       = "reason"
	fieldSource       = "source"
	fieldOwnership    = "ownership"
	fieldGeneration   = "generation"
	fieldCode         = "code"
	fieldToolCount    = "tool_count"
	ownershipHarness  = "harness"
	reasonBrokerClose = "broker_close"
)

func (e *executor) recordBrowserEvent(eventType testkit.EventType, browserID webmcp.BrowserID, targetID webmcp.TargetID, generation uint64, payload any) error {
	if e == nil || e.recorder == nil {
		return errors.New("browser evidence recorder is unavailable")
	}
	input, err := testkit.NewEventInput(eventType, payload)
	if err != nil {
		return fmt.Errorf("encode browser event %s: %w", eventType, err)
	}
	input.BrowserID = string(browserID)
	input.TargetID = string(targetID)
	input.Generation = generation
	if _, err := e.recorder.Record(input); err != nil {
		return fmt.Errorf("record browser event %s: %w", eventType, err)
	}
	return nil
}

func (e *executor) recordDiscoveryStarted() error {
	return e.recordBrowserEvent(testkit.EventBrowserDiscoveryStarted, "", "", 0, map[string]any{
		fieldSource: e.browserEvidenceSource(),
		"mode":      e.browserEvidenceMode(),
	})
}

func (e *executor) recordDiscoveryEvidence(ctx context.Context) error {
	if err := e.recordBrowserEvent(testkit.EventBrowserDiscoveryCompleted, discoveryBrowserID(e.discovered), "", 0, map[string]any{
		"candidate_count": len(e.discovered),
		"candidates":      discoveryCandidateEvidence(e.discovered),
		fieldSource:       e.browserEvidenceSource(),
	}); err != nil {
		return err
	}
	for _, candidate := range e.discovered {
		if err := e.recordCandidateEvidence(ctx, candidate); err != nil {
			return err
		}
	}
	return nil
}

func (e *executor) recordCandidateEvidence(ctx context.Context, candidate webmcp.BrowserCandidate) error {
	if err := e.recordBrowserEvent(testkit.EventBrowserEndpointVersion, candidate.ID, "", 0, map[string]any{
		"browser":                candidate.Product,
		"protocol_version":       candidate.Protocol,
		"websocket_debugger_url": objective.SafeURL(candidate.BrowserWSURL),
	}); err != nil {
		return err
	}
	targets, err := e.broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
	if err != nil {
		return fmt.Errorf("record targets for browser %q: %w", candidate.ID, err)
	}
	return e.recordBrowserEvent(testkit.EventBrowserTargetsSnapshot, candidate.ID, "", 0, map[string]any{
		"target_count": len(targets),
		"targets":      targetEvidence(targets),
	})
}

func (e *executor) browserEvidenceMode() string {
	if e != nil && e.mode == BrowserExecutorReal {
		return string(BrowserExecutorReal)
	}
	return string(BrowserExecutorHermetic)
}

func (e *executor) browserEvidenceSource() string {
	if e != nil && e.mode == BrowserExecutorReal {
		return "webmcp-browser-adapter"
	}
	return "browser-script"
}

func (e *executor) evidenceTransport() string {
	if e != nil && e.mode == BrowserExecutorReal {
		return "browser"
	}
	return "replay"
}

func (e *executor) evidenceClockBase() string {
	if e != nil && e.mode == BrowserExecutorReal {
		return "runtime"
	}
	return "fake:0"
}

func discoveryBrowserID(candidates []webmcp.BrowserCandidate) webmcp.BrowserID {
	if len(candidates) == 1 {
		return candidates[0].ID
	}
	return ""
}

func discoveryCandidateEvidence(candidates []webmcp.BrowserCandidate) []map[string]any {
	result := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, map[string]any{
			"id":            string(candidate.ID),
			fieldSource:     string(candidate.Source),
			"product":       candidate.Product,
			"protocol":      candidate.Protocol,
			"loopback":      candidate.Loopback,
			"explicit":      candidate.Explicit,
			"harness_owned": candidate.HarnessOwned,
		})
	}
	return result
}

func targetEvidence(targets []webmcp.Target) []map[string]any {
	result := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		result = append(result, map[string]any{
			"id":            string(target.ID),
			"type":          target.Type,
			"title":         target.Title,
			"url":           objective.SafeURL(target.URL),
			"origin":        objective.SafeURL(target.Origin),
			fieldGeneration: target.Generation,
			"eligible":      target.Eligible,
		})
	}
	return result
}

func (e *executor) recordSelectionEvidence(page webmcp.PageContext, reason string) error {
	browserID := page.Key.BrowserID
	targetID := page.Key.TargetID
	if err := e.recordBrowserEvent(testkit.EventBrowserTargetSelected, browserID, targetID, 0, map[string]any{
		fieldGeneration: page.Generation,
		fieldReason:     reason,
	}); err != nil {
		return err
	}
	if err := e.recordBrowserEvent(testkit.EventBrowserChromeTargetAttached, browserID, targetID, 0, map[string]any{
		"phase":        "attached",
		fieldOwnership: ownershipHarness,
		fieldReason:    reason,
	}); err != nil {
		return err
	}
	return e.recordBrowserEvent(testkit.EventBrowserWebMCPEnabled, browserID, targetID, page.Generation, map[string]any{
		"enabled":    true,
		"capability": "webmcp",
		"status":     "ready",
	})
}

func (e *executor) recordCatalogEvidence(catalog webmcp.ToolCatalogSnapshot) error {
	browserID := catalog.Context.Key.BrowserID
	targetID := catalog.Context.Key.TargetID
	tools := make([]map[string]any, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		tools = append(tools, toolEvidence(tool))
	}
	if len(tools) > 0 {
		if err := e.recordBrowserEvent(testkit.EventBrowserCatalogToolAdded, browserID, targetID, catalog.Generation, map[string]any{
			"tools":        tools,
			fieldToolCount: len(tools),
		}); err != nil {
			return err
		}
	}
	return e.recordBrowserEvent(testkit.EventBrowserCatalogReady, browserID, targetID, catalog.Generation, map[string]any{
		fieldToolCount:  len(catalog.Tools),
		"schema_digest": catalogSchemaDigest(catalog.Tools),
	})
}

func catalogSchemaDigest(tools []webmcp.ToolDescriptor) string {
	digest := sha256.New()
	for _, tool := range tools {
		digest.Write([]byte(tool.Name))
		digest.Write([]byte{0})
		digest.Write(bytes.TrimSpace(tool.InputSchema))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func toolEvidence(tool webmcp.ToolDescriptor) map[string]any {
	evidence := map[string]any{
		"ref":           string(tool.Ref),
		"name":          tool.Name,
		"description":   tool.Description,
		"input_schema":  json.RawMessage(append([]byte(nil), tool.InputSchema...)),
		"frame_id":      string(tool.FrameID),
		fieldGeneration: tool.Generation,
		"origin":        objective.SafeURL(tool.Origin),
		"schema_digest": tool.SchemaDigest,
	}
	if len(tool.Annotations.Raw) > 0 {
		evidence["annotations"] = json.RawMessage(append([]byte(nil), tool.Annotations.Raw...))
	}
	return evidence
}
