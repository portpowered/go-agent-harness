package webmcp

import (
	"errors"
	"io"
	"net"
	"strings"
)

func selectionStateErrorLocked(selected *brokerSession, phase, reason string) error {
	if selected != nil && selected.invalidatedCode == ErrorBrowserDisconnected {
		return browserDisconnectedErrorForSession(selected, phase, nil)
	}
	return staleSelectionForSession(selected, reason)
}

func (b *StatefulBroker) selectedStateError(selected *brokerSession, phase, reason string) error {
	if selected == nil {
		return staleSelectionForSession(nil, reason)
	}
	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected {
		return staleSelectionForSession(selected, reason)
	}
	cause := selected.session.Err()
	if selected.invalidatedCode == ErrorBrowserDisconnected || isBrowserEndpointLossError(cause) {
		b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, phase)
		return browserDisconnectedErrorForSession(selected, phase, cause)
	}
	if !selected.active || !selected.context.Connected {
		return staleSelectionForSession(selected, reason)
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
	if len(phase) > 32 {
		phase = phase[:32]
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
	if selected != nil && selector.BrowserID != "" && selected.context.Key.BrowserID != selector.BrowserID {
		selected = nil
	}
	if selected != nil {
		b.mu.Lock()
		if b.selected == selected {
			if failure := sessionLifecycleFailure(selected); failure != nil {
				if classified, ok := lifecycleClassifiedError(failure); ok && classified.Code == ErrorBrowserDisconnected {
					b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, phase)
					b.mu.Unlock()
					return browserDisconnectedErrorForSession(selected, phase, failure)
				}
			}
		}
		b.mu.Unlock()
		if !isBrowserEndpointLossError(cause) {
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
	if !isBrowserEndpointLossError(cause) {
		return nil
	}
	known := false
	if selector.BrowserID != "" {
		b.mu.Lock()
		state := b.browsers[selector.BrowserID]
		known = state != nil && state.candidate.ID != ""
		b.mu.Unlock()
	}
	if known || isBrowserDisconnectedTransportError(cause) {
		return browserDisconnectedErrorForSelector(selector, phase, cause)
	}
	return nil
}
