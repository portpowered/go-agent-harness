package doctor

import (
	"context"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const targetTypePage = "page"

func isPageTarget(target webmcp.Target) bool {
	return target.Type == "" || strings.EqualFold(target.Type, targetTypePage)
}

// targetsFrom returns the redacted targets sorted by browser and target ID,
// with a missing browser ID filled from browserID, and the page count.
func targetsFrom(targets []webmcp.Target, browserID webmcp.BrowserID) ([]Target, int) {
	normalized := append([]webmcp.Target(nil), targets...)
	for index := range normalized {
		if normalized[index].BrowserID == "" {
			normalized[index].BrowserID = browserID
		}
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		leftBrowser, rightBrowser := normalized[i].BrowserID, normalized[j].BrowserID
		if leftBrowser != rightBrowser {
			return leftBrowser < rightBrowser
		}
		return normalized[i].ID < normalized[j].ID
	})

	result := make([]Target, 0, len(normalized))
	pageCount := 0
	for _, target := range normalized {
		if isPageTarget(target) {
			pageCount++
		}
		result = append(result, targetFromTarget(target))
	}
	return result, pageCount
}

func targetFromTarget(target webmcp.Target) Target {
	typeName := target.Type
	if typeName == "" {
		typeName = targetTypePage
	}
	return Target{
		BrowserID:             string(target.BrowserID),
		TargetID:              string(target.ID),
		Type:                  normalize.BoundedText(typeName, maxTypeLength),
		Title:                 normalize.BoundedText(target.Title, maxTitleLength),
		Origin:                normalize.RedactedOrigin(target.Origin),
		Eligible:              target.Eligible,
		EligibilityReason:     normalize.BoundedText(target.EligibilityReason, maxReasonLength),
		Attached:              target.Attached,
		WebMCPDomainSupported: target.WebMCPDomainSupported,
		PageToolsReady:        target.PageToolsReady,
		PageToolsKnown:        target.PageToolsKnown,
		PageToolsEvidence:     target.PageToolsEvidence,
	}
}

func countEligiblePages(targets []webmcp.Target) int {
	count := 0
	for _, target := range targets {
		if isPageTarget(target) && target.Eligible {
			count++
		}
	}
	return count
}

// markSelected records page as the selected target and flags the matching
// entry of the target list.
func (r *Report) markSelected(page Target) {
	r.SelectedPage = &page
	for index := range r.Targets {
		if r.Targets[index].BrowserID == page.BrowserID && r.Targets[index].TargetID == page.TargetID {
			r.Targets[index].Selected = true
			r.Targets[index].Attached = page.Attached
		}
	}
}

func markTargetSelected(report *Report, target *webmcp.Target, attached bool) {
	if report == nil || target == nil {
		return
	}
	selected := targetFromTarget(*target)
	selected.Selected = true
	selected.Attached = attached
	report.markSelected(selected)
}

// browserIndex returns the index of the listed browser with id, or -1.
func (r *Report) browserIndex(id webmcp.BrowserID) int {
	for index := range r.Browsers {
		if r.Browsers[index].ID == string(id) {
			return index
		}
	}
	return -1
}

func setBrowserVersion(report *Report, candidate webmcp.BrowserCandidate) {
	if report == nil {
		return
	}
	index := report.browserIndex(candidate.ID)
	if index < 0 {
		return
	}
	if report.Browsers[index].Product == "" {
		report.Browsers[index].Product = normalize.BoundedText(candidate.Product, maxProductLength)
	}
	if report.Browsers[index].Protocol == "" {
		report.Browsers[index].Protocol = normalize.BoundedText(candidate.Protocol, maxProtocolLength)
	}
}

// applyVersion overwrites the product and protocol of every listed browser
// with the candidate's ID using the non-empty version fields.
func (r *Report) applyVersion(id webmcp.BrowserID, version webmcp.BrowserVersion) {
	for index := range r.Browsers {
		if r.Browsers[index].ID != string(id) {
			continue
		}
		if version.Browser != "" {
			r.Browsers[index].Product = normalize.BoundedText(version.Browser, maxProductLength)
		}
		if version.ProtocolVersion != "" {
			r.Browsers[index].Protocol = normalize.BoundedText(version.ProtocolVersion, maxProtocolLength)
		}
	}
}

// browserVersion reads the browser version through the runtime's version
// seam, the catalog, or the candidate's own metadata. The boolean is false
// when none of them can supply it.
func browserVersion(ctx context.Context, runtime direct.Runtime, candidate webmcp.BrowserCandidate) (webmcp.BrowserVersion, bool, error) {
	if runtime.VersionFunc != nil {
		version, err := runtime.VersionFunc(ctx, candidate)
		return version, true, err
	}
	if runtime.Catalog != nil {
		version, err := runtime.Catalog.Version(ctx, candidate)
		return version, true, err
	}
	if candidate.Product != "" || candidate.Protocol != "" {
		return webmcp.BrowserVersion{Browser: candidate.Product, ProtocolVersion: candidate.Protocol, WebSocketDebuggerURL: candidate.BrowserWSURL, BrowserInstanceID: candidate.BrowserInstanceID}, true, nil
	}
	return webmcp.BrowserVersion{}, false, nil
}
