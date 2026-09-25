package objective

import (
	"bytes"
	"encoding/json"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

// Payload field names read from persisted browser events.
const (
	fieldInvocationID = "invocation_id"
	fieldToolRef      = "tool_ref"
	fieldCode         = "code"
)

// BrowserEvidence is the index of one persisted browser event stream. It is
// rebuilt from the canonical artifact so objective verdicts never depend on
// live broker state.
type BrowserEvidence struct {
	browserCount      int64
	browserCountSet   bool
	browserCountPos   int
	targets           map[string]persistedTarget
	selectedTarget    string
	selectedTargetPos int
	catalog           map[string]persistedTool
	catalogByRef      map[string]string
	catalogGeneration int64
	catalogSet        bool
	catalogPos        int
	invocations       []*persistedInvocation
	invocationByID    map[string]*persistedInvocation
	stale             bool
	stalePos          int
	staleToolRef      string
	canceled          bool
	canceledPos       int
	closed            bool
	closedPos         int
	approvalRequested bool
	approvalPos       int
	operations        []ObservedOperation
	methods           []ObservedOperation
}

type persistedTarget struct {
	Origin   string
	Eligible bool
	Position int
}

type persistedTool struct {
	Name        string
	Ref         string
	InputSchema json.RawMessage
	Position    int
}

type persistedInvocation struct {
	ID            string
	Name          string
	ToolRef       string
	Input         json.RawMessage
	Output        json.RawMessage
	State         string
	ErrorCode     string
	Terminal      bool
	CreatedPos    int
	DispatchedPos int
	TerminalPos   int
	Approval      bool
	ApprovalPos   int
}

// ObservedOperation is one Chrome operation or generated CDP method in the
// order it was persisted.
type ObservedOperation struct {
	Name              string
	OperationPosition int
	EventPosition     int
}

// eventContext is one decoded event handed to an event handler.
type eventContext struct {
	event     testkit.Event
	fields    map[string]json.RawMessage
	hasFields bool
	position  int
}

type eventHandler func(*BrowserEvidence, eventContext)

// Index rebuilds browser evidence from a validated persisted event stream.
func Index(events []testkit.Event) BrowserEvidence {
	evidence := BrowserEvidence{
		targets:        make(map[string]persistedTarget),
		catalog:        make(map[string]persistedTool),
		catalogByRef:   make(map[string]string),
		invocationByID: make(map[string]*persistedInvocation),
	}
	handlers := eventHandlers()
	for index, event := range events {
		handler, known := handlers[event.Type]
		if !known {
			continue
		}
		position := int(event.Sequence)
		if position <= 0 {
			position = index + 1
		}
		fields, hasFields := eventFields(event)
		handler(&evidence, eventContext{event: event, fields: fields, hasFields: hasFields, position: position})
	}
	for _, invocation := range evidence.invocations {
		if invocation.Name == "" && invocation.ToolRef != "" {
			invocation.Name = evidence.catalogByRef[invocation.ToolRef]
		}
	}
	return evidence
}

func eventHandlers() map[testkit.EventType]eventHandler {
	return map[testkit.EventType]eventHandler{
		testkit.EventBrowserDiscoveryStarted:      operationHandler("connect"),
		testkit.EventBrowserDiscoveryCompleted:    (*BrowserEvidence).onDiscoveryCompleted,
		testkit.EventBrowserTargetsSnapshot:       (*BrowserEvidence).onTargetsSnapshot,
		testkit.EventBrowserTargetSelected:        (*BrowserEvidence).onTargetSelected,
		testkit.EventBrowserCatalogToolAdded:      (*BrowserEvidence).onCatalogToolAdded,
		testkit.EventBrowserCatalogToolRemoved:    (*BrowserEvidence).onCatalogToolRemoved,
		testkit.EventBrowserCatalogReady:          (*BrowserEvidence).onCatalogReady,
		testkit.EventBrowserInvocationCreated:     (*BrowserEvidence).onInvocationCreated,
		testkit.EventBrowserInvocationDispatched:  (*BrowserEvidence).onInvocationDispatched,
		testkit.EventBrowserInvocationApproval:    (*BrowserEvidence).onInvocationApproval,
		testkit.EventBrowserInvocationCompleted:   (*BrowserEvidence).onInvocationCompleted,
		testkit.EventBrowserInvocationError:       (*BrowserEvidence).onInvocationError,
		testkit.EventBrowserInvocationCanceled:    (*BrowserEvidence).onInvocationCanceled,
		testkit.EventBrowserInvocationCancel:      (*BrowserEvidence).onInvocationCancel,
		testkit.EventBrowserWebMCPEnabled:         methodHandler("WebMCP.enable"),
		testkit.EventBrowserPageGenerationChanged: operationHandler("navigate"),
		testkit.EventBrowserChromeTargetAttached:  operationHandler("attach"),
		testkit.EventBrowserTargetDetached:        operationHandler("detach"),
		testkit.EventBrowserChromeTargetClosed:    (*BrowserEvidence).onChromeTargetClosed,
	}
}

func operationHandler(name string) eventHandler {
	return func(evidence *BrowserEvidence, ctx eventContext) {
		appendObserved(&evidence.operations, name, ctx.position)
	}
}

func methodHandler(name string) eventHandler {
	return func(evidence *BrowserEvidence, ctx eventContext) {
		appendObserved(&evidence.methods, name, ctx.position)
	}
}

func (e *BrowserEvidence) onDiscoveryCompleted(ctx eventContext) {
	appendObserved(&e.operations, "discover", ctx.position)
	if !ctx.hasFields {
		return
	}
	if count, ok := payloadInt(ctx.fields, "candidate_count"); ok {
		e.browserCount = count
		e.browserCountSet = true
		e.browserCountPos = ctx.position
	}
}

func (e *BrowserEvidence) onTargetsSnapshot(ctx eventContext) {
	if !ctx.hasFields {
		return
	}
	var targets []map[string]json.RawMessage
	if json.Unmarshal(ctx.fields["targets"], &targets) != nil {
		return
	}
	for _, target := range targets {
		id, idOK := payloadString(target, "id")
		if !idOK || id == "" {
			continue
		}
		eligible, _ := payloadBool(target, "eligible")
		origin, _ := payloadString(target, "origin")
		key := ctx.event.BrowserID + "\x00" + id
		e.targets[key] = persistedTarget{Origin: origin, Eligible: eligible, Position: ctx.position}
	}
}

func (e *BrowserEvidence) onTargetSelected(ctx eventContext) {
	e.selectedTarget = ctx.event.TargetID
	e.selectedTargetPos = ctx.position
	appendObserved(&e.operations, "select", ctx.position)
}

func (e *BrowserEvidence) onCatalogToolAdded(ctx eventContext) {
	if !ctx.hasFields {
		return
	}
	var tools []map[string]json.RawMessage
	if json.Unmarshal(ctx.fields["tools"], &tools) != nil {
		return
	}
	for _, rawTool := range tools {
		name, nameOK := payloadString(rawTool, "name")
		if !nameOK || name == "" {
			continue
		}
		ref, _ := payloadString(rawTool, "ref")
		e.catalog[name] = persistedTool{Name: name, Ref: ref, InputSchema: append(json.RawMessage(nil), rawTool["input_schema"]...), Position: ctx.position}
		if ref != "" {
			e.catalogByRef[ref] = name
		}
	}
}

func (e *BrowserEvidence) onCatalogToolRemoved(ctx eventContext) {
	var refs []string
	if json.Unmarshal(ctx.fields["tool_refs"], &refs) != nil {
		return
	}
	for _, ref := range refs {
		if name := e.catalogByRef[ref]; name != "" {
			delete(e.catalog, name)
			delete(e.catalogByRef, ref)
		}
	}
}

func (e *BrowserEvidence) onCatalogReady(ctx eventContext) {
	e.catalogGeneration = int64(ctx.event.Generation)
	e.catalogSet = true
	e.catalogPos = ctx.position
	appendObserved(&e.operations, "list_tools", ctx.position)
}

func (e *BrowserEvidence) onChromeTargetClosed(ctx eventContext) {
	e.closed = true
	e.closedPos = ctx.position
	appendObserved(&e.operations, "close", ctx.position)
}

func appendObserved(operations *[]ObservedOperation, name string, position int) {
	if operations == nil || name == "" {
		return
	}
	*operations = append(*operations, ObservedOperation{
		Name:              name,
		OperationPosition: len(*operations) + 1,
		EventPosition:     position,
	})
}

func eventFields(event testkit.Event) (map[string]json.RawMessage, bool) {
	if len(event.Payload) == 0 || bytes.TrimSpace(event.Payload)[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return nil, false
	}
	return fields, true
}

func payloadString(fields map[string]json.RawMessage, key string) (string, bool) {
	var value string
	if err := json.Unmarshal(fields[key], &value); err != nil {
		return "", false
	}
	return value, true
}

func payloadBool(fields map[string]json.RawMessage, key string) (bool, bool) {
	var value bool
	if err := json.Unmarshal(fields[key], &value); err != nil {
		return false, false
	}
	return value, true
}

func payloadInt(fields map[string]json.RawMessage, key string) (int64, bool) {
	decoder := json.NewDecoder(bytes.NewReader(fields[key]))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil
}
