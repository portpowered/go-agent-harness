package bootstrap

import (
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// errorFacts is the classification-relevant view of one selection error:
// the first discovery error and the first classified WebMCP error in its
// chain, if any.
type errorFacts struct {
	discovery  *discovery.DiscoveryError
	classified *webmcp.ClassifiedError
}

func factsOf(err error) errorFacts {
	var facts errorFacts
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil {
		facts.discovery = discoveryErr
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		facts.classified = classified
	}
	return facts
}

func (f errorFacts) discoveryCodeIn(codes ...discovery.Code) bool {
	if f.discovery == nil {
		return false
	}
	for _, code := range codes {
		if f.discovery.Code == code {
			return true
		}
	}
	return false
}

func (f errorFacts) classifiedCodeIn(codes ...webmcp.ErrorCode) bool {
	if f.classified == nil {
		return false
	}
	for _, code := range codes {
		if f.classified.Code == code {
			return true
		}
	}
	return false
}

// recoverableSelectionError reports whether a failed automatic selection may
// leave a reachable browser connected but unselected.
func recoverableSelectionError(err error) bool {
	facts := factsOf(err)
	return facts.discoveryCodeIn(discovery.CodeNoEligibleTab, discovery.CodeAmbiguousBrowser, discovery.CodeAmbiguousTab) ||
		facts.classifiedCodeIn(webmcp.ErrorNoEligibleTab, webmcp.ErrorAmbiguousBrowser, webmcp.ErrorAmbiguousTab)
}

// recoverableRestoredSelectionError reports whether a failed persisted
// restore may leave the session connected but unselected. A stale record is
// only recoverable while the failure is retryable; a hard identity or
// lifecycle failure still fails closed.
func recoverableRestoredSelectionError(err error) bool {
	if recoverableSelectionError(err) {
		return true
	}
	facts := factsOf(err)
	if facts.discovery != nil {
		return facts.discovery.Code == discovery.CodeStaleSelection && facts.discovery.Retryable
	}
	if facts.classified != nil {
		return facts.classified.Code == webmcp.ErrorStaleSelection && facts.classified.Retryable
	}
	return false
}

func noSelectionError(err error) bool {
	facts := factsOf(err)
	return facts.discoveryCodeIn(discovery.CodeNoEligibleTab) || facts.classifiedCodeIn(webmcp.ErrorNoEligibleTab)
}

func staleSelectionError(err error) bool {
	facts := factsOf(err)
	return facts.discoveryCodeIn(discovery.CodeStaleSelection) || facts.classifiedCodeIn(webmcp.ErrorStaleSelection)
}

// retryableCatalogDeadline reports a late page-tool catalog: the exact
// connected selection stays usable for a later model-facing list/retry.
func retryableCatalogDeadline(err error) bool {
	classified := factsOf(err).classified
	if classified == nil || classified.Code != webmcp.ErrorBrowserProtocol || !classified.Retryable || classified.Details == nil {
		return false
	}
	return classified.Details["reason_code"] == "page_tools_unverified" && classified.Details["reason"] == "deadline_exceeded"
}
