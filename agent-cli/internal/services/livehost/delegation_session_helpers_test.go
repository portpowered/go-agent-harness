package livehost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	livedelegationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

// These helpers compose the real live session service, the openai-live
// provider over the fake GPT-Live server, and the live delegation service
// over a scripted backend provider, all on synctest's virtual clock.

const (
	delegationLiveKey   = "sk-live-delegation"
	scriptedBackendName = "scripted-backend"
	delegationProvider  = "openai-live"
)

// backendReply is one scripted backend inference: text, a tool call, or an
// error, optionally after a delay on the virtual clock.
type backendReply struct {
	text  string
	call  *messages.ToolCall
	err   error
	delay time.Duration
	usage int
}

func textReply(text string) backendReply { return backendReply{text: text, usage: 10} }

func callReply(id, name, arguments string) backendReply {
	return backendReply{call: &messages.ToolCall{ID: id, Name: name, Arguments: arguments}, usage: 10}
}

// scriptedBackend is the delegation backend: a provider service whose
// provider answers each inference from the script keyed by a marker that
// appears in the delegation's task, so concurrent delegations get their own
// replies. It records how many inferences ran at once.
type scriptedBackend struct {
	mu        sync.Mutex
	scripts   map[string][]backendReply
	configs   []providers.Config
	requests  []messages.InferenceRequest
	active    int
	maxActive int
	buildErr  error
	streamErr error
}

func newScriptedBackend(scripts map[string][]backendReply) *scriptedBackend {
	return &scriptedBackend{scripts: scripts}
}

func (b *scriptedBackend) Build(_ context.Context, config providers.Config) (llmproviders.Provider, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.configs = append(b.configs, config)
	if b.buildErr != nil {
		return nil, b.buildErr
	}
	return b, nil
}

func (*scriptedBackend) Name() string { return scriptedBackendName }

// Infer is the loop's fallback after a refused stream; it repeats the
// stream's scripted error.
func (b *scriptedBackend) Infer(context.Context, llmproviders.InferenceRequest) (llmproviders.InferenceResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.streamErr != nil {
		return llmproviders.InferenceResponse{}, b.streamErr
	}
	return llmproviders.InferenceResponse{}, errors.New("scripted backend streams only")
}

func (b *scriptedBackend) next(req llmproviders.InferenceRequest) (backendReply, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests = append(b.requests, messages.InferenceRequest{Messages: req.Messages})
	task := ""
	for _, message := range req.Messages {
		if message.Role == messages.RoleUser {
			task = message.TextContent()
			break
		}
	}
	// The transcript window quotes earlier requests too; the latest marker
	// is the request this delegation is about.
	marker, at := "", -1
	for candidate := range b.scripts {
		if index := strings.LastIndex(task, candidate); index > at {
			marker, at = candidate, index
		}
	}
	replies := b.scripts[marker]
	if at < 0 || len(replies) == 0 {
		return backendReply{}, errors.New("scripted backend has no reply for this task")
	}
	reply := replies[0]
	if len(replies) > 1 {
		b.scripts[marker] = replies[1:]
	}
	b.streamErr = reply.err
	return reply, nil
}

func (b *scriptedBackend) InferStream(ctx context.Context, req llmproviders.InferenceRequest) (<-chan messages.StreamMessage, error) {
	reply, err := b.next(req)
	if err != nil {
		return nil, err
	}
	if reply.err != nil {
		return nil, reply.err
	}
	b.enter()
	out := make(chan messages.StreamMessage)
	go func() {
		defer close(out)
		defer b.leave()
		if reply.delay > 0 {
			select {
			case <-time.After(reply.delay):
			case <-ctx.Done():
				return
			}
		}
		for _, msg := range reply.stream() {
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (b *scriptedBackend) enter() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active++
	b.maxActive = max(b.maxActive, b.active)
}

func (b *scriptedBackend) leave() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active--
}

func (b *scriptedBackend) peakConcurrency() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxActive
}

func (r backendReply) stream() []messages.StreamMessage {
	usage := messages.TokenUsage{PromptTokens: r.usage / 2, CompletionTokens: r.usage - r.usage/2, TotalTokens: r.usage}
	out := []messages.StreamMessage{{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}}
	if r.call != nil {
		out = append(out,
			messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: 1, Value: messages.NewToolCallStartValue(r.call.ID, r.call.Name)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: 1, Value: messages.NewToolCallDeltaValue(r.call.Arguments)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: 1, Value: messages.NewToolCallEndValue(r.call.ID, r.call.Name, r.call.Arguments)},
		)
	} else {
		out = append(out,
			messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()},
			messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(r.text)},
			messages.StreamMessage{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()},
		)
	}
	return append(out, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(usage)})
}

// sessionTool is one of the session's own tools, as the live session would
// offer it to a provider that calls tools.
type sessionTool func(ctx context.Context, call messages.ToolCall) (string, error)

// sessionTools is the session's tool executor. It records every call and
// the context error each call ended with.
type sessionTools struct {
	mu        sync.Mutex
	tools     map[string]sessionTool
	calls     []messages.ToolCall
	cancelled []string
}

func (s *sessionTools) definitions() []messages.ToolDefinition {
	definitions := make([]messages.ToolDefinition, 0, len(s.tools))
	for name := range s.tools {
		definitions = append(definitions, messages.ToolDefinition{Name: name, Description: "test tool " + name})
	}
	return definitions
}

func (s *sessionTools) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	s.mu.Lock()
	s.calls = append(s.calls, call)
	tool := s.tools[call.Name]
	s.mu.Unlock()
	content, err := tool(ctx, call)
	if ctx.Err() != nil {
		s.mu.Lock()
		s.cancelled = append(s.cancelled, call.Name)
		s.mu.Unlock()
	}
	if err != nil {
		return messages.ToolCallResponse{}, err
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: content}, nil
}

func (s *sessionTools) snapshot() ([]messages.ToolCall, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.ToolCall(nil), s.calls...), append([]string(nil), s.cancelled...)
}

// delegationSession is one running live session with delegations.
type delegationSession struct {
	handle  session.LiveHandle
	done    chan error
	drained chan struct{}
	mu      sync.Mutex
	events  []session.LiveEvent
}

// eventKinds lists the kinds of the session events published so far: the
// voice loop's own observations.
func (s *delegationSession) eventKinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := make([]string, 0, len(s.events))
	for _, event := range s.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

// delegationToolEvents returns the delegation tool evidence the session
// published, as "kind delegation_id call_id tool".
func (s *delegationSession) delegationToolEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, event := range s.events {
		if event.Kind == string(session.LiveEventDelegationToolCall) || event.Kind == string(session.LiveEventDelegationToolResult) {
			out = append(out, event.Kind+" "+event.ItemID+" "+event.ToolCallID+" "+event.Text)
		}
	}
	return out
}

// startDelegationSession starts an openai-live session against fake with the
// delegation executor answering on backend.
func startDelegationSession(t *testing.T, fake *fakelive.Server, backend *scriptedBackend, tools *sessionTools, limits livedelegation.Limits, configure ...func(*session.LiveRequest)) *delegationSession {
	t.Helper()
	scheduler := platformclock.Real{}
	factory := sessionwire.NewProviderInferencerFactory(sessionwire.ProviderInferenceDependencies{
		Providers: providerswire.NewService(providerswire.Dependencies{Clock: scheduler}),
		Dialer:    fake.Dialer(),
		Credentials: func(context.Context, string) (string, error) {
			return delegationLiveKey, nil
		},
	})
	dependencies := sessionwire.LiveDependencies{
		InferencerFactory: factory,
		Clock:             scheduler.Now,
		Scheduler:         scheduler,
		Delegations:       livedelegationwire.NewService(livedelegationwire.Dependencies{Providers: backend, Scheduler: scheduler}),
	}
	if tools != nil {
		dependencies.ToolExecutor = tools
		dependencies.ToolDefinitions = tools.definitions()
	}
	request := session.LiveRequest{
		SessionID: "delegation", Provider: delegationProvider, Model: live.Model1, CredentialReference: "live-key",
		InputAudioFormat: "pcm16", OutputAudioFormat: "pcm16", InputAudioSampleRate: 24000, OutputAudioSampleRate: 24000,
		Instructions: "Be brief.",
		Delegation: &livedelegation.Policy{
			Backend: livedelegation.Backend{Provider: scriptedBackendName, Model: "backend-model"},
			Limits:  limits,
		},
	}
	for _, apply := range configure {
		apply(&request)
	}
	handle, err := sessionwire.NewLiveService(dependencies).OpenLive(t.Context(), request)
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	run := &delegationSession{handle: handle, done: make(chan error, 1), drained: make(chan struct{})}
	go func() {
		defer close(run.drained)
		for event := range handle.Events() {
			run.mu.Lock()
			run.events = append(run.events, event)
			run.mu.Unlock()
		}
	}()
	go func() { run.done <- handle.Wait() }()
	return run
}

// wait joins the session after the fake ended it, then releases the handle.
func (s *delegationSession) wait(t *testing.T) error {
	t.Helper()
	err := <-s.done
	if closeErr := s.handle.Close(); closeErr != nil {
		t.Errorf("Close: %v", closeErr)
	}
	closeEndpoints(t, s.handle)
	<-s.drained
	return err
}

func closeEndpoints(t *testing.T, handle session.LiveHandle) {
	t.Helper()
	media := handle.Media()
	if media.Inbound != nil {
		if err := media.Inbound.Close(); err != nil {
			t.Errorf("close inbound media: %v", err)
		}
	}
	if media.Outbound != nil {
		if err := media.Outbound.Close(); err != nil {
			t.Errorf("close outbound media: %v", err)
		}
	}
}

// appendEvent is one context append the fake GPT-Live server received.
type appendEvent struct {
	kind         string
	delegationID string
	content      string
}

func receivedAppends(fake *fakelive.Server) []appendEvent {
	var out []appendEvent
	for _, event := range fake.ClientEvents() {
		var command live.ContextAppend
		switch typed := event.(type) {
		case live.CommentaryAppend:
			command = live.ContextAppend(typed)
		case live.ThinkingAppend:
			command = live.ContextAppend(typed)
		case live.InstructionsAppend:
			command = live.ContextAppend(typed)
		default:
			continue
		}
		id := ""
		if command.DelegationID != nil {
			id = *command.DelegationID
		}
		out = append(out, appendEvent{kind: event.EventType(), delegationID: id, content: command.Content})
	}
	return out
}

func commentaries(fake *fakelive.Server) []appendEvent {
	var out []appendEvent
	for _, event := range receivedAppends(fake) {
		if event.kind == live.TypeCommentaryAppend {
			out = append(out, event)
		}
	}
	return out
}

// hasCommentary reports whether the fake has a commentary for id.
func hasCommentary(fake *fakelive.Server, id string) bool {
	for _, event := range commentaries(fake) {
		if event.delegationID == id {
			return true
		}
	}
	return false
}

// clientEventTypes lists the client event types the fake received.
func clientEventTypes(fake *fakelive.Server) []string {
	var out []string
	for _, event := range fake.ClientEvents() {
		out = append(out, event.EventType())
	}
	return out
}

func delegationAt(id string, offsetMS int64) live.DelegationCreated {
	return live.DelegationCreated{EventID: "evt_" + id, OffsetMS: offsetMS, Delegation: live.DelegationInfo{ID: id, Type: "delegation", Target: live.DelegationClient}}
}

func userSaid(text string, startMS, endMS int64) live.InputTranscriptDelta {
	return live.InputTranscriptDelta{EventID: "evt_in", Delta: text, StartMS: startMS, EndMS: endMS}
}
