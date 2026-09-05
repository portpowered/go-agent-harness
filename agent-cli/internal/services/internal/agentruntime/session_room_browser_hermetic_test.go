package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRoomBrowserCapabilitiesProveSharedTopologyIsolationAndReceipts(t *testing.T) {
	const (
		alphaID       = "alpha"
		betaID        = "beta"
		browserID     = "browser-shared"
		sharedTarget  = "tab-cubecade"
		alphaTarget   = "tab-alpha-only"
		sharedOrigin  = "https://cubecade.test"
		sharedPageURL = sharedOrigin + "/cube"
	)

	candidate := webmcp.BrowserCandidate{
		ID:       browserID,
		Product:  "Chrome/Fake",
		HTTPURL:  "http://127.0.0.1:9222",
		Loopback: true,
	}
	queueTool := roomHermeticPageTool(
		"queue_cube_moves",
		"frame-cube",
		`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}},"required":["moves"],"additionalProperties":false}`,
	)
	stateTool := roomHermeticPageTool(
		"get_cube_state",
		"frame-cube",
		`{"type":"object","properties":{},"additionalProperties":false}`,
	)
	alphaOnlyTool := roomHermeticPageTool(
		"alpha_private_state",
		"frame-alpha",
		`{"type":"object","properties":{},"additionalProperties":false}`,
	)
	runtime := &roomHermeticBrowserRuntime{inner: testkit.NewScriptedBrowserRuntime(
		testkit.NewBrowserConfig(candidate,
			testkit.NewTargetConfig(
				webmcp.Target{
					BrowserID: candidate.ID,
					ID:        sharedTarget,
					Type:      "page",
					Title:     "Cubecade",
					URL:       sharedPageURL,
					Origin:    sharedOrigin,
				},
				testkit.WithInitialCatalog(queueTool, stateTool),
			),
			testkit.NewTargetConfig(
				webmcp.Target{
					BrowserID: candidate.ID,
					ID:        alphaTarget,
					Type:      "page",
					Title:     "Alpha-only page",
					URL:       sharedOrigin + "/alpha",
					Origin:    sharedOrigin,
				},
				testkit.WithInitialCatalog(alphaOnlyTool),
			),
		),
	)}
	t.Cleanup(func() { _ = runtime.inner.Close() })

	ids := []string{alphaID, betaID}
	inferencers := map[string]*roomTestInferencer{
		alphaID: {},
		betaID:  {},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	for index := range opts.Manifest.Participants {
		browser := room.DefaultBrowserToolsConfig()
		browser.Connection.CDPURL = candidate.HTTPURL
		browser.Selection.Browser = string(candidate.ID)
		browser.Selection.Tab = sharedTarget
		browser.Selection.Persist = false
		opts.Manifest.Participants[index].BrowserTools = &browser
	}

	var capabilityMu sync.Mutex
	brokers := make(map[string]*webmcp.StatefulBroker, len(ids))
	refreshes := make(map[string]func(context.Context) ([]messages.ToolDefinition, error), len(ids))
	closeCalls := make(map[string]int, len(ids))
	opts.BrowserCapabilitiesFactory = func(participant room.Participant) (RoomParticipantBrowserCapabilities, error) {
		idSource := &roomHermeticIDSource{prefix: participant.ID, source: testkit.NewDeterministicIDSource(participant.ID)}
		broker := webmcp.NewBroker(webmcp.BrokerOptions{
			Runtime:           runtime,
			Discoverer:        roomHermeticDiscoverer{candidate: candidate},
			IDs:               idSource,
			ToolRefFactory:    webmcp.StableToolRef,
			InvocationTimeout: time.Minute,
		})
		toolSet := webmcpTools.NewBrokerToolSet(broker)
		stableDefinitions := toolSet.Definitions()
		reservedNames := roomDefinitionNames(stableDefinitions)
		toolSet.SetReservedToolNames(reservedNames)
		refresh := func(ctx context.Context) ([]messages.ToolDefinition, error) {
			pageDefinitions, err := toolSet.PageToolDefinitionsWithError(ctx)
			if err != nil {
				return nil, err
			}
			return append(append([]messages.ToolDefinition(nil), stableDefinitions...), pageDefinitions...), nil
		}
		initialize := func(ctx context.Context) error {
			_, err := broker.Select(ctx, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: sharedTarget})
			return err
		}
		capabilityMu.Lock()
		brokers[participant.ID] = broker
		refreshes[participant.ID] = refresh
		capabilityMu.Unlock()
		return RoomParticipantBrowserCapabilities{
			Executor:               toolSet.Executor(),
			Definitions:            stableDefinitions,
			ToolDefinitionBase:     stableDefinitions,
			RefreshToolDefinitions: refresh,
			BrowserWatch:           broker.Watch,
			Initialize:             initialize,
			Close: func() error {
				capabilityMu.Lock()
				closeCalls[participant.ID]++
				capabilityMu.Unlock()
				return broker.Close()
			},
		}, nil
	}

	var plans []*roomParticipantPlan
	t.Cleanup(func() { _ = closeRoomParticipantPlanCapabilities(plans) })
	var err error
	plans, _, err = buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("build room participant plans: %v", err)
	}
	if len(plans) != len(ids) || len(runtime.handlesSnapshot()) != len(ids) {
		t.Fatalf("plans/handles = %d/%d, want one per participant", len(plans), len(ids))
	}

	for _, plan := range plans {
		assertRoomBrowserDefinitionNames(t, plan.options.ToolDefinitions,
			webmcp.GetContextToolName,
			webmcp.ListTabsToolName,
			webmcp.SelectTabToolName,
			webmcp.ListToolsToolName,
			webmcp.InvokeToolName,
			webmcp.CancelToolName,
			webmcp.OpenTabToolName,
			webmcp.NavigateTabToolName,
			webmcp.ShowPageToolName,
			queueTool.Name,
			stateTool.Name,
		)
	}

	alphaBroker := brokers[alphaID]
	betaBroker := brokers[betaID]
	if alphaBroker == nil || betaBroker == nil || alphaBroker == betaBroker {
		t.Fatalf("broker ownership = alpha=%p beta=%p, want distinct brokers", alphaBroker, betaBroker)
	}
	handles := runtime.handlesSnapshot()
	alphaSession := handles[0].TargetSession(sharedTarget)
	betaSession := handles[1].TargetSession(sharedTarget)
	if alphaSession == nil || betaSession == nil || alphaSession == betaSession {
		t.Fatalf("target sessions = alpha=%p beta=%p, want distinct attached clients", alphaSession, betaSession)
	}
	alphaCatalog := roomHermeticListTools(t, alphaBroker)
	betaCatalog := roomHermeticListTools(t, betaBroker)
	alphaRefs := roomHermeticCatalogRefs(t, alphaCatalog)
	betaRefs := roomHermeticCatalogRefs(t, betaCatalog)
	if alphaRefs[queueTool.Name] == "" || betaRefs[stateTool.Name] == "" {
		t.Fatalf("initial catalogs = alpha=%v beta=%v, want shared page tools", alphaRefs, betaRefs)
	}

	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	alphaWatch := alphaBroker.Watch(watchContext)
	betaWatch := betaBroker.Watch(watchContext)
	alphaSession.BlockInvocations()
	betaSession.BlockInvocations()
	operationContext, cancelOperations := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelOperations()
	start := make(chan struct{})
	alphaCallCh := make(chan roomHermeticInvocationCall, 1)
	betaCallCh := make(chan roomHermeticInvocationCall, 1)
	go func() {
		<-start
		result, invokeErr := alphaBroker.Invoke(operationContext, webmcp.InvokeRequest{
			ToolRef: alphaRefs[queueTool.Name],
			Input:   json.RawMessage(`{"moves":["R","U","R'"]}`),
			Reason:  "scramble the shared cube",
		})
		alphaCallCh <- roomHermeticInvocationCall{result: result, err: invokeErr}
	}()
	go func() {
		<-start
		result, invokeErr := betaBroker.Invoke(operationContext, webmcp.InvokeRequest{
			ToolRef: betaRefs[stateTool.Name],
			Input:   json.RawMessage(`{}`),
			Reason:  "read the shared cube after the scramble",
		})
		betaCallCh <- roomHermeticInvocationCall{result: result, err: invokeErr}
	}()
	close(start)
	alphaCall := roomHermeticReceiveInvocation(t, operationContext, alphaCallCh)
	betaCall := roomHermeticReceiveInvocation(t, operationContext, betaCallCh)
	if alphaCall.err != nil || betaCall.err != nil || alphaCall.result.State != webmcp.InvocationDispatched || betaCall.result.State != webmcp.InvocationDispatched {
		t.Fatalf("concurrent dispatches = alpha=%#v/%v beta=%#v/%v, want both dispatched", alphaCall.result, alphaCall.err, betaCall.result, betaCall.err)
	}
	if alphaCall.result.InvocationID == betaCall.result.InvocationID {
		t.Fatalf("participant invocation IDs collided: alpha=%q beta=%q", alphaCall.result.InvocationID, betaCall.result.InvocationID)
	}
	if _, err := roomHermeticWaitTargetInvocation(t, operationContext, alphaSession); err != nil {
		t.Fatalf("observe alpha target invocation: %v", err)
	}
	if _, err := roomHermeticWaitTargetInvocation(t, operationContext, betaSession); err != nil {
		t.Fatalf("observe beta target invocation: %v", err)
	}
	if len(alphaSession.PendingInvocations()) != 1 || len(betaSession.PendingInvocations()) != 1 {
		t.Fatalf("pending target invocations = alpha=%v beta=%v, want both admitted before release", alphaSession.PendingInvocations(), betaSession.PendingInvocations())
	}
	cubePage := &roomHermeticCubePage{state: roomHermeticCubeState{Target: sharedTarget}}
	moves := []string{"R", "U", "R'"}
	writeOutput, err := cubePage.QueueMoves(moves)
	if err != nil {
		t.Fatalf("apply fake cube write: %v", err)
	}
	if err := alphaSession.ReleaseInvocation(alphaCall.result.InvocationID, writeOutput); err != nil {
		t.Fatalf("release alpha write: %v", err)
	}
	alphaTerminal, err := alphaBroker.WaitInvocation(operationContext, alphaCall.result.InvocationID)
	if err != nil {
		t.Fatalf("wait alpha terminal result: %v", err)
	}
	if alphaTerminal.State != webmcp.InvocationCompleted || string(alphaTerminal.Output) != string(writeOutput) {
		t.Fatalf("alpha terminal = %#v, want completed write receipt", alphaTerminal)
	}
	alphaTargetTerminal, err := alphaSession.WaitForTerminal(operationContext, alphaCall.result.InvocationID)
	if err != nil || alphaTargetTerminal.State != webmcp.InvocationCompleted || alphaTargetTerminal.TargetID != sharedTarget {
		t.Fatalf("alpha target terminal = %#v/%v, want completed shared-target status", alphaTargetTerminal, err)
	}
	stateOutput, err := cubePage.ReadState()
	if err != nil {
		t.Fatalf("read fake cube state: %v", err)
	}
	if err := betaSession.ReleaseInvocation(betaCall.result.InvocationID, stateOutput); err != nil {
		t.Fatalf("release beta read: %v", err)
	}
	betaTerminal, err := betaBroker.WaitInvocation(operationContext, betaCall.result.InvocationID)
	if err != nil {
		t.Fatalf("wait beta terminal result: %v", err)
	}
	if betaTerminal.State != webmcp.InvocationCompleted || string(betaTerminal.Output) != string(stateOutput) {
		t.Fatalf("beta terminal = %#v, want state after alpha write %s", betaTerminal, stateOutput)
	}
	betaTargetTerminal, err := betaSession.WaitForTerminal(operationContext, betaCall.result.InvocationID)
	if err != nil || betaTargetTerminal.State != webmcp.InvocationCompleted || betaTargetTerminal.TargetID != sharedTarget {
		t.Fatalf("beta target terminal = %#v/%v, want completed shared-target status", betaTargetTerminal, err)
	}
	alphaCreated := roomHermeticWaitBrokerEvent(t, operationContext, alphaWatch, func(event webmcp.BrokerEvent) bool {
		return event.Type == webmcp.BrokerEventInvocationCreated && event.InvocationID == alphaCall.result.InvocationID && event.ToolRef == alphaRefs[queueTool.Name]
	})
	betaCreated := roomHermeticWaitBrokerEvent(t, operationContext, betaWatch, func(event webmcp.BrokerEvent) bool {
		return event.Type == webmcp.BrokerEventInvocationCreated && event.InvocationID == betaCall.result.InvocationID && event.ToolRef == betaRefs[stateTool.Name]
	})
	if alphaCreated.State != webmcp.InvocationQueued || betaCreated.State != webmcp.InvocationQueued {
		t.Fatalf("created receipts = alpha=%#v beta=%#v, want queued admission receipts", alphaCreated, betaCreated)
	}
	alphaReceiptTerminal := roomHermeticWaitBrokerEvent(t, operationContext, alphaWatch, func(event webmcp.BrokerEvent) bool {
		return event.Type == webmcp.BrokerEventInvocationTerminal && event.InvocationID == alphaCall.result.InvocationID
	})
	betaReceiptTerminal := roomHermeticWaitBrokerEvent(t, operationContext, betaWatch, func(event webmcp.BrokerEvent) bool {
		return event.Type == webmcp.BrokerEventInvocationTerminal && event.InvocationID == betaCall.result.InvocationID
	})
	if alphaReceiptTerminal.State != webmcp.InvocationCompleted || betaReceiptTerminal.State != webmcp.InvocationCompleted {
		t.Fatalf("terminal receipts = alpha=%#v beta=%#v, want completed own statuses", alphaReceiptTerminal, betaReceiptTerminal)
	}
	if _, ok := alphaBroker.Invocation(betaCall.result.InvocationID); ok {
		t.Fatal("alpha broker retained beta invocation ownership")
	}
	if _, ok := betaBroker.Invocation(alphaCall.result.InvocationID); ok {
		t.Fatal("beta broker retained alpha invocation ownership")
	}
	if _, err := alphaBroker.WaitInvocation(operationContext, betaCall.result.InvocationID); !errors.Is(err, webmcp.ErrInvocationNotFound) {
		t.Fatalf("alpha wait for beta invocation = %v, want ErrInvocationNotFound", err)
	}
	if _, err := betaBroker.WaitInvocation(operationContext, alphaCall.result.InvocationID); !errors.Is(err, webmcp.ErrInvocationNotFound) {
		t.Fatalf("beta wait for alpha invocation = %v, want ErrInvocationNotFound", err)
	}

	if _, err := alphaBroker.Select(operationContext, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: alphaTarget}); err != nil {
		t.Fatalf("alpha select second fake tab: %v", err)
	}
	alphaRefreshed, err := refreshes[alphaID](operationContext)
	if err != nil {
		t.Fatalf("refresh alpha catalog after tab switch: %v", err)
	}
	betaRefreshed, err := refreshes[betaID](operationContext)
	if err != nil {
		t.Fatalf("refresh beta catalog after alpha tab switch: %v", err)
	}
	if !roomDefinitionNamesContain(alphaRefreshed, alphaOnlyTool.Name) || roomDefinitionNamesContain(alphaRefreshed, stateTool.Name) {
		t.Fatalf("alpha refreshed definitions = %v, want alpha-only catalog", roomDefinitionNames(alphaRefreshed))
	}
	if !roomDefinitionNamesContain(betaRefreshed, stateTool.Name) || roomDefinitionNamesContain(betaRefreshed, alphaOnlyTool.Name) {
		t.Fatalf("beta refreshed definitions = %v, want shared catalog unchanged", roomDefinitionNames(betaRefreshed))
	}
	betaSelected, err := betaBroker.Selected(operationContext)
	if err != nil || betaSelected.Key.TargetID != sharedTarget {
		t.Fatalf("beta selection after alpha switch = %#v/%v, want %q", betaSelected, err, sharedTarget)
	}

	if err := plans[0].capabilityCoordinator.Close(); err != nil {
		t.Fatalf("close alpha capability owner: %v", err)
	}
	betaSelected, err = betaBroker.Selected(operationContext)
	if err != nil || betaSelected.Key.TargetID != sharedTarget || !betaSelected.Connected {
		t.Fatalf("beta after alpha close = %#v/%v, want connected shared target", betaSelected, err)
	}
	betaAfterClose, err := betaBroker.Invoke(operationContext, webmcp.InvokeRequest{
		ToolRef: betaRefs[stateTool.Name],
		Input:   json.RawMessage(`{}`),
		Reason:  "prove beta survives alpha close",
	})
	if err != nil || betaAfterClose.State != webmcp.InvocationDispatched {
		t.Fatalf("beta invoke after alpha close = %#v/%v, want dispatched", betaAfterClose, err)
	}
	if err := betaSession.ReleaseInvocation(betaAfterClose.InvocationID, stateOutput); err != nil {
		t.Fatalf("release beta post-close read: %v", err)
	}
	postCloseTerminal, err := betaBroker.WaitInvocation(operationContext, betaAfterClose.InvocationID)
	if err != nil || postCloseTerminal.State != webmcp.InvocationCompleted || string(postCloseTerminal.Output) != string(stateOutput) {
		t.Fatalf("beta post-close terminal = %#v/%v, want completed state read", postCloseTerminal, err)
	}
	if _, err := betaSession.WaitForTerminal(operationContext, betaAfterClose.InvocationID); err != nil {
		t.Fatalf("observe beta post-close terminal: %v", err)
	}

	if len(alphaBroker.PendingInvocations()) != 0 || len(betaBroker.PendingInvocations()) != 0 || len(alphaSession.PendingInvocations()) != 0 || len(betaSession.PendingInvocations()) != 0 {
		t.Fatalf("pending after hermetic proof = alpha broker=%v/session=%v beta broker=%v/session=%v, want none", alphaBroker.PendingInvocations(), alphaSession.PendingInvocations(), betaBroker.PendingInvocations(), betaSession.PendingInvocations())
	}
	if err := closeRoomParticipantPlanCapabilities(plans); err != nil {
		t.Fatalf("close all room browser capability owners: %v", err)
	}
	if err := closeRoomParticipantPlanCapabilities(plans); err != nil {
		t.Fatalf("repeat close all room browser capability owners: %v", err)
	}
	capabilityMu.Lock()
	defer capabilityMu.Unlock()
	for _, id := range ids {
		if closeCalls[id] != 1 {
			t.Fatalf("participant %q capability close calls = %d, want exactly one", id, closeCalls[id])
		}
	}
}

type roomHermeticInvocationCall struct {
	result webmcp.InvokeResult
	err    error
}

type roomHermeticCubeState struct {
	Target string   `json:"target"`
	Moves  []string `json:"moves"`
}

type roomHermeticCubePage struct {
	mu    sync.Mutex
	state roomHermeticCubeState
}

func (p *roomHermeticCubePage) QueueMoves(moves []string) (json.RawMessage, error) {
	p.mu.Lock()
	p.state.Moves = append([]string(nil), moves...)
	moveCount := len(p.state.Moves)
	p.mu.Unlock()
	return json.Marshal(struct {
		Accepted  bool `json:"accepted"`
		MoveCount int  `json:"move_count"`
	}{Accepted: true, MoveCount: moveCount})
}

func (p *roomHermeticCubePage) ReadState() (json.RawMessage, error) {
	p.mu.Lock()
	state := roomHermeticCubeState{Target: p.state.Target, Moves: append([]string(nil), p.state.Moves...)}
	p.mu.Unlock()
	return json.Marshal(state)
}

type roomHermeticBrowserRuntime struct {
	inner   *testkit.ScriptedBrowserRuntime
	mu      sync.Mutex
	handles []*testkit.ScriptedBrowserHandle
}

func (r *roomHermeticBrowserRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	handle, err := r.inner.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	scripted, ok := handle.(*testkit.ScriptedBrowserHandle)
	if !ok {
		_ = handle.Close()
		return nil, fmt.Errorf("room hermetic runtime returned %T, want scripted handle", handle)
	}
	r.mu.Lock()
	r.handles = append(r.handles, scripted)
	r.mu.Unlock()
	return handle, nil
}

func (r *roomHermeticBrowserRuntime) handlesSnapshot() []*testkit.ScriptedBrowserHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*testkit.ScriptedBrowserHandle(nil), r.handles...)
}

type roomHermeticDiscoverer struct {
	candidate webmcp.BrowserCandidate
}

func (d roomHermeticDiscoverer) Discover(_ context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if options.BrowserID != "" && options.BrowserID != d.candidate.ID {
		return nil, nil
	}
	return []webmcp.BrowserCandidate{d.candidate}, nil
}

type roomHermeticIDSource struct {
	prefix string
	source *testkit.DeterministicIDSource
}

func (s *roomHermeticIDSource) NewToolRef() (webmcp.ToolRef, error) {
	return s.source.NewToolRef()
}

func (s *roomHermeticIDSource) NewInvocationID() (webmcp.InvocationID, error) {
	id, err := s.source.NewInvocationID()
	if err != nil {
		return "", err
	}
	return webmcp.InvocationID(s.prefix + "-" + string(id)), nil
}

func roomHermeticPageTool(name string, frame webmcp.FrameID, schema string) webmcp.ToolDescriptor {
	return webmcp.ToolDescriptor{
		Name:        name,
		Description: "Hermetic room page tool " + name,
		FrameID:     frame,
		InputSchema: json.RawMessage(schema),
	}
}

func roomHermeticListTools(t *testing.T, broker *webmcp.StatefulBroker) webmcp.ToolCatalogSnapshot {
	t.Helper()
	catalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list hermetic room page tools: %v", err)
	}
	return catalog
}

func roomHermeticCatalogRefs(t *testing.T, catalog webmcp.ToolCatalogSnapshot) map[string]webmcp.ToolRef {
	t.Helper()
	refs := make(map[string]webmcp.ToolRef, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		refs[tool.Name] = tool.Ref
	}
	return refs
}

func roomHermeticReceiveInvocation(t *testing.T, ctx context.Context, results <-chan roomHermeticInvocationCall) roomHermeticInvocationCall {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-ctx.Done():
		t.Fatalf("receive hermetic invocation: %v", ctx.Err())
		return roomHermeticInvocationCall{}
	}
}

func roomHermeticWaitTargetInvocation(t *testing.T, ctx context.Context, session *testkit.ScriptedTargetSession) (testkit.InvocationRecord, error) {
	t.Helper()
	record, err := session.WaitForInvocation(ctx)
	if err != nil {
		return testkit.InvocationRecord{}, err
	}
	if record.BrowserID == "" || record.TargetID == "" || record.ID == "" {
		t.Fatalf("target invocation record = %#v, want browser/target/correlation identity", record)
	}
	return record, nil
}

func roomHermeticWaitBrokerEvent(t *testing.T, ctx context.Context, events <-chan webmcp.BrokerEvent, match func(webmcp.BrokerEvent) bool) webmcp.BrokerEvent {
	t.Helper()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("hermetic broker watch closed before the expected event")
			}
			if match(event) {
				return event
			}
		case <-ctx.Done():
			t.Fatalf("wait for hermetic broker event: %v", ctx.Err())
			return webmcp.BrokerEvent{}
		}
	}
}
