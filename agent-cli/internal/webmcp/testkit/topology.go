package testkit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	maxPublishedEvents  = 512
	maxTerminalHistory  = 512
	maxChurnReasonBytes = 96
)

var (
	// ErrPublishedEventEvicted means a caller requested an event older than
	// the bounded publication history. The session event channel remains the
	// authoritative stream for callers that need every observation.
	ErrPublishedEventEvicted = errors.New("webmcp testkit: published event evicted")
	// ErrNoActiveTargetSession means a retired session has no current session
	// to which a late event can be injected.
	ErrNoActiveTargetSession = errors.New("webmcp testkit: no active target session")
)

// PublishedEvent is the testkit's publication observation. Sequence is local
// to the runtime publication ledger; Event.Sequence remains the sequence
// assigned by the browser session that produced the event.
type PublishedEvent struct {
	Sequence uint64
	Event    webmcp.BrowserEvent
}

// EventMatcher selects a publication at a deterministic synchronization
// point. A nil matcher accepts every event.
type EventMatcher func(webmcp.BrowserEvent) bool

// TerminalObservation records the point at which a fake target observed a
// terminal invocation outcome. The event may be empty when a configured
// cancellation is acknowledged without a browser response.
type TerminalObservation struct {
	Sequence            uint64
	PublicationSequence uint64
	Event               webmcp.BrowserEvent
	Invocation          InvocationRecord
}

// Navigation is one deterministic page navigation step.
type Navigation struct {
	URL    string
	Origin string
}

// NavigationStep is a descriptive alias for Navigation.
type NavigationStep = Navigation

// browserEndpointKey intentionally keeps only the endpoint address. Endpoint
// paths and browser websocket tokens are routing details, not continuity
// identity, so a same-address replacement can be modelled even when the
// websocket path changes.
func browserEndpointKey(candidate webmcp.BrowserCandidate) string {
	for _, raw := range []string{candidate.BrowserWSURL, candidate.HTTPURL} {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err == nil && parsed.Host != "" {
			return strings.ToLower(parsed.Host)
		}
		return strings.ToLower(value)
	}
	return ""
}

func sessionKey(browserID webmcp.BrowserID, targetID webmcp.TargetID) string {
	return string(browserID) + "\x00" + string(targetID)
}

func endpointSessionKey(endpoint string, targetID webmcp.TargetID) string {
	return endpoint + "\x00" + string(targetID)
}

func boundedChurnReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > maxChurnReasonBytes {
		return reason[:maxChurnReasonBytes]
	}
	return reason
}

func disconnectedError(browserID webmcp.BrowserID, targetID webmcp.TargetID, phase, reason string) error {
	details := map[string]any{
		"browser_id":         string(browserID),
		"target_id":          string(targetID),
		"phase":              boundedChurnReason(phase),
		"reconnect_required": true,
	}
	if reason = boundedChurnReason(reason); reason != "" {
		details["reason"] = reason
	}
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, webmcp.DefaultErrorMessage(webmcp.ErrorBrowserDisconnected), details)
}

func clonePublishedEvent(event PublishedEvent) PublishedEvent {
	event.Event = cloneBrowserEvent(event.Event)
	return event
}

func cloneTerminalObservation(observation TerminalObservation) TerminalObservation {
	observation.Event = cloneBrowserEvent(observation.Event)
	observation.Invocation = cloneInvocationRecord(observation.Invocation)
	return observation
}

func cloneBrowserEvent(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	event.Tools = cloneTools(event.Tools)
	event.RemovedToolNames = append([]string(nil), event.RemovedToolNames...)
	event.Input = cloneBytes(event.Input)
	event.Output = cloneBytes(event.Output)
	return event
}

// publishEvent records an event after it has been admitted to a target event
// channel. The history is bounded and exists only as a synchronization seam;
// it never controls broker behavior.
func (r *ScriptedBrowserRuntime) publishEvent(event webmcp.BrowserEvent) PublishedEvent {
	if r == nil {
		return PublishedEvent{}
	}
	r.eventMu.Lock()
	r.nextEvent++
	publication := PublishedEvent{Sequence: r.nextEvent, Event: cloneBrowserEvent(event)}
	if len(r.publishedEvents) >= maxPublishedEvents {
		copy(r.publishedEvents, r.publishedEvents[1:])
		r.publishedEvents[len(r.publishedEvents)-1] = publication
	} else {
		r.publishedEvents = append(r.publishedEvents, publication)
	}
	close(r.eventChanges)
	r.eventChanges = make(chan struct{})
	r.eventMu.Unlock()
	return clonePublishedEvent(publication)
}

// EventCursor returns the latest runtime publication sequence. Capture it
// before triggering churn, then pass it to WaitForPublishedEvent to await only
// the event caused by that trigger.
func (r *ScriptedBrowserRuntime) EventCursor() uint64 {
	if r == nil {
		return 0
	}
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	return r.nextEvent
}

// PublishedEvents returns a defensive copy of the bounded publication ledger.
func (r *ScriptedBrowserRuntime) PublishedEvents() []PublishedEvent {
	if r == nil {
		return nil
	}
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	result := make([]PublishedEvent, len(r.publishedEvents))
	for i, event := range r.publishedEvents {
		result[i] = clonePublishedEvent(event)
	}
	return result
}

// WaitForPublishedEvent waits for a publication strictly after after. It is
// the event-published synchronization point and does not consume a session's
// event channel.
func (r *ScriptedBrowserRuntime) WaitForPublishedEvent(ctx context.Context, after uint64, match ...EventMatcher) (PublishedEvent, error) {
	if r == nil {
		return PublishedEvent{}, webmcp.ErrClosed
	}
	ctx = nonNilContext(ctx)
	for {
		r.eventMu.Lock()
		oldest := uint64(0)
		if len(r.publishedEvents) > 0 {
			oldest = r.publishedEvents[0].Sequence
		}
		for _, publication := range r.publishedEvents {
			if publication.Sequence <= after || !matchesEvent(publication.Event, match) {
				continue
			}
			result := clonePublishedEvent(publication)
			r.eventMu.Unlock()
			return result, nil
		}
		changes := r.eventChanges
		r.eventMu.Unlock()
		if after != 0 && oldest != 0 && after+1 < oldest {
			return PublishedEvent{}, fmt.Errorf("%w: after=%d oldest=%d", ErrPublishedEventEvicted, after, oldest)
		}
		select {
		case <-ctx.Done():
			return PublishedEvent{}, ctx.Err()
		case <-changes:
		case <-r.closeDone:
			return PublishedEvent{}, webmcp.ErrClosed
		}
	}
}

// WaitForEvent is the concise event-published barrier for callers that do
// not need the runtime publication sequence.
func (r *ScriptedBrowserRuntime) WaitForEvent(ctx context.Context, match ...EventMatcher) (webmcp.BrowserEvent, error) {
	publication, err := r.WaitForPublishedEvent(ctx, 0, match...)
	if err != nil {
		return webmcp.BrowserEvent{}, err
	}
	return cloneBrowserEvent(publication.Event), nil
}

// WaitForEventAfter waits for an event after a captured EventCursor.
func (r *ScriptedBrowserRuntime) WaitForEventAfter(ctx context.Context, after uint64, match ...EventMatcher) (webmcp.BrowserEvent, error) {
	publication, err := r.WaitForPublishedEvent(ctx, after, match...)
	if err != nil {
		return webmcp.BrowserEvent{}, err
	}
	return cloneBrowserEvent(publication.Event), nil
}

func matchesEvent(event webmcp.BrowserEvent, match []EventMatcher) bool {
	for _, matcher := range match {
		if matcher != nil && !matcher(cloneBrowserEvent(event)) {
			return false
		}
	}
	return true
}

// OperationCursor returns the latest admitted operation sequence.
func (r *ScriptedBrowserRuntime) OperationCursor() uint64 {
	if r == nil {
		return 0
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	return r.nextOperation
}

// WaitForOperationAfter waits for an admitted operation strictly after after.
func (r *ScriptedBrowserRuntime) WaitForOperationAfter(ctx context.Context, after uint64, kind OperationKind) (Operation, error) {
	if r == nil {
		return Operation{}, webmcp.ErrClosed
	}
	ctx = nonNilContext(ctx)
	for {
		r.operationMu.Lock()
		for _, operation := range r.operations {
			if operation.Sequence > after && (kind == "" || operation.Kind == kind) {
				result := cloneOperation(operation)
				r.operationMu.Unlock()
				return result, nil
			}
		}
		changes := r.operationChanges
		r.operationMu.Unlock()
		select {
		case <-ctx.Done():
			return Operation{}, ctx.Err()
		case <-changes:
		case <-r.closeDone:
			return Operation{}, webmcp.ErrClosed
		}
	}
}

// WaitForOperationAdmitted names the operation-admitted synchronization point
// and optionally starts after a previously captured operation sequence.
func (r *ScriptedBrowserRuntime) WaitForOperationAdmitted(ctx context.Context, kind OperationKind, after ...uint64) (Operation, error) {
	var cursor uint64
	if len(after) > 0 {
		cursor = after[0]
	}
	return r.WaitForOperationAfter(ctx, cursor, kind)
}

// Browser returns a handle by identity, including a retired handle. Retired
// handles are intentionally inspectable so their late events can be injected
// into a replacement session without changing the old instance.
func (r *ScriptedBrowserRuntime) Browser(browserID webmcp.BrowserID) *ScriptedBrowserHandle {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.browsers[browserID]
}

// Handle is an alias for Browser.
func (r *ScriptedBrowserRuntime) Handle(browserID webmcp.BrowserID) *ScriptedBrowserHandle {
	return r.Browser(browserID)
}

// ReplaceBrowser retires the old identity at the runtime's active lookup
// boundary and installs a fresh browser handle. No session, target, catalog,
// or state is copied from the old instance.
func (r *ScriptedBrowserRuntime) ReplaceBrowser(previousID webmcp.BrowserID, replacement webmcp.BrowserCandidate, targets ...TargetConfig) (*ScriptedBrowserHandle, error) {
	if r == nil {
		return nil, webmcp.ErrClosed
	}
	if replacement.ID == "" || replacement.ID == previousID {
		return nil, ErrInvalidBrowserConfig
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	if _, exists := r.browsers[previousID]; !exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: browser %q", webmcp.ErrBrowserNotFound, previousID)
	}
	if _, exists := r.browsers[replacement.ID]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: browser %q already exists", ErrInvalidBrowserConfig, replacement.ID)
	}
	if endpoint := browserEndpointKey(replacement); endpoint != "" {
		if owner, exists := r.endpoints[endpoint]; exists && owner != previousID && r.active[owner] {
			r.mu.Unlock()
			return nil, fmt.Errorf("%w: endpoint is already owned by browser %q", ErrInvalidBrowserConfig, owner)
		}
	}
	old := r.browsers[previousID]
	handle := newScriptedBrowserHandle(r, NewBrowserConfig(replacement, targets...))
	r.active[previousID] = false
	if endpoint := browserEndpointKey(old.candidate); endpoint != "" && r.endpoints[endpoint] == previousID {
		delete(r.endpoints, endpoint)
	}
	r.browsers[replacement.ID] = handle
	r.active[replacement.ID] = true
	if endpoint := browserEndpointKey(replacement); endpoint != "" {
		r.endpoints[endpoint] = replacement.ID
	}
	r.mu.Unlock()
	r.record(Operation{Kind: OperationReplace, BrowserID: replacement.ID, Reason: string(previousID)})
	return handle, nil
}

// ReplaceEndpoint uses the old candidate as the lookup value and is useful
// when a test is modelling an address-reused endpoint directly.
func (r *ScriptedBrowserRuntime) ReplaceEndpoint(previous webmcp.BrowserCandidate, replacement webmcp.BrowserCandidate, targets ...TargetConfig) (*ScriptedBrowserHandle, error) {
	return r.ReplaceBrowser(previous.ID, replacement, targets...)
}

// ReplaceBrowserHandle is the handle-oriented spelling of ReplaceBrowser.
func (r *ScriptedBrowserRuntime) ReplaceBrowserHandle(previous *ScriptedBrowserHandle, replacement webmcp.BrowserCandidate, targets ...TargetConfig) (*ScriptedBrowserHandle, error) {
	if previous == nil {
		return nil, fmt.Errorf("%w: nil previous handle", webmcp.ErrBrowserNotFound)
	}
	return r.ReplaceBrowser(previous.Candidate().ID, replacement, targets...)
}

// Disconnect terminates one browser handle without closing or replacing any
// other browser instance.
func (r *ScriptedBrowserRuntime) Disconnect(browserID webmcp.BrowserID, reason string) error {
	handle := r.Browser(browserID)
	if handle == nil {
		return fmt.Errorf("%w: %s", webmcp.ErrBrowserNotFound, browserID)
	}
	return handle.Disconnect(reason)
}

func (r *ScriptedBrowserRuntime) retireHandle(handle *ScriptedBrowserHandle) {
	if r == nil || handle == nil {
		return
	}
	candidate := handle.Candidate()
	endpoint := browserEndpointKey(candidate)
	r.mu.Lock()
	if current := r.browsers[candidate.ID]; current == handle {
		r.active[candidate.ID] = false
		if endpoint != "" && r.endpoints[endpoint] == candidate.ID {
			delete(r.endpoints, endpoint)
		}
	}
	r.mu.Unlock()
}

// registerSession makes an attached session discoverable for one-argument
// late-event injection. It does not transfer ownership or state.
func (r *ScriptedBrowserRuntime) registerSession(session *ScriptedTargetSession) {
	if r == nil || session == nil {
		return
	}
	target := session.Target()
	r.mu.Lock()
	r.sessions[sessionKey(target.BrowserID, target.ID)] = session
	if endpoint := browserEndpointKey(session.handle.candidate); endpoint != "" {
		r.sessions[endpointSessionKey(endpoint, target.ID)] = session
	}
	r.mu.Unlock()
}

func (r *ScriptedBrowserRuntime) unregisterSession(session *ScriptedTargetSession) {
	if r == nil || session == nil {
		return
	}
	// sessionClosed calls this while the session mutex is held. Browser and
	// target IDs are immutable for a session, so read those identity fields
	// directly instead of re-entering Target and deadlocking the close path.
	target := session.target
	r.mu.Lock()
	if current := r.sessions[sessionKey(target.BrowserID, target.ID)]; current == session {
		delete(r.sessions, sessionKey(target.BrowserID, target.ID))
	}
	if endpoint := browserEndpointKey(session.handle.candidate); endpoint != "" {
		key := endpointSessionKey(endpoint, target.ID)
		if current := r.sessions[key]; current == session {
			delete(r.sessions, key)
		}
	}
	r.mu.Unlock()
}

func (h *ScriptedBrowserHandle) state() (closed, disconnected bool) {
	if h == nil {
		return true, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed, h.disconnected
}

func (r *ScriptedBrowserRuntime) lateEventDestination(source *ScriptedTargetSession) *ScriptedTargetSession {
	if r == nil || source == nil {
		return nil
	}
	target := source.Target()
	endpoint := browserEndpointKey(source.handle.candidate)
	r.mu.Lock()
	defer r.mu.Unlock()
	candidates := []*ScriptedTargetSession{
		r.sessions[sessionKey(target.BrowserID, target.ID)],
	}
	if endpoint != "" {
		candidates = append(candidates, r.sessions[endpointSessionKey(endpoint, target.ID)])
	}
	for _, candidate := range candidates {
		if candidate != nil && candidate != source && !candidate.isClosed() {
			return candidate
		}
	}
	var only *ScriptedTargetSession
	for _, candidate := range r.sessions {
		if candidate == nil || candidate == source || candidate.Target().ID != target.ID || candidate.isClosed() {
			continue
		}
		if only != nil && only != candidate {
			return nil
		}
		only = candidate
	}
	return only
}

// BlockListTargets holds ListTargets at an admitted operation boundary until
// the test explicitly releases the gate or disconnects the handle.
func (h *ScriptedBrowserHandle) BlockListTargets() {
	if h == nil {
		return
	}
	if h.control == nil {
		return
	}
	h.control.mu.Lock()
	h.control.listBlocked = true
	h.control.mu.Unlock()
}

func (h *ScriptedBrowserHandle) updateTarget(session *ScriptedTargetSession, target webmcp.Target, page webmcp.PageContext) {
	if h == nil || session == nil {
		return
	}
	h.mu.Lock()
	entry := h.targets[target.ID]
	if entry != nil {
		entry.mu.Lock()
		_, attached := entry.sessions[session]
		if attached {
			entry.target = cloneTarget(target)
			entry.config.Target = cloneTarget(target)
			entry.config.Session.Context = page
		}
		entry.mu.Unlock()
	}
	h.mu.Unlock()
}

func (h *ScriptedBrowserHandle) UnblockListTargets() {
	if h == nil {
		return
	}
	if h.control == nil {
		return
	}
	h.control.mu.Lock()
	if h.control.listBlocked {
		h.control.listBlocked = false
		close(h.control.listChanges)
		h.control.listChanges = make(chan struct{})
	}
	h.control.mu.Unlock()
}

// Disconnect closes the browser transport and all attached target sessions.
// It is idempotent and deliberately leaves external targets as detached
// entries; it never issues a target-close operation as cleanup.
func (h *ScriptedBrowserHandle) Disconnect(reasons ...string) error {
	if h == nil {
		return webmcp.ErrClosed
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return webmcp.ErrClosed
	}
	if h.disconnected {
		done := h.disconnectDone
		h.mu.Unlock()
		<-done
		h.mu.Lock()
		err := h.disconnectErr
		h.mu.Unlock()
		return err
	}
	h.disconnected = true
	sessions := make([]*ScriptedTargetSession, 0)
	for _, entry := range h.targets {
		entry.mu.Lock()
		for session := range entry.sessions {
			sessions = append(sessions, session)
		}
		entry.mu.Unlock()
	}
	browserID := h.candidate.ID
	h.mu.Unlock()
	if h.control != nil {
		h.control.mu.Lock()
		h.control.disconnected = true
		if h.control.listBlocked {
			h.control.listBlocked = false
			close(h.control.listChanges)
			h.control.listChanges = make(chan struct{})
		}
		h.control.mu.Unlock()
	}
	h.runtime.retireHandle(h)

	reason := ""
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	if reason == "" {
		reason = "browser_disconnected"
	}
	h.runtime.record(Operation{Kind: OperationDisconnect, BrowserID: browserID, Reason: boundedChurnReason(reason)})
	var joined error
	for _, session := range sessions {
		joined = errors.Join(joined, session.terminate(webmcp.EventBrowserDisconnected, reason, disconnectedError(browserID, session.target.ID, "session", reason)))
	}
	h.mu.Lock()
	h.disconnectErr = joined
	close(h.disconnectDone)
	h.mu.Unlock()
	return joined
}

// DisconnectBrowser is a descriptive alias for Disconnect.
func (h *ScriptedBrowserHandle) DisconnectBrowser(reasons ...string) error {
	return h.Disconnect(reasons...)
}

// IsDisconnected reports whether the transport was explicitly terminated.
func (h *ScriptedBrowserHandle) IsDisconnected() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.disconnected
}

// CloseTarget models an actual page/tab close. It is distinct from closing a
// caller-owned session: the target disappears from ListTargets even when its
// ownership mode is external.
func (h *ScriptedBrowserHandle) CloseTarget(ctx context.Context, targetID webmcp.TargetID) error {
	if h == nil {
		return webmcp.ErrClosed
	}
	ctx = nonNilContext(ctx)
	if err := contextError(ctx); err != nil {
		return err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return webmcp.ErrClosed
	}
	if h.disconnected {
		err := disconnectedError(h.candidate.ID, targetID, "close_target", "browser_disconnected")
		h.mu.Unlock()
		return err
	}
	entry := h.targets[targetID]
	if entry == nil {
		h.mu.Unlock()
		return fmt.Errorf("%w: %s", webmcp.ErrTargetNotFound, targetID)
	}
	entry.mu.Lock()
	if entry.removed {
		entry.mu.Unlock()
		h.mu.Unlock()
		return fmt.Errorf("%w: %s", webmcp.ErrTargetNotFound, targetID)
	}
	sessions := make([]*ScriptedTargetSession, 0, len(entry.sessions))
	for session := range entry.sessions {
		sessions = append(sessions, session)
	}
	generation := entry.target.Generation
	entry.mu.Unlock()
	if len(sessions) == 0 {
		entry.mu.Lock()
		entry.removed = true
		entry.target.Attached = false
		entry.mu.Unlock()
		browserID := h.candidate.ID
		h.mu.Unlock()
		h.runtime.record(Operation{Kind: OperationCloseTarget, BrowserID: browserID, TargetID: targetID, Generation: generation, Reason: "target_closed"})
		return nil
	}
	h.mu.Unlock()
	var joined error
	for _, session := range sessions {
		joined = errors.Join(joined, session.terminateWithOptions(webmcp.EventTargetDetached, "target_closed", webmcp.ErrClosed, true, true))
	}
	return joined
}

// ClosePage and DestroyTarget are descriptive aliases for CloseTarget.
func (h *ScriptedBrowserHandle) ClosePage(ctx context.Context, targetID webmcp.TargetID) error {
	return h.CloseTarget(ctx, targetID)
}

func (h *ScriptedBrowserHandle) DestroyTarget(ctx context.Context, targetID webmcp.TargetID) error {
	return h.CloseTarget(ctx, targetID)
}

func (s *ScriptedTargetSession) shouldRemoveTarget() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeTargetOnClose
}

func (s *ScriptedTargetSession) emitPublished(event webmcp.BrowserEvent) (PublishedEvent, error) {
	if s == nil {
		return PublishedEvent{}, webmcp.ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return PublishedEvent{}, webmcp.ErrClosed
	}
	return s.emitPublishedLocked(event, false)
}

// BlockEnableWebMCP holds the enable operation after admission. Disconnect
// also releases it, which makes selection-loss races deterministic.
func (s *ScriptedTargetSession) BlockEnableWebMCP() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.enableBlocked = true
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) UnblockEnableWebMCP() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.enableBlocked {
		s.enableBlocked = false
		close(s.enableChanges)
		s.enableChanges = make(chan struct{})
	}
	s.mu.Unlock()
}

// WaitForInvocationAdmission names the operation-admitted point already
// exposed by WaitForInvocation.
func (s *ScriptedTargetSession) WaitForInvocationAdmission(ctx context.Context) (InvocationRecord, error) {
	return s.WaitForInvocation(ctx)
}

// WaitForTerminalObservation waits until the target has published a terminal
// response or lifecycle outcome for id.
func (s *ScriptedTargetSession) WaitForTerminalObservation(ctx context.Context, id webmcp.InvocationID) (TerminalObservation, error) {
	if s == nil {
		return TerminalObservation{}, webmcp.ErrClosed
	}
	if id == "" {
		return TerminalObservation{}, fmt.Errorf("%w: empty invocation ID", webmcp.ErrInvocationNotFound)
	}
	ctx = nonNilContext(ctx)
	for {
		s.mu.Lock()
		if observation, ok := s.terminalObserved[id]; ok {
			result := cloneTerminalObservation(observation)
			s.mu.Unlock()
			return result, nil
		}
		if _, ok := s.invokes[id]; !ok {
			s.mu.Unlock()
			return TerminalObservation{}, fmt.Errorf("%w: %s", webmcp.ErrInvocationNotFound, id)
		}
		changes := s.terminalChanges
		done := s.done
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return TerminalObservation{}, ctx.Err()
		case <-changes:
		case <-done:
			// terminate marks all unresolved records before closing done. A
			// second pass observes that lifecycle publication; if publication
			// failed, return the session's terminal error instead of hanging.
			if err := s.Err(); err != nil {
				return TerminalObservation{}, err
			}
		}
	}
}

// WaitForTerminal is the concise terminal-observed barrier.
func (s *ScriptedTargetSession) WaitForTerminal(ctx context.Context, id webmcp.InvocationID) (InvocationRecord, error) {
	observation, err := s.WaitForTerminalObservation(ctx, id)
	if err != nil {
		return InvocationRecord{}, err
	}
	return cloneInvocationRecord(observation.Invocation), nil
}

// TerminalObservations returns a bounded defensive snapshot of terminal
// outcomes observed by this session.
func (s *ScriptedTargetSession) TerminalObservations() []TerminalObservation {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]TerminalObservation, len(s.terminalHistory))
	for i, observation := range s.terminalHistory {
		result[i] = cloneTerminalObservation(observation)
	}
	return result
}

func (s *ScriptedTargetSession) markTerminalObserved(id webmcp.InvocationID, publication PublishedEvent) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	if _, exists := s.terminalObserved[id]; exists {
		s.mu.Unlock()
		return
	}
	record, exists := s.invokes[id]
	if !exists || !record.Terminal {
		s.mu.Unlock()
		return
	}
	observation := TerminalObservation{
		Sequence:            publication.Event.Sequence,
		PublicationSequence: publication.Sequence,
		Event:               cloneBrowserEvent(publication.Event),
		Invocation:          cloneInvocationRecord(*record),
	}
	s.terminalObserved[id] = observation
	if len(s.terminalHistory) >= maxTerminalHistory {
		copy(s.terminalHistory, s.terminalHistory[1:])
		s.terminalHistory[len(s.terminalHistory)-1] = observation
	} else {
		s.terminalHistory = append(s.terminalHistory, observation)
	}
	close(s.terminalChanges)
	s.terminalChanges = make(chan struct{})
	s.mu.Unlock()
}

// InjectLateEvent publishes an event produced by this session into the
// current session for the same target/address. The source identity,
// generation, and producer sequence are preserved; the destination's fake
// catalog and invocation state are not mutated.
func (s *ScriptedTargetSession) InjectLateEvent(event webmcp.BrowserEvent) error {
	if s == nil {
		return webmcp.ErrClosed
	}
	destination := s.runtime.lateEventDestination(s)
	if destination == nil {
		return ErrNoActiveTargetSession
	}
	return s.InjectLateEventInto(destination, event)
}

// InjectLateEventInto targets a specific current session, which is useful
// when two browser identities deliberately share a target ID.
func (s *ScriptedTargetSession) InjectLateEventInto(destination *ScriptedTargetSession, event webmcp.BrowserEvent) error {
	if s == nil || destination == nil {
		return ErrNoActiveTargetSession
	}
	produced := s.produceEvent(event)
	return destination.admitInjectedEvent(produced)
}

// EmitLateEvent is an alias for InjectLateEvent.
func (s *ScriptedTargetSession) EmitLateEvent(event webmcp.BrowserEvent) error {
	return s.InjectLateEvent(event)
}

func (s *ScriptedTargetSession) produceEvent(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	s.mu.Lock()
	produced := s.decorateProducedEventLocked(event)
	s.mu.Unlock()
	return produced
}

func (s *ScriptedTargetSession) admitInjectedEvent(event webmcp.BrowserEvent) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	select {
	case s.events <- cloneBrowserEvent(event):
		s.mu.Unlock()
		s.runtime.publishEvent(event)
		return nil
	default:
		s.mu.Unlock()
		return webmcp.ErrEventBufferFull
	}
}

// NavigateSequence emits each step synchronously in the order supplied.
func (s *ScriptedTargetSession) NavigateSequence(steps ...Navigation) error {
	for _, step := range steps {
		if err := s.Navigate(step.URL, step.Origin); err != nil {
			return err
		}
	}
	return nil
}

// EmitNavigationSequence is an event-oriented alias for NavigateSequence.
func (s *ScriptedTargetSession) EmitNavigationSequence(steps ...Navigation) error {
	return s.NavigateSequence(steps...)
}

// NavigateBurst accepts URL-only navigation steps for compact churn tests.
func (s *ScriptedTargetSession) NavigateBurst(urls ...string) error {
	steps := make([]Navigation, len(urls))
	for i, value := range urls {
		steps[i] = Navigation{URL: value}
	}
	return s.NavigateSequence(steps...)
}

// EmitNavigationBurst is an alias for NavigateBurst.
func (s *ScriptedTargetSession) EmitNavigationBurst(urls ...string) error {
	return s.NavigateBurst(urls...)
}

// CloseTarget explicitly closes the target represented by this session. It
// differs from Close for an external session, whose ordinary close remains a
// detach-only release.
func (s *ScriptedTargetSession) CloseTarget() error {
	if s == nil {
		return webmcp.ErrClosed
	}
	return s.terminateWithOptions(webmcp.EventTargetDetached, "target_closed", webmcp.ErrClosed, true, true)
}

// EmitTargetClosed is a descriptive alias for CloseTarget.
func (s *ScriptedTargetSession) EmitTargetClosed() error { return s.CloseTarget() }

func (s *ScriptedTargetSession) Detach(reason string) error {
	return s.terminate(webmcp.EventTargetDetached, reason, webmcp.ErrClosed)
}

func (s *ScriptedTargetSession) Disconnect(reasons ...string) error {
	reason := ""
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	if reason == "" {
		reason = "browser_disconnected"
	}
	return s.terminate(webmcp.EventBrowserDisconnected, reason, disconnectedError(s.target.BrowserID, s.target.ID, "session", reason))
}

func (s *ScriptedTargetSession) EmitTargetDetached(reason string) error { return s.Detach(reason) }

func (s *ScriptedTargetSession) EmitDisconnected(reasons ...string) error {
	return s.Disconnect(reasons...)
}

func (s *ScriptedTargetSession) decorateProducedEventLocked(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	if event.Sequence == 0 {
		s.sequence++
		event.Sequence = s.sequence
	} else if event.Sequence > s.sequence {
		s.sequence = event.Sequence
	}
	if event.BrowserID == "" {
		event.BrowserID = s.target.BrowserID
	}
	if event.TargetID == "" {
		event.TargetID = s.target.ID
	}
	if event.Generation == 0 {
		event.Generation = s.context.Generation
	}
	for i := range event.Tools {
		if event.Tools[i].BrowserID == "" {
			event.Tools[i].BrowserID = s.target.BrowserID
		}
		if event.Tools[i].TargetID == "" {
			event.Tools[i].TargetID = s.target.ID
		}
		if event.Tools[i].Generation == 0 {
			event.Tools[i].Generation = event.Generation
		}
		if event.Tools[i].Origin == "" {
			event.Tools[i].Origin = s.context.Origin
		}
	}
	return s.runtime.decorateEvent(event, s.target.BrowserID, s.target.ID, s.context.Generation, event.Sequence)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
