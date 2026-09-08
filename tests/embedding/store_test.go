package embedding_test

import (
	"context"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// memoryStore deliberately has no filesystem implementation. Each host owns
// one store, so equal session identifiers cannot alias another host's state.
type memoryStore struct {
	mu       sync.Mutex
	next     int
	idError  error
	latest   string
	messages map[string][]session.Message
}

func (store *memoryStore) Load(ctx context.Context, id string) ([]session.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]session.Message(nil), store.messages[id]...), nil
}

func (store *memoryStore) Latest(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.latest, nil
}

func (store *memoryStore) NewSessionID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.idError != nil {
		return "", store.idError
	}
	store.next++
	return fmt.Sprintf("embedded-%d", store.next), nil
}

func (store *memoryStore) Save(ctx context.Context, id string, messages []session.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.messages == nil {
		store.messages = make(map[string][]session.Message)
	}
	store.messages[id] = append([]session.Message(nil), messages...)
	store.latest = id
	return nil
}
