package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

type runtimeTestPolicy struct{ class tools.InteractiveToolClass }

func (p runtimeTestPolicy) Settings() tools.InteractiveToolPolicySettings {
	return tools.InteractiveToolPolicySettings{}
}
func (p runtimeTestPolicy) ClassForTool(string) tools.InteractiveToolClass { return p.class }
func (p runtimeTestPolicy) TimeoutForTool(string) time.Duration            { return time.Second }
func (p runtimeTestPolicy) Clone() tools.InteractiveToolPolicy             { return p }
func (p runtimeTestPolicy) Validate() error                                { return nil }

type runtimeTestPolicyFactory struct {
	request tools.InteractiveToolPolicyRequest
}

func (f *runtimeTestPolicyFactory) Resolve(request tools.InteractiveToolPolicyRequest) (tools.InteractiveToolPolicy, error) {
	f.request = request
	return runtimeTestPolicy{class: tools.InteractiveToolClassBoundedLongRunning}, nil
}
func (*runtimeTestPolicyFactory) ValidateSettings(tools.InteractiveToolPolicySettings) error {
	return nil
}

type runtimeTestTool struct{ calls int }

func (e *runtimeTestTool) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls++
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: `{"ok":true}`}, nil
}

type runtimeFailingTool struct{ err error }

func (e runtimeFailingTool) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{}, e.err
}

func TestPreparedRuntimeSnapshotsPolicyAndToolsAndClosesOwnedResourcesOnce(t *testing.T) {
	cleanupErr := errors.New("image cleanup failed")
	cleanupCalls := 0
	tool := &runtimeTestTool{}
	var observedCall messages.ToolCall
	var observedResult messages.ToolCallResponse
	var observedFailure bool
	definitions := []messages.ToolDefinition{{Name: "lookup", Parameters: []messages.ToolParameter{{Name: "query", Type: "string"}}}}
	runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{
		ToolExecutor:          tool,
		ToolDefinitions:       definitions,
		InteractiveToolPolicy: runtimeTestPolicy{class: tools.InteractiveToolClassBoundedLongRunning},
		ToolCallObserver:      func(call messages.ToolCall) { observedCall = call },
		ToolResultObserver: func(_ messages.ToolCall, response messages.ToolCallResponse, failed bool) {
			observedResult, observedFailure = response, failed
		},
		ImageCleanup: func() error { cleanupCalls++; return cleanupErr },
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	definitions[0].Parameters[0].Name = "mutated"
	if got := runtime.ToolDefinitions(); len(got) != 1 || got[0].Parameters[0].Name != "query" {
		t.Fatalf("runtime tool definitions = %+v, want an independent query snapshot", got)
	}
	policy := runtime.InteractiveToolPolicy()
	if policy == nil || policy.ClassForTool("lookup") != tools.InteractiveToolClassBoundedLongRunning {
		t.Fatalf("runtime tool policy = %v, want the prepared classification", policy)
	}
	if _, ok := runtime.ToolExecutor().(sessionturn.ServiceOwnedToolExecutor); !ok {
		t.Fatal("prepared executor does not identify its service-owned lifecycle")
	}
	response, err := runtime.ToolExecutor().Execute(context.Background(), messages.ToolCall{ID: "call-1", Name: "lookup"})
	if err != nil || response.ToolCallID != "call-1" || response.Name != "lookup" || tool.calls != 1 {
		t.Fatalf("tool result = %+v, %v; calls=%d", response, err, tool.calls)
	}
	if observedCall.ID != "call-1" || observedResult.ToolCallID != "call-1" || observedFailure {
		t.Fatalf("tool observations = call:%+v result:%+v failed:%v", observedCall, observedResult, observedFailure)
	}

	var output bytes.Buffer
	if n, err := runtime.NewOutput(&output).Write([]byte("response")); err != nil || n != 8 || output.String() != "response" {
		t.Fatalf("runtime output write = %d, %v, contents=%q", n, err, output.String())
	}
	if got := runtime.PublicationState(); got != (sessionturn.PublicationState{}) {
		t.Fatalf("unstarted publication state = %+v, want empty state", got)
	}
	if _, err := runtime.StartPublication(context.Background(), sessionturn.PublicationRequest{}); err == nil {
		t.Fatal("runtime publication accepted missing watch/refresh/publish callbacks")
	}
	if _, err := runtime.AttachAudioOutput(nil, func(context.Context, []byte, messages.StreamMessage) error { return nil }); !errors.Is(err, sessionturn.ErrMissingTurnInferencer) {
		t.Fatalf("audio output without inferencer = %v", err)
	}
	if _, err := runtime.RunTurn(context.Background(), sessionturn.TurnRequest{Input: sessionturn.TurnInput{Text: "hello"}, Direction: sessionturn.TurnDirectionUser, StartTick: 1, EndTick: 2}); !errors.Is(err, sessionturn.ErrMissingTurnInferencer) {
		t.Fatalf("turn without inferencer = %v", err)
	}
	if err := runtime.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("Close = %v, want image cleanup cause", err)
	}
	if err := runtime.Close(); !errors.Is(err, cleanupErr) || cleanupCalls != 1 {
		t.Fatalf("second Close = %v, cleanup calls=%d; want cached error and one cleanup", err, cleanupCalls)
	}
}

type runtimeProbeSession struct {
	receive          *messages.TypedBuffer[messages.StreamMessage]
	done             chan struct{}
	sent             chan messages.StreamMessage
	complete         chan messages.Message
	completeDeferred chan messages.Message
	rejectSends      bool
	rejectComplete   bool
	terminalErr      error
	close            sync.Once
}

func newRuntimeProbeSession() *runtimeProbeSession {
	return &runtimeProbeSession{
		receive:          messages.NewTypedBuffer[messages.StreamMessage](8),
		done:             make(chan struct{}),
		sent:             make(chan messages.StreamMessage, 8),
		complete:         make(chan messages.Message, 8),
		completeDeferred: make(chan messages.Message, 8),
	}
}

func (s *runtimeProbeSession) Send(_ context.Context, message messages.StreamMessage) bool {
	s.sent <- message
	return !s.rejectSends
}
func (s *runtimeProbeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *runtimeProbeSession) Done() <-chan struct{} { return s.done }
func (s *runtimeProbeSession) SendMessage(_ context.Context, message messages.Message) bool {
	s.complete <- message
	return !s.rejectComplete
}
func (s *runtimeProbeSession) SendMessageWithoutResponse(_ context.Context, message messages.Message) bool {
	s.completeDeferred <- message
	return !s.rejectComplete
}
func (s *runtimeProbeSession) RTCMedia() sharedaudio.MediaEndpoints {
	return sharedaudio.MediaEndpoints{}
}
func (s *runtimeProbeSession) TerminalError() error { return s.terminalErr }
func (s *runtimeProbeSession) Close() error {
	s.close.Do(func() { close(s.done) })
	return nil
}

type runtimeProbeInferencer struct{ session *runtimeProbeSession }

func (i runtimeProbeInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}
func (runtimeProbeInferencer) Request() inference.SessionRequest { return inference.SessionRequest{} }

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
	requester, ok := inferencer.(interface {
		Request() inference.SessionRequest
	})
	if !ok || requester.Request().Config.Instructions != instructions {
		t.Fatalf("prepared provider request = %v, want instructions", inferencer)
	}
	connected, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if !provider.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen}) {
		t.Fatal("provider rejected session-open event")
	}
	select {
	case sent := <-provider.sent:
		if sent.Type != messages.StreamTypeSessionUpdate {
			t.Fatalf("first provider update = %+v, want SESSION.UPDATE", sent)
		}
		value, ok := sent.Value.(*messages.SessionUpdateValue)
		if !ok || value.Instructions != instructions {
			t.Fatalf("provider instructions update = %#v", sent.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not receive resolved instructions")
	}
	if _, ok := connected.Receive().ReadBlocking(connected.Done()); !ok {
		t.Fatal("session-open event was not forwarded")
	}
	if !provider.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionCreated}) {
		t.Fatal("provider rejected session-created event")
	}
	if _, ok := connected.Receive().ReadBlocking(connected.Done()); !ok {
		t.Fatal("session-created event was not forwarded")
	}
	select {
	case duplicate := <-provider.sent:
		t.Fatalf("provider received a duplicate instructions update: %+v", duplicate)
	default:
	}
	wrapped, ok := connected.(sessionturn.Session)
	if !ok || !wrapped.SupportsCompleteMessages() || !wrapped.SupportsCompleteMessagesWithoutResponse() || wrapped.SupportsResponseRequests() || !errors.Is(wrapped.TerminalError(), provider.terminalErr) {
		t.Fatal("instruction session did not preserve the provider's complete-message, response, and terminal capabilities")
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
	if outcome := wrapped.RequestResponse(context.Background()); outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("unsupported response request = %+v", outcome)
	}
	streamMessage := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("forwarded")}
	if !wrapped.Send(context.Background(), streamMessage) || !wrapped.SendWithOutcome(context.Background(), streamMessage).OK() {
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

type publicationTimerFactoryProbe struct {
	created chan *publicationTimerProbe
}

func (f publicationTimerFactoryProbe) NewTimer(time.Duration) sessionturn.Timer {
	timer := &publicationTimerProbe{events: make(chan time.Time, 1), resetCalled: make(chan struct{}, 1)}
	f.created <- timer
	return timer
}

type publicationTimerProbe struct {
	events      chan time.Time
	resetCalled chan struct{}
}

func (t *publicationTimerProbe) C() <-chan time.Time { return t.events }
func (*publicationTimerProbe) Stop() bool            { return true }
func (t *publicationTimerProbe) Reset(time.Duration) bool {
	select {
	case t.resetCalled <- struct{}{}:
	default:
	}
	return true
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
	var consume func(sessionturn.BrowserEvent) bool
	select {
	case consume = <-watchStarted:
	case <-time.After(time.Second):
		t.Fatal("browser watch did not start")
	}
	publication.MarkReady()
	select {
	case count := <-refreshes:
		if count != 1 {
			t.Fatalf("initial refresh count = %d", count)
		}
	case <-time.After(time.Second):
		t.Fatal("initial ready refresh did not run")
	}
	select {
	case definitions := <-published:
		t.Fatalf("unchanged initial tool snapshot was republished: %+v", definitions)
	default:
	}
	if !consume(sessionturn.BrowserEvent{Type: sessionturn.BrowserEventSelectionChanged, BrowserID: "browser", TargetID: "tab-a", Generation: 1, Sequence: 1}) {
		t.Fatal("first browser selection event was rejected")
	}
	timer := <-timerFactory.created
	if !consume(sessionturn.BrowserEvent{Type: sessionturn.BrowserEventCatalogChanged, BrowserID: "browser", TargetID: "tab-a", Generation: 2, Sequence: 2}) {
		t.Fatal("replacement browser selection event was rejected")
	}
	select {
	case <-timer.resetCalled:
	case <-time.After(time.Second):
		t.Fatal("second selection change did not reset the debounce timer")
	}
	select {
	case definitions := <-published:
		t.Fatalf("browser tools published before debounce elapsed: %+v", definitions)
	default:
	}
	timer.events <- time.Now()
	select {
	case count := <-refreshes:
		if count != 2 {
			t.Fatalf("browser event refresh count = %d, want one post-debounce refresh", count)
		}
	case <-time.After(time.Second):
		t.Fatal("debounced browser refresh did not run")
	}
	select {
	case definitions := <-published:
		if len(definitions) != 2 || definitions[1].Name != "tab_tool" {
			t.Fatalf("published browser tools = %+v", definitions)
		}
	case <-time.After(time.Second):
		t.Fatal("changed browser tool snapshot was not published")
	}
	state := publication.State()
	if state.LastSequence != 2 || state.TargetID != "tab-a" || state.Generation != 2 || state.PublicationCount != 1 {
		t.Fatalf("debounced publication state = %+v", state)
	}
}

func TestAudioOutputWaitDrainsObservedAudioAndReturnsWithinItsShutdownBound(t *testing.T) {
	runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	provider := newRuntimeProbeSession()
	provider.terminalErr = errors.New("audio provider terminal error")
	observed := make(chan []byte, 1)
	audio, err := runtime.AttachAudioOutput(runtimeProbeInferencer{session: provider}, func(_ context.Context, content []byte, _ messages.StreamMessage) error {
		observed <- append([]byte(nil), content...)
		return nil
	})
	if err != nil {
		t.Fatalf("AttachAudioOutput: %v", err)
	}
	connected, err := audio.Inferencer().ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	pcm := []byte{1, 2, 3, 4}
	if !provider.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioDeltaValueWithMediaType(pcm, "audio/pcm"),
	}) {
		t.Fatal("provider rejected audio output delta")
	}
	select {
	case got := <-observed:
		if !bytes.Equal(got, pcm) {
			t.Fatalf("observed audio = %v, want %v", got, pcm)
		}
	case <-time.After(time.Second):
		t.Fatal("assistant audio delta was not observed")
	}
	if _, ok := connected.Receive().ReadBlocking(connected.Done()); !ok {
		t.Fatal("assistant audio delta was not forwarded to the consumer")
	}
	audioSession, ok := connected.(sessionturn.Session)
	if !ok || !audioSession.SupportsCompleteMessages() || !audioSession.SupportsCompleteMessagesWithoutResponse() || audioSession.SupportsResponseRequests() || !errors.Is(audioSession.TerminalError(), provider.terminalErr) {
		t.Fatal("audio output session did not preserve optional provider capabilities")
	}
	if outcome := audioSession.RequestResponse(context.Background()); outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("unsupported response request = %+v", outcome)
	}
	mediaOwner, ok := connected.(interface {
		RTCMedia() (sharedaudio.MediaEndpoints, bool)
	})
	if !ok {
		t.Fatalf("audio output session %T lost its neutral RTC media seam", connected)
	}
	if _, available := mediaOwner.RTCMedia(); !available {
		t.Fatal("audio output session did not preserve RTC media ownership")
	}
	streamMessage := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("forwarded")}
	if !audioSession.Send(context.Background(), streamMessage) || !audioSession.SendWithOutcome(context.Background(), streamMessage).OK() {
		t.Fatal("audio output session did not forward stream sends")
	}
	if sent := <-provider.sent; sent.Type != messages.StreamTypeTextDelta {
		t.Fatalf("forwarded stream message = %+v", sent)
	}
	if sent := <-provider.sent; sent.Type != messages.StreamTypeTextDelta {
		t.Fatalf("forwarded outcome stream message = %+v", sent)
	}
	if !audioSession.SendMessage(context.Background(), messages.Message{ToolCallID: "complete"}) || !audioSession.SendMessageWithoutResponse(context.Background(), messages.Message{ToolCallID: "deferred"}) {
		t.Fatal("audio output session did not forward complete-message sends")
	}
	if (<-provider.complete).ToolCallID != "complete" || (<-provider.completeDeferred).ToolCallID != "deferred" {
		t.Fatal("audio output session changed a complete-message send")
	}

	waited := make(chan error, 1)
	started := time.Now()
	go func() { waited <- audio.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("audio output Wait exceeded its bounded shutdown interval")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("audio output shutdown took %s, want bounded completion", time.Since(started))
	}
	select {
	case <-provider.Done():
	case <-time.After(time.Second):
		t.Fatal("audio output shutdown did not close its provider session")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("runtime Close: %v", err)
	}
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
	for _, deferResponse := range []bool{false, true} {
		name := "requests response"
		if deferResponse {
			name = "defers response"
		}
		t.Run(name, func(t *testing.T) {
			provider := newRuntimeProbeSession()
			firstTurn := make(chan error, 1)
			imageBytes := []byte{10, 20, 30}
			imageRequest := &sessionturn.ImageRequest{
				Parts:               []messages.ImagePart{{Bytes: imageBytes, MediaType: "image/png"}},
				DeferResponse:       deferResponse,
				FirstTurn:           firstTurn,
				PromptSentinel:      "image-only",
				DeferredInstruction: sessionturn.DeferredImageInstruction,
			}
			imageCaps := sessionturn.ImageCapabilityRequest{
				Provider:     "openai",
				Model:        "model",
				ModelCatalog: imageTestCatalog{model: providers.RealtimeModel{ID: "model", SupportsImageInput: true}},
			}
			runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{
				SessionInferencer:      runtimeProbeInferencer{session: provider},
				Image:                  imageRequest,
				ImageCapabilityRequest: &imageCaps,
			})
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			imageRequest.Parts[0].Bytes[0] = 99
			connected, err := runtime.Inferencer().ConnectSession(context.Background())
			if err != nil {
				t.Fatalf("ConnectSession: %v", err)
			}
			session, ok := connected.(sessionturn.Session)
			if !ok {
				t.Fatalf("image session = %T, want the public session contract", connected)
			}
			if !session.SupportsCompleteMessages() || !session.SupportsCompleteMessagesWithoutResponse() || session.SupportsResponseRequests() {
				t.Fatal("image session did not preserve complete-message and response capabilities")
			}
			if outcome := session.RequestResponse(context.Background()); outcome.Status != messages.SessionSendTerminalFailure {
				t.Fatalf("unsupported response request = %+v", outcome)
			}
			if outcome := session.SendWithOutcome(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("image-only")}); !outcome.OK() {
				t.Fatalf("send image turn = %+v", outcome)
			}
			select {
			case err := <-firstTurn:
				if err != nil {
					t.Fatalf("image first-turn result: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("image first-turn result was not reported")
			}
			var sent messages.Message
			if deferResponse {
				sent = <-provider.completeDeferred
				if sent.TextContent() != sessionturn.DeferredImageInstruction {
					t.Fatalf("deferred image instruction = %q", sent.TextContent())
				}
			} else {
				sent = <-provider.complete
				if sent.TextContent() != "" {
					t.Fatalf("image-only turn included unexpected prompt text %q", sent.TextContent())
				}
			}
			if sent.Role != messages.RoleUser || len(sent.ContentParts) != 1+boolCount(deferResponse) {
				t.Fatalf("image turn = %+v", sent)
			}
			imageIndex := len(sent.ContentParts) - 1
			part, ok := sent.ContentParts[imageIndex].(messages.ImagePart)
			if !ok || part.MediaType != "image/png" || !bytes.Equal(part.Bytes, []byte{10, 20, 30}) {
				t.Fatalf("copied image part = %#v", sent.ContentParts[imageIndex])
			}
			if !session.SendMessage(context.Background(), messages.Message{ToolCallID: "complete"}) || !session.SendMessageWithoutResponse(context.Background(), messages.Message{ToolCallID: "deferred"}) {
				t.Fatal("image session did not forward complete tool results")
			}
			if (<-provider.complete).ToolCallID != "complete" || (<-provider.completeDeferred).ToolCallID != "deferred" {
				t.Fatal("image session changed the forwarded complete tool result")
			}
			if err := connected.Close(); err != nil {
				t.Fatalf("close image session: %v", err)
			}
			if err := runtime.Close(); err != nil {
				t.Fatalf("close runtime: %v", err)
			}
		})
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
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
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace instruction"), 0o600); err != nil {
		t.Fatalf("write workspace instructions: %v", err)
	}
	promptPath := filepath.Join(workspace, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("file prompt"), 0o600); err != nil {
		t.Fatalf("write explicit prompt: %v", err)
	}
	service = New(Dependencies{InstructionService: sessionwire.NewInstructionService()})
	got, err := service.ResolveInstructions(context.Background(), sessionturn.InstructionRequest{
		Request: session.InstructionRequest{WorkspaceDir: workspace, FilesystemScopeDescription: "read only", FilesystemScopeSet: true},
	})
	if err != nil || !strings.Contains(got, "workspace instruction") || !strings.Contains(got, "Filesystem scope: read only") {
		t.Fatalf("workspace instruction resolution = %q, %v", got, err)
	}
	got, err = service.ResolveInstructions(context.Background(), sessionturn.InstructionRequest{
		Request: session.InstructionRequest{Prompt: promptPath, WorkspaceDir: workspace},
	})
	if err != nil || got != "file prompt" {
		t.Fatalf("explicit prompt file = %q, %v", got, err)
	}
}

func TestPreparedRuntimeReportsConfiguredToolResultFailure(t *testing.T) {
	toolErr := errors.New("tool backend unavailable")
	var diagnosed error
	var failed bool
	runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{
		ToolExecutor:          runtimeFailingTool{err: toolErr},
		InteractiveToolPolicy: runtimeTestPolicy{},
		ToolDiagnostic:        func(_ messages.ToolCall, err error) { diagnosed = err },
		ToolResultObserver: func(_ messages.ToolCall, response messages.ToolCallResponse, isFailure bool) {
			failed = isFailure && strings.Contains(response.Content, "tool backend unavailable")
		},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	response, err := runtime.ToolExecutor().Execute(context.Background(), messages.ToolCall{ID: "call-failed", Name: "lookup"})
	if err != nil || response.ToolCallID != "call-failed" || !errors.Is(diagnosed, toolErr) || !failed {
		t.Fatalf("tool failure response = %+v, err=%v diagnostic=%v failed=%v", response, err, diagnosed, failed)
	}
	if closeErr := runtime.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
