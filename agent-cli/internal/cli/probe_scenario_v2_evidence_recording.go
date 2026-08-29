package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func (c *ProbeRunCommand) prepareProbeScenarioV2RecordingRoot(count int) (string, error) {
	if c == nil {
		return "", errors.New("probe run command is nil")
	}
	root := strings.TrimSpace(c.RecordingRoot)
	if root == "" {
		created, err := os.MkdirTemp("", "go-agent-probe-v2-evidence-")
		if err != nil {
			return "", fmt.Errorf("create v2 evidence root for %d scenarios: %w", count, err)
		}
		return created, nil
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve v2 evidence root %q: %w", c.RecordingRoot, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create v2 evidence root %q: %w", root, err)
	}
	return root, nil
}

func probeScenarioV2RecordingDirectory(root string, index int, entry probeScenarioV2Selection) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	name := entry.Scenario.ID
	if name == "" {
		name = entry.Selection
	}
	name = probeScenarioV2PathSlug(name)
	if name == "" {
		name = "scenario"
	}
	return filepath.Join(root, fmt.Sprintf("%03d-%s", index+1, name))
}

func probeScenarioV2PathSlug(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			builder.WriteRune(character)
			continue
		}
		builder.WriteByte('_')
	}
	return strings.Trim(builder.String(), ".")
}

func (e *probeScenarioV2Executor) recordBrowserEvent(eventType testkit.EventType, browserID webmcp.BrowserID, targetID webmcp.TargetID, generation uint64, payload any) error {
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

func (e *probeScenarioV2Executor) recordDiscoveryStarted() error {
	return e.recordBrowserEvent(testkit.EventBrowserDiscoveryStarted, "", "", 0, map[string]any{
		"source": e.browserEvidenceSource(),
		"mode":   e.browserEvidenceMode(),
	})
}

func (e *probeScenarioV2Executor) recordDiscoveryEvidence(ctx context.Context) error {
	if err := e.recordBrowserEvent(testkit.EventBrowserDiscoveryCompleted, discoveryBrowserID(e.discovered), "", 0, map[string]any{
		"candidate_count": len(e.discovered),
		"candidates":      discoveryCandidateEvidence(e.discovered),
		"source":          e.browserEvidenceSource(),
	}); err != nil {
		return err
	}
	for _, candidate := range e.discovered {
		if err := e.recordBrowserEvent(testkit.EventBrowserEndpointVersion, candidate.ID, "", 0, map[string]any{
			"browser":                candidate.Product,
			"protocol_version":       candidate.Protocol,
			"websocket_debugger_url": safeEvidenceURL(candidate.BrowserWSURL),
		}); err != nil {
			return err
		}
		targets, err := e.broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
		if err != nil {
			return fmt.Errorf("record targets for browser %q: %w", candidate.ID, err)
		}
		if err := e.recordBrowserEvent(testkit.EventBrowserTargetsSnapshot, candidate.ID, "", 0, map[string]any{
			"target_count": len(targets),
			"targets":      targetEvidence(targets),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *probeScenarioV2Executor) browserEvidenceMode() string {
	if e != nil && e.mode == ProbeScenarioV2BrowserExecutorReal {
		return string(ProbeScenarioV2BrowserExecutorReal)
	}
	return string(ProbeScenarioV2BrowserExecutorHermetic)
}

func (e *probeScenarioV2Executor) browserEvidenceSource() string {
	if e != nil && e.mode == ProbeScenarioV2BrowserExecutorReal {
		return "webmcp-browser-adapter"
	}
	return "browser-script"
}

func probeScenarioV2EvidenceTransport(e *probeScenarioV2Executor) string {
	if e != nil && e.mode == ProbeScenarioV2BrowserExecutorReal {
		return "browser"
	}
	return "replay"
}

func probeScenarioV2EvidenceClockBase(e *probeScenarioV2Executor) string {
	if e != nil && e.mode == ProbeScenarioV2BrowserExecutorReal {
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
			"source":        string(candidate.Source),
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
			"id":         string(target.ID),
			"type":       target.Type,
			"title":      target.Title,
			"url":        safeEvidenceURL(target.URL),
			"origin":     safeEvidenceURL(target.Origin),
			"generation": target.Generation,
			"eligible":   target.Eligible,
		})
	}
	return result
}

func safeEvidenceURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func (e *probeScenarioV2Executor) recordSelectionEvidence(page webmcp.PageContext, reason string) error {
	browserID := page.Key.BrowserID
	targetID := page.Key.TargetID
	if err := e.recordBrowserEvent(testkit.EventBrowserTargetSelected, browserID, targetID, 0, map[string]any{
		"generation": page.Generation,
		"reason":     reason,
	}); err != nil {
		return err
	}
	if err := e.recordBrowserEvent(testkit.EventBrowserChromeTargetAttached, browserID, targetID, 0, map[string]any{
		"phase":     "attached",
		"ownership": "harness",
		"reason":    reason,
	}); err != nil {
		return err
	}
	return e.recordBrowserEvent(testkit.EventBrowserWebMCPEnabled, browserID, targetID, page.Generation, map[string]any{
		"enabled":    true,
		"capability": "webmcp",
		"status":     "ready",
	})
}

func (e *probeScenarioV2Executor) recordCatalogEvidence(catalog webmcp.ToolCatalogSnapshot) error {
	tools := make([]map[string]any, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		tools = append(tools, toolEvidence(tool))
	}
	if len(tools) > 0 {
		if err := e.recordBrowserEvent(testkit.EventBrowserCatalogToolAdded, catalog.Context.Key.BrowserID, catalog.Context.Key.TargetID, catalog.Generation, map[string]any{
			"tools":      tools,
			"tool_count": len(tools),
		}); err != nil {
			return err
		}
	}
	digest := sha256.New()
	for _, tool := range catalog.Tools {
		digest.Write([]byte(tool.Name))
		digest.Write([]byte{0})
		digest.Write(bytes.TrimSpace(tool.InputSchema))
		digest.Write([]byte{0})
	}
	return e.recordBrowserEvent(testkit.EventBrowserCatalogReady, catalog.Context.Key.BrowserID, catalog.Context.Key.TargetID, catalog.Generation, map[string]any{
		"tool_count":    len(catalog.Tools),
		"schema_digest": hex.EncodeToString(digest.Sum(nil)),
	})
}

func toolEvidence(tool webmcp.ToolDescriptor) map[string]any {
	evidence := map[string]any{
		"ref":           string(tool.Ref),
		"name":          tool.Name,
		"description":   tool.Description,
		"input_schema":  json.RawMessage(append([]byte(nil), tool.InputSchema...)),
		"frame_id":      string(tool.FrameID),
		"generation":    tool.Generation,
		"origin":        safeEvidenceURL(tool.Origin),
		"schema_digest": tool.SchemaDigest,
	}
	if len(tool.Annotations.Raw) > 0 {
		evidence["annotations"] = json.RawMessage(append([]byte(nil), tool.Annotations.Raw...))
	}
	return evidence
}

func (e *probeScenarioV2Executor) recordInvocationAdmission(invocation probeScenarioV2Invocation) error {
	browserID := e.selected.Key.BrowserID
	targetID := e.selected.Key.TargetID
	generation := e.selected.Generation
	if invocation.PublicID == "" {
		fields := map[string]any{"code": probeScenarioV2ErrorCode(invocation.Err)}
		if invocation.ToolRef != "" {
			fields["tool_ref"] = string(invocation.ToolRef)
		}
		if invocation.Name != "" {
			fields["tool_name"] = invocation.Name
		}
		return e.recordBrowserEvent(testkit.EventBrowserInvocationError, browserID, targetID, generation, fields)
	}
	fields := map[string]any{
		"invocation_id": string(invocation.PublicID),
		"tool_ref":      string(invocation.ToolRef),
	}
	if invocation.Name != "" {
		fields["tool_name"] = invocation.Name
	}
	if descriptor, ok := e.toolForRef(string(invocation.ToolRef)); ok && descriptor.FrameID != "" {
		fields["frame_id"] = string(descriptor.FrameID)
	}
	if err := e.recordBrowserEvent(testkit.EventBrowserInvocationCreated, browserID, targetID, generation, fields); err != nil {
		return err
	}
	if invocation.Err != nil {
		return e.recordInvocationError(browserID, targetID, generation, string(invocation.PublicID), invocation.Err)
	}
	return e.recordBrowserEvent(testkit.EventBrowserInvocationDispatched, browserID, targetID, generation, map[string]any{
		"invocation_id": string(invocation.PublicID),
		"tool_ref":      string(invocation.ToolRef),
		"input":         json.RawMessage(append([]byte(nil), invocation.Input...)),
	})
}

func (e *probeScenarioV2Executor) recordInvocationError(browserID webmcp.BrowserID, targetID webmcp.TargetID, generation uint64, invocationID string, err error) error {
	fields := map[string]any{"code": probeScenarioV2ErrorCode(err)}
	if invocationID != "" {
		fields["invocation_id"] = invocationID
	}
	return e.recordBrowserEvent(testkit.EventBrowserInvocationError, browserID, targetID, generation, fields)
}

func probeScenarioV2ErrorCode(err error) string {
	if err == nil {
		return "invocation_error"
	}
	var executorErr *ProbeScenarioV2BrowserExecutorError
	if errors.As(err, &executorErr) && executorErr != nil {
		return string(executorErr.Code)
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil && classified.Code != "" {
		return string(classified.Code)
	}
	if errors.Is(err, webmcp.ErrStaleToolRef) {
		return string(webmcp.ErrorStaleToolRef)
	}
	if errors.Is(err, webmcp.ErrInvocationNotFound) {
		return "invocation_not_found"
	}
	return "invocation_error"
}

func (e *probeScenarioV2Executor) recordInvocationTerminal(invocation probeScenarioV2Invocation) error {
	if invocation.PublicID == "" {
		return nil
	}
	browserID := e.selected.Key.BrowserID
	targetID := e.selected.Key.TargetID
	generation := e.selected.Generation
	if invocation.Err != nil {
		return e.recordInvocationError(browserID, targetID, generation, string(invocation.PublicID), invocation.Err)
	}
	switch invocation.Result.State {
	case webmcp.InvocationCanceled, webmcp.InvocationTimedOut:
		return e.recordBrowserEvent(testkit.EventBrowserInvocationCanceled, browserID, targetID, generation, map[string]any{
			"invocation_id": string(invocation.PublicID),
			"source":        "browser",
			"reason":        string(invocation.Result.State),
		})
	case webmcp.InvocationError, webmcp.InvocationOrphaned, webmcp.InvocationPolicyDenied:
		return e.recordInvocationError(browserID, targetID, generation, string(invocation.PublicID), errors.New(string(invocation.Result.ErrorCode)))
	default:
		return e.recordBrowserEvent(testkit.EventBrowserInvocationCompleted, browserID, targetID, generation, map[string]any{
			"invocation_id": string(invocation.PublicID),
			"status":        string(invocation.Result.State),
			"output":        json.RawMessage(append([]byte(nil), invocation.Result.Output...)),
		})
	}
}

func (e *probeScenarioV2Executor) recordInvocationCancel(step probe.ScenarioV2Step) error {
	return e.recordBrowserEvent(testkit.EventBrowserInvocationCancel, e.selected.Key.BrowserID, e.selected.Key.TargetID, e.selected.Generation, map[string]any{
		"invocation_id": string(step.InvocationID),
		"source":        "scenario",
		"reason":        step.Reason,
	})
}

func (e *probeScenarioV2Executor) recordGenerationChange(previous, current uint64) error {
	return e.recordBrowserEvent(testkit.EventBrowserPageGenerationChanged, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, map[string]any{
		"previous_generation": previous,
		"current_generation":  current,
		"reason":              "fixture_navigation",
	})
}

func (e *probeScenarioV2Executor) recordCleanupEvidence() error {
	if e == nil || e.selected.Key.BrowserID == "" || e.selected.Key.TargetID == "" {
		return nil
	}
	if err := e.recordBrowserEvent(testkit.EventBrowserTargetDetached, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, map[string]any{
		"reason":    "broker_close",
		"ownership": "harness",
	}); err != nil {
		return err
	}
	return e.recordBrowserEvent(testkit.EventBrowserChromeTargetClosed, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, map[string]any{
		"reason":    "broker_close",
		"ownership": "harness",
	})
}
