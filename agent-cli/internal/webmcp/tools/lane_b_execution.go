package tools

import (
	"context"
	"sort"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

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
