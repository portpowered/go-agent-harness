package tools

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"sort"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func ambiguousBrowserError(candidates []discovery.BrowserCandidate) error {
	ids := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		id := safeID(candidate.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return &discovery.DiscoveryError{
		Code:      discovery.CodeAmbiguousBrowser,
		Message:   "multiple browsers matched; an exact browser ID is required",
		Retryable: true,
		Details:   map[string]any{"candidate_browser_ids": ids},
	}
}

// invocationStateErrorCode maps a terminal invocation state to the error code
// reported when the page supplied no known code.
func invocationStateErrorCode(state webmcp.InvocationState) webmcp.ErrorCode {
	switch state {
	case webmcp.InvocationCanceled:
		return webmcp.ErrorInvocationCanceled
	case webmcp.InvocationTimedOut:
		return webmcp.ErrorInvocationTimedOut
	case webmcp.InvocationOrphaned:
		return webmcp.ErrorInvocationOrphaned
	case webmcp.InvocationCreated, webmcp.InvocationAwaitingApproval, webmcp.InvocationQueued,
		webmcp.InvocationDispatching, webmcp.InvocationDispatched, webmcp.InvocationCompleted,
		webmcp.InvocationError, webmcp.InvocationPolicyDenied:
		return webmcp.ErrorInvocationFailed
	default:
		return webmcp.ErrorInvocationFailed
	}
}
