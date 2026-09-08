package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

type memoryStore struct {
	mu       sync.Mutex
	next     uint64
	sessions map[string][]messages.Message
	traces   map[string]session.TraceRecord
}

func newMemoryStore() *memoryStore {
	return &memoryStore{sessions: make(map[string][]messages.Message), traces: make(map[string]session.TraceRecord)}
}
func (s *memoryStore) Load(ctx context.Context, id string) ([]messages.Message, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.Message(nil), s.sessions[id]...), nil
}
func (s *memoryStore) Latest(ctx context.Context) (string, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var id string
	for candidate := range s.sessions {
		if candidate > id {
			id = candidate
		}
	}
	return id, nil
}
func (s *memoryStore) NewSessionID(ctx context.Context) (string, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("embedded-%d-%d", time.Now().UnixNano(), s.next), nil
}
func (s *memoryStore) Save(ctx context.Context, id string, msgs []messages.Message) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = append([]messages.Message(nil), msgs...)
	return nil
}
func (s *memoryStore) LoadTrace(ctx context.Context, id string) (*session.TraceRecord, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	trace, ok := s.traces[id]
	if !ok {
		return nil, nil
	}
	return &trace, nil
}
func (s *memoryStore) SaveTrace(ctx context.Context, trace session.TraceRecord) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces[trace.TraceID] = trace
	return nil
}
func (s *memoryStore) NewTraceID(ctx context.Context) (string, error) { return s.NewSessionID(ctx) }
