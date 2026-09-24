package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

type runtimeToolObservations struct {
	call   messages.ToolCall
	result messages.ToolCallResponse
	failed bool
}

func prepareRuntimeWithToolObservers(t *testing.T, cleanupErr error, cleanupCalls *int) (sessionturn.Runtime, []messages.ToolDefinition, *runtimeTestTool, *runtimeToolObservations) {
	t.Helper()
	tool := &runtimeTestTool{}
	observed := &runtimeToolObservations{}
	definitions := []messages.ToolDefinition{{Name: "lookup", Parameters: []messages.ToolParameter{{Name: "query", Type: "string"}}}}
	runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{
		ToolExecutor:          tool,
		ToolDefinitions:       definitions,
		InteractiveToolPolicy: runtimeTestPolicy{class: tools.InteractiveToolClassBoundedLongRunning},
		ToolCallObserver:      func(call messages.ToolCall) { observed.call = call },
		ToolResultObserver: func(_ messages.ToolCall, response messages.ToolCallResponse, failed bool) {
			observed.result, observed.failed = response, failed
		},
		ImageCleanup: func() error { *cleanupCalls++; return cleanupErr },
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return runtime, definitions, tool, observed
}

func assertRuntimeToolSnapshots(t *testing.T, runtime sessionturn.Runtime, definitions []messages.ToolDefinition, tool *runtimeTestTool, observed *runtimeToolObservations) {
	t.Helper()
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
	if observed.call.ID != "call-1" || observed.result.ToolCallID != "call-1" || observed.failed {
		t.Fatalf("tool observations = call:%+v result:%+v failed:%v", observed.call, observed.result, observed.failed)
	}
}

func assertRuntimeSurfaceAndMissingInferencerFailures(t *testing.T, runtime sessionturn.Runtime) {
	t.Helper()
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
}

func assertConfiguredToolFailureIsReported(t *testing.T) {
	t.Helper()
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
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

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

func awaitBrowserWatcher(t *testing.T, started <-chan func(sessionturn.BrowserEvent) bool) func(sessionturn.BrowserEvent) bool {
	t.Helper()
	select {
	case consume := <-started:
		return consume
	case <-time.After(time.Second):
		t.Fatal("browser watch did not start")
		return nil
	}
}

func awaitPublicationRefresh(t *testing.T, refreshes <-chan int, want int, failure string) {
	t.Helper()
	select {
	case count := <-refreshes:
		if count != want {
			t.Fatalf("refresh count = %d, want %d", count, want)
		}
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func assertNoBrowserPublication(t *testing.T, published <-chan []messages.ToolDefinition, stage string) {
	t.Helper()
	select {
	case definitions := <-published:
		t.Fatalf("browser tools published %s: %+v", stage, definitions)
	default:
	}
}

func requireBrowserEvent(t *testing.T, consume func(sessionturn.BrowserEvent) bool, event sessionturn.BrowserEvent, label string) {
	t.Helper()
	if !consume(event) {
		t.Fatalf("%s browser event was rejected", label)
	}
}

func awaitBrowserPublication(t *testing.T, published <-chan []messages.ToolDefinition) {
	t.Helper()
	select {
	case definitions := <-published:
		if len(definitions) != 2 || definitions[1].Name != "tab_tool" {
			t.Fatalf("published browser tools = %+v", definitions)
		}
	case <-time.After(time.Second):
		t.Fatal("changed browser tool snapshot was not published")
	}
}

func assertDebouncedPublicationState(t *testing.T, publication sessionturn.Publication) {
	t.Helper()
	state := publication.State()
	if state.LastSequence != 2 || state.TargetID != "tab-a" || state.Generation != 2 || state.PublicationCount != 1 {
		t.Fatalf("debounced publication state = %+v", state)
	}
}

func awaitDebounceReset(t *testing.T, reset <-chan struct{}) {
	t.Helper()
	select {
	case <-reset:
	case <-time.After(time.Second):
		t.Fatal("second selection change did not reset the debounce timer")
	}
}

func prepareObservedAudioOutput(t *testing.T) (sessionturn.Runtime, sessionturn.AudioOutputRuntime, *runtimeProbeSession, messages.Session, []byte) {
	t.Helper()
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
		Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant,
		Value: messages.NewAudioDeltaValueWithMediaType(pcm, "audio/pcm"),
	}) {
		t.Fatal("provider rejected audio output delta")
	}
	awaitObservedAudio(t, observed, pcm)
	if _, ok := connected.Receive().ReadBlocking(connected.Done()); !ok {
		t.Fatal("assistant audio delta was not forwarded to the consumer")
	}
	return runtime, audio, provider, connected, pcm
}

func awaitObservedAudio(t *testing.T, observed <-chan []byte, want []byte) {
	t.Helper()
	select {
	case got := <-observed:
		if !bytes.Equal(got, want) {
			t.Fatalf("observed audio = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("assistant audio delta was not observed")
	}
}

func assertAudioOutputSessionCapabilities(t *testing.T, connected messages.Session, provider *runtimeProbeSession) sessionturn.Session {
	t.Helper()
	audioSession, ok := connected.(sessionturn.Session)
	if !ok || !audioSession.SupportsCompleteMessages() || !audioSession.SupportsCompleteMessagesWithoutResponse() || audioSession.SupportsResponseRequests() || !errors.Is(audioSession.TerminalError(), provider.terminalErr) {
		t.Fatal("audio output session did not preserve optional provider capabilities")
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
	if status := audioSession.RequestResponse(context.Background()).Status; status != messages.SessionSendTerminalFailure {
		t.Fatalf("unsupported response request status = %v", status)
	}
	return audioSession
}

func assertAudioOutputForwards(t *testing.T, session sessionturn.Session, provider *runtimeProbeSession) {
	t.Helper()
	stream := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("forwarded")}
	if !session.Send(context.Background(), stream) || !session.SendWithOutcome(context.Background(), stream).OK() {
		t.Fatal("audio output session did not forward stream sends")
	}
	if (<-provider.sent).Type != messages.StreamTypeTextDelta || (<-provider.sent).Type != messages.StreamTypeTextDelta {
		t.Fatal("audio output session changed a forwarded stream send")
	}
	if !session.SendMessage(context.Background(), messages.Message{ToolCallID: "complete"}) || !session.SendMessageWithoutResponse(context.Background(), messages.Message{ToolCallID: "deferred"}) {
		t.Fatal("audio output session did not forward complete-message sends")
	}
	if (<-provider.complete).ToolCallID != "complete" || (<-provider.completeDeferred).ToolCallID != "deferred" {
		t.Fatal("audio output session changed a complete-message send")
	}
}

func awaitBoundedAudioShutdown(t *testing.T, runtime sessionturn.Runtime, audio sessionturn.AudioOutputRuntime, provider *runtimeProbeSession) {
	t.Helper()
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

func prepareImageInferencerScenario(t *testing.T, deferResponse bool) (sessionturn.Runtime, *runtimeProbeSession, *sessionturn.ImageRequest, chan error, []byte) {
	t.Helper()
	provider := newRuntimeProbeSession()
	firstTurn := make(chan error, 1)
	original := []byte{10, 20, 30}
	expected := append([]byte(nil), original...)
	request := &sessionturn.ImageRequest{
		Parts:         []messages.ImagePart{{Bytes: original, MediaType: "image/png"}},
		DeferResponse: deferResponse, FirstTurn: firstTurn,
		PromptSentinel: "image-only", DeferredInstruction: sessionturn.DeferredImageInstruction,
	}
	caps := sessionturn.ImageCapabilityRequest{
		Provider: "openai", Model: "model",
		ModelCatalog: imageTestCatalog{model: providers.RealtimeModel{ID: "model", SupportsImageInput: true}},
	}
	runtime, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{
		SessionInferencer: runtimeProbeInferencer{session: provider},
		Image:             request, ImageCapabilityRequest: &caps,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	request.Parts[0].Bytes[0] = 99
	return runtime, provider, request, firstTurn, expected
}

func connectImageInferencerSession(t *testing.T, runtime sessionturn.Runtime) sessionturn.Session {
	t.Helper()
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
	if status := session.RequestResponse(context.Background()).Status; status != messages.SessionSendTerminalFailure {
		t.Fatalf("unsupported response request status = %v", status)
	}
	return session
}

func sendPreparedImageFirstTurn(t *testing.T, session sessionturn.Session, firstTurn <-chan error) {
	t.Helper()
	outcome := session.SendWithOutcome(context.Background(), messages.StreamMessage{
		Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("image-only"),
	})
	if !outcome.OK() {
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
}

func assertPreparedImageMessage(t *testing.T, provider *runtimeProbeSession, deferResponse bool, original []byte) {
	t.Helper()
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
	part, ok := sent.ContentParts[len(sent.ContentParts)-1].(messages.ImagePart)
	if !ok || part.MediaType != "image/png" || !bytes.Equal(part.Bytes, original) {
		t.Fatalf("copied image part = %#v", sent.ContentParts[len(sent.ContentParts)-1])
	}
}

func assertImageSessionForwardsToolResults(t *testing.T, session sessionturn.Session, provider *runtimeProbeSession) {
	t.Helper()
	if !session.SendMessage(context.Background(), messages.Message{ToolCallID: "complete"}) || !session.SendMessageWithoutResponse(context.Background(), messages.Message{ToolCallID: "deferred"}) {
		t.Fatal("image session did not forward complete tool results")
	}
	if (<-provider.complete).ToolCallID != "complete" || (<-provider.completeDeferred).ToolCallID != "deferred" {
		t.Fatal("image session changed the forwarded complete tool result")
	}
}

func assertPreparedImageInferencerMode(t *testing.T, deferResponse bool) {
	t.Helper()
	runtime, provider, _, firstTurn, original := prepareImageInferencerScenario(t, deferResponse)
	connected := connectImageInferencerSession(t, runtime)
	sendPreparedImageFirstTurn(t, connected, firstTurn)
	assertPreparedImageMessage(t, provider, deferResponse, original)
	assertImageSessionForwardsToolResults(t, connected, provider)
	if err := connected.Close(); err != nil {
		t.Fatalf("close image session: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
}

type instructionServiceProbe struct {
	request session.InstructionRequest
}

func (p *instructionServiceProbe) Resolve(_ context.Context, request session.InstructionRequest) (session.InstructionResult, error) {
	p.request = request
	return session.InstructionResult{Instructions: "resolved by instruction owner"}, nil
}

func (*instructionServiceProbe) Compose(composition session.InstructionComposition) string {
	return composition.Instructions
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
