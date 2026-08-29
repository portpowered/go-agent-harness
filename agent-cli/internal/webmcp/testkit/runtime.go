package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

var (
	ErrInvalidBrowserConfig        = errors.New("webmcp testkit: invalid browser configuration")
	ErrInvalidTargetConfig         = errors.New("webmcp testkit: invalid target configuration")
	ErrTargetAlreadyAttached       = errors.New("webmcp testkit: target already attached")
	ErrInvocationAlreadyReleased   = errors.New("webmcp testkit: invocation already released")
	ErrCancellationNotAcknowledged = errors.New("webmcp testkit: cancellation not acknowledged")
	ErrGenerationExhausted         = errors.New("webmcp testkit: page generation exhausted")
)

// OperationKind identifies an observable fake-runtime operation. It aliases
// the fixture operation vocabulary so shared operation names such as
// enable_webmcp and close_target retain one package-wide type.
type OperationKind = OperationType

const (
	OperationOpen        OperationKind = "open_browser"
	OperationListTargets OperationKind = "list_targets"
	OperationActivate    OperationKind = "activate_target"
	OperationAttach      OperationKind = "attach_target"
	OperationInvoke      OperationKind = "invoke_tool"
	OperationCancel      OperationKind = "cancel_invocation"
	OperationDetach      OperationKind = "detach_target"
	OperationCloseHandle OperationKind = "close_browser"
	OperationDisconnect  OperationKind = "disconnect_browser"
	OperationReplace     OperationKind = "replace_browser"
)

// Operation is a race-safe snapshot of one fake-runtime command. Input and
// Arguments are owned copies; callers may mutate the returned value.
type Operation struct {
	Sequence                 uint64
	At                       time.Time
	Kind                     OperationKind
	BrowserID                webmcp.BrowserID
	TargetID                 webmcp.TargetID
	FrameID                  webmcp.FrameID
	ToolName                 string
	InvocationID             webmcp.InvocationID
	Ownership                webmcp.TargetOwnership
	Generation               uint64
	Input                    json.RawMessage
	Arguments                json.RawMessage
	Reason                   string
	CancellationAcknowledged bool
}

// InvocationRecord is the fake's observable state for one dispatched page
// invocation.
type InvocationRecord struct {
	ID                       webmcp.InvocationID
	BrowserID                webmcp.BrowserID
	TargetID                 webmcp.TargetID
	Generation               uint64
	FrameID                  webmcp.FrameID
	ToolName                 string
	Input                    json.RawMessage
	State                    webmcp.InvocationState
	CancelRequested          bool
	CancellationAcknowledged bool
	Terminal                 bool
	Output                   json.RawMessage
	Status                   string
}

type RuntimeOptions struct {
	IDs   webmcp.IDSource
	Clock webmcp.Clock
}

type BrowserConfig struct {
	Candidate webmcp.BrowserCandidate
	Targets   []TargetConfig
}

func NewBrowserConfig(candidate webmcp.BrowserCandidate, targets ...TargetConfig) BrowserConfig {
	return BrowserConfig{Candidate: candidate, Targets: append([]TargetConfig(nil), targets...)}
}

type TargetConfig struct {
	Target  webmcp.Target
	Session ScriptedTargetSessionOptions
}

func NewTargetConfig(target webmcp.Target, options ...ScriptedTargetSessionOption) TargetConfig {
	resolved := ScriptedTargetSessionOptions{}
	for _, option := range options {
		if option != nil {
			option.applySession(&resolved)
		}
	}
	return TargetConfig{Target: target, Session: resolved}
}

type ScriptedTargetSessionOptions struct {
	Context                  webmcp.PageContext
	EventBuffer              int
	BlockEnable              bool
	EnableEvents             []webmcp.BrowserEvent
	InitialCatalog           []webmcp.ToolDescriptor
	EnableError              error
	InvokeError              error
	CancelError              error
	AutoRespond              bool
	AutoResponseStatus       string
	AutoResponseOutput       json.RawMessage
	AcknowledgeCancellation  *bool
	EmitCancellationResponse *bool
	IDs                      webmcp.IDSource
	Clock                    webmcp.Clock
}

// WithBlockedEnable prevents EnableWebMCP from completing until the test
// explicitly calls UnblockEnableWebMCP or disconnects the browser.
func WithBlockedEnable() ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) { options.BlockEnable = true })
}

func WithEventBuffer(size int) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) { options.EventBuffer = size })
}

func WithContext(page webmcp.PageContext) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) { options.Context = page })
}

func WithEnableEvents(events ...webmcp.BrowserEvent) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) {
		options.EnableEvents = append([]webmcp.BrowserEvent(nil), events...)
	})
}

func WithInitialCatalog(tools ...webmcp.ToolDescriptor) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) {
		options.InitialCatalog = append([]webmcp.ToolDescriptor(nil), tools...)
	})
}

func WithAutoResponse(output json.RawMessage) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) {
		options.AutoRespond = true
		options.AutoResponseOutput = cloneBytes(output)
	})
}

func WithAutoResponseStatus(status string, output json.RawMessage) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) {
		options.AutoRespond = true
		options.AutoResponseStatus = status
		options.AutoResponseOutput = cloneBytes(output)
	})
}

func WithEnableError(err error) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) { options.EnableError = err })
}

func WithInvokeError(err error) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) { options.InvokeError = err })
}

func WithCancelError(err error) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) { options.CancelError = err })
}

func WithCancellationAcknowledgement(acknowledged bool) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) {
		options.AcknowledgeCancellation = boolPointer(acknowledged)
	})
}

func WithCancellationResponse(enabled bool) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) { options.EmitCancellationResponse = boolPointer(enabled) })
}

func WithIDs(ids webmcp.IDSource) ScriptedTargetSessionOption {
	return scriptedTargetSessionOptionFunc(func(options *ScriptedTargetSessionOptions) { options.IDs = ids })
}

type ScriptedBrowserRuntime struct {
	mu        sync.Mutex
	browsers  map[webmcp.BrowserID]*ScriptedBrowserHandle
	handles   map[*ScriptedBrowserHandle]struct{}
	active    map[webmcp.BrowserID]bool
	endpoints map[string]webmcp.BrowserID
	sessions  map[string]*ScriptedTargetSession
	closed    bool

	operationMu      sync.Mutex
	operations       []Operation
	nextOperation    uint64
	operationChanges chan struct{}

	eventMu         sync.Mutex
	nextEvent       uint64
	publishedEvents []PublishedEvent
	eventChanges    chan struct{}
	ids             webmcp.IDSource
	clock           webmcp.Clock
	closeDone       chan struct{}
	closeErr        error
}

func NewScriptedBrowserRuntime(configs ...BrowserConfig) *ScriptedBrowserRuntime {
	return NewScriptedBrowserRuntimeWithOptions(RuntimeOptions{}, configs...)
}

func NewScriptedBrowserRuntimeWithOptions(options RuntimeOptions, configs ...BrowserConfig) *ScriptedBrowserRuntime {
	ids := options.IDs
	if ids == nil {
		ids = NewDeterministicIDs()
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock{}
	}
	runtime := &ScriptedBrowserRuntime{
		browsers:         make(map[webmcp.BrowserID]*ScriptedBrowserHandle),
		handles:          make(map[*ScriptedBrowserHandle]struct{}),
		active:           make(map[webmcp.BrowserID]bool),
		endpoints:        make(map[string]webmcp.BrowserID),
		sessions:         make(map[string]*ScriptedTargetSession),
		operationChanges: make(chan struct{}),
		eventChanges:     make(chan struct{}),
		ids:              ids,
		clock:            clock,
		closeDone:        make(chan struct{}),
	}
	for _, config := range configs {
		if err := runtime.AddBrowser(config); err != nil {
			panic(err)
		}
	}
	return runtime
}

// AddBrowser adds a browser before it is opened. Browser and target IDs must
// be unique within this runtime.
func (r *ScriptedBrowserRuntime) AddBrowser(config BrowserConfig) error {
	if config.Candidate.ID == "" {
		return ErrInvalidBrowserConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return webmcp.ErrClosed
	}
	if _, exists := r.browsers[config.Candidate.ID]; exists {
		return fmt.Errorf("%w: browser %q already exists", ErrInvalidBrowserConfig, config.Candidate.ID)
	}
	if endpoint := browserEndpointKey(config.Candidate); endpoint != "" {
		if owner, exists := r.endpoints[endpoint]; exists && r.active[owner] {
			return fmt.Errorf("%w: endpoint is already owned by browser %q", ErrInvalidBrowserConfig, owner)
		}
	}
	handle := newScriptedBrowserHandle(r, config)
	r.browsers[config.Candidate.ID] = handle
	r.active[config.Candidate.ID] = true
	if endpoint := browserEndpointKey(config.Candidate); endpoint != "" {
		r.endpoints[endpoint] = config.Candidate.ID
	}
	return nil
}

// AddCandidate is a convenience form for tests that build a runtime
// incrementally.
func (r *ScriptedBrowserRuntime) AddCandidate(candidate webmcp.BrowserCandidate, targets ...TargetConfig) error {
	return r.AddBrowser(NewBrowserConfig(candidate, targets...))
}

func (r *ScriptedBrowserRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	template, ok := r.browsers[candidate.ID]
	var handle *ScriptedBrowserHandle
	if ok {
		handle = template.newClient()
		r.handles[handle] = struct{}{}
	}
	active := ok && r.active[candidate.ID]
	r.mu.Unlock()
	if !ok || !active {
		if ok {
			if handleClosed, disconnected := handle.state(); handleClosed {
				return nil, webmcp.ErrClosed
			} else if disconnected {
				return nil, disconnectedError(candidate.ID, "", "open", "browser_disconnected")
			}
			return nil, disconnectedError(candidate.ID, "", "open", "browser_replaced")
		}
		return nil, fmt.Errorf("%w: %s", webmcp.ErrBrowserNotFound, candidate.ID)
	}
	r.record(Operation{Kind: OperationOpen, BrowserID: candidate.ID})
	return handle, nil
}

// Close is not part of webmcp.BrowserRuntime because ownership belongs to
// the browser handle, but it is useful for fixture teardown and is idempotent.
func (r *ScriptedBrowserRuntime) Close() error {
	r.mu.Lock()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		<-done
		r.mu.Lock()
		err := r.closeErr
		r.mu.Unlock()
		return err
	}
	r.closed = true
	handles := make([]*ScriptedBrowserHandle, 0, len(r.handles))
	for handle := range r.handles {
		handles = append(handles, handle)
	}
	r.mu.Unlock()

	var joined error
	for _, handle := range handles {
		joined = errors.Join(joined, handle.Close())
	}
	r.mu.Lock()
	r.closeErr = joined
	close(r.closeDone)
	r.mu.Unlock()
	return joined
}

func (r *ScriptedBrowserRuntime) Operations() []Operation {
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	operations := make([]Operation, len(r.operations))
	for i, operation := range r.operations {
		operations[i] = cloneOperation(operation)
	}
	return operations
}

func (r *ScriptedBrowserRuntime) ResetOperations() {
	r.operationMu.Lock()
	r.operations = nil
	r.operationMu.Unlock()
}

func (r *ScriptedBrowserRuntime) WaitForOperation(ctx context.Context, kind OperationKind) (Operation, error) {
	for {
		r.operationMu.Lock()
		for _, operation := range r.operations {
			if operation.Kind == kind {
				r.operationMu.Unlock()
				return operation, nil
			}
		}
		changes := r.operationChanges
		r.operationMu.Unlock()
		select {
		case <-ctx.Done():
			return Operation{}, ctx.Err()
		case <-changes:
		}
	}
}

func (r *ScriptedBrowserRuntime) record(operation Operation) {
	r.operationMu.Lock()
	r.nextOperation++
	operation.Sequence = r.nextOperation
	operation.At = r.clock.Now()
	operation.Input = cloneBytes(operation.Input)
	operation.Arguments = cloneBytes(operation.Arguments)
	r.operations = append(r.operations, operation)
	close(r.operationChanges)
	r.operationChanges = make(chan struct{})
	r.operationMu.Unlock()
}

func (r *ScriptedBrowserRuntime) decorateEvent(event webmcp.BrowserEvent, browserID webmcp.BrowserID, targetID webmcp.TargetID, generation, sequence uint64) webmcp.BrowserEvent {
	if event.Version == "" {
		event.Version = webmcp.BrowserEventsVersion
	}
	if event.Sequence == 0 {
		event.Sequence = sequence
	}
	if event.At.IsZero() {
		event.At = r.clock.Now()
	}
	if event.BrowserID == "" {
		event.BrowserID = browserID
	}
	if event.TargetID == "" {
		event.TargetID = targetID
	}
	if event.Generation == 0 {
		event.Generation = generation
	}
	event.Tools = cloneTools(event.Tools)
	event.RemovedToolNames = append([]string(nil), event.RemovedToolNames...)
	event.Input = cloneBytes(event.Input)
	event.Output = cloneBytes(event.Output)
	return event
}

func newScriptedBrowserHandle(runtime *ScriptedBrowserRuntime, config BrowserConfig) *ScriptedBrowserHandle {
	handle := &ScriptedBrowserHandle{
		runtime:        runtime,
		candidate:      cloneCandidate(config.Candidate),
		targets:        make(map[webmcp.TargetID]*scriptedTargetEntry, len(config.Targets)),
		sessions:       make(map[webmcp.TargetID]*ScriptedTargetSession),
		control:        &scriptedBrowserControl{listChanges: make(chan struct{})},
		closeDone:      make(chan struct{}),
		disconnectDone: make(chan struct{}),
	}
	for _, target := range config.Targets {
		if target.Target.ID == "" {
			continue
		}
		if target.Target.BrowserID == "" {
			target.Target.BrowserID = config.Candidate.ID
		}
		if target.Target.BrowserID != config.Candidate.ID {
			continue
		}
		if target.Target.Type == "" {
			target.Target.Type = "page"
		}
		handle.targets[target.Target.ID] = &scriptedTargetEntry{
			target:   cloneTarget(target.Target),
			config:   cloneTargetConfig(target),
			sessions: make(map[*ScriptedTargetSession]struct{}),
		}
	}
	return handle
}

func (h *ScriptedBrowserHandle) newClient() *ScriptedBrowserHandle {
	return &ScriptedBrowserHandle{
		runtime:        h.runtime,
		candidate:      cloneCandidate(h.candidate),
		targets:        h.targets,
		sessions:       make(map[webmcp.TargetID]*ScriptedTargetSession),
		control:        h.control,
		closeDone:      make(chan struct{}),
		disconnectDone: make(chan struct{}),
	}
}

type scriptedTargetEntry struct {
	mu       sync.Mutex
	target   webmcp.Target
	config   TargetConfig
	removed  bool
	sessions map[*ScriptedTargetSession]struct{}
	// lastSession keeps an externally-owned retired session inspectable after
	// transport loss. A new Attach still wins through h.sessions, while the
	// retained pointer lets recovery tests inject late events explicitly.
	lastSession *ScriptedTargetSession
}

type scriptedBrowserControl struct {
	mu           sync.Mutex
	listBlocked  bool
	listChanges  chan struct{}
	disconnected bool
}

type ScriptedBrowserHandle struct {
	runtime        *ScriptedBrowserRuntime
	mu             sync.Mutex
	candidate      webmcp.BrowserCandidate
	targets        map[webmcp.TargetID]*scriptedTargetEntry
	sessions       map[webmcp.TargetID]*ScriptedTargetSession
	closed         bool
	closeErr       error
	closeDone      chan struct{}
	disconnected   bool
	disconnectErr  error
	disconnectDone chan struct{}
	control        *scriptedBrowserControl
}

func (h *ScriptedBrowserHandle) Candidate() webmcp.BrowserCandidate {
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneCandidate(h.candidate)
}

func (h *ScriptedBrowserHandle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	if h.disconnected {
		err := disconnectedError(h.candidate.ID, "", "list_targets", "browser_disconnected")
		h.mu.Unlock()
		return nil, err
	}
	disconnectDone := h.disconnectDone
	closeDone := h.closeDone
	candidateID := h.candidate.ID
	control := h.control
	h.mu.Unlock()
	control.mu.Lock()
	blocked := control.listBlocked
	listChanges := control.listChanges
	controlDisconnected := control.disconnected
	control.mu.Unlock()
	if controlDisconnected {
		return nil, disconnectedError(candidateID, "", "list_targets", "browser_disconnected")
	}
	h.runtime.record(Operation{Kind: OperationListTargets, BrowserID: candidateID})
	if blocked {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-listChanges:
		case <-disconnectDone:
			return nil, disconnectedError(candidateID, "", "list_targets", "browser_disconnected")
		case <-closeDone:
			return nil, webmcp.ErrClosed
		}
	}
	control.mu.Lock()
	controlDisconnected = control.disconnected
	control.mu.Unlock()
	if controlDisconnected {
		return nil, disconnectedError(candidateID, "", "list_targets", "browser_disconnected")
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	if h.disconnected {
		err := disconnectedError(h.candidate.ID, "", "list_targets", "browser_disconnected")
		h.mu.Unlock()
		return nil, err
	}
	targets := make([]webmcp.Target, 0, len(h.targets))
	for _, entry := range h.targets {
		entry.mu.Lock()
		if !entry.removed {
			targets = append(targets, cloneTarget(entry.target))
		}
		entry.mu.Unlock()
	}
	h.mu.Unlock()
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	return targets, nil
}

func (h *ScriptedBrowserHandle) Activate(ctx context.Context, targetID webmcp.TargetID) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return webmcp.ErrClosed
	}
	if h.disconnected {
		err := disconnectedError(h.candidate.ID, targetID, "activate", "browser_disconnected")
		h.mu.Unlock()
		return err
	}
	if _, ok := h.targets[targetID]; !ok {
		h.mu.Unlock()
		return fmt.Errorf("%w: %s", webmcp.ErrTargetNotFound, targetID)
	}
	h.mu.Unlock()
	h.runtime.record(Operation{Kind: OperationActivate, BrowserID: h.candidate.ID, TargetID: targetID})
	return nil
}

func (h *ScriptedBrowserHandle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if ownership == "" {
		ownership = webmcp.TargetOwnershipExternal
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	if h.disconnected {
		err := disconnectedError(h.candidate.ID, targetID, "attach", "browser_disconnected")
		h.mu.Unlock()
		return nil, err
	}
	entry, ok := h.targets[targetID]
	if !ok {
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", webmcp.ErrTargetNotFound, targetID)
	}
	entry.mu.Lock()
	if entry.removed {
		entry.mu.Unlock()
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", webmcp.ErrTargetNotFound, targetID)
	}
	if session := h.sessions[targetID]; session != nil && !session.isClosed() {
		entry.mu.Unlock()
		if session.ownership != ownership {
			h.mu.Unlock()
			return nil, fmt.Errorf("%w: %s", ErrTargetAlreadyAttached, targetID)
		}
		h.mu.Unlock()
		return session, nil
	}
	page := entry.config.Session.Context
	if page.Key.BrowserID == "" {
		page.Key.BrowserID = h.candidate.ID
	}
	if page.Key.TargetID == "" {
		page.Key.TargetID = targetID
	}
	if page.Title == "" {
		page.Title = entry.target.Title
	}
	if page.URL == "" {
		page.URL = entry.target.URL
	}
	if page.Origin == "" {
		page.Origin = entry.target.Origin
	}
	if page.Generation == 0 {
		page.Generation = 1
	}
	page.Connected = true
	session := newScriptedTargetSession(h, entry, entry.target, page, ownership, entry.config.Session)
	h.sessions[targetID] = session
	entry.sessions[session] = struct{}{}
	entry.lastSession = session
	entry.target.Attached = true
	entry.mu.Unlock()
	h.mu.Unlock()
	h.runtime.registerSession(session)

	h.runtime.record(Operation{Kind: OperationAttach, BrowserID: h.candidate.ID, TargetID: targetID, Generation: page.Generation, Ownership: ownership})
	_ = session.emitLocal(webmcp.BrowserEvent{Type: webmcp.EventTargetAttached, Generation: page.Generation})
	return session, nil
}

func (h *ScriptedBrowserHandle) Close() error {
	h.mu.Lock()
	if h.closed {
		done := h.closeDone
		h.mu.Unlock()
		<-done
		h.mu.Lock()
		err := h.closeErr
		h.mu.Unlock()
		return err
	}
	h.closed = true
	sessions := make([]*ScriptedTargetSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.Unlock()
	h.runtime.retireHandle(h)

	var joined error
	for _, session := range sessions {
		joined = errors.Join(joined, session.Close())
	}
	h.runtime.record(Operation{Kind: OperationCloseHandle, BrowserID: h.candidate.ID})
	h.mu.Lock()
	h.closeErr = joined
	close(h.closeDone)
	h.mu.Unlock()
	return joined
}

func (h *ScriptedBrowserHandle) sessionClosed(session *ScriptedTargetSession) {
	h.mu.Lock()
	if h.sessions[session.target.ID] == session {
		delete(h.sessions, session.target.ID)
	}
	h.mu.Unlock()
	entry := session.entry
	entry.mu.Lock()
	delete(entry.sessions, session)
	entry.lastSession = session
	entry.target.Attached = len(entry.sessions) > 0
	if session.removeTargetOnClose || session.ownership == webmcp.TargetOwnershipHarnessOwned {
		entry.removed = true
	}
	entry.mu.Unlock()
	h.runtime.unregisterSession(session)
}

func (h *ScriptedBrowserHandle) TargetSession(targetID webmcp.TargetID) *ScriptedTargetSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.targets[targetID]
	if entry == nil {
		return nil
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.removed {
		return nil
	}
	if session := h.sessions[targetID]; session != nil {
		return session
	}
	for session := range entry.sessions {
		return session
	}
	return entry.lastSession
}

type ScriptedTargetSession struct {
	handle    *ScriptedBrowserHandle
	runtime   *ScriptedBrowserRuntime
	entry     *scriptedTargetEntry
	target    webmcp.Target
	mu        sync.Mutex
	context   webmcp.PageContext
	ownership webmcp.TargetOwnership
	options   ScriptedTargetSessionOptions

	events          chan webmcp.BrowserEvent
	done            chan struct{}
	change          chan struct{}
	enableChanges   chan struct{}
	terminalChanges chan struct{}

	closed              bool
	closedState         atomic.Bool
	closeErr            error
	closeResult         error
	removeTargetOnClose bool
	blocked             bool
	enableBlocked       bool
	sequence            uint64
	tools               map[string]webmcp.ToolDescriptor
	invokes             map[webmcp.InvocationID]*InvocationRecord
	order               []webmcp.InvocationID
	observed            map[webmcp.InvocationID]bool
	terminalObserved    map[webmcp.InvocationID]TerminalObservation
	terminalHistory     []TerminalObservation
}

func newScriptedTargetSession(handle *ScriptedBrowserHandle, entry *scriptedTargetEntry, target webmcp.Target, page webmcp.PageContext, ownership webmcp.TargetOwnership, options ScriptedTargetSessionOptions) *ScriptedTargetSession {
	if options.EventBuffer <= 0 {
		options.EventBuffer = 64
	}
	if options.IDs == nil {
		options.IDs = handle.runtime.ids
	}
	if options.Clock == nil {
		options.Clock = handle.runtime.clock
	}
	if options.AcknowledgeCancellation == nil {
		options.AcknowledgeCancellation = boolPointer(true)
	}
	if options.EmitCancellationResponse == nil {
		options.EmitCancellationResponse = boolPointer(true)
	}
	if options.AutoResponseStatus == "" {
		options.AutoResponseStatus = "Completed"
	}
	session := &ScriptedTargetSession{
		handle:           handle,
		runtime:          handle.runtime,
		entry:            entry,
		target:           cloneTarget(target),
		context:          page,
		ownership:        ownership,
		options:          options,
		events:           make(chan webmcp.BrowserEvent, options.EventBuffer),
		done:             make(chan struct{}),
		change:           make(chan struct{}),
		enableChanges:    make(chan struct{}),
		terminalChanges:  make(chan struct{}),
		tools:            make(map[string]webmcp.ToolDescriptor),
		invokes:          make(map[webmcp.InvocationID]*InvocationRecord),
		observed:         make(map[webmcp.InvocationID]bool),
		enableBlocked:    options.BlockEnable,
		terminalObserved: make(map[webmcp.InvocationID]TerminalObservation),
	}
	for _, tool := range options.InitialCatalog {
		session.tools[toolKey(tool.FrameID, tool.Name)] = cloneTool(tool)
	}
	return session
}

func (s *ScriptedTargetSession) Context() webmcp.PageContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.context
}

func (s *ScriptedTargetSession) Target() webmcp.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTarget(s.target)
}

func (s *ScriptedTargetSession) Ownership() webmcp.TargetOwnership {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownership
}

func (s *ScriptedTargetSession) Events() <-chan webmcp.BrowserEvent { return s.events }

func (s *ScriptedTargetSession) Done() <-chan struct{} { return s.done }

func (s *ScriptedTargetSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

func (s *ScriptedTargetSession) EnableWebMCP(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	err := s.options.EnableError
	blocked := s.enableBlocked
	enableChanges := s.enableChanges
	done := s.done
	generation := s.context.Generation
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.runtime.record(Operation{Kind: OperationEnableWebMCP, BrowserID: s.target.BrowserID, TargetID: s.target.ID, Generation: generation})
	if blocked {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-enableChanges:
		case <-done:
			err := s.Err()
			if err != nil {
				return err
			}
			return webmcp.ErrClosed
		}
	}
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return webmcp.ErrClosed
	}
	s.mu.Unlock()

	events := append([]webmcp.BrowserEvent(nil), s.options.EnableEvents...)
	if len(events) == 0 && len(s.options.InitialCatalog) > 0 {
		events = append(events, webmcp.BrowserEvent{Type: webmcp.EventToolsAdded, Tools: cloneTools(s.options.InitialCatalog)})
	}
	for _, event := range events {
		if err := s.Emit(event); err != nil {
			return err
		}
	}
	return nil
}

func (s *ScriptedTargetSession) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	s.mu.Lock()
	id, err := s.options.IDs.NewInvocationID()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return s.invokeWebMCPWithID(ctx, id, frameID, toolName, input)
}

// InvokeWebMCPWithID is an optional deterministic-session seam used by the
// broker tests. Real adapters can continue to allocate their protocol IDs in
// InvokeWebMCP; the fake accepts the broker's public ID so records prove the
// two correlation surfaces agree.
func (s *ScriptedTargetSession) InvokeWebMCPWithID(ctx context.Context, id webmcp.InvocationID, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	return s.invokeWebMCPWithID(ctx, id, frameID, toolName, input)
}

func (s *ScriptedTargetSession) invokeWebMCPWithID(ctx context.Context, id webmcp.InvocationID, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("webmcp testkit: invocation ID is empty")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", webmcp.ErrClosed
	}
	if s.options.InvokeError != nil {
		err := s.options.InvokeError
		s.mu.Unlock()
		return "", err
	}
	if _, exists := s.invokes[id]; exists {
		s.mu.Unlock()
		return "", fmt.Errorf("webmcp testkit: invocation %q already exists", id)
	}
	record := &InvocationRecord{
		ID:         id,
		BrowserID:  s.target.BrowserID,
		TargetID:   s.target.ID,
		Generation: s.context.Generation,
		FrameID:    frameID,
		ToolName:   toolName,
		Input:      cloneBytes(input),
		State:      webmcp.InvocationDispatched,
	}
	s.invokes[id] = record
	s.order = append(s.order, id)
	s.notifyLocked()
	blocked := s.blocked
	autoRespond := s.options.AutoRespond && !blocked
	autoStatus := s.options.AutoResponseStatus
	autoOutput := cloneBytes(s.options.AutoResponseOutput)
	generation := record.Generation
	s.mu.Unlock()

	s.runtime.record(Operation{
		Kind:         OperationInvoke,
		BrowserID:    s.target.BrowserID,
		TargetID:     s.target.ID,
		Generation:   generation,
		FrameID:      frameID,
		ToolName:     toolName,
		InvocationID: id,
		Input:        input,
		Arguments:    input,
	})
	if err := s.emit(webmcp.BrowserEvent{Type: webmcp.EventToolInvoked, FrameID: frameID, ToolName: toolName, InvocationID: id, Input: input, Generation: generation}); err != nil {
		return id, err
	}
	if autoRespond {
		if err := s.EmitToolResponse(id, autoStatus, autoOutput); err != nil {
			return id, err
		}
	}
	return id, nil
}

func (s *ScriptedTargetSession) CancelWebMCP(ctx context.Context, id webmcp.InvocationID) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	cancelErr := s.options.CancelError
	acknowledged := *s.options.AcknowledgeCancellation
	emitResponse := *s.options.EmitCancellationResponse && acknowledged
	s.mu.Unlock()
	if cancelErr != nil {
		return cancelErr
	}
	owner, record := s.findInvocation(id)
	if owner == nil || record == nil {
		return fmt.Errorf("%w: %s", webmcp.ErrInvocationNotFound, id)
	}
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return webmcp.ErrClosed
	}
	record.CancelRequested = true
	record.CancellationAcknowledged = acknowledged
	record.Terminal = acknowledged
	record.State = webmcp.InvocationCanceled
	generation := record.Generation
	owner.notifyLocked()
	owner.mu.Unlock()

	s.runtime.record(Operation{
		Kind:                     OperationCancel,
		BrowserID:                s.target.BrowserID,
		TargetID:                 s.target.ID,
		Generation:               generation,
		InvocationID:             id,
		Reason:                   "cancel",
		CancellationAcknowledged: acknowledged,
	})
	if !acknowledged {
		return ErrCancellationNotAcknowledged
	}
	if emitResponse {
		return owner.EmitToolResponse(id, "Canceled", nil)
	}
	if acknowledged {
		s.markTerminalObserved(id, PublishedEvent{})
	}
	return nil
}

// findInvocation models the browser-owned invocation registry. A direct
// cancellation request may arrive through a different target session than
// the one that dispatched the call, while local invocation APIs remain
// scoped to each client session.
func (s *ScriptedTargetSession) findInvocation(id webmcp.InvocationID) (*ScriptedTargetSession, *InvocationRecord) {
	s.mu.Lock()
	if record := s.invokes[id]; record != nil {
		s.mu.Unlock()
		return s, record
	}
	entry := s.entry
	s.mu.Unlock()
	if entry == nil {
		return nil, nil
	}
	entry.mu.Lock()
	sessions := make([]*ScriptedTargetSession, 0, len(entry.sessions))
	for session := range entry.sessions {
		sessions = append(sessions, session)
	}
	entry.mu.Unlock()
	for _, session := range sessions {
		if session == s {
			continue
		}
		session.mu.Lock()
		record := session.invokes[id]
		session.mu.Unlock()
		if record != nil {
			return session, record
		}
	}
	return nil, nil
}

// BlockInvocations prevents configured automatic responses. It does not
// prevent admission or ID assignment, which lets tests exercise cancellation
// and late-result races against a known invocation ID.
func (s *ScriptedTargetSession) BlockInvocations() {
	s.mu.Lock()
	s.blocked = true
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) UnblockInvocations() {
	s.mu.Lock()
	s.blocked = false
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) SetAutoResponse(status string, output json.RawMessage) {
	s.mu.Lock()
	s.options.AutoRespond = true
	if status != "" {
		s.options.AutoResponseStatus = status
	}
	s.options.AutoResponseOutput = cloneBytes(output)
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) SetCancellationAcknowledgement(acknowledged bool) {
	s.mu.Lock()
	s.options.AcknowledgeCancellation = boolPointer(acknowledged)
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) SetCancellationResponse(enabled bool) {
	s.mu.Lock()
	s.options.EmitCancellationResponse = boolPointer(enabled)
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) Close() error {
	s.mu.Lock()
	if s.closed {
		err := s.closeResult
		s.mu.Unlock()
		return err
	}
	ownership := s.ownership
	s.mu.Unlock()
	if ownership == webmcp.TargetOwnershipHarnessOwned {
		return s.terminate(webmcp.EventTargetDetached, "close", webmcp.ErrClosed)
	}
	return s.terminate(webmcp.EventTargetDetached, "detach", webmcp.ErrClosed)
}

func (s *ScriptedTargetSession) terminate(eventType webmcp.BrowserEventType, reason string, terminalErr error) error {
	return s.terminateWithOptions(eventType, reason, terminalErr, eventType == webmcp.EventTargetDetached && s.Ownership() == webmcp.TargetOwnershipHarnessOwned, false)
}

func (s *ScriptedTargetSession) terminateWithOptions(eventType webmcp.BrowserEventType, reason string, terminalErr error, removeTarget, explicitTargetClose bool) error {
	s.mu.Lock()
	if s.closed {
		err := s.closeResult
		s.mu.Unlock()
		return err
	}
	s.closed = true
	s.closedState.Store(true)
	s.closeErr = terminalErr
	s.context.Connected = false
	s.context.Ready = false
	s.removeTargetOnClose = removeTarget
	orphaned := make([]webmcp.InvocationID, 0)
	for _, record := range s.invokes {
		if record.Terminal {
			continue
		}
		record.State = webmcp.InvocationOrphaned
		record.Terminal = true
		orphaned = append(orphaned, record.ID)
	}
	s.notifyLocked()
	if eventType == webmcp.EventTargetDetached {
		if explicitTargetClose || s.ownership == webmcp.TargetOwnershipHarnessOwned {
			s.runtime.record(Operation{Kind: OperationCloseTarget, BrowserID: s.target.BrowserID, TargetID: s.target.ID, Generation: s.context.Generation, Ownership: s.ownership, Reason: reason})
		} else {
			s.runtime.record(Operation{Kind: OperationDetach, BrowserID: s.target.BrowserID, TargetID: s.target.ID, Generation: s.context.Generation, Ownership: s.ownership, Reason: reason})
		}
	}
	event := webmcp.BrowserEvent{Type: eventType, Reason: reason}
	if eventType == webmcp.EventBrowserDisconnected {
		event.ErrorCode = string(webmcp.ErrorBrowserDisconnected)
	} else if eventType == webmcp.EventTargetDetached {
		event.ErrorCode = string(webmcp.ErrorTargetDetached)
	}
	published, eventErr := s.emitPublishedLocked(event, true)
	s.closeResult = eventErr
	s.handle.sessionClosed(s)
	close(s.events)
	s.mu.Unlock()
	if eventErr == nil {
		for _, id := range orphaned {
			s.markTerminalObserved(id, published)
		}
	}
	close(s.done)
	if eventErr != nil {
		return eventErr
	}
	return nil
}

func (s *ScriptedTargetSession) emit(event webmcp.BrowserEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return webmcp.ErrClosed
	}
	return s.emitLocked(event)
}

func (s *ScriptedTargetSession) emitLocked(event webmcp.BrowserEvent) error {
	_, err := s.emitPublishedLocked(event, false)
	return err
}

// emitPublishedLocked publishes a shared browser event, or a local terminal
// lifecycle event when terminal is true. The variadic form keeps the helper
// compatible with older testkit callers that did not need the terminal flag.
func (s *ScriptedTargetSession) emitPublishedLocked(event webmcp.BrowserEvent, terminal ...bool) (PublishedEvent, error) {
	if len(terminal) > 0 && terminal[0] {
		return s.emitLocalPublishedLocked(event, true)
	}
	decorated := s.decorateProducedEventLocked(event)
	if err := s.entry.broadcast(decorated); err != nil {
		return PublishedEvent{}, err
	}
	return s.runtime.publishEvent(decorated), nil
}

func (s *ScriptedTargetSession) emitLocal(event webmcp.BrowserEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return webmcp.ErrClosed
	}
	_, err := s.emitLocalPublishedLocked(event, false)
	return err
}

func (s *ScriptedTargetSession) emitLocalPublishedLocked(event webmcp.BrowserEvent, terminal bool) (PublishedEvent, error) {
	decorated := s.decorateProducedEventLocked(event)
	select {
	case s.events <- decorated:
		return s.runtime.publishEvent(decorated), nil
	default:
		if !terminal {
			return PublishedEvent{}, webmcp.ErrEventBufferFull
		}
	}
	// Terminal lifecycle events must remain observable even when a test has
	// intentionally filled the bounded event buffer.
	select {
	case <-s.events:
	default:
	}
	select {
	case s.events <- decorated:
		return s.runtime.publishEvent(decorated), nil
	default:
		return PublishedEvent{}, webmcp.ErrEventBufferFull
	}
}

func (entry *scriptedTargetEntry) broadcast(event webmcp.BrowserEvent) error {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	var result error
	for session := range entry.sessions {
		select {
		case session.events <- cloneEvent(event):
		default:
			result = webmcp.ErrEventBufferFull
		}
	}
	return result
}

func (s *ScriptedTargetSession) notifyLocked() {
	close(s.change)
	s.change = make(chan struct{})
}

func (s *ScriptedTargetSession) isClosed() bool {
	return s.closedState.Load()
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func boolPointer(value bool) *bool { return &value }

var (
	_ webmcp.BrowserRuntime = (*ScriptedBrowserRuntime)(nil)
	_ webmcp.BrowserHandle  = (*ScriptedBrowserHandle)(nil)
	_ webmcp.TargetSession  = (*ScriptedTargetSession)(nil)
)
