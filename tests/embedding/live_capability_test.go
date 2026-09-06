package embedding_test

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

type embeddedCapability struct {
	events       chan session.LiveCapabilityEvent
	watchContext chan context.Context
	refresh      func() []messages.ToolDefinition
	refreshCalls atomic.Int32
	initialized  atomic.Int32
	closed       atomic.Int32
}

func (c *embeddedCapability) Initialize(context.Context) error {
	c.initialized.Add(1)
	return nil
}

func (c *embeddedCapability) RefreshDefinitions(context.Context) ([]messages.ToolDefinition, error) {
	c.refreshCalls.Add(1)
	if c.refresh == nil {
		return nil, nil
	}
	return c.refresh(), nil
}

func (c *embeddedCapability) BrowserWatch(ctx context.Context) <-chan session.LiveCapabilityEvent {
	c.watchContext <- ctx
	return c.events
}

func (c *embeddedCapability) Close() error {
	c.closed.Add(1)
	return nil
}

func TestExternalLiveCapabilityPreservesCorrelationAndJoinsWatcher(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := newEmbeddedLiveProvider()
	defer closeForTest(t, provider)
	capability := &embeddedCapability{events: make(chan session.LiveCapabilityEvent, 1), watchContext: make(chan context.Context, 1)}
	host := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) { return provider, nil },
	})
	handle, err := host.OpenLive(ctx, session.LiveRequest{ParticipantID: "alice", Capabilities: &session.LiveCapabilities{Handle: capability}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	if capability.initialized.Load() != 0 {
		t.Fatal("OpenLive initialized capability")
	}
	if err := handle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var watchContext context.Context
	select {
	case watchContext = <-capability.watchContext:
	case <-ctx.Done():
		t.Fatal("capability watcher did not start")
	}
	want := session.LiveCapabilityEvent{Type: "invocation", Sequence: 7, Timestamp: time.Unix(123, 0), BrowserID: "browser-a", TargetID: "page-a", Generation: 3, InvocationID: "invoke-a", ToolName: "resume", State: "finished", Reason: "confirmed"}
	capability.events <- want
	awaitCapabilityEvent(t, ctx, handle.Events(), want)
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if watchContext.Err() == nil {
		t.Fatal("Close left capability watcher context active")
	}
	if capability.initialized.Load() != 1 || capability.closed.Load() != 1 {
		t.Fatalf("capability lifecycle initialized=%d closed=%d", capability.initialized.Load(), capability.closed.Load())
	}
}

func awaitCapabilityEvent(t *testing.T, ctx context.Context, events <-chan session.LiveEvent, want session.LiveCapabilityEvent) {
	t.Helper()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("live events closed before capability observation")
			}
			if event.Capability == nil {
				continue
			}
			if event.ParticipantID != "alice" || !reflect.DeepEqual(*event.Capability, want) {
				t.Fatalf("capability observation=%+v, want participant alice and %+v", event, want)
			}
			return
		case <-ctx.Done():
			t.Fatal("capability event was not delivered")
		}
	}
}

func TestExternalLiveCapabilityCatalogChangeRefreshesProviderInOrder(t *testing.T) {
	fixture := newLiveCapabilityRefreshFixture(t)
	defer fixture.close(t)
	fixture.changed.Store(true)
	fixture.capability.events <- session.LiveCapabilityEvent{Type: "catalog_changed", Sequence: 1}
	fixture.waitForOrderedUpdate(t)
	// A second catalog event arriving while the provider update is admitted is
	// coalesced into one follow-up refresh after the ordered barrier releases.
	fixture.capability.events <- session.LiveCapabilityEvent{Type: "generation_changed", Sequence: 2}
	fixture.assertTextWaitsForUpdate(t)
	close(fixture.releaseUpdate)
	fixture.assertOrderedMessages(t)
	if got := fixture.capability.refreshCalls.Load(); got < 2 {
		t.Fatalf("catalog refresh calls = %d, want admission and event refresh", got)
	}
}

type liveCapabilityRefreshFixture struct {
	ctx           context.Context
	cancel        context.CancelFunc
	provider      *embeddedLiveProvider
	capability    *embeddedCapability
	changed       atomic.Bool
	sent          chan messages.StreamMessage
	enteredUpdate chan struct{}
	releaseUpdate chan struct{}
	controlDone   <-chan error
	handle        session.LiveHandle
}

func newLiveCapabilityRefreshFixture(t *testing.T) *liveCapabilityRefreshFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	fixture := &liveCapabilityRefreshFixture{
		ctx: ctx, cancel: cancel, provider: newEmbeddedLiveProvider(),
		capability: &embeddedCapability{events: make(chan session.LiveCapabilityEvent, 2), watchContext: make(chan context.Context, 1)},
		sent:       make(chan messages.StreamMessage, 8), enteredUpdate: make(chan struct{}), releaseUpdate: make(chan struct{}),
	}
	initial := []messages.ToolDefinition{{Name: "browser_initial"}}
	updated := []messages.ToolDefinition{{Name: "browser_updated"}}
	fixture.capability.refresh = func() []messages.ToolDefinition {
		if fixture.changed.Load() {
			return updated
		}
		return initial
	}
	var updateOnce sync.Once
	fixture.provider.onSend = func(ctx context.Context, message messages.StreamMessage) bool {
		if message.Type == messages.StreamTypeSessionUpdate {
			updateOnce.Do(func() { close(fixture.enteredUpdate) })
			select {
			case <-fixture.releaseUpdate:
			case <-ctx.Done():
				return false
			}
		}
		select {
		case fixture.sent <- message:
			return true
		case <-ctx.Done():
			return false
		}
	}
	host := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return fixture.provider, nil
		},
	})
	handle, err := host.OpenLive(ctx, session.LiveRequest{ParticipantID: "browser", Capabilities: &session.LiveCapabilities{Handle: fixture.capability}})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	fixture.handle = handle
	if err := handle.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-fixture.capability.watchContext:
	case <-ctx.Done():
		cancel()
		t.Fatal("capability watcher did not start")
	}
	if got := fixture.capability.refreshCalls.Load(); got != 1 {
		cancel()
		t.Fatalf("initial capability refresh calls = %d, want one", got)
	}
	return fixture
}

func (fixture *liveCapabilityRefreshFixture) close(t *testing.T) {
	t.Helper()
	closeForTest(t, fixture.handle)
	closeForTest(t, fixture.provider)
	fixture.cancel()
}

func (fixture *liveCapabilityRefreshFixture) waitForOrderedUpdate(t *testing.T) {
	t.Helper()
	select {
	case <-fixture.enteredUpdate:
	case <-fixture.ctx.Done():
		t.Fatal("catalog change did not reach ordered provider update")
	}
}

func (fixture *liveCapabilityRefreshFixture) assertTextWaitsForUpdate(t *testing.T) {
	t.Helper()
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- fixture.handle.Send(fixture.ctx, session.LiveControl{Kind: session.LiveControlText, Text: "after catalog"})
	}()
	select {
	case message := <-fixture.sent:
		if message.Type == messages.StreamTypeTextDelta {
			t.Fatal("text control overtook the pending catalog update")
		}
	case <-time.After(50 * time.Millisecond):
	}
	fixture.controlDone = controlDone
}

func (fixture *liveCapabilityRefreshFixture) assertOrderedMessages(t *testing.T) {
	t.Helper()
	select {
	case err := <-fixture.controlDone:
		if err != nil {
			t.Fatalf("ordered text control: %v", err)
		}
	case <-fixture.ctx.Done():
		t.Fatal("ordered text control did not complete")
	}
	var sawUpdate, sawText bool
	deadline := time.After(time.Second)
	for !sawUpdate || !sawText {
		select {
		case message := <-fixture.sent:
			if message.Type == messages.StreamTypeSessionUpdate {
				fixture.assertUpdatedTools(t, message)
				sawUpdate = true
			} else if message.Type == messages.StreamTypeTextDelta {
				sawText = true
			}
		case <-deadline:
			t.Fatalf("provider messages update=%t text=%t refreshes=%d", sawUpdate, sawText, fixture.capability.refreshCalls.Load())
		}
	}
}

func (fixture *liveCapabilityRefreshFixture) assertUpdatedTools(t *testing.T, message messages.StreamMessage) {
	t.Helper()
	value, ok := message.Value.(*messages.SessionUpdateValue)
	if !ok || value == nil || len(value.Tools) != 1 || value.Tools[0].Name != "browser_updated" {
		t.Fatalf("catalog update = %#v, want browser_updated", message.Value)
	}
}

func TestExternalLiveCapabilityPublicationStaysParticipantLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	alphaProvider, betaProvider := newEmbeddedLiveProvider(), newEmbeddedLiveProvider()
	defer closeForTest(t, alphaProvider)
	defer closeForTest(t, betaProvider)
	alphaSent, betaSent := make(chan messages.StreamMessage, 8), make(chan messages.StreamMessage, 8)
	alphaProvider.onSend = providerMessageRecorder(alphaSent)
	betaProvider.onSend = providerMessageRecorder(betaSent)
	var alphaChanged, betaChanged atomic.Bool
	alphaCapability := newPublicationCapability(&alphaChanged, "alpha")
	betaCapability := newPublicationCapability(&betaChanged, "beta")
	alphaHost := sessionwire.NewLiveService(sessionwire.LiveDependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return alphaProvider, nil
	}})
	betaHost := sessionwire.NewLiveService(sessionwire.LiveDependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return betaProvider, nil
	}})
	alpha, err := alphaHost.OpenLive(ctx, session.LiveRequest{ParticipantID: "alpha", Capabilities: &session.LiveCapabilities{Handle: alphaCapability}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, alpha)
	beta, err := betaHost.OpenLive(ctx, session.LiveRequest{ParticipantID: "beta", Capabilities: &session.LiveCapabilities{Handle: betaCapability}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, beta)
	if err := alpha.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := beta.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []*embeddedCapability{alphaCapability, betaCapability} {
		select {
		case <-capability.watchContext:
		case <-ctx.Done():
			t.Fatal("capability watcher did not start")
		}
	}
	alphaChanged.Store(true)
	alphaCapability.events <- session.LiveCapabilityEvent{Type: "catalog_changed", Sequence: 1, BrowserID: "alpha-browser"}
	update := awaitProviderMessage(t, ctx, alphaSent, messages.StreamTypeSessionUpdate)
	value, ok := update.Value.(*messages.SessionUpdateValue)
	if !ok || value == nil || len(value.Tools) != 1 || value.Tools[0].Name != "browser_alpha_updated" {
		t.Fatalf("alpha update = %#v, want alpha's refreshed catalog", update.Value)
	}
	select {
	case message := <-betaSent:
		if message.Type == messages.StreamTypeSessionUpdate {
			t.Fatalf("beta received alpha catalog update: %#v", message.Value)
		}
	case <-time.After(100 * time.Millisecond):
	}
	if got := alphaCapability.refreshCalls.Load(); got < 2 {
		t.Fatalf("alpha refresh calls = %d, want admission and catalog refresh", got)
	}
	if got := betaCapability.refreshCalls.Load(); got != 1 {
		t.Fatalf("beta refresh calls = %d, want admission only", got)
	}
}

func newPublicationCapability(changed *atomic.Bool, id string) *embeddedCapability {
	return &embeddedCapability{
		events:       make(chan session.LiveCapabilityEvent, 1),
		watchContext: make(chan context.Context, 1),
		refresh: func() []messages.ToolDefinition {
			name := "browser_" + id
			if changed.Load() {
				name += "_updated"
			}
			return []messages.ToolDefinition{{Name: name}}
		},
	}
}

func providerMessageRecorder(output chan<- messages.StreamMessage) func(context.Context, messages.StreamMessage) bool {
	return func(ctx context.Context, message messages.StreamMessage) bool {
		select {
		case output <- message:
			return true
		case <-ctx.Done():
			return false
		}
	}
}

func awaitProviderMessage(t *testing.T, ctx context.Context, input <-chan messages.StreamMessage, kind messages.StreamMessageType) messages.StreamMessage {
	t.Helper()
	for {
		select {
		case message := <-input:
			if message.Type == kind {
				return message
			}
		case <-ctx.Done():
			t.Fatalf("provider did not receive %s: %v", kind, ctx.Err())
		}
	}
}
