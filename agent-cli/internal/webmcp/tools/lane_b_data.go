package tools

import (
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

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
