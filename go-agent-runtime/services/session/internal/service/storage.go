package service

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	agent "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/execution"
	internalSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
)

type storageAdapter struct {
	ctx          context.Context
	store        session.SessionStore
	traces       session.TraceStore
	workspaceDir string
}

// persistenceCleanupTimeout bounds the context used for terminal evidence.
// A caller is allowed to cancel execution, but cancellation must not leave a
// trace in its transient RUNNING state simply because the store observes the
// same context.  The detached context is used only for writes that finalize
// already admitted work; reads and identifier allocation still honor the
// caller's context.
const persistenceCleanupTimeout = 2 * time.Second

func newStorageAdapter(ctx context.Context, store session.SessionStore, traces session.TraceStore, workspaceDir string) agent.Storage {
	if store == nil || traces == nil {
		return nil
	}
	if ctx == nil {
		return nil
	}
	return &storageAdapter{ctx: ctx, store: store, traces: traces, workspaceDir: workspaceDir}
}

func (s *storageAdapter) Load(id string) ([]messages.Message, error) {
	return s.store.Load(s.ctx, id)
}
func (s *storageAdapter) Latest() (string, error) { return s.store.Latest(s.ctx) }
func (s *storageAdapter) NewSessionID() string {
	id, err := s.NewSessionIDWithError()
	if err != nil {
		return ""
	}
	return id
}
func (s *storageAdapter) NewSessionIDWithError() (string, error) {
	id, err := s.store.NewSessionID(s.ctx)
	if err != nil {
		return "", err
	}
	return id, nil
}
func (s *storageAdapter) Save(id string, msgs []messages.Message) error {
	ctx, cancel := s.finalizationContext()
	defer cancel()
	return s.store.Save(ctx, id, msgs)
}
func (s *storageAdapter) WorkspaceDir() string { return s.workspaceDir }
func (s *storageAdapter) NewTraceID() string {
	id, err := s.NewTraceIDWithError()
	if err != nil {
		return ""
	}
	return id
}
func (s *storageAdapter) NewTraceIDWithError() (string, error) {
	id, err := s.traces.NewTraceID(s.ctx)
	if err != nil {
		return "", err
	}
	return id, nil
}
func (s *storageAdapter) LoadTrace(id string) (*internalSession.TraceRecord, error) {
	trace, err := s.traces.LoadTrace(s.ctx, id)
	if err != nil || trace == nil {
		return nil, err
	}
	converted := toInternalTrace(*trace)
	return &converted, nil
}
func (s *storageAdapter) SaveTrace(trace internalSession.TraceRecord) error {
	ctx, cancel := s.finalizationContext()
	defer cancel()
	return s.traces.SaveTrace(ctx, toPublicTrace(trace))
}

func (s *storageAdapter) finalizationContext() (context.Context, context.CancelFunc) {
	if s == nil || s.ctx == nil || s.ctx.Err() == nil {
		return s.ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(s.ctx), persistenceCleanupTimeout)
}

func toInternalTrace(trace session.TraceRecord) internalSession.TraceRecord {
	converted := internalSession.TraceRecord{
		TraceID: trace.TraceID, Status: internalSession.TraceStatus(trace.Status),
		Config:           internalSession.TraceConfig{MaxIterations: trace.Config.MaxIterations, StopWord: trace.Config.StopWord, Prompt: trace.Config.Prompt},
		CurrentIteration: trace.CurrentIteration,
	}
	converted.Iterations = make([]internalSession.IterationTrace, 0, len(trace.Iterations))
	for _, iteration := range trace.Iterations {
		converted.Iterations = append(converted.Iterations, internalSession.IterationTrace{
			Iteration: iteration.Iteration, SessionID: iteration.SessionID, SubAgentSessionIDs: append([]string(nil), iteration.SubAgentSessionIDs...), Status: internalSession.IterationStatus(iteration.Status),
		})
	}
	return converted
}

func toPublicTrace(trace internalSession.TraceRecord) session.TraceRecord {
	converted := session.TraceRecord{
		TraceID: trace.TraceID, Status: session.TraceStatus(trace.Status),
		Config:           session.TraceConfig{MaxIterations: trace.Config.MaxIterations, StopWord: trace.Config.StopWord, Prompt: trace.Config.Prompt},
		CurrentIteration: trace.CurrentIteration,
	}
	converted.Iterations = make([]session.IterationTrace, 0, len(trace.Iterations))
	for _, iteration := range trace.Iterations {
		converted.Iterations = append(converted.Iterations, session.IterationTrace{
			Iteration: iteration.Iteration, SessionID: iteration.SessionID, SubAgentSessionIDs: append([]string(nil), iteration.SubAgentSessionIDs...), Status: session.IterationStatus(iteration.Status),
		})
	}
	return converted
}

// memoryStore keeps the zero-dependency runtime usable in deterministic
// embedded tests. Production hosts should inject a durable SessionStore.
