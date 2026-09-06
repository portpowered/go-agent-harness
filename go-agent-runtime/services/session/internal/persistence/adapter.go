package session

import (
	"context"
	"fmt"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// Factory owns construction of the built-in storage adapter.
type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

func (*Factory) Open(options public.FileStoreOptions) (public.ManagedStore, error) {
	if options.Directory == "" {
		return nil, fmt.Errorf("session storage directory is required")
	}
	return &Adapter{storage: NewStorageWithWorkspace(options.Directory, options.WorkspaceDirectory)}, nil
}

// Adapter presents context-aware public ports over the single file codec.
type Adapter struct{ storage *Storage }

func (s *Adapter) Load(ctx context.Context, id string) ([]public.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.storage.Load(id)
}
func (s *Adapter) Save(ctx context.Context, id string, value []public.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.storage.Save(id, value)
}
func (s *Adapter) Latest(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.storage.Latest()
}
func (s *Adapter) NewSessionID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.storage.NewSessionID(), nil
}
func (s *Adapter) NewTraceID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.storage.NewTraceID(), nil
}
func (s *Adapter) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.storage.Delete(id)
}
func (s *Adapter) List(ctx context.Context, options public.SessionListOptions) ([]public.SessionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	infos, err := s.storage.ListWithOptions(SessionListOptions{Limit: options.Limit, Since: options.Since, Filter: options.Filter})
	if err != nil {
		return nil, err
	}
	result := make([]public.SessionInfo, len(infos))
	for i, info := range infos {
		result[i] = public.SessionInfo{ID: info.ID, ModTime: info.ModTime}
	}
	return result, nil
}
func (s *Adapter) ListTraces(ctx context.Context) ([]public.TraceInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	infos, err := s.storage.ListTraces()
	if err != nil {
		return nil, err
	}
	result := make([]public.TraceInfo, len(infos))
	for i, info := range infos {
		result[i] = public.TraceInfo{ID: info.ID, ModTime: info.ModTime}
	}
	return result, nil
}
func (s *Adapter) LoadTrace(ctx context.Context, id string) (*public.TraceRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	trace, err := s.storage.LoadTrace(id)
	if err != nil || trace == nil {
		return nil, err
	}
	result := public.TraceRecord{
		TraceID: trace.TraceID, Status: public.TraceStatus(trace.Status),
		Config:           public.TraceConfig{MaxIterations: trace.Config.MaxIterations, StopWord: trace.Config.StopWord, Prompt: trace.Config.Prompt},
		CurrentIteration: trace.CurrentIteration,
		Iterations:       make([]public.IterationTrace, len(trace.Iterations)),
	}
	for i, iteration := range trace.Iterations {
		result.Iterations[i] = public.IterationTrace{Iteration: iteration.Iteration, SessionID: iteration.SessionID, SubAgentSessionIDs: append([]string(nil), iteration.SubAgentSessionIDs...), Status: public.IterationStatus(iteration.Status)}
	}
	return &result, nil
}
func (s *Adapter) SaveTrace(ctx context.Context, value public.TraceRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := TraceRecord{
		TraceID: value.TraceID, Status: TraceStatus(value.Status),
		Config:           TraceConfig{MaxIterations: value.Config.MaxIterations, StopWord: value.Config.StopWord, Prompt: value.Config.Prompt},
		CurrentIteration: value.CurrentIteration,
		Iterations:       make([]IterationTrace, len(value.Iterations)),
	}
	for i, iteration := range value.Iterations {
		result.Iterations[i] = IterationTrace{Iteration: iteration.Iteration, SessionID: iteration.SessionID, SubAgentSessionIDs: append([]string(nil), iteration.SubAgentSessionIDs...), Status: IterationStatus(iteration.Status)}
	}
	return s.storage.SaveTrace(result)
}
