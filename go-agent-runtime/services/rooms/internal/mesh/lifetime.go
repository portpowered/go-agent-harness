package mesh

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// This file owns the mesh lifetime: the in-flight join registry, shutdown,
// and pair release.

// contextRequiredError reports a call made without a caller context.
type contextRequiredError string

func (e contextRequiredError) Error() string { return string(e) }

// errMeshContextRequired reports a nil lifetime or operation context.
const errMeshContextRequired contextRequiredError = "room mesh context is required"

func (m *mesh) shutdown() {
	m.mu.Lock()
	m.closed = true
	all := m.capturePairsLocked()
	m.participants = make(map[string]struct{})
	m.pairs = make(map[rooms.PairSpec]*meshPair)
	m.pending, m.closing = nil, nil
	stopParent := m.stopParent
	cancels := make([]context.CancelFunc, 0, len(m.operations))
	for _, cancel := range m.operations {
		cancels = append(cancels, cancel)
	}
	m.mu.Unlock()
	if stopParent != nil {
		stopParent()
	}
	for _, cancel := range cancels {
		cancel()
	}
	m.closeErr = closeMeshPairs(all)
	close(m.done)
}

func closeMeshPairs(pairs []*meshPair) (closeErr error) {
	seen := make(map[*meshPair]struct{}, len(pairs))
	for index := len(pairs) - 1; index >= 0; index-- {
		pair := pairs[index]
		if pair == nil || nilPairResource(pair.resource) {
			continue
		}
		if _, exists := seen[pair]; exists {
			continue
		}
		seen[pair] = struct{}{}
		closeErr = errors.Join(closeErr, pair.close())
	}
	return closeErr
}

// operationContext derives a Join context from the caller's context that
// shutdown also cancels; after shutdown it is returned already cancelled.
func (m *mesh) operationContext(ctx context.Context) (context.Context, func()) {
	op, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return op, cancel
	}
	m.nextOperation++
	id := m.nextOperation
	m.operations[id] = cancel
	m.mu.Unlock()
	return op, func() {
		m.mu.Lock()
		delete(m.operations, id)
		m.mu.Unlock()
		cancel()
	}
}

func (m *mesh) lifetimeEndedLocked() bool {
	if m.closed {
		return true
	}
	select {
	case <-m.parentDone:
		return true
	default:
		return false
	}
}

func withoutPairs(have, remove []*meshPair) []*meshPair {
	wanted := make(map[*meshPair]struct{}, len(remove))
	for _, pair := range remove {
		if pair != nil {
			wanted[pair] = struct{}{}
		}
	}
	kept := have[:0]
	for _, pair := range have {
		if pair == nil {
			continue
		}
		if _, exists := wanted[pair]; !exists {
			kept = append(kept, pair)
		}
	}
	for index := len(kept); index < len(have); index++ {
		have[index] = nil
	}
	return kept
}
