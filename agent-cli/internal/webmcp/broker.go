package webmcp

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultBrokerWatchBuffer = 64
	maxToolRefMintAttempts   = 64
	initialCatalogWait       = time.Second
)

// BrokerOptions supplies the browser-neutral seams used by StatefulBroker.
// Runtime and Discoverer are intentionally interfaces so this package never
// needs a browser protocol dependency. A nil Discoverer is useful for tests
// whose runtime already knows the candidate by ID.
type BrokerOptions struct {
	Runtime    BrowserRuntime
	Discoverer BrowserDiscoverer
	IDs        IDSource
	// ToolRefFactory optionally derives references from a normalized page-tool
	// descriptor. When omitted, IDs.NewToolRef provides random references.
	ToolRefFactory ToolRefFactory
	Clock          Clock
	// Timers drives invocation deadlines. When omitted, a Clock that also
	// implements TimerFactory is used; production falls back to wall time.
	Timers TimerFactory
	// CancelOnInterrupt controls target cancellation caused by an invocation
	// context ending. The explicit Broker.Cancel operation always honors the
	// caller's direct cancellation request. Empty uses the C0 read-only policy.
	CancelOnInterrupt string
	Ownership         TargetOwnership
	WatchBuffer       int
	// MaxInputBytes bounds the serialized UTF-8 input_json sent to a page
	// tool. Zero uses DefaultMaxInputBytes.
	MaxInputBytes int
	// MaxResultBytes bounds the serialized textual result envelope returned
	// for a completed page invocation. Zero uses DefaultMaxResultBytes.
	MaxResultBytes int
	// InvocationTimeout is the default deadline for admitted invocations. Zero
	// uses DefaultInvocationTimeout.
	InvocationTimeout time.Duration
}

// StatefulBroker owns selection, page catalog, generation, and session-local
// ToolRef state. Browser calls happen outside the broker mutex; all catalog
// and reference transitions are reconciled back through that mutex.
type StatefulBroker struct {
	mu                sync.Mutex
	runtime           BrowserRuntime
	discoverer        BrowserDiscoverer
	ids               IDSource
	toolRefFactory    ToolRefFactory
	clock             Clock
	timers            TimerFactory
	cancelOnInterrupt string
	ownership         TargetOwnership
	watchBuffer       int
	maxInputBytes     int
	maxResultBytes    int
	invocationTimeout time.Duration

	browsers map[BrowserID]*browserState
	selected *brokerSession
	refs     map[ToolRef]refRecord
	retired  map[ToolRef]struct{}

	invocations          map[InvocationID]*brokerInvocation
	terminalResults      map[InvocationID]terminalInvocation
	terminalOrder        []InvocationID
	terminalSeen         map[InvocationID]struct{}
	terminalSeenOrder    []InvocationID
	browserInvocations   map[InvocationID]*brokerInvocation
	browserTerminalSeen  map[InvocationID]struct{}
	browserTerminalOrder []InvocationID
	earlyTerminals       map[InvocationID]terminalObservation
	earlyTerminalOrder   []InvocationID

	eventSequence uint64
	watchers      map[chan BrokerEvent]struct{}
	closed        bool
	closedCh      chan struct{}
	closeDone     chan struct{}
	closeErr      error
	wg            sync.WaitGroup
}

type browserState struct {
	candidate BrowserCandidate
	handle    BrowserHandle
}

type brokerSession struct {
	handle  BrowserHandle
	session TargetSession
	target  Target
	context PageContext

	active       bool
	catalog      map[catalogKey]ToolDescriptor
	catalogError error

	// dispatchMu establishes a linearization point between the final ref
	// check and a page command. Lifecycle/catalog events wait for it before
	// retiring a binding, so an event cannot be observed halfway through the
	// final validation/dispatch transition.
	dispatchMu sync.Mutex
	flush      chan chan struct{}
	loopDone   chan struct{}

	queue           []*brokerInvocation
	queueWake       chan struct{}
	queueStop       chan struct{}
	queueWorkerDone chan struct{}
	current         *brokerInvocation
	queueClosed     bool
	// observedInvocations tracks page invocations initiated by another
	// command-scoped broker. The browser event stream is shared by attached
	// DevTools clients, so a watch command can report external invocation
	// lifecycle events without claiming ownership of their result.
	observedInvocations map[InvocationID]ToolRef
	catalogSignal       chan struct{}
}

type catalogKey struct {
	frame FrameID
	name  string
}

type refRecord struct {
	binding    ToolRefBinding
	descriptor ToolDescriptor
	key        catalogKey
}

// ToolRefBinding is the complete semantic identity protected by a session-
// local ToolRef. It deliberately contains exactly the six C0 binding fields.
type ToolRefBinding struct {
	BrowserID    BrowserID
	TargetID     TargetID
	FrameID      FrameID
	Generation   uint64
	ToolName     string
	SchemaDigest string
}

// NewBroker constructs a browser-neutral stateful broker. It does not dial a
// browser until a discovery/list/select operation needs one.
func NewBroker(options BrokerOptions) *StatefulBroker {
	ids := options.IDs
	if ids == nil {
		ids = randomIDs{}
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock{}
	}
	timers := options.Timers
	if timers == nil {
		if clockTimers, ok := clock.(TimerFactory); ok {
			timers = clockTimers
		} else {
			timers = wallTimerFactory{}
		}
	}
	cancelOnInterrupt := options.CancelOnInterrupt
	if cancelOnInterrupt == "" {
		cancelOnInterrupt = CancelOnInterruptReadOnly
	}
	ownership := options.Ownership
	if ownership == "" {
		ownership = TargetOwnershipExternal
	}
	watchBuffer := options.WatchBuffer
	if watchBuffer <= 0 {
		watchBuffer = defaultBrokerWatchBuffer
	}
	maxInputBytes := options.MaxInputBytes
	if maxInputBytes <= 0 {
		maxInputBytes = DefaultMaxInputBytes
	}
	maxResultBytes := options.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = DefaultMaxResultBytes
	}
	invocationTimeout := options.InvocationTimeout
	if invocationTimeout <= 0 {
		invocationTimeout = DefaultInvocationTimeout
	}
	return &StatefulBroker{
		runtime:             options.Runtime,
		discoverer:          options.Discoverer,
		ids:                 ids,
		toolRefFactory:      options.ToolRefFactory,
		clock:               clock,
		timers:              timers,
		cancelOnInterrupt:   cancelOnInterrupt,
		ownership:           ownership,
		watchBuffer:         watchBuffer,
		maxInputBytes:       maxInputBytes,
		maxResultBytes:      maxResultBytes,
		invocationTimeout:   invocationTimeout,
		browsers:            make(map[BrowserID]*browserState),
		refs:                make(map[ToolRef]refRecord),
		retired:             make(map[ToolRef]struct{}),
		invocations:         make(map[InvocationID]*brokerInvocation),
		terminalResults:     make(map[InvocationID]terminalInvocation),
		terminalSeen:        make(map[InvocationID]struct{}),
		browserInvocations:  make(map[InvocationID]*brokerInvocation),
		browserTerminalSeen: make(map[InvocationID]struct{}),
		earlyTerminals:      make(map[InvocationID]terminalObservation),
		watchers:            make(map[chan BrokerEvent]struct{}),
		closedCh:            make(chan struct{}),
		closeDone:           make(chan struct{}),
	}
}

// NewStatefulBroker is a descriptive constructor alias.
func NewStatefulBroker(options BrokerOptions) *StatefulBroker { return NewBroker(options) }

// New is a concise constructor alias for callers that use the package as a
// broker implementation rather than only as a contract package.
func New(options BrokerOptions) *StatefulBroker { return NewBroker(options) }

// NewBrokerWithRuntime is a convenience constructor for small tests and
// adapters that already have their runtime and discoverer separated.
func NewBrokerWithRuntime(runtime BrowserRuntime, discoverer BrowserDiscoverer, options ...BrokerOptions) *StatefulBroker {
	resolved := BrokerOptions{Runtime: runtime, Discoverer: discoverer}
	if len(options) > 0 {
		resolved = options[0]
		if resolved.Runtime == nil {
			resolved.Runtime = runtime
		}
		if resolved.Discoverer == nil {
			resolved.Discoverer = discoverer
		}
	}
	return NewBroker(resolved)
}

var _ Broker = (*StatefulBroker)(nil)

// Discover obtains candidates from the injected discovery seam and retains
// their normalized identity for exact later selection.
func (b *StatefulBroker) Discover(ctx context.Context, options DiscoverOptions) ([]BrowserCandidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrClosed
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	discoverer := b.discoverer
	b.mu.Unlock()
	if discoverer == nil {
		return nil, classified(ErrorEndpointNotFound, "browser endpoint was not found", map[string]any{
			"endpoint_kind": "configured",
			"source":        string(DiscoverySourceConfigured),
		}, nil)
	}
	candidates, err := discoverer.Discover(ctx, options)
	if err != nil {
		return nil, err
	}
	filtered := make([]BrowserCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if options.BrowserID != "" && candidate.ID != options.BrowserID {
			continue
		}
		filtered = append(filtered, cloneBrowserCandidate(candidate))
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrClosed
	}
	for _, candidate := range filtered {
		state := b.browsers[candidate.ID]
		if state == nil {
			state = &browserState{}
			b.browsers[candidate.ID] = state
		}
		state.candidate = candidate
	}
	if options.BrowserID != "" && len(filtered) == 0 {
		return nil, classified(ErrorEndpointNotFound, "browser endpoint was not found", map[string]any{
			"browser_id": string(options.BrowserID),
		}, ErrBrowserNotFound)
	}
	return cloneBrowserCandidates(filtered), nil
}

// ListTargets refreshes one exact browser target list. It never silently
// selects a different candidate when BrowserID is supplied.
func (b *StatefulBroker) ListTargets(ctx context.Context, selector BrowserSelector) ([]Target, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	candidate, err := b.candidateFor(ctx, selector.BrowserID)
	if err != nil {
		return nil, err
	}
	handle, err := b.handleFor(ctx, candidate)
	if err != nil {
		return nil, err
	}
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		return nil, classified(ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
			"browser_id":          string(candidate.ID),
			"target_id":           "",
			"selected_generation": uint64(0),
			"reason":              "target_list_failed",
		}, err)
	}
	for i := range targets {
		if targets[i].BrowserID == "" {
			targets[i].BrowserID = candidate.ID
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	return cloneTargets(targets), nil
}

// Select attaches the exact requested target and starts consuming its
// semantic event stream before enabling WebMCP.
func (b *StatefulBroker) Select(ctx context.Context, selector TargetSelector) (PageContext, error) {
	return b.selectWithOptions(ctx, selector, SelectOptions{})
}

// SelectWithOptions is an optional extension used by the stable tool adapter
// when a caller asks the browser to activate the selected target.
func (b *StatefulBroker) SelectWithOptions(ctx context.Context, selector TargetSelector, options SelectOptions) (PageContext, error) {
	return b.selectWithOptions(ctx, selector, options)
}

func (b *StatefulBroker) selectWithOptions(ctx context.Context, selector TargetSelector, options SelectOptions) (PageContext, error) {
	if err := contextError(ctx); err != nil {
		return PageContext{}, err
	}
	if selector.BrowserID == "" || selector.TargetID == "" {
		return PageContext{}, staleSelectionError(selector.BrowserID, selector.TargetID, 0, "exact_browser_and_target_required")
	}
	if b == nil {
		return PageContext{}, ErrClosed
	}
	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return PageContext{}, ErrClosed
	}
	if current := b.selected; current != nil && current.active &&
		current.context.Key.BrowserID == selector.BrowserID && current.context.Key.TargetID == selector.TargetID {
		handle := current.handle
		contextValue := clonePageContext(current.context)
		b.mu.Unlock()
		if options.Activate {
			if err := handle.Activate(ctx, selector.TargetID); err != nil {
				return PageContext{}, staleSelectionError(selector.BrowserID, selector.TargetID, contextValue.Generation, "activation_failed")
			}
		}
		return contextValue, nil
	}
	b.mu.Unlock()

	candidate, err := b.candidateFor(ctx, selector.BrowserID)
	if err != nil {
		return PageContext{}, err
	}
	handle, err := b.handleFor(ctx, candidate)
	if err != nil {
		return PageContext{}, err
	}
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		return PageContext{}, targetAttachError(selector, "list_targets", err)
	}
	target, ok := findTarget(targets, selector.TargetID)
	if !ok {
		return PageContext{}, staleSelectionError(selector.BrowserID, selector.TargetID, 0, "target_not_present")
	}
	if target.BrowserID == "" {
		target.BrowserID = candidate.ID
	}
	if options.Activate {
		if err := handle.Activate(ctx, selector.TargetID); err != nil {
			return PageContext{}, targetAttachError(selector, "activate", err)
		}
	}
	session, err := handle.Attach(ctx, selector.TargetID, b.ownershipValue())
	if err != nil {
		return PageContext{}, targetAttachError(selector, "attach", err)
	}
	page := session.Context()
	page = normalizePageContext(page, candidate.ID, target)
	if page.SelectedAt.IsZero() {
		page.SelectedAt = b.clock.Now()
	}
	newSession := &brokerSession{
		handle:              handle,
		session:             session,
		target:              cloneTarget(target),
		context:             page,
		active:              true,
		catalog:             make(map[catalogKey]ToolDescriptor),
		flush:               make(chan chan struct{}),
		loopDone:            make(chan struct{}),
		queueWake:           make(chan struct{}, 1),
		queueStop:           make(chan struct{}),
		queueWorkerDone:     make(chan struct{}),
		observedInvocations: make(map[InvocationID]ToolRef),
		catalogSignal:       make(chan struct{}),
	}
	if page.CatalogReady {
		close(newSession.catalogSignal)
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		_ = session.Close()
		return PageContext{}, ErrClosed
	}
	old := b.selected
	if old != nil {
		b.retireSessionLocked(old, "target_switch")
	}
	b.selected = newSession
	b.emitLocked(BrokerEvent{Type: BrokerEventSelected, BrowserID: page.Key.BrowserID, TargetID: page.Key.TargetID, Generation: page.Generation, Reason: "selected"})
	b.wg.Add(2)
	go b.runSession(newSession)
	go b.runInvocationQueue(newSession)
	b.mu.Unlock()

	if old != nil {
		_ = old.session.Close()
	}
	if err := session.EnableWebMCP(ctx); err != nil {
		b.invalidateSession(newSession, "enable_failed")
		_ = session.Close()
		return PageContext{}, targetAttachError(selector, "enable_webmcp", err)
	}
	b.flushSession(newSession)
	b.syncSessionReadiness(newSession)
	if err := b.waitForInitialCatalog(ctx, newSession); err != nil {
		b.invalidateSession(newSession, "catalog_wait_canceled")
		_ = session.Close()
		if isCatalogEvidenceError(err) {
			return PageContext{}, err
		}
		return PageContext{}, targetAttachError(selector, "catalog", err)
	}
	b.flushSession(newSession)
	b.mu.Lock()
	if b.selected == newSession && newSession.active {
		b.updateReadinessLocked(newSession)
	}
	page = clonePageContext(newSession.context)
	b.mu.Unlock()
	return page, nil
}

// waitForInitialCatalog gives the browser a bounded opportunity to deliver
// affirmative page-tool evidence triggered by WebMCP.enable. A timeout is a
// diagnostic failure: an empty catalog is valid only when the page explicitly
// proves that its catalog is ready.
func (b *StatefulBroker) waitForInitialCatalog(ctx context.Context, selected *brokerSession) error {
	if selected == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	if selected.context.CatalogReady {
		b.mu.Unlock()
		return nil
	}
	signal := selected.catalogSignal
	loopDone := selected.loopDone
	b.mu.Unlock()
	timer := time.NewTimer(initialCatalogWait)
	defer timer.Stop()
	select {
	case <-signal:
		return nil
	case <-loopDone:
		if err := selected.session.Err(); err != nil {
			var classifiedErr *ClassifiedError
			if errors.As(err, &classifiedErr) && classifiedErr != nil {
				switch classifiedErr.Code {
				case ErrorBrowserDisconnected, ErrorTargetDetached:
					return err
				}
			}
		}
		return b.catalogEvidenceError(selected, "session_ended")
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return b.catalogEvidenceError(selected, "deadline_exceeded")
	}
}

func (b *StatefulBroker) syncSessionReadiness(selected *brokerSession) {
	if selected == nil {
		return
	}
	page := selected.session.Context()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected || !selected.active {
		return
	}
	// EnableWebMCP returned successfully, so this is domain support evidence.
	// It is deliberately recorded independently from page-tool/catalog
	// readiness, which still requires an affirmative page observation.
	selected.context.WebMCPDomainSupported = true
	if page.CatalogReady {
		b.markCatalogReadyLocked(selected, page.CatalogEvidence)
	}
	b.updateReadinessLocked(selected)
}

func (b *StatefulBroker) updateReadinessLocked(selected *brokerSession) {
	if selected == nil {
		return
	}
	selected.context.Ready = selected.active && selected.context.Connected &&
		selected.context.WebMCPDomainSupported && selected.context.CatalogReady
}

func (b *StatefulBroker) markCatalogReadyLocked(selected *brokerSession, evidence string) {
	if selected == nil {
		return
	}
	selected.context.WebMCPDomainSupported = true
	if !selected.context.CatalogReady {
		selected.context.CatalogReady = true
		if selected.catalogSignal != nil {
			close(selected.catalogSignal)
		}
	}
	if selected.context.CatalogEvidence == "" {
		selected.context.CatalogEvidence = evidence
	}
	b.updateReadinessLocked(selected)
}

func (b *StatefulBroker) catalogEvidenceError(selected *brokerSession, reason string) error {
	details := map[string]any{
		"phase":           "catalog",
		"reason_code":     "page_tools_unverified",
		"webmcp_domain":   "supported",
		"page_tools":      "unverified",
		"catalog":         "unverified",
		"deadline_ms":     int(initialCatalogWait / time.Millisecond),
		"evidence_needed": "affirmative page producer/catalog-ready observation",
		"reason":          reason,
	}
	if selected != nil {
		b.mu.Lock()
		details["browser_id"] = string(selected.context.Key.BrowserID)
		details["target_id"] = string(selected.context.Key.TargetID)
		details["generation"] = selected.context.Generation
		b.mu.Unlock()
	}
	return classified(ErrorBrowserProtocol, "the WebMCP domain is supported, but the selected page did not provide affirmative page-tool catalog evidence before the diagnostic deadline", details, nil)
}

func isCatalogEvidenceError(err error) bool {
	var classifiedErr *ClassifiedError
	if !errors.As(err, &classifiedErr) || classifiedErr == nil || classifiedErr.Code != ErrorBrowserProtocol {
		return false
	}
	return classifiedErr.Details != nil && classifiedErr.Details["reason_code"] == "page_tools_unverified"
}

// Selected returns the current selection after reconciling any already
// queued lifecycle/catalog events.
func (b *StatefulBroker) Selected(ctx context.Context) (PageContext, error) {
	return b.SelectedWithRefresh(ctx, false)
}

// SelectedWithRefresh is an optional extension for webmcp_get_context.
func (b *StatefulBroker) SelectedWithRefresh(ctx context.Context, refresh bool) (PageContext, error) {
	if err := contextError(ctx); err != nil {
		return PageContext{}, err
	}
	if b == nil {
		return PageContext{}, ErrClosed
	}
	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return PageContext{}, ErrClosed
	}
	selected := b.selected
	b.mu.Unlock()
	if selected == nil || !selected.active || !selected.context.Connected {
		return PageContext{}, staleSelectionForSession(selected, "selection_not_connected")
	}
	if refresh {
		if err := selected.session.EnableWebMCP(ctx); err != nil {
			return PageContext{}, targetAttachError(TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err)
		}
		b.flushSession(selected)
		b.syncSessionReadiness(selected)
		if err := b.waitForInitialCatalog(ctx, selected); err != nil {
			if isCatalogEvidenceError(err) {
				return PageContext{}, err
			}
			return PageContext{}, targetAttachError(TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected || !selected.active || !selected.context.Connected {
		return PageContext{}, staleSelectionForSession(selected, "selection_changed")
	}
	return clonePageContext(selected.context), nil
}

// ListTools returns the current selected page catalog. Refresh re-enables the
// semantic catalog stream; the stable broker definitions remain elsewhere.
func (b *StatefulBroker) ListTools(ctx context.Context, options ListToolsOptions) (ToolCatalogSnapshot, error) {
	if err := contextError(ctx); err != nil {
		return ToolCatalogSnapshot{}, err
	}
	if b == nil {
		return ToolCatalogSnapshot{}, ErrClosed
	}
	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ToolCatalogSnapshot{}, ErrClosed
	}
	selected := b.selected
	b.mu.Unlock()
	if selected == nil || !selected.active || !selected.context.Connected {
		return ToolCatalogSnapshot{}, staleSelectionForSession(selected, "selection_not_connected")
	}
	if options.Refresh {
		if err := selected.session.EnableWebMCP(ctx); err != nil {
			return ToolCatalogSnapshot{}, targetAttachError(TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err)
		}
		b.flushSession(selected)
		b.syncSessionReadiness(selected)
		if err := b.waitForInitialCatalog(ctx, selected); err != nil {
			if isCatalogEvidenceError(err) {
				return ToolCatalogSnapshot{}, err
			}
			return ToolCatalogSnapshot{}, targetAttachError(TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected || !selected.active || !selected.context.Connected {
		return ToolCatalogSnapshot{}, staleSelectionForSession(selected, "selection_changed")
	}
	if selected.catalogError != nil {
		return ToolCatalogSnapshot{}, classified(ErrorBrowserProtocol, "the page catalog is invalid", map[string]any{
			"phase":       "catalog",
			"protocol":    "webmcp",
			"reason_code": "invalid_descriptor",
		}, selected.catalogError)
	}
	tools := make([]ToolDescriptor, 0, len(selected.catalog))
	for _, descriptor := range selected.catalog {
		if options.NameContains != "" && !strings.Contains(descriptor.Name, options.NameContains) {
			continue
		}
		if options.FrameID != "" && descriptor.FrameID != options.FrameID {
			continue
		}
		tools = append(tools, cloneToolDescriptor(descriptor))
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].FrameID != tools[j].FrameID {
			return tools[i].FrameID < tools[j].FrameID
		}
		return tools[i].Name < tools[j].Name
	})
	return ToolCatalogSnapshot{Context: clonePageContext(selected.context), Generation: selected.context.Generation, Tools: tools}, nil
}

// Invoke admits one validated page call into the selected target's FIFO. The
// call returns once the page command has received its invocation ID; terminal
// browser results are reconciled asynchronously by the session event loop.
func (b *StatefulBroker) Invoke(ctx context.Context, request InvokeRequest) (InvokeResult, error) {
	return b.admitInvocation(ctx, request)
}

// Cancel requests cancellation of a registered invocation. The invocation
// registry, rather than the caller's selected target, determines where the
// browser cancellation is sent.
func (b *StatefulBroker) Cancel(ctx context.Context, request CancelRequest) error {
	return b.cancelInvocation(ctx, request)
}

// Watch subscribes to bounded broker lifecycle observations. Dropped
// observations never affect catalog correctness; the stateful API remains
// authoritative for snapshots and invocation checks.
func (b *StatefulBroker) Watch(ctx context.Context) <-chan BrokerEvent {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan BrokerEvent, defaultBrokerWatchBuffer)
	if b == nil {
		close(out)
		return out
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(out)
		return out
	}
	if cap(out) != b.watchBuffer {
		out = make(chan BrokerEvent, b.watchBuffer)
	}
	b.watchers[out] = struct{}{}
	closedCh := b.closedCh
	b.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			b.removeWatcher(out)
		case <-closedCh:
			b.removeWatcher(out)
		}
	}()
	return out
}

// Close retires every session-local reference and closes the broker-owned
// handles. It is idempotent; a later call returns the first aggregate error.
func (b *StatefulBroker) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		done := b.closeDone
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		err := b.closeErr
		b.mu.Unlock()
		return err
	}
	b.closed = true
	close(b.closedCh)
	selected := b.selected
	if selected != nil {
		b.retireSessionLocked(selected, "broker_close")
	}
	handles := make([]BrowserHandle, 0, len(b.browsers))
	seenHandles := make(map[BrowserHandle]struct{}, len(b.browsers))
	for _, state := range b.browsers {
		if state.handle == nil {
			continue
		}
		if _, seen := seenHandles[state.handle]; seen {
			continue
		}
		seenHandles[state.handle] = struct{}{}
		handles = append(handles, state.handle)
	}
	for watcher := range b.watchers {
		delete(b.watchers, watcher)
		close(watcher)
	}
	b.mu.Unlock()

	var joined error
	if selected != nil {
		joined = errors.Join(joined, selected.session.Close())
	}
	for _, handle := range handles {
		joined = errors.Join(joined, handle.Close())
	}
	b.wg.Wait()
	b.mu.Lock()
	b.closeErr = joined
	close(b.closeDone)
	b.mu.Unlock()
	return joined
}

func (b *StatefulBroker) candidateFor(ctx context.Context, browserID BrowserID) (BrowserCandidate, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return BrowserCandidate{}, ErrClosed
	}
	if state := b.browsers[browserID]; browserID != "" && state != nil && state.candidate.ID != "" {
		candidate := cloneBrowserCandidate(state.candidate)
		b.mu.Unlock()
		return candidate, nil
	}
	discoverer := b.discoverer
	b.mu.Unlock()
	if browserID == "" {
		if discoverer == nil {
			return BrowserCandidate{}, staleSelectionError("", "", 0, "browser_id_required")
		}
		candidates, err := b.Discover(ctx, DiscoverOptions{})
		if err != nil {
			return BrowserCandidate{}, err
		}
		if len(candidates) != 1 {
			return BrowserCandidate{}, classified(ErrorAmbiguousBrowser, "multiple browsers matched; an exact browser ID is required", map[string]any{
				"candidate_browser_ids": browserIDs(candidates),
			}, nil)
		}
		return candidates[0], nil
	}
	if discoverer != nil {
		candidates, err := b.Discover(ctx, DiscoverOptions{BrowserID: browserID})
		if err != nil {
			return BrowserCandidate{}, err
		}
		if len(candidates) == 1 {
			return candidates[0], nil
		}
	}
	// A runtime fake may intentionally be keyed by the exact ID without a
	// discovery implementation. This fallback does not enumerate or choose a
	// different browser.
	return BrowserCandidate{ID: browserID}, nil
}

func (b *StatefulBroker) handleFor(ctx context.Context, candidate BrowserCandidate) (BrowserHandle, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	state := b.browsers[candidate.ID]
	if state == nil {
		state = &browserState{candidate: cloneBrowserCandidate(candidate)}
		b.browsers[candidate.ID] = state
	}
	if state.handle != nil {
		handle := state.handle
		b.mu.Unlock()
		return handle, nil
	}
	runtime := b.runtime
	b.mu.Unlock()
	if runtime == nil {
		return nil, classified(ErrorEndpointNotFound, "browser endpoint was not found", map[string]any{
			"endpoint_kind": "runtime",
			"source":        string(candidate.Source),
		}, ErrBrowserNotFound)
	}
	handle, err := runtime.Open(ctx, candidate)
	if err != nil {
		return nil, classified(ErrorEndpointUnreachable, "browser endpoint could not be reached", map[string]any{
			"endpoint_kind": "runtime",
			"address_class": addressClass(candidate),
			"phase":         "open",
		}, err)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		_ = handle.Close()
		return nil, ErrClosed
	}
	state = b.browsers[candidate.ID]
	if state.handle == nil {
		state.handle = handle
		state.candidate = cloneBrowserCandidate(candidate)
		b.mu.Unlock()
		return handle, nil
	}
	current := state.handle
	b.mu.Unlock()
	_ = handle.Close()
	return current, nil
}

func (b *StatefulBroker) ownershipValue() TargetOwnership {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ownership == "" {
		return TargetOwnershipExternal
	}
	return b.ownership
}

func (b *StatefulBroker) flushSelected() {
	b.mu.Lock()
	selected := b.selected
	b.mu.Unlock()
	if selected != nil {
		b.flushSession(selected)
	}
}

func (b *StatefulBroker) flushSession(selected *brokerSession) {
	if selected == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case selected.flush <- ack:
		<-ack
	case <-selected.loopDone:
	}
}

func (b *StatefulBroker) runSession(selected *brokerSession) {
	defer b.wg.Done()
	defer close(selected.loopDone)
	events := selected.session.Events()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				b.drainSessionEvents(selected, events)
				b.markSessionEnded(selected, "events_closed")
				return
			}
			b.applyBrowserEvent(selected, event)
		case <-selected.session.Done():
			b.drainSessionEvents(selected, events)
			b.markSessionEnded(selected, "session_done")
			return
		case ack := <-selected.flush:
			b.drainSessionEvents(selected, events)
			close(ack)
		}
	}
}

func (b *StatefulBroker) drainSessionEvents(selected *brokerSession, events <-chan BrowserEvent) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			b.applyBrowserEvent(selected, event)
		default:
			return
		}
	}
}

func (b *StatefulBroker) applyBrowserEvent(selected *brokerSession, event BrowserEvent) {
	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.selected != selected || !selected.active {
		return
	}
	if event.BrowserID != "" && event.BrowserID != selected.context.Key.BrowserID {
		return
	}
	if event.TargetID != "" && event.TargetID != selected.context.Key.TargetID {
		return
	}
	switch event.Type {
	case EventTargetAttached:
		selected.context.Connected = true
	case EventToolsAdded:
		b.applyToolsAddedLocked(selected, event)
	case EventToolsRemoved:
		b.applyToolsRemovedLocked(selected, event)
	case EventCatalogReady:
		b.applyCatalogReadyLocked(selected, event)
	case EventToolInvoked:
		b.observeBrowserInvocationLocked(selected, event)
	case EventToolResponded:
		if b.reconcileObservedBrowserResponseLocked(selected, event) {
			return
		}
		b.reconcileBrowserResponseLocked(selected, event)
	case EventPageNavigated, EventFrameNavigated:
		b.applyGenerationChangeLocked(selected, event, string(event.Type))
	case EventTargetDetached:
		b.invalidateSessionWithCodeLocked(selected, ErrorTargetDetached, event.Reason)
	case EventBrowserDisconnected:
		b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, event.Reason)
	case EventSessionClosed:
		b.invalidateSessionLocked(selected, event.Reason)
	}
}

// observeBrowserInvocationLocked turns a protocol invocation initiated by a
// different command-scoped broker into a safe lifecycle observation. Direct
// CLI commands intentionally create a fresh broker for every process, while
// Chrome broadcasts target events to every attached DevTools session. The
// invoking broker already owns its ID and is therefore ignored here; a watch
// broker records only the opaque invocation ID and the catalog-bound ref.
func (b *StatefulBroker) observeBrowserInvocationLocked(selected *brokerSession, event BrowserEvent) {
	if selected == nil || event.InvocationID == "" {
		return
	}
	if _, owned := b.browserInvocations[event.InvocationID]; owned {
		return
	}
	if _, observed := selected.observedInvocations[event.InvocationID]; observed {
		return
	}
	if _, terminal := b.browserTerminalSeen[event.InvocationID]; terminal {
		return
	}

	var ref ToolRef
	if descriptor, ok := observedToolDescriptorLocked(selected, event); ok {
		ref = descriptor.Ref
	}
	selected.observedInvocations[event.InvocationID] = ref
	b.emitLocked(BrokerEvent{
		Type:         BrokerEventInvocationCreated,
		BrowserID:    selected.context.Key.BrowserID,
		TargetID:     selected.context.Key.TargetID,
		Generation:   selected.context.Generation,
		InvocationID: event.InvocationID,
		ToolRef:      ref,
		State:        InvocationDispatched,
		Reason:       "browser_observed",
	})
}

func (b *StatefulBroker) reconcileObservedBrowserResponseLocked(selected *brokerSession, event BrowserEvent) bool {
	if selected == nil || event.InvocationID == "" {
		return false
	}
	if _, owned := b.browserInvocations[event.InvocationID]; owned {
		return false
	}
	ref, observed := selected.observedInvocations[event.InvocationID]
	if !observed {
		return false
	}
	delete(selected.observedInvocations, event.InvocationID)
	state, _ := terminalState(event.Status)
	reason := event.ErrorCode
	if reason == "" {
		reason = event.Reason
	}
	if reason == "" {
		reason = strings.ToLower(strings.TrimSpace(event.Status))
	}
	b.emitLocked(BrokerEvent{
		Type:         BrokerEventInvocationTerminal,
		At:           event.At,
		BrowserID:    selected.context.Key.BrowserID,
		TargetID:     selected.context.Key.TargetID,
		Generation:   selected.context.Generation,
		InvocationID: event.InvocationID,
		ToolRef:      ref,
		State:        state,
		Reason:       reason,
	})
	return true
}

func observedToolDescriptorLocked(selected *brokerSession, event BrowserEvent) (ToolDescriptor, bool) {
	if selected == nil {
		return ToolDescriptor{}, false
	}
	if event.FrameID != "" {
		if descriptor, ok := selected.catalog[catalogKey{frame: event.FrameID, name: event.ToolName}]; ok {
			return descriptor, true
		}
	}
	var match ToolDescriptor
	for key, descriptor := range selected.catalog {
		if key.name != event.ToolName {
			continue
		}
		if match.Ref != "" {
			return ToolDescriptor{}, false
		}
		match = descriptor
	}
	return match, match.Ref != ""
}

func (b *StatefulBroker) applyToolsAddedLocked(selected *brokerSession, event BrowserEvent) {
	if event.Generation > selected.context.Generation {
		b.advanceGenerationLocked(selected, event.Generation, "catalog_generation")
	}
	if event.Generation != 0 && event.Generation < selected.context.Generation {
		return
	}
	changed := false
	affirmative := false
	for _, input := range event.Tools {
		descriptor, err := normalizeToolDescriptor(input, selected.context)
		if err != nil {
			selected.catalogError = err
			continue
		}
		affirmative = true
		if descriptor.Generation != selected.context.Generation {
			if descriptor.Generation > selected.context.Generation {
				b.advanceGenerationLocked(selected, descriptor.Generation, "descriptor_generation")
			} else {
				continue
			}
		}
		key := catalogKey{frame: descriptor.FrameID, name: descriptor.Name}
		if current, ok := selected.catalog[key]; ok && descriptorEqual(current, descriptor) {
			continue
		}
		if current, ok := selected.catalog[key]; ok {
			b.retireRefLocked(current.Ref)
		}
		ref, err := b.mintToolRefLocked(descriptor)
		if err != nil {
			selected.catalogError = err
			continue
		}
		b.eventSequence++
		descriptor.Ref = ref
		descriptor.AddedSequence = b.eventSequence
		selected.catalog[key] = cloneToolDescriptor(descriptor)
		b.refs[ref] = refRecord{binding: bindingFor(descriptor), descriptor: cloneToolDescriptor(descriptor), key: key}
		changed = true
	}
	if changed {
		b.emitLocked(BrokerEvent{Type: BrokerEventCatalogChanged, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: selected.context.Generation, Reason: "tools_added"})
	}
	if affirmative {
		b.markCatalogReadyLocked(selected, "tools_added")
	}
}

func (b *StatefulBroker) applyToolsRemovedLocked(selected *brokerSession, event BrowserEvent) {
	if event.Generation > selected.context.Generation {
		b.advanceGenerationLocked(selected, event.Generation, "catalog_generation")
	}
	if event.Generation != 0 && event.Generation < selected.context.Generation {
		return
	}
	changed := false
	for _, name := range event.RemovedToolNames {
		key := catalogKey{frame: event.FrameID, name: name}
		if current, ok := selected.catalog[key]; ok {
			b.retireRefLocked(current.Ref)
			delete(selected.catalog, key)
			changed = true
		}
	}
	if changed {
		b.emitLocked(BrokerEvent{Type: BrokerEventCatalogChanged, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: selected.context.Generation, Reason: "tools_removed"})
	}
	if len(event.RemovedToolNames) > 0 {
		b.markCatalogReadyLocked(selected, "tools_removed")
	}
}

func (b *StatefulBroker) applyCatalogReadyLocked(selected *brokerSession, event BrowserEvent) {
	if event.Generation > selected.context.Generation {
		b.advanceGenerationLocked(selected, event.Generation, "catalog_generation")
	}
	if event.Generation != 0 && event.Generation < selected.context.Generation {
		return
	}
	if len(event.Tools) > 0 {
		b.applyToolsAddedLocked(selected, event)
	}
	b.markCatalogReadyLocked(selected, "page_producer")
}

func (b *StatefulBroker) applyGenerationChangeLocked(selected *brokerSession, event BrowserEvent, reason string) {
	next := event.Generation
	if next <= selected.context.Generation {
		next = selected.context.Generation + 1
		if next == 0 {
			next = selected.context.Generation
		}
	}
	b.advanceGenerationLocked(selected, next, reason)
	contextValue := selected.session.Context()
	if contextValue.Key.BrowserID == "" {
		contextValue.Key.BrowserID = selected.context.Key.BrowserID
	}
	if contextValue.Key.TargetID == "" {
		contextValue.Key.TargetID = selected.context.Key.TargetID
	}
	contextValue.Generation = selected.context.Generation
	contextValue.Connected = true
	contextValue.WebMCPDomainSupported = selected.context.WebMCPDomainSupported || contextValue.WebMCPDomainSupported
	contextValue.CatalogReady = false
	contextValue.CatalogEvidence = ""
	contextValue.Ready = false
	if contextValue.SelectedAt.IsZero() {
		contextValue.SelectedAt = selected.context.SelectedAt
	}
	selected.context = contextValue
}

func (b *StatefulBroker) advanceGenerationLocked(selected *brokerSession, generation uint64, reason string) {
	if generation <= selected.context.Generation {
		return
	}
	selected.context.Generation = generation
	b.terminalizeSessionInvocationsLocked(selected, ErrorPageNavigated, reason)
	b.retireCatalogLocked(selected)
	selected.context.CatalogReady = false
	selected.context.CatalogEvidence = ""
	selected.catalogSignal = make(chan struct{})
	selected.context.Ready = false
	b.emitLocked(BrokerEvent{Type: BrokerEventGenerationChanged, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: generation, Reason: reason})
}

func (b *StatefulBroker) invalidateSession(selected *brokerSession, reason string) {
	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected == selected {
		b.invalidateSessionLocked(selected, reason)
	}
}

func (b *StatefulBroker) invalidateSessionLocked(selected *brokerSession, reason string) {
	b.invalidateSessionWithCodeLocked(selected, lifecycleInvocationErrorCode(reason, ErrorInvocationOrphaned), reason)
}

func (b *StatefulBroker) invalidateSessionWithCodeLocked(selected *brokerSession, code ErrorCode, reason string) {
	if !selected.active {
		return
	}
	b.terminalizeSessionInvocationsLocked(selected, code, reason)
	closeInvocationQueueLocked(selected)
	b.retireCatalogLocked(selected)
	selected.active = false
	selected.context.Connected = false
	selected.context.CatalogReady = false
	selected.context.CatalogEvidence = ""
	selected.context.Ready = false
	if reason == "" {
		reason = "session_closed"
	}
	b.emitLocked(BrokerEvent{Type: BrokerEventGenerationChanged, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: selected.context.Generation, Reason: reason})
}

func (b *StatefulBroker) markSessionEnded(selected *brokerSession, reason string) {
	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected == selected && selected.active {
		b.invalidateSessionLocked(selected, reason)
	}
}

func (b *StatefulBroker) retireSessionLocked(selected *brokerSession, reason string) {
	if selected == nil {
		return
	}
	b.terminalizeSessionInvocationsLocked(selected, ErrorInvocationOrphaned, reason)
	closeInvocationQueueLocked(selected)
	b.retireCatalogLocked(selected)
	selected.active = false
	selected.context.Connected = false
	selected.context.CatalogReady = false
	selected.context.CatalogEvidence = ""
	selected.context.Ready = false
	if reason != "" {
		b.emitLocked(BrokerEvent{Type: BrokerEventGenerationChanged, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: selected.context.Generation, Reason: reason})
	}
}

func (b *StatefulBroker) retireCatalogLocked(selected *brokerSession) {
	for _, descriptor := range selected.catalog {
		b.retireRefLocked(descriptor.Ref)
	}
	selected.catalog = make(map[catalogKey]ToolDescriptor)
}

func (b *StatefulBroker) retireRefLocked(ref ToolRef) {
	if ref == "" {
		return
	}
	delete(b.refs, ref)
	b.retired[ref] = struct{}{}
}

func (b *StatefulBroker) mintToolRefLocked(descriptor ToolDescriptor) (ToolRef, error) {
	for attempt := 0; attempt < maxToolRefMintAttempts; attempt++ {
		var (
			ref ToolRef
			err error
		)
		if b.toolRefFactory != nil {
			ref, err = b.toolRefFactory(descriptor)
		} else {
			ref, err = b.ids.NewToolRef()
		}
		if err != nil {
			return "", err
		}
		if err := validateToolRefSyntax(ref); err != nil {
			continue
		}
		if _, active := b.refs[ref]; active {
			continue
		}
		if _, wasRetired := b.retired[ref]; wasRetired {
			// A stable factory cannot produce a second value for the same
			// descriptor. Fall back to the configured ID source after a
			// same-generation remove/re-add so a retired ref stays stale.
			if b.toolRefFactory != nil {
				ref, err = b.ids.NewToolRef()
				if err != nil {
					return "", err
				}
				if err := validateToolRefSyntax(ref); err != nil {
					continue
				}
				if _, active := b.refs[ref]; active {
					continue
				}
				if _, wasRetired := b.retired[ref]; wasRetired {
					continue
				}
				return ref, nil
			}
			continue
		}
		return ref, nil
	}
	return "", errors.New("webmcp: tool ref source did not produce a unique valid ref")
}

func staleSelectionError(browserID BrowserID, targetID TargetID, generation uint64, reason string) error {
	return classified(ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
		"browser_id":          string(browserID),
		"target_id":           string(targetID),
		"selected_generation": generation,
		"reason":              reason,
	}, ErrStaleSelection)
}

func staleSelectionForSession(selected *brokerSession, reason string) error {
	if selected == nil {
		return staleSelectionError("", "", 0, reason)
	}
	return staleSelectionError(selected.context.Key.BrowserID, selected.context.Key.TargetID, selected.context.Generation, reason)
}

func targetAttachError(selector TargetSelector, phase string, cause error) error {
	return classified(ErrorTargetAttachFailed, "the selected browser target could not be initialized", map[string]any{
		"browser_id":  string(selector.BrowserID),
		"target_id":   string(selector.TargetID),
		"phase":       phase,
		"reason_code": "attach_failed",
	}, cause)
}

func classified(code ErrorCode, message string, details map[string]any, cause error) error {
	err := NewClassifiedError(code, message, details)
	err.Cause = cause
	return err
}

func (b *StatefulBroker) emitLocked(event BrokerEvent) {
	if b.closed {
		return
	}
	b.eventSequence++
	event.Version = BrowserEventsVersion
	event.Sequence = b.eventSequence
	if event.At.IsZero() {
		event.At = b.clock.Now()
	}
	for watcher := range b.watchers {
		select {
		case watcher <- event:
		default:
		}
	}
}

func (b *StatefulBroker) removeWatcher(watcher chan BrokerEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.watchers[watcher]; !ok {
		return
	}
	delete(b.watchers, watcher)
	close(watcher)
}
