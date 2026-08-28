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
	watchers      map[*brokerWatcher]struct{}
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

	active            bool
	invalidatedCode   ErrorCode
	invalidatedReason string
	catalog           map[catalogKey]ToolDescriptor
	catalogError      error

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
	observedInvocations map[InvocationID]observedInvocation
	catalogObserved     bool
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

// observedInvocation is the target-owned lifecycle identity retained by a
// watcher. It has no local admission record; the target event stream is the
// only source of truth for its protocol ID and terminal state.
type observedInvocation struct {
	browserID  BrowserID
	targetID   TargetID
	generation uint64
	toolRef    ToolRef
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
		watchers:            make(map[*brokerWatcher]struct{}),
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
	if b == nil {
		return nil, ErrClosed
	}
	if selected := b.selectedForBrowser(selector.BrowserID); selected != nil {
		if err := b.selectedStateError(selected, "list_targets", "selection_not_connected"); err != nil {
			return nil, err
		}
	}
	candidate, err := b.candidateFor(ctx, selector.BrowserID)
	if err != nil {
		if failure := b.promoteBrowserLoss(b.selectedForBrowser(selector.BrowserID), TargetSelector{BrowserID: selector.BrowserID}, "discover", err); failure != nil {
			return nil, failure
		}
		return nil, err
	}
	handle, err := b.handleFor(ctx, candidate)
	if err != nil {
		if failure := b.promoteBrowserLoss(b.selectedForBrowser(candidate.ID), TargetSelector{BrowserID: candidate.ID}, "open", err); failure != nil {
			return nil, failure
		}
		return nil, err
	}
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		if failure := b.promoteBrowserLoss(b.selectedForBrowser(candidate.ID), TargetSelector{BrowserID: candidate.ID}, "list_targets", err); failure != nil {
			return nil, failure
		}
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
	current := b.selected
	if current != nil && current.active &&
		current.context.Key.BrowserID == selector.BrowserID && current.context.Key.TargetID == selector.TargetID {
		handle := current.handle
		contextValue := clonePageContext(current.context)
		b.mu.Unlock()
		if err := b.selectedStateError(current, "lifecycle", "selection_not_connected"); err != nil {
			return PageContext{}, err
		}
		if options.Activate {
			if err := handle.Activate(ctx, selector.TargetID); err != nil {
				if failure := b.promoteBrowserLoss(current, selector, "activate", err); failure != nil {
					return PageContext{}, failure
				}
				return PageContext{}, staleSelectionError(selector.BrowserID, selector.TargetID, contextValue.Generation, "activation_failed")
			}
		}
		return contextValue, nil
	}
	b.mu.Unlock()
	if current != nil && current.context.Key.BrowserID == selector.BrowserID {
		if err := b.selectedStateError(current, "selection", "selection_not_connected"); err != nil {
			return PageContext{}, err
		}
	}

	candidate, err := b.candidateFor(ctx, selector.BrowserID)
	if err != nil {
		if failure := b.promoteBrowserLoss(current, selector, "discover", err); failure != nil {
			return PageContext{}, failure
		}
		return PageContext{}, err
	}
	handle, err := b.handleFor(ctx, candidate)
	if err != nil {
		if failure := b.promoteBrowserLoss(current, selector, "open", err); failure != nil {
			return PageContext{}, failure
		}
		return PageContext{}, err
	}
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		if failure := b.promoteBrowserLoss(current, selector, "list_targets", err); failure != nil {
			return PageContext{}, failure
		}
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
			if failure := b.promoteBrowserLoss(current, selector, "activate", err); failure != nil {
				return PageContext{}, failure
			}
			return PageContext{}, targetAttachError(selector, "activate", err)
		}
	}
	session, err := handle.Attach(ctx, selector.TargetID, b.ownershipValue())
	if err != nil {
		if failure := b.promoteBrowserLoss(current, selector, "attach", err); failure != nil {
			return PageContext{}, failure
		}
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
		observedInvocations: make(map[InvocationID]observedInvocation),
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
		if failure := b.promoteBrowserLoss(newSession, selector, "enable_webmcp", err); failure != nil {
			_ = session.Close()
			return PageContext{}, failure
		}
		b.invalidateSession(newSession, "enable_failed")
		_ = session.Close()
		return PageContext{}, targetAttachError(selector, "enable_webmcp", err)
	}
	b.flushSession(newSession)
	b.syncSessionReadiness(newSession)
	if err := b.waitForInitialCatalog(ctx, newSession); err != nil {
		if failure := b.promoteBrowserLoss(newSession, selector, "catalog", err); failure != nil {
			_ = session.Close()
			return PageContext{}, failure
		}
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
		close(watcher.events)
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

func (b *StatefulBroker) selectedForBrowser(browserID BrowserID) *brokerSession {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected == nil || b.selected.context.Key.BrowserID != browserID {
		return nil
	}
	return b.selected
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
		if event.Reason == BrowserEventBufferFullReason {
			b.invalidateSessionWithCodeLocked(selected, ErrorBrowserProtocol, event.Reason)
		} else {
			b.invalidateSessionLocked(selected, event.Reason)
		}
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

	generation := event.Generation
	if generation == 0 {
		generation = selected.context.Generation
	}
	if generation > selected.context.Generation {
		b.advanceGenerationLocked(selected, generation, "invocation_generation")
	}
	if generation != selected.context.Generation {
		return
	}
	observed := observedInvocation{
		browserID:  selected.context.Key.BrowserID,
		targetID:   selected.context.Key.TargetID,
		generation: generation,
	}
	if descriptor, ok := observedToolDescriptorLocked(selected, event, generation); ok {
		observed.toolRef = descriptor.Ref
	}
	selected.observedInvocations[event.InvocationID] = observed
	b.emitLocked(BrokerEvent{
		Type:         BrokerEventInvocationCreated,
		BrowserID:    observed.browserID,
		TargetID:     observed.targetID,
		Generation:   observed.generation,
		InvocationID: event.InvocationID,
		ToolRef:      observed.toolRef,
		State:        InvocationDispatched,
		Reason:       "browser_observed",
	})
	if terminal, ok := b.takeEarlyTerminalLocked(event.InvocationID, observed.generation); ok {
		b.recordBrowserTerminalIDLocked(event.InvocationID)
		b.emitObservedBrowserTerminalLocked(observed, event.InvocationID, terminal.status, terminal.errorCode, terminal.reason, terminal.at)
	}
}

func (b *StatefulBroker) reconcileObservedBrowserResponseLocked(selected *brokerSession, event BrowserEvent) bool {
	if selected == nil || event.InvocationID == "" {
		return false
	}
	if _, owned := b.browserInvocations[event.InvocationID]; owned {
		return false
	}
	if !b.acceptBrowserEventGenerationLocked(selected, event) {
		return true
	}
	if _, terminal := b.browserTerminalSeen[event.InvocationID]; terminal {
		return true
	}
	observed, ok := selected.observedInvocations[event.InvocationID]
	if !ok {
		return false
	}
	delete(selected.observedInvocations, event.InvocationID)
	b.recordBrowserTerminalIDLocked(event.InvocationID)
	b.emitObservedBrowserTerminalLocked(observed, event.InvocationID, event.Status, event.ErrorCode, event.Reason, event.At)
	return true
}

func (b *StatefulBroker) emitObservedBrowserTerminalLocked(observed observedInvocation, id InvocationID, status, errorCode, reason string, at time.Time) {
	state, _ := terminalState(status)
	if errorCode != "" {
		reason = errorCode
	}
	if reason == "" {
		reason = strings.ToLower(strings.TrimSpace(status))
	}
	b.emitLocked(BrokerEvent{
		Type:         BrokerEventInvocationTerminal,
		At:           at,
		BrowserID:    observed.browserID,
		TargetID:     observed.targetID,
		Generation:   observed.generation,
		InvocationID: id,
		ToolRef:      observed.toolRef,
		State:        state,
		Reason:       reason,
	})
}

func (b *StatefulBroker) acceptBrowserEventGenerationLocked(selected *brokerSession, event BrowserEvent) bool {
	if selected == nil || event.Generation == 0 {
		return selected != nil
	}
	if event.Generation > selected.context.Generation {
		b.advanceGenerationLocked(selected, event.Generation, "event_generation")
	}
	return event.Generation == selected.context.Generation
}

func observedToolDescriptorLocked(selected *brokerSession, event BrowserEvent, generation uint64) (ToolDescriptor, bool) {
	if selected == nil {
		return ToolDescriptor{}, false
	}
	if event.FrameID != "" {
		if descriptor, ok := selected.catalog[catalogKey{frame: event.FrameID, name: event.ToolName}]; ok {
			if descriptor.Generation == generation {
				return descriptor, true
			}
			return ToolDescriptor{}, false
		}
	}
	var match ToolDescriptor
	for key, descriptor := range selected.catalog {
		if key.name != event.ToolName || descriptor.Generation != generation {
			continue
		}
		if event.FrameID != "" && key.frame != event.FrameID {
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
		if event.FrameID != "" {
			key := catalogKey{frame: event.FrameID, name: name}
			if current, ok := selected.catalog[key]; ok {
				b.retireRefLocked(current.Ref)
				delete(selected.catalog, key)
				changed = true
			}
			continue
		}
		// A neutral producer may omit the frame when the protocol event does
		// not carry one. Remove every current descriptor with that name rather
		// than retaining a stale reference or guessing a frame.
		for key, current := range selected.catalog {
			if key.name != name {
				continue
			}
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
	if selected == nil {
		return
	}
	if code == "" {
		code = ErrorInvocationOrphaned
	}
	if reason == "" {
		reason = "session_closed"
	}
	if selected.invalidatedCode == "" {
		selected.invalidatedCode = code
		selected.invalidatedReason = reason
	}
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
	b.emitLocked(BrokerEvent{Type: BrokerEventGenerationChanged, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: selected.context.Generation, Reason: reason})
	b.emitLocked(BrokerEvent{Type: BrokerEventSessionClosed, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: selected.context.Generation, Reason: reason})
}

func (b *StatefulBroker) markSessionEnded(selected *brokerSession, reason string) {
	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected == selected && selected.active {
		code := lifecycleInvocationErrorCode(reason, ErrorInvocationOrphaned)
		if code == ErrorInvocationOrphaned && isBrowserDisconnectedTransportError(selected.session.Err()) {
			code = ErrorBrowserDisconnected
		}
		b.invalidateSessionWithCodeLocked(selected, code, reason)
	}
}

func (b *StatefulBroker) retireSessionLocked(selected *brokerSession, reason string) {
	if selected == nil {
		return
	}
	b.terminalizeSessionInvocationsLocked(selected, ErrorInvocationOrphaned, reason)
	closeInvocationQueueLocked(selected)
	b.retireCatalogLocked(selected)
	if selected.invalidatedCode == "" {
		selected.invalidatedCode = ErrorInvocationOrphaned
		selected.invalidatedReason = reason
	}
	selected.active = false
	selected.context.Connected = false
	selected.context.CatalogReady = false
	selected.context.CatalogEvidence = ""
	selected.context.Ready = false
	if reason != "" {
		b.emitLocked(BrokerEvent{Type: BrokerEventGenerationChanged, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: selected.context.Generation, Reason: reason})
		b.emitLocked(BrokerEvent{Type: BrokerEventSessionClosed, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: selected.context.Generation, Reason: reason})
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
