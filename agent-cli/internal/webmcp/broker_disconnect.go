package webmcp

import (
	"errors"
	"io"
	"net"
	"strings"
)

// brokerPhaseMaxBytes bounds the lifecycle phase label echoed in disconnect errors.
const brokerPhaseMaxBytes = 32

const (
	// brokerLifecyclePhase labels selection failures detected while checking
	// the selected session's lifecycle state.
	brokerLifecyclePhase = "lifecycle"
	// reasonSelectionNotConnected is the stale-selection reason for a
	// selection whose session is no longer connected.
	reasonSelectionNotConnected = "selection_not_connected"
)

func selectionStateErrorLocked(selected *brokerSession, reason string) error {
	if selected != nil && selected.invalidatedCode == ErrorBrowserDisconnected {
		return browserDisconnectedErrorForSession(selected, brokerLifecyclePhase, nil)
	}
	return staleSelectionForSession(selected, reason)
}

func (b *StatefulBroker) selectedStateError(selected *brokerSession, phase string) error {
	if selected == nil {
		return staleSelectionForSession(nil, reasonSelectionNotConnected)
	}
	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected {
		return staleSelectionForSession(selected, reasonSelectionNotConnected)
	}
	cause := selected.session.Err()
	if selected.invalidatedCode == ErrorBrowserDisconnected || isBrowserEndpointLossError(cause) {
		b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, phase)
		return browserDisconnectedErrorForSession(selected, phase, cause)
	}
	if !selected.active || !selected.context.Connected {
		return staleSelectionForSession(selected, reasonSelectionNotConnected)
	}
	return nil
}

func browserDisconnectedErrorForSession(selected *brokerSession, phase string, cause error) error {
	if selected == nil {
		return browserDisconnectedErrorForSelector(TargetSelector{}, phase, cause)
	}
	return browserDisconnectedErrorForSelector(TargetSelector{
		BrowserID: selected.context.Key.BrowserID,
		TargetID:  selected.context.Key.TargetID,
	}, phase, cause)
}

func browserDisconnectedErrorForSelector(selector TargetSelector, phase string, cause error) error {
	details := map[string]any{
		"browser_id":         string(selector.BrowserID),
		"target_id":          string(selector.TargetID),
		"phase":              safeBrokerPhase(phase),
		"reconnect_required": true,
	}
	return classified(ErrorBrowserDisconnected, DefaultErrorMessage(ErrorBrowserDisconnected), details, cause)
}

func safeBrokerPhase(phase string) string {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return "lifecycle"
	}
	if len(phase) > brokerPhaseMaxBytes {
		phase = phase[:brokerPhaseMaxBytes]
	}
	for _, character := range phase {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return "lifecycle"
		}
	}
	return phase
}

func errorContainsCode(err error, wanted ErrorCode) bool {
	if err == nil {
		return false
	}
	var classifiedErr *ClassifiedError
	if errors.As(err, &classifiedErr) && classifiedErr != nil && classifiedErr.Code == wanted {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if errorContainsCode(cause, wanted) {
				return true
			}
		}
	}
	return errorContainsCode(errors.Unwrap(err), wanted)
}

func isBrowserDisconnectedTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errorContainsCode(err, ErrorBrowserDisconnected) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "disconnect") ||
		strings.Contains(message, "connection lost") ||
		strings.Contains(message, "connection closed") ||
		strings.Contains(message, "closed connection") ||
		strings.Contains(message, "transport closed") ||
		(strings.Contains(message, "websocket") && strings.Contains(message, "close"))
}

func isBrowserEndpointLossError(err error) bool {
	if err == nil {
		return false
	}
	if isBrowserDisconnectedTransportError(err) || errorContainsCode(err, ErrorEndpointNotFound) || errorContainsCode(err, ErrorEndpointUnreachable) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection refused") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "endpoint not found")
}

func (b *StatefulBroker) browserDisconnectedLocked(selected *brokerSession, phase string, cause error) error {
	if selected != nil && b.selected == selected {
		if selected.invalidatedCode != "" && selected.invalidatedCode != ErrorBrowserDisconnected {
			return nil
		}
		b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, phase)
		return browserDisconnectedErrorForSession(selected, phase, cause)
	}
	if isBrowserDisconnectedTransportError(cause) {
		return browserDisconnectedErrorForSelector(TargetSelector{}, phase, cause)
	}
	return nil
}

func (b *StatefulBroker) promoteBrowserLoss(selected *brokerSession, selector TargetSelector, phase string, cause error) error {
	return b.promoteBrowserLossWithPredicate(selected, selector, phase, cause, isBrowserEndpointLossError)
}

// promoteActivationLoss is intentionally stricter than the general browser
// loss classifier. Target activation is an ancillary foreground operation;
// an adapter can reject it while the attached WebMCP session remains healthy.
// Only an explicit classified loss, a closed transport sentinel, or a
// classified endpoint failure may promote that operation to browser loss.
func (b *StatefulBroker) promoteActivationLoss(selected *brokerSession, selector TargetSelector, phase string, cause error) error {
	return b.promoteBrowserLossWithPredicate(selected, selector, phase, cause, isExplicitBrowserLossError)
}

func isExplicitBrowserLossError(err error) bool {
	if err == nil {
		return false
	}
	return errorContainsCode(err, ErrorBrowserDisconnected) ||
		errorContainsCode(err, ErrorEndpointNotFound) ||
		errorContainsCode(err, ErrorEndpointUnreachable) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed)
}

// selectedLifecycleDisconnect invalidates selected and returns its browser
// disconnect error when it is still selected and its session lifecycle already
// recorded a browser disconnect; it returns nil otherwise.
func (b *StatefulBroker) selectedLifecycleDisconnect(selected *brokerSession, phase string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected {
		return nil
	}
	failure := sessionLifecycleFailure(selected)
	if failure == nil {
		return nil
	}
	if classified, ok := lifecycleClassifiedError(failure); !ok || classified.Code != ErrorBrowserDisconnected {
		return nil
	}
	b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, phase)
	return browserDisconnectedErrorForSession(selected, phase, failure)
}

func (b *StatefulBroker) promoteBrowserLossWithPredicate(selected *brokerSession, selector TargetSelector, phase string, cause error, isLoss func(error) bool) error {
	if selected != nil && selector.BrowserID != "" && selected.context.Key.BrowserID != selector.BrowserID {
		selected = nil
	}
	if selected != nil {
		if err := b.selectedLifecycleDisconnect(selected, phase); err != nil {
			return err
		}
		if !isLoss(cause) {
			return nil
		}
		selected.dispatchMu.Lock()
		defer selected.dispatchMu.Unlock()
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.selected != selected {
			return nil
		}
		return b.browserDisconnectedLocked(selected, phase, cause)
	}
	if !isLoss(cause) {
		return nil
	}
	known := false
	if selector.BrowserID != "" {
		b.mu.Lock()
		state := b.browsers[selector.BrowserID]
		// Discovery records candidates before the first Open succeeds. That
		// observation proves only that an endpoint was advertised, not that
		// this broker established a browser transport. Keep initial dial/open
		// failures in the endpoint error vocabulary until a handle exists.
		known = state != nil && state.handle != nil
		b.mu.Unlock()
	}
	if known {
		return browserDisconnectedErrorForSelector(selector, phase, cause)
	}
	return nil
}
