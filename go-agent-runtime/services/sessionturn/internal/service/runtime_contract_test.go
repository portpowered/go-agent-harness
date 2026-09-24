package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

func assertPreparedProviderInstructions(t *testing.T, inferencer messages.SessionInferencer, want string) {
	t.Helper()
	requester, ok := inferencer.(interface {
		Request() inference.SessionRequest
	})
	if !ok || requester.Request().Config.Instructions != want {
		t.Fatalf("prepared provider request = %v, want instructions", inferencer)
	}
}

func openInstructionSession(t *testing.T, inferencer messages.SessionInferencer, provider *runtimeProbeSession, instructions string) messages.Session {
	t.Helper()
	connected, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if !provider.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen}) {
		t.Fatal("provider rejected session-open event")
	}
	select {
	case sent := <-provider.sent:
		value, ok := sent.Value.(*messages.SessionUpdateValue)
		if sent.Type != messages.StreamTypeSessionUpdate || !ok || value.Instructions != instructions {
			t.Fatalf("provider instructions update = %+v, want one SESSION.UPDATE", sent)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not receive resolved instructions")
	}
	if _, ok := connected.Receive().ReadBlocking(connected.Done()); !ok {
		t.Fatal("session-open event was not forwarded")
	}
	forwardProviderEvent(t, connected, provider, messages.StreamTypeSessionCreated)
	select {
	case duplicate := <-provider.sent:
		t.Fatalf("provider received a duplicate instructions update: %+v", duplicate)
	default:
	}
	return connected
}

func forwardProviderEvent(t *testing.T, connected messages.Session, provider *runtimeProbeSession, kind messages.StreamMessageType) {
	t.Helper()
	if !provider.receive.Write(context.Background(), messages.StreamMessage{Type: kind}) {
		t.Fatalf("provider rejected %s event", kind)
	}
	if _, ok := connected.Receive().ReadBlocking(connected.Done()); !ok {
		t.Fatalf("%s event was not forwarded", kind)
	}
}

func assertInstructionSessionCapabilities(t *testing.T, connected messages.Session, provider *runtimeProbeSession) sessionturn.Session {
	t.Helper()
	wrapped, ok := connected.(sessionturn.Session)
	if !ok || !wrapped.SupportsCompleteMessages() || !wrapped.SupportsCompleteMessagesWithoutResponse() || wrapped.SupportsResponseRequests() || !errors.Is(wrapped.TerminalError(), provider.terminalErr) {
		t.Fatal("instruction session did not preserve provider message and terminal capabilities")
	}
	mediaOwner, ok := connected.(interface {
		RTCMedia() (sharedaudio.MediaEndpoints, bool)
	})
	if !ok {
		t.Fatalf("instruction session %T lost its neutral RTC media seam", connected)
	}
	if _, available := mediaOwner.RTCMedia(); !available {
		t.Fatal("instruction session did not preserve RTC media ownership")
	}
	if got := wrapped.RequestResponse(context.Background()).Status; got != messages.SessionSendTerminalFailure {
		t.Fatalf("unsupported response request status = %v", got)
	}
	return wrapped
}

func assertInstructionSessionForwards(t *testing.T, wrapped sessionturn.Session, provider *runtimeProbeSession) {
	t.Helper()
	stream := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("forwarded")}
	if !wrapped.Send(context.Background(), stream) || !wrapped.SendWithOutcome(context.Background(), stream).OK() {
		t.Fatal("instruction session did not forward stream sends")
	}
	if (<-provider.sent).Type != messages.StreamTypeTextDelta || (<-provider.sent).Type != messages.StreamTypeTextDelta {
		t.Fatal("instruction session changed a forwarded stream send")
	}
	if !wrapped.SendMessage(context.Background(), messages.Message{ToolCallID: "complete"}) || !wrapped.SendMessageWithoutResponse(context.Background(), messages.Message{ToolCallID: "deferred"}) {
		t.Fatal("instruction session did not forward complete-message sends")
	}
	if (<-provider.complete).ToolCallID != "complete" || (<-provider.completeDeferred).ToolCallID != "deferred" {
		t.Fatal("instruction session changed a forwarded complete message")
	}
}

func TestPreparedRuntimeSnapshotsPolicyAndToolsAndClosesOwnedResourcesOnce(t *testing.T) {
	cleanupErr := errors.New("image cleanup failed")
	cleanupCalls := 0
	runtime, definitions, tool, observed := prepareRuntimeWithToolObservers(t, cleanupErr, &cleanupCalls)
	assertRuntimeToolSnapshots(t, runtime, definitions, tool, observed)
	assertRuntimeSurfaceAndMissingInferencerFailures(t, runtime)
	if err := runtime.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("Close = %v, want image cleanup cause", err)
	}
	if err := runtime.Close(); !errors.Is(err, cleanupErr) || cleanupCalls != 1 {
		t.Fatalf("second Close = %v, cleanup calls=%d; want cached error and one cleanup", err, cleanupCalls)
	}
}

func TestPreparedRuntimeSendsFallbackInstructionsOncePerProviderSession(t *testing.T) {
	provider := newRuntimeProbeSession()
	provider.terminalErr = errors.New("provider terminal error")
	instructions := "keep the user's terms intact"
	runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{
		SessionInferencer: runtimeProbeInferencer{session: provider},
		InstructionsText:  instructions,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	inferencer := runtime.Inferencer()
	assertPreparedProviderInstructions(t, inferencer, instructions)
	connected := openInstructionSession(t, inferencer, provider, instructions)
	wrapped := assertInstructionSessionCapabilities(t, connected, provider)
	assertInstructionSessionForwards(t, wrapped, provider)
	if err := connected.Close(); err != nil {
		t.Fatalf("close connected session: %v", err)
	}
}

func TestInstructionSessionReportsRejectedProviderConfiguration(t *testing.T) {
	provider := newRuntimeProbeSession()
	provider.rejectSends = true
	runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{
		SessionInferencer: runtimeProbeInferencer{session: provider},
		InstructionsText:  "required safety policy",
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	connected, err := runtime.Inferencer().ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if !provider.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen}) {
		t.Fatal("provider rejected session-open event")
	}
	message, ok := connected.Receive().ReadBlocking(connected.Done())
	if !ok || message.Type != messages.StreamTypeError {
		t.Fatalf("provider configuration failure message = %+v, ok=%v", message, ok)
	}
	value, ok := message.Value.(*messages.ErrorValue)
	if !ok || !strings.Contains(value.Message, "send session instructions") {
		t.Fatalf("provider configuration error = %#v", message.Value)
	}
	select {
	case <-connected.Done():
	case <-time.After(time.Second):
		t.Fatal("instruction session did not stop after the provider rejected configuration")
	}
}

func TestPublicationReadyRefreshesAndPublishesSelectedBrowserTools(t *testing.T) {
	watchStarted := make(chan func(sessionturn.BrowserEvent) bool, 1)
	published := make(chan []messages.ToolDefinition, 1)
	dynamic := messages.ToolDefinition{Name: "browser_search", Description: "search the selected page"}
	service := New(Dependencies{})
	publication, err := service.StartPublication(context.Background(), sessionturn.PublicationRequest{
		BaseDefinitions: []messages.ToolDefinition{{Name: "base_tool"}},
		Browser: sessionturn.BrowserRequest{
			Watch: func(ctx context.Context, consume func(sessionturn.BrowserEvent) bool) error {
				watchStarted <- consume
				<-ctx.Done()
				return nil
			},
			Refresh: func(context.Context) ([]messages.ToolDefinition, error) {
				return []messages.ToolDefinition{dynamic}, nil
			},
		},
		Publish: func(_ context.Context, definitions []messages.ToolDefinition) error {
			published <- append([]messages.ToolDefinition(nil), definitions...)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartPublication: %v", err)
	}
	defer publication.Stop()
	var consume func(sessionturn.BrowserEvent) bool
	select {
	case consume = <-watchStarted:
	case <-time.After(time.Second):
		t.Fatal("browser watch did not start")
	}
	if !consume(sessionturn.BrowserEvent{
		Type:       sessionturn.BrowserEventSelectionChanged,
		BrowserID:  "browser-1",
		TargetID:   "tab-2",
		Generation: 4,
		Sequence:   7,
	}) {
		t.Fatal("publication rejected the current browser selection")
	}
	publication.MarkReady()
	select {
	case definitions := <-published:
		if len(definitions) != 2 || definitions[0].Name != "base_tool" || definitions[1].Name != "browser_search" {
			t.Fatalf("published definitions = %+v, want base and selected browser tools", definitions)
		}
	case <-time.After(time.Second):
		t.Fatal("publication did not refresh the ready browser tool snapshot")
	}
	state := publication.State()
	if state.Lifecycle != sessionturn.PublicationReady || state.LastSequence != 7 || state.PublicationCount != 1 || state.BrowserID != "browser-1" || state.TargetID != "tab-2" || state.Generation != 4 {
		t.Fatalf("publication state = %+v, want the current browser selection and one publish", state)
	}
}

func TestPublicationDebouncesRapidSelectionChangesBeforeRefresh(t *testing.T) {
	watchStarted := make(chan func(sessionturn.BrowserEvent) bool, 1)
	timerFactory := publicationTimerFactoryProbe{created: make(chan *publicationTimerProbe, 1)}
	refreshes := make(chan int, 4)
	published := make(chan []messages.ToolDefinition, 1)
	var refreshCount int
	service := New(Dependencies{})
	publication, err := service.StartPublication(context.Background(), sessionturn.PublicationRequest{
		BaseDefinitions: []messages.ToolDefinition{{Name: "base_tool"}},
		Browser: sessionturn.BrowserRequest{
			TimerFactory: timerFactory,
			Watch: func(ctx context.Context, consume func(sessionturn.BrowserEvent) bool) error {
				watchStarted <- consume
				<-ctx.Done()
				return nil
			},
			Refresh: func(context.Context) ([]messages.ToolDefinition, error) {
				refreshCount++
				refreshes <- refreshCount
				if refreshCount == 1 {
					return []messages.ToolDefinition{{Name: "base_tool"}}, nil
				}
				return []messages.ToolDefinition{{Name: "base_tool"}, {Name: "tab_tool"}}, nil
			},
		},
		Publish: func(_ context.Context, definitions []messages.ToolDefinition) error {
			published <- append([]messages.ToolDefinition(nil), definitions...)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartPublication: %v", err)
	}
	defer publication.Stop()
	consume := awaitBrowserWatcher(t, watchStarted)
	publication.MarkReady()
	awaitPublicationRefresh(t, refreshes, 1, "initial ready refresh did not run")
	assertNoBrowserPublication(t, published, "before a browser change")
	requireBrowserEvent(t, consume, sessionturn.BrowserEvent{Type: sessionturn.BrowserEventSelectionChanged, BrowserID: "browser", TargetID: "tab-a", Generation: 1, Sequence: 1}, "first selection")
	timer := <-timerFactory.created
	requireBrowserEvent(t, consume, sessionturn.BrowserEvent{Type: sessionturn.BrowserEventCatalogChanged, BrowserID: "browser", TargetID: "tab-a", Generation: 2, Sequence: 2}, "replacement selection")
	awaitDebounceReset(t, timer.resetCalled)
	assertNoBrowserPublication(t, published, "before debounce elapsed")
	timer.events <- time.Now()
	awaitPublicationRefresh(t, refreshes, 2, "debounced browser refresh did not run")
	awaitBrowserPublication(t, published)
	assertDebouncedPublicationState(t, publication)
}

func TestAudioOutputWaitDrainsObservedAudioAndReturnsWithinItsShutdownBound(t *testing.T) {
	runtime, audio, provider, connected, _ := prepareObservedAudioOutput(t)
	audioSession := assertAudioOutputSessionCapabilities(t, connected, provider)
	assertAudioOutputForwards(t, audioSession, provider)
	awaitBoundedAudioShutdown(t, runtime, audio, provider)
}

func TestAudioOutputRetainsObserverFailureAndJoinsProviderClose(t *testing.T) {
	runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	provider := newRuntimeProbeSession()
	observerErr := errors.New("audio observer failed")
	audio, err := runtime.AttachAudioOutput(runtimeProbeInferencer{session: provider}, func(context.Context, []byte, messages.StreamMessage) error {
		return observerErr
	})
	if err != nil {
		t.Fatalf("AttachAudioOutput: %v", err)
	}
	connected, err := audio.Inferencer().ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if !provider.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioDeltaValueWithMediaType([]byte{1}, "audio/pcm"),
	}) {
		t.Fatal("provider rejected audio output delta")
	}
	select {
	case <-connected.Done():
	case <-time.After(time.Second):
		t.Fatal("observer failure did not stop and join the audio session")
	}
	if err := audio.Wait(); !errors.Is(err, observerErr) {
		t.Fatalf("Wait error = %v, want retained observer cause", err)
	}
	select {
	case <-provider.Done():
	case <-time.After(time.Second):
		t.Fatal("observer failure did not close the provider session")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("runtime Close: %v", err)
	}
}

func TestPreparedImageInferencerSendsOneCopiedImageTurnWithRequestedResponseMode(t *testing.T) {
	assertPreparedImageInferencerMode(t, false)
	assertPreparedImageInferencerMode(t, true)
}

func TestResolveInstructionsFailsClosedForMissingCompositionInputs(t *testing.T) {
	service := New(Dependencies{})
	if got, err := service.ResolveInstructions(context.Background(), sessionturn.InstructionRequest{Text: "literal prompt"}); err != nil || got != "literal prompt" {
		t.Fatalf("literal instructions = %q, %v", got, err)
	}
	if _, err := service.ResolveInstructions(context.Background(), sessionturn.InstructionRequest{
		Text:        "literal prompt",
		Composition: &session.InstructionComposition{BrowserToolsEnabled: true},
	}); err == nil {
		t.Fatal("instruction composition succeeded without the service that owns its policy")
	}
	if _, err := service.ResolveInstructions(context.Background(), sessionturn.InstructionRequest{
		Request: session.InstructionRequest{Prompt: "read this", WorkspaceDir: filepath.Join(t.TempDir(), "missing")},
	}); err == nil {
		t.Fatal("instruction resolution accepted a missing explicit workspace")
	}

	workspace := t.TempDir()
	instructions := &instructionServiceProbe{}
	service = New(Dependencies{InstructionService: instructions})
	got, err := service.ResolveInstructions(context.Background(), sessionturn.InstructionRequest{
		Request: session.InstructionRequest{WorkspaceDir: workspace, FilesystemScopeDescription: "read only", FilesystemScopeSet: true},
	})
	if err != nil || got != "resolved by instruction owner" {
		t.Fatalf("workspace instruction resolution = %q, %v", got, err)
	}
	if instructions.request.WorkspaceDir != workspace || instructions.request.Loader == nil {
		t.Fatalf("instruction owner request = %+v, want workspace and loader", instructions.request)
	}
}

func TestPreparedRuntimeReportsConfiguredToolResultFailure(t *testing.T) {
	assertConfiguredToolFailureIsReported(t)
}

func TestPreparedRuntimeResolvesPolicyFromAdvertisedToolSnapshot(t *testing.T) {
	factory := &runtimeTestPolicyFactory{}
	definitions := []messages.ToolDefinition{{Name: "webmcp_invoke"}}
	base := []messages.ToolDefinition{{Name: "search"}}
	settings := tools.InteractiveToolPolicySettings{FastReadTimeout: 3 * time.Second}
	runtime, err := New(Dependencies{PolicyFactory: factory}).Prepare(context.Background(), sessionturn.Request{
		ToolExecutor:       &runtimeTestTool{},
		ToolDefinitions:    definitions,
		ToolDefinitionBase: base,
		ToolPolicySettings: &settings,
		DynamicToolPolicy:  true,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(factory.request.Definitions) != 1 || factory.request.Definitions[0].Name != "webmcp_invoke" || len(factory.request.BaseDefinitions) != 1 || factory.request.BaseDefinitions[0].Name != "search" || !factory.request.DynamicLongRunning || factory.request.Settings.FastReadTimeout != settings.FastReadTimeout {
		t.Fatalf("resolved interactive policy request = %+v", factory.request)
	}
	if !containsString(factory.request.ExplicitLongRunningNames, "webmcp_invoke") || !containsString(factory.request.ExplicitLongRunningNames, "webmcp_cancel") {
		t.Fatalf("default long-running tool names = %v", factory.request.ExplicitLongRunningNames)
	}
	if runtime.InteractiveToolPolicy().ClassForTool("webmcp_invoke") != tools.InteractiveToolClassBoundedLongRunning {
		t.Fatal("runtime did not retain the resolved tool policy")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
