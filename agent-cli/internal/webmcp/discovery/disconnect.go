package discovery

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
)

const maxDisconnectReason = 64

// DisconnectEvent is the neutral browser-connection loss notification. IDs
// must already be normalized; raw endpoint values and transport errors stay at
// the injected seam and are never copied into a classified result.
type DisconnectEvent struct {
	BrowserID string
	TargetID  string
	Phase     string
	Reason    string
	Cause     error `json:"-"`
	Err       error `json:"-"`
}

// BrowserDisconnectEvent and DisconnectRequest are descriptive aliases for
// adapters that name the same notification differently.
type BrowserDisconnectEvent = DisconnectEvent
type DisconnectRequest = DisconnectEvent
type BrowserDisconnectRequest = DisconnectEvent

// BrowserDisconnectedError lets an injected browser/runtime seam identify a
// transport loss while retaining safe normalized identity for classification.
// Error text intentionally contains no endpoint, URL, or underlying cause.
type BrowserDisconnectedError struct {
	BrowserID string
	TargetID  string
	Phase     string
	Cause     error `json:"-"`
	Err       error `json:"-"`
}

// BrowserDisconnectError is a compatibility spelling for BrowserDisconnectedError.
type BrowserDisconnectError = BrowserDisconnectedError

func (e *BrowserDisconnectedError) Error() string {
	if e == nil {
		return "browser connection disconnected"
	}
	return "browser connection disconnected"
}

func (e *BrowserDisconnectedError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause != nil {
		return e.Cause
	}
	return e.Err
}

func (e *BrowserDisconnectedError) Is(target error) bool {
	if e == nil {
		return false
	}
	var codeErr *classifiedCode
	return errors.As(target, &codeErr) && codeErr.code == CodeBrowserDisconnected
}

// NewBrowserDisconnectedError constructs a safe marker for injected seams.
func NewBrowserDisconnectedError(browserID, targetID, phase string, cause error) error {
	return &BrowserDisconnectedError{
		BrowserID: browserID,
		TargetID:  targetID,
		Phase:     phase,
		Cause:     cause,
	}
}

// NewBrowserDisconnectError is a concise constructor alias.
func NewBrowserDisconnectError(browserID, targetID, phase string, cause error) error {
	return NewBrowserDisconnectedError(browserID, targetID, phase, cause)
}

// IsBrowserDisconnected reports whether an injected error represents loss of
// the browser connection. EOF and a closed network connection are included so
// simple neutral fakes need not import a browser websocket package.
func IsBrowserDisconnected(err error) bool {
	return isBrowserDisconnected(err)
}

func isBrowserDisconnected(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrBrowserDisconnected) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
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

func browserDisconnectMetadata(err error) (browserID, targetID, phase string) {
	var marker *BrowserDisconnectedError
	if errors.As(err, &marker) && marker != nil {
		return marker.BrowserID, marker.TargetID, marker.Phase
	}
	var discoveryErr *DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil && discoveryErr.Code == CodeBrowserDisconnected {
		return detailString(discoveryErr.Details, "browser_id"), detailString(discoveryErr.Details, "target_id"), detailString(discoveryErr.Details, "phase")
	}
	return "", "", ""
}

func newBrowserDisconnectedFromError(err error, fallbackBrowserID, fallbackTargetID, fallbackPhase string) *DiscoveryError {
	browserID, targetID, phase := browserDisconnectMetadata(err)
	if !publicIDPattern.MatchString(strings.TrimSpace(browserID)) {
		browserID = fallbackBrowserID
	}
	if !publicIDPattern.MatchString(strings.TrimSpace(targetID)) {
		targetID = fallbackTargetID
	}
	if strings.TrimSpace(phase) == "" {
		phase = fallbackPhase
	}
	return newBrowserDisconnected(browserID, targetID, phase, err)
}

func enrichBrowserDisconnected(failure *DiscoveryError, fallbackBrowserID, fallbackTargetID, fallbackPhase string) *DiscoveryError {
	if failure == nil || failure.Code != CodeBrowserDisconnected {
		return failure
	}
	return newBrowserDisconnectedFromError(failure, fallbackBrowserID, fallbackTargetID, fallbackPhase)
}

func classifySelectionOperationError(err error, browserID, targetID, phase, reason string) *DiscoveryError {
	if isBrowserDisconnected(err) {
		return newBrowserDisconnectedFromError(err, browserID, targetID, phase)
	}
	var discoveryErr *DiscoveryError
	if errors.As(err, &discoveryErr) {
		return discoveryErr
	}
	return newTargetAttachFailed(browserID, targetID, phase, reason, err)
}

func detailString(details map[string]any, key string) string {
	if details == nil {
		return ""
	}
	value, ok := details[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func normalizeDisconnectEvent(event DisconnectEvent) (DisconnectEvent, *DiscoveryError) {
	event.BrowserID = strings.TrimSpace(event.BrowserID)
	event.TargetID = strings.TrimSpace(event.TargetID)
	if event.BrowserID == "" || hasControl(event.BrowserID) || !publicIDPattern.MatchString(event.BrowserID) {
		return DisconnectEvent{}, newProtocolInvalidAt("disconnect", "unknown", "normalized_browser_id_required", nil)
	}
	if event.TargetID != "" && (hasControl(event.TargetID) || !publicIDPattern.MatchString(event.TargetID)) {
		return DisconnectEvent{}, newProtocolInvalidAt("disconnect", "unknown", "normalized_target_id_required", nil)
	}
	event.Phase = boundedLabel(event.Phase, 32)
	if event.Phase == "" {
		event.Phase = "disconnect"
	}
	event.Reason = boundedLabel(event.Reason, maxDisconnectReason)
	return event, nil
}

func disconnectEventInput(input any, args []any) (DisconnectEvent, *DiscoveryError) {
	var event DisconnectEvent
	switch value := input.(type) {
	case DisconnectEvent:
		event = value
	case *DisconnectEvent:
		if value == nil {
			return DisconnectEvent{}, newProtocolInvalidAt("disconnect", "unknown", "disconnect_event_required", nil)
		}
		event = *value
	case BrowserCandidate:
		event.BrowserID = value.ID
	case string:
		event.BrowserID = value
	default:
		return DisconnectEvent{}, newProtocolInvalidAt("disconnect", "unknown", "disconnect_event_required", nil)
	}
	for index, argument := range args {
		value, ok := argument.(string)
		if !ok {
			return DisconnectEvent{}, newProtocolInvalidAt("disconnect", "unknown", "disconnect_argument_invalid", nil)
		}
		switch index {
		case 0:
			event.TargetID = value
		case 1:
			event.Phase = value
		case 2:
			event.Reason = value
		default:
			return DisconnectEvent{}, newProtocolInvalidAt("disconnect", "unknown", "too_many_disconnect_arguments", nil)
		}
	}
	return normalizeDisconnectEvent(event)
}

// HandleDisconnect marks one browser connection unavailable and returns the
// classified failure that callers should surface. The flexible input accepts a
// DisconnectEvent or the convenient (browserID, targetID, phase) spelling.
func (s *Service) HandleDisconnect(ctx context.Context, input any, args ...any) (Selection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Selection{}, err
	}
	event, failure := disconnectEventInput(input, args)
	if failure != nil {
		return Selection{}, failure
	}
	s.mu.Lock()
	s.markBrowserDisconnectedLocked(event.BrowserID, event.TargetID, event.Phase)
	selection := s.currentSelectionLocked()
	s.mu.Unlock()
	return selection, newBrowserDisconnected(event.BrowserID, event.TargetID, event.Phase, disconnectEventCause(event))
}

// HandleBrowserDisconnect is the browser-specific spelling of HandleDisconnect.
func (s *Service) HandleBrowserDisconnect(ctx context.Context, input any, args ...any) (Selection, error) {
	return s.HandleDisconnect(ctx, input, args...)
}

// Disconnect is a concise notification alias.
func (s *Service) Disconnect(ctx context.Context, input any, args ...any) (Selection, error) {
	return s.HandleDisconnect(ctx, input, args...)
}

// OnDisconnect is an adapter-facing notification alias.
func (s *Service) OnDisconnect(ctx context.Context, input any, args ...any) (Selection, error) {
	return s.HandleDisconnect(ctx, input, args...)
}

// MarkBrowserDisconnected records a loss without requiring callers to know
// the event struct spelling.
func (s *Service) MarkBrowserDisconnected(ctx context.Context, input any, args ...any) (Selection, error) {
	return s.HandleDisconnect(ctx, input, args...)
}

func disconnectEventCause(event DisconnectEvent) error {
	if event.Cause != nil {
		return event.Cause
	}
	return event.Err
}

type browserDisconnectState struct {
	TargetID string
	Phase    string
}

func (s *Service) markBrowserDisconnectedLocked(browserID, targetID, phase string) {
	browserID = strings.TrimSpace(browserID)
	if !publicIDPattern.MatchString(browserID) {
		return
	}
	if s.disconnected == nil {
		s.disconnected = make(map[string]browserDisconnectState)
	}
	state := s.disconnected[browserID]
	if publicIDPattern.MatchString(strings.TrimSpace(targetID)) {
		state.TargetID = strings.TrimSpace(targetID)
	}
	state.Phase = boundedLabel(phase, 32)
	if state.Phase == "" {
		state.Phase = "disconnect"
	}
	s.disconnected[browserID] = state
	if s.selection != nil && s.selection.BrowserID == browserID {
		selection := *s.selection
		selection.statusSet = true
		selection.connected = false
		selection.ready = false
		s.selection = &selection
	}
}

func (s *Service) noteBrowserDisconnectedFailureLocked(failure *DiscoveryError, fallbackBrowserID, fallbackTargetID, fallbackPhase string) {
	if failure == nil || failure.Code != CodeBrowserDisconnected {
		return
	}
	browserID := detailString(failure.Details, "browser_id")
	if !publicIDPattern.MatchString(browserID) || browserID == "unknown" {
		browserID = fallbackBrowserID
	}
	targetID := detailString(failure.Details, "target_id")
	if !publicIDPattern.MatchString(targetID) {
		targetID = fallbackTargetID
	}
	phase := detailString(failure.Details, "phase")
	if phase == "" {
		phase = fallbackPhase
	}
	s.markBrowserDisconnectedLocked(browserID, targetID, phase)
}

func (s *Service) browserDisconnectedFailureLocked(browserID, targetID, phase string) *DiscoveryError {
	if s == nil || s.disconnected == nil {
		return nil
	}
	browserID = strings.TrimSpace(browserID)
	state, ok := s.disconnected[browserID]
	if !ok {
		return nil
	}
	if targetID == "" {
		targetID = state.TargetID
	}
	if phase == "" {
		phase = state.Phase
	}
	return newBrowserDisconnected(browserID, targetID, phase, nil)
}

func (s *Service) clearBrowserDisconnectedLocked(browserID string) {
	if s != nil && s.disconnected != nil {
		delete(s.disconnected, strings.TrimSpace(browserID))
	}
}

func (s *Service) advanceDisconnectedSelectionGenerationLocked(browserID, targetID string, target *Target) *DiscoveryError {
	if s == nil || target == nil || s.disconnected == nil {
		return nil
	}
	if _, disconnected := s.disconnected[browserID]; !disconnected || s.selection == nil || s.selection.BrowserID != browserID || s.selection.TargetID != targetID || s.selection.connected {
		return nil
	}
	previous := s.selection.Generation
	if previous == 0 {
		previous = target.Generation
	}
	if previous == ^uint64(0) || target.Generation == ^uint64(0) {
		return newSelectionStateError("generation_exhausted", nil)
	}
	current := target.Generation + 1
	if current <= previous {
		current = previous + 1
	}
	target.Generation = current
	state := s.targets[browserID][targetID]
	state.target = *target
	state.generation = current
	state.closed = false
	s.storeLifecycleTargetLocked(browserID, targetID, state)
	s.emitTarget(EventPageGenerationChanged, browserID, targetID, current, map[string]any{
		"previous_generation": previous,
		"current_generation":  current,
		"reason":              "reconnect",
	})
	return nil
}

// IsBrowserDisconnected reports whether the service has an invalidated
// connection for the exact normalized browser ID.
func (s *Service) IsBrowserDisconnected(browserID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.disconnected[strings.TrimSpace(browserID)]
	return ok
}

func (s *Service) browserIDForEndpoint(endpoint Endpoint) string {
	if s == nil {
		return ""
	}
	if raw := strings.TrimSpace(endpoint.BrowserWSEndpoint); raw != "" {
		if normalized, failure := parseBrowserWebSocketURL(raw); failure == nil {
			identity := BrowserIdentity{
				Scheme: normalized.url.Scheme,
				Host:   normalized.url.Hostname(),
				Port:   normalized.url.Port(),
				Path:   normalized.url.EscapedPath(),
			}
			return normalizePublicID(s.idMapper.BrowserID(identity), identity)
		}
	}
	if raw := strings.TrimSpace(endpoint.CDPURL); raw != "" {
		if parsed, failure := parseHTTPURL(raw); failure == nil {
			base := targetListBaseURL(parsed)
			for browserID, known := range s.endpoints {
				if known.httpURL == base {
					return browserID
				}
			}
			identity := BrowserIdentity{
				Scheme: parsed.Scheme,
				Host:   parsed.Hostname(),
				Port:   parsed.Port(),
				Path:   parsed.EscapedPath(),
			}
			return normalizePublicID(s.idMapper.BrowserID(identity), identity)
		}
	}
	return ""
}
