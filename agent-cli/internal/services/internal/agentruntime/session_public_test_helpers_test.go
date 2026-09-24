package agentruntime

import (
	"bytes"
	"context"
	"io"
	"sync"

	"encoding/json"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	runtimeSessionDurationWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	runtimeSessionTerminalWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"reflect"
	"testing"
)

// These small fixtures keep unrelated runtime and room tests independent of
// the retired observability implementation. They record only the public
// service contracts exercised by those tests.
type diagnosticRecordSink struct {
	mu      sync.Mutex
	records []SessionDiagnosticRecord
}

func (s *diagnosticRecordSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
}

func (s *diagnosticRecordSink) all() []SessionDiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SessionDiagnosticRecord(nil), s.records...)
}

func (s *diagnosticRecordSink) events(event string) []SessionDiagnosticRecord {
	var matched []SessionDiagnosticRecord
	for _, record := range s.all() {
		if record.Event == event {
			matched = append(matched, record)
		}
	}
	return matched
}

type sessionDiagnosticArtifacts struct {
	records  *diagnosticRecordSink
	snapshot metrics.Snapshot
	runErr   error
}

func (a sessionDiagnosticArtifacts) failureRecords() []SessionDiagnosticRecord {
	return a.records.events(SessionDiagnosticEventFailure)
}

func (a sessionDiagnosticArtifacts) turnRecords() []SessionDiagnosticRecord {
	return a.records.events(SessionDiagnosticEventTurn)
}

func (a sessionDiagnosticArtifacts) series(direction metrics.Direction, modality metrics.Modality) metrics.SeriesSnapshot {
	return a.snapshot.SeriesFor(direction, modality)
}

func runSessionWithDiagnostics(t testingT, mutate func(*SessionRunOptions)) sessionDiagnosticArtifacts {
	t.Helper()
	sink := &diagnosticRecordSink{}
	metricSink, err := metrics.NewInMemorySink()
	if err != nil {
		t.Fatalf("metrics.NewInMemorySink: %v", err)
	}
	opts := SessionRunOptions{ModelCatalog: testModelCatalog(), AudioService: audioiowire.NewService(), Diagnostics: sink, MetricsRecorder: metricSink}
	if mutate != nil {
		mutate(&opts)
	}
	var out bytes.Buffer
	runErr := runSessionForTest(context.Background(), &out, opts)
	return sessionDiagnosticArtifacts{records: sink, snapshot: metricSink.Snapshot(), runErr: runErr}
}

type testingT interface {
	Helper()
	Fatalf(string, ...any)
}

type recordingSessionRuntimeObserver struct {
	mu           sync.Mutex
	observations []SessionRuntimeObservation
}

func (o *recordingSessionRuntimeObserver) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
}

func sessionTerminalObservationForCancellation(outputState messages.TerminalOutputState, roomBound bool) sessionTerminalObservation {
	return sessionTerminalObservation{
		TerminalReason:     messages.TerminalReasonCancellation,
		TerminalProvenance: messages.TerminalProvenanceRoom,
		OutputState:        outputState,
		RoomBound:          roomBound,
	}
}

func defaultSessionRuntimeFactory() SessionRuntimeFactory { return newDefaultSessionRuntimeFactory() }

func testSessionRuntimeFactory() SessionRuntimeFactory {
	durationService := runtimeSessionDurationWire.NewService()
	durationRunner := runtimeSessionWire.NewDurationRunner(runtimeSessionWire.DurationDependencies{
		DurationService: durationService,
		LoopFactory:     runtimeSessionWire.NewDuplexLoopFactory(),
	})
	var _ runtimeSession.DurationRunner = durationRunner
	return NewSessionRuntimeFactory(durationService, durationRunner)
}

func withTestSessionRuntimeFactory(factory SessionRuntimeFactory) SessionRuntimeFactory {
	defaults := testSessionRuntimeFactory()
	if !factory.configured() {
		return defaults
	}
	if factory.durationService == nil {
		factory.durationService = defaults.durationService
	}
	if factory.durationRunner == nil {
		factory.durationRunner = defaults.durationRunner
	}
	if factory.newDefaultLiveDialer == nil {
		factory.newDefaultLiveDialer = defaults.newDefaultLiveDialer
	}
	if factory.newRecordingDialer == nil {
		factory.newRecordingDialer = defaults.newRecordingDialer
	}
	if factory.newReplayDialer == nil {
		factory.newReplayDialer = defaults.newReplayDialer
	}
	if factory.newRecordedTimingReplayDialer == nil {
		factory.newRecordedTimingReplayDialer = defaults.newRecordedTimingReplayDialer
	}
	if factory.newReplayInferencer == nil {
		factory.newReplayInferencer = defaults.newReplayInferencer
	}
	if factory.newGrokSessionInferencer == nil {
		factory.newGrokSessionInferencer = defaults.newGrokSessionInferencer
	}
	if factory.newOpenAISessionInf == nil {
		factory.newOpenAISessionInf = defaults.newOpenAISessionInf
	}
	if factory.newBareLiveSessionInferencer == nil {
		factory.newBareLiveSessionInferencer = defaults.newBareLiveSessionInferencer
	}
	if factory.newGrokSessionWithTools == nil {
		factory.newGrokSessionWithTools = defaults.newGrokSessionWithTools
	}
	if factory.newOpenAISessionWithTools == nil {
		factory.newOpenAISessionWithTools = defaults.newOpenAISessionWithTools
	}
	if factory.newOpenAIScheduledSessionWithTools == nil {
		factory.newOpenAIScheduledSessionWithTools = defaults.newOpenAIScheduledSessionWithTools
	}
	return factory
}

func withTestSessionRunOptions(opts SessionRunOptions) SessionRunOptions {
	opts.runtimeFactory = withTestSessionRuntimeFactory(opts.runtimeFactory)
	return opts
}

func runSessionForTest(ctx context.Context, out io.Writer, opts SessionRunOptions) error {
	return RunSession(ctx, out, withTestSessionRunOptions(opts))
}

func runSessionWithRecordingDirectoryForTest(ctx context.Context, out io.Writer, opts SessionRunOptions, directory string) error {
	return RunSessionWithRecordingDirectory(ctx, out, withTestSessionRunOptions(opts), directory)
}

func runRoomForTest(ctx context.Context, out io.Writer, opts RoomRunOptions) (RoomResult, error) {
	opts.RuntimeFactory = withTestSessionRuntimeFactory(opts.RuntimeFactory)
	return RunRoomWithResult(ctx, out, opts)
}

func runTestAgentLoopSession(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	if opts.durationService == nil || opts.durationRunner == nil {
		factory := testSessionRuntimeFactory()
		if opts.durationService == nil {
			opts.durationService = factory.durationService
		}
		if opts.durationRunner == nil {
			opts.durationRunner = factory.durationRunner
		}
	}
	return runAgentLoopSession(ctx, out, inferencer, opts)
}

func runTestSessionRuntimePlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan) error {
	if plan.loop.durationService == nil || plan.loop.durationRunner == nil {
		factory := testSessionRuntimeFactory()
		if plan.loop.durationService == nil {
			plan.loop.durationService = factory.durationService
		}
		if plan.loop.durationRunner == nil {
			plan.loop.durationRunner = factory.durationRunner
		}
	}
	return plan.run(ctx, out)
}

type SessionDurationArtifactLifecycle = sessionduration.ArtifactLifecycle
type SessionDurationArtifactPaths = sessionduration.SessionDurationArtifactPaths

func WithSessionDurationArtifacts(ctx context.Context, artifacts SessionDurationArtifactLifecycle) context.Context {
	return runtimeSessionDurationWire.NewService().WithArtifacts(ctx, artifacts)
}

func WithSessionDurationArtifactPaths(ctx context.Context, paths SessionDurationArtifactPaths) context.Context {
	return runtimeSessionDurationWire.NewService().WithArtifactPaths(ctx, paths)
}

func newSessionReplayRenderer(out io.Writer) sessionterminal.TranscriptRenderer {
	return runtimeSessionTerminalWire.NewService().NewTranscriptRenderer(out, nil)
}

func writeSessionReplayMessage(out io.Writer, msg messages.StreamMessage) error {
	return runtimeSessionTerminalWire.NewService().WriteTranscriptMessage(out, msg)
}

type dynamicPublisherTestSession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	sent      chan messages.StreamMessage
	done      chan struct{}
	closeOnce sync.Once
}

func newDynamicPublisherTestSession() *dynamicPublisherTestSession {
	return &dynamicPublisherTestSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](16),
		sent: make(chan messages.StreamMessage, 16),
		done: make(chan struct{}),
	}
}

func (s *dynamicPublisherTestSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case s.sent <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *dynamicPublisherTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *dynamicPublisherTestSession) Done() <-chan struct{} { return s.done }

func (s *dynamicPublisherTestSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

type dynamicPublisherTestInferencer struct {
	session   *dynamicPublisherTestSession
	connected int
}

func (i *dynamicPublisherTestInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connected++
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("dynamic-publisher-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("dynamic-publisher-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	return i.session, nil
}

func dynamicPublisherTestDefinition(name, description string) messages.ToolDefinition {
	return messages.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters: []messages.ToolParameter{{
			Name:        "value",
			Type:        "string",
			Description: "value for the test tool",
			Required:    true,
		}},
		ParametersClosed: true,
	}
}

func waitForDynamicPublisherRefresh(t *testing.T, ctx context.Context, refreshStarted <-chan struct{}) {
	t.Helper()
	select {
	case <-refreshStarted:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for catalog refresh: %v", ctx.Err())
	}
}

func readDynamicPublisherUpdate(t *testing.T, ctx context.Context, session *dynamicPublisherTestSession) []messages.ToolDefinition {
	t.Helper()
	select {
	case msg := <-session.sent:
		if msg.Type != messages.StreamTypeSessionUpdate {
			t.Fatalf("provider message type = %s, want %s", msg.Type, messages.StreamTypeSessionUpdate)
		}
		value, ok := msg.Value.(*messages.SessionUpdateValue)
		if !ok || value == nil {
			t.Fatalf("provider message value = %T, want *messages.SessionUpdateValue", msg.Value)
		}
		return value.Tools
	case <-ctx.Done():
		t.Fatalf("timed out waiting for provider session.update: %v", ctx.Err())
		return nil
	}
}

type dynamicPublisherCatalogBroker struct {
	mu           sync.Mutex
	catalog      webmcp.ToolCatalogSnapshot
	events       chan webmcp.BrokerEvent
	invocations  []webmcp.InvokeRequest
	invocationID uint64
	watchStopped chan struct{}
	watchOnce    sync.Once
}

func newDynamicPublisherCatalogBroker(catalog webmcp.ToolCatalogSnapshot) *dynamicPublisherCatalogBroker {
	return &dynamicPublisherCatalogBroker{
		catalog:      cloneDynamicPublisherCatalog(catalog),
		events:       make(chan webmcp.BrokerEvent, 8),
		watchStopped: make(chan struct{}),
	}
}

func (b *dynamicPublisherCatalogBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return nil, nil
}

func (b *dynamicPublisherCatalogBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return nil, nil
}

func (b *dynamicPublisherCatalogBroker) Select(_ context.Context, _ webmcp.TargetSelector) (webmcp.PageContext, error) {
	return webmcp.PageContext{}, nil
}

func (b *dynamicPublisherCatalogBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return webmcp.PageContext{}, nil
}

func (b *dynamicPublisherCatalogBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneDynamicPublisherCatalog(b.catalog), nil
}

func (b *dynamicPublisherCatalogBroker) Invoke(_ context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.invocations = append(b.invocations, request)
	b.invocationID++
	return webmcp.InvokeResult{
		InvocationID: webmcp.InvocationID("dynamic-invocation-" + string(rune('0'+b.invocationID))),
		State:        webmcp.InvocationCompleted,
		Output:       json.RawMessage(`{"accepted":true}`),
	}, nil
}

func (b *dynamicPublisherCatalogBroker) Cancel(context.Context, webmcp.CancelRequest) error {
	return nil
}

func (b *dynamicPublisherCatalogBroker) Watch(ctx context.Context) <-chan webmcp.BrokerEvent {
	b.watchOnce.Do(func() {
		go func() {
			<-ctx.Done()
			close(b.watchStopped)
		}()
	})
	return b.events
}

func (b *dynamicPublisherCatalogBroker) Close() error {
	return nil
}

func (b *dynamicPublisherCatalogBroker) SetCatalog(catalog webmcp.ToolCatalogSnapshot) {
	b.mu.Lock()
	b.catalog = cloneDynamicPublisherCatalog(catalog)
	b.mu.Unlock()
}

func (b *dynamicPublisherCatalogBroker) InvocationCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.invocations)
}

func (b *dynamicPublisherCatalogBroker) LastInvocation() webmcp.InvokeRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.invocations) == 0 {
		return webmcp.InvokeRequest{}
	}
	return b.invocations[len(b.invocations)-1]
}

func dynamicPublisherCatalog(tools ...webmcp.ToolDescriptor) webmcp.ToolCatalogSnapshot {
	return dynamicPublisherCatalogAt(1, tools...)
}

func dynamicPublisherCatalogAt(generation uint64, tools ...webmcp.ToolDescriptor) webmcp.ToolCatalogSnapshot {
	return webmcp.ToolCatalogSnapshot{Generation: generation, Tools: tools}
}

func dynamicPublisherPageTool(name, description, refSuffix, schema string) webmcp.ToolDescriptor {
	return webmcp.ToolDescriptor{
		Ref:         webmcp.ToolRef(webmcp.ToolRefPrefix + refSuffix),
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(schema),
	}
}

func cloneDynamicPublisherCatalog(catalog webmcp.ToolCatalogSnapshot) webmcp.ToolCatalogSnapshot {
	clone := catalog
	clone.Tools = make([]webmcp.ToolDescriptor, len(catalog.Tools))
	for index, descriptor := range catalog.Tools {
		clone.Tools[index] = descriptor
		clone.Tools[index].InputSchema = append(json.RawMessage(nil), descriptor.InputSchema...)
	}
	return clone
}

func pageDefinitionsFromCatalog(t *testing.T, toolSet *webmcpTools.BrokerToolSet, ctx context.Context, catalog webmcp.ToolCatalogSnapshot) []messages.ToolDefinition {
	t.Helper()
	pageDefinitions, err := toolSet.PageToolDefinitionsWithError(ctx)
	if err != nil {
		t.Fatalf("page definitions for catalog %#v: %v", catalog, err)
	}
	return pageDefinitions
}

func pageToolRef(catalog webmcp.ToolCatalogSnapshot, name string) webmcp.ToolRef {
	for _, descriptor := range catalog.Tools {
		if descriptor.Name == name {
			return descriptor.Ref
		}
	}
	return ""
}

func executeDynamicPublisherPageCall(t *testing.T, ctx context.Context, executor messages.ToolExecutor, id, name, arguments string) webmcp.ToolResultEnvelope {
	t.Helper()
	response, err := executor.Execute(ctx, messages.ToolCall{ID: id, Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode %s result: %v (%s)", name, err, response.Content)
	}
	return envelope
}

func assertDynamicPublisherSurface(t *testing.T, got, base, page []messages.ToolDefinition, label string) {
	t.Helper()
	want := mergeSessionToolDefinitionBase(base, page)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s surface = %#v, want %#v", label, got, want)
	}
	gotByName := make(map[string]messages.ToolDefinition, len(got))
	for _, definition := range got {
		gotByName[definition.Name] = definition
	}
	for _, definition := range messages.CanonicalToolDefinitions(base) {
		if !reflect.DeepEqual(gotByName[definition.Name], definition) {
			t.Fatalf("%s changed base definition %q = %#v, want %#v", label, definition.Name, gotByName[definition.Name], definition)
		}
	}
}

func assertDynamicPublisherDefinition(t *testing.T, definitions []messages.ToolDefinition, name, description string, parameterCount int) {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name != name {
			continue
		}
		if definition.Description != description || len(definition.Parameters) != parameterCount || !definition.ParametersClosed {
			t.Fatalf("definition %q = %#v, want description %q, %d parameters, closed schema", name, definition, description, parameterCount)
		}
		return
	}
	t.Fatalf("definition %q missing from %#v", name, definitions)
}

func assertDynamicPublisherAbsent(t *testing.T, definitions []messages.ToolDefinition, name, label string) {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name == name {
			t.Fatalf("%s unexpectedly contains %q", label, name)
		}
	}
}

func assertDynamicPublisherPresent(t *testing.T, definitions []messages.ToolDefinition, name, label string) {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name == name {
			return
		}
	}
	t.Fatalf("%s is missing %q", label, name)
}

var _ webmcp.Broker = (*dynamicPublisherCatalogBroker)(nil)
