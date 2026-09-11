package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type meshPair struct {
	resource  rooms.PairResource
	closeOnce sync.Once
	closeErr  error
}

func (p *meshPair) close() error {
	p.closeOnce.Do(func() { p.closeErr = p.resource.Close() })
	return p.closeErr
}

type mesh struct {
	mu, mutateMu sync.RWMutex
	participants map[string]struct{}
	pairs        map[rooms.PairSpec]*meshPair
	pending      []*meshPair
	closing      []*meshPair
	closed       bool
	done         chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	stopParent   func() bool
	factory      rooms.PairFactory
	closeOnce    sync.Once
	closeErr     error
}

var _ rooms.Mesh = (*mesh)(nil)

func NewMesh(config rooms.MeshConfig) rooms.Mesh {
	parent := config.Context
	if parent == nil {
		parent = context.Background()
	}
	factory := config.PairFactory
	if factory == nil {
		factory = func(context.Context, rooms.PairSpec) (rooms.PairResource, error) {
			return nil, rooms.ErrMeshPairFactoryUnavailable
		}
	}
	ctx, cancel := context.WithCancel(parent)
	m := &mesh{participants: make(map[string]struct{}), pairs: make(map[rooms.PairSpec]*meshPair), done: make(chan struct{}), ctx: ctx, cancel: cancel, factory: factory}
	m.stopParent = context.AfterFunc(parent, func() { _ = m.Close() }) //nolint:errcheck // Parent cancellation invokes the same idempotent Close path; its error is stored for explicit callers.
	return m
}
func NewPairSpec(firstID, secondID string) (rooms.PairSpec, error) {
	firstID, err := normalizeID(firstID)
	if err != nil {
		return rooms.PairSpec{}, err
	}
	secondID, err = normalizeID(secondID)
	if err != nil {
		return rooms.PairSpec{}, err
	}
	if firstID == secondID {
		return rooms.PairSpec{}, rooms.ErrMeshInvalidPair
	}
	if firstID > secondID {
		firstID, secondID = secondID, firstID
	}
	return rooms.PairSpec{FirstID: firstID, SecondID: secondID}, nil
}
func (m *mesh) Context() context.Context { return m.ctx }
func (m *mesh) Done() <-chan struct{}    { return m.done }
func (m *mesh) Join(ctx context.Context, participantID string) error {
	id, err := normalizeID(participantID)
	if err != nil {
		return meshError("join", participantID, "", err)
	}
	m.mutateMu.Lock()
	defer m.mutateMu.Unlock()
	m.mu.RLock()
	closed := m.closed
	_, duplicate := m.participants[id]
	existing := make([]string, 0, len(m.participants))
	for existingID := range m.participants {
		existing = append(existing, existingID)
	}
	m.mu.RUnlock()
	if closed {
		return meshError("join", id, "", rooms.ErrMeshClosed)
	}
	if duplicate {
		return meshError("join", id, "", rooms.ErrMeshDuplicateParticipant)
	}
	sort.Strings(existing)
	joinCtx, stop := m.operationContext(ctx)
	defer stop()
	created := make([]*meshPair, 0, len(existing))
	for _, existingID := range existing {
		pair, pairErr := m.connectPair(joinCtx, id, existingID, created)
		if pairErr != nil {
			return pairErr
		}
		created = append(created, pair)
	}
	if err := joinCtx.Err(); err != nil {
		return m.joinFailure(id, "join", err, created)
	}
	return m.commitJoin(id, existing, created)
}
func (m *mesh) connectPair(ctx context.Context, participantID, remoteID string, created []*meshPair) (*meshPair, error) {
	if err := ctx.Err(); err != nil {
		return nil, m.joinFailure(participantID, "connect pair", err, created)
	}
	spec, err := NewPairSpec(participantID, remoteID)
	if err != nil {
		return nil, m.joinFailure(participantID, "create pair", meshError("create pair", participantID, remoteID, err), created)
	}
	resource, err := m.factory(ctx, spec)
	if err != nil {
		return nil, m.joinFailure(participantID, "create pair", meshError("create pair", participantID, remoteID, err), append(created, &meshPair{resource: resource}))
	}
	if nilPairResource(resource) {
		return nil, m.joinFailure(participantID, "create pair", meshError("create pair", participantID, remoteID, rooms.ErrMeshNilPairResource), created)
	}
	pair := &meshPair{resource: resource}
	if err := m.addPending(pair); err != nil {
		return nil, m.joinFailure(participantID, "connect pair", err, append(created, pair))
	}
	if err := resource.Connect(ctx); err != nil {
		return nil, m.joinFailure(participantID, "connect pair", meshError("connect pair", participantID, remoteID, err), append(created, pair))
	}
	return pair, nil
}
func (m *mesh) commitJoin(id string, existing []string, created []*meshPair) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return m.joinFailure(id, "join", rooms.ErrMeshClosed, created)
	}
	if _, exists := m.participants[id]; exists {
		m.mu.Unlock()
		return m.joinFailure(id, "join", rooms.ErrMeshDuplicateParticipant, created)
	}
	m.participants[id] = struct{}{}
	for index, remoteID := range existing {
		spec, _ := NewPairSpec(id, remoteID) //nolint:errcheck // IDs were normalized and checked distinct before this commit.
		m.pairs[spec] = created[index]
	}
	m.pending = withoutPairs(m.pending, created)
	m.mu.Unlock()
	return nil
}
func (m *mesh) joinFailure(id, operation string, cause error, created []*meshPair) error {
	cleanupErr := closeMeshPairs(created)
	m.discard(created, false)
	var meshErr *rooms.MeshError
	if cleanupErr != nil {
		if errors.As(cause, &meshErr) {
			copyOf := *meshErr
			copyOf.Cause = errors.Join(meshErr.Cause, cleanupErr)
			cause = &copyOf
		} else {
			cause = errors.Join(cause, cleanupErr)
		}
	}
	if errors.As(cause, &meshErr) {
		return meshErr
	}
	return meshError(operation, id, "", cause)
}
func (m *mesh) AddParticipant(c context.Context, id string) error { return m.Join(c, id) }
func (m *mesh) RemoveParticipant(id string) error                 { return m.Remove(id) }
func (m *mesh) Leave(id string) error                             { return m.Remove(id) }
func (m *mesh) Remove(participantID string) error {
	id, err := normalizeID(participantID)
	if err != nil {
		return meshError("remove", participantID, "", err)
	}
	m.mutateMu.Lock()
	defer m.mutateMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return meshError("remove", id, "", rooms.ErrMeshClosed)
	}
	if _, exists := m.participants[id]; !exists {
		m.mu.Unlock()
		return meshError("remove", id, "", rooms.ErrMeshUnknownParticipant)
	}
	delete(m.participants, id)
	incident := make([]*meshPair, 0)
	for spec, pair := range m.pairs {
		if spec.FirstID != id && spec.SecondID != id {
			continue
		}
		incident = append(incident, pair)
		delete(m.pairs, spec)
	}
	m.closing = append(m.closing, incident...)
	m.mu.Unlock()
	err = closeMeshPairs(incident)
	m.discard(incident, true)
	if err != nil {
		return meshError("remove", id, "", err)
	}
	return nil
}
func (m *mesh) Participants() []string {
	m.mu.RLock()
	ids := make([]string, 0, len(m.participants))
	for id := range m.participants {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	return ids
}
func (m *mesh) ParticipantIDs() []string { return m.Participants() }
func (m *mesh) Peers(participantID string) (map[string]rooms.PeerView, error) {
	id, err := normalizeID(participantID)
	if err != nil {
		return nil, meshError("inspect peers", participantID, "", err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, meshError("inspect peers", id, "", rooms.ErrMeshClosed)
	}
	if _, exists := m.participants[id]; !exists {
		return nil, meshError("inspect peers", id, "", rooms.ErrMeshUnknownParticipant)
	}
	peers := make(map[string]rooms.PeerView)
	for spec, pair := range m.pairs {
		remoteID := ""
		if spec.FirstID == id {
			remoteID = spec.SecondID
		} else if spec.SecondID == id {
			remoteID = spec.FirstID
		}
		if remoteID != "" {
			peers[remoteID] = rooms.PeerView{LocalID: id, RemoteID: remoteID, Pair: pair.resource}
		}
	}
	return peers, nil
}
func (m *mesh) RemotePeers(id string) (map[string]rooms.PeerView, error) { return m.Peers(id) }
func (m *mesh) Pair(firstID, secondID string) (rooms.PairResource, error) {
	spec, err := NewPairSpec(firstID, secondID)
	if err != nil {
		return nil, meshError("inspect pair", firstID, secondID, err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, meshError("inspect pair", spec.FirstID, spec.SecondID, rooms.ErrMeshClosed)
	}
	pair, exists := m.pairs[spec]
	if !exists {
		return nil, meshError("inspect pair", spec.FirstID, spec.SecondID, rooms.ErrMeshPairNotFound)
	}
	return pair.resource, nil
}
func (m *mesh) Pairs() []rooms.PairSnapshot {
	m.mu.RLock()
	pairs := make([]rooms.PairSnapshot, 0, len(m.pairs))
	for spec, pair := range m.pairs {
		pairs = append(pairs, rooms.PairSnapshot{Spec: spec, Resource: pair.resource})
	}
	m.mu.RUnlock()
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Spec.FirstID == pairs[j].Spec.FirstID {
			return pairs[i].Spec.SecondID < pairs[j].Spec.SecondID
		}
		return pairs[i].Spec.FirstID < pairs[j].Spec.FirstID
	})
	return pairs
}
func (m *mesh) PairCount() int { m.mu.RLock(); defer m.mu.RUnlock(); return len(m.pairs) }
func (m *mesh) Close() error   { m.closeOnce.Do(m.shutdown); return m.closeErr }
func (m *mesh) shutdown() {
	m.mu.Lock()
	m.closed = true
	all := m.capturePairsLocked()
	m.participants = make(map[string]struct{})
	m.pairs = make(map[rooms.PairSpec]*meshPair)
	m.pending, m.closing = nil, nil
	stopParent, cancel := m.stopParent, m.cancel
	m.mu.Unlock()
	if stopParent != nil {
		stopParent()
	}
	if cancel != nil {
		cancel()
	}
	m.closeErr = closeMeshPairs(all)
	close(m.done)
}
func (m *mesh) capturePairsLocked() []*meshPair {
	all := make([]*meshPair, 0, len(m.pairs)+len(m.pending)+len(m.closing))
	for _, pair := range m.pairs {
		all = append(all, pair)
	}
	all = append(all, m.pending...)
	return append(all, m.closing...)
}
func (m *mesh) operationContext(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	op, cancel := context.WithCancel(m.ctx)
	stop := context.AfterFunc(ctx, cancel)
	return op, func() { stop(); cancel() }
}
func (m *mesh) addPending(pair *meshPair) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return rooms.ErrMeshClosed
	}
	m.pending = append(m.pending, pair)
	return nil
}
func (m *mesh) discard(pairs []*meshPair, closing bool) {
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	target := &m.pending
	if closing {
		target = &m.closing
	}
	*target = withoutPairs(*target, pairs)
	m.mu.Unlock()
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
func nilPairResource(resource rooms.PairResource) bool {
	if resource == nil {
		return true
	}
	value := reflect.ValueOf(resource)
	if kind := value.Kind(); kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice {
		return value.IsNil()
	}
	return false
}
func normalizeID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", rooms.ErrMeshEmptyParticipantID
	}
	if strings.ContainsAny(id, "\x00\r\n\t") {
		return "", rooms.ErrMeshInvalidParticipantID
	}
	return id, nil
}
func meshError(operation, participantID, remoteID string, cause error) *rooms.MeshError {
	return &rooms.MeshError{Operation: operation, ParticipantID: participantID, RemoteID: remoteID, Cause: cause}
}
