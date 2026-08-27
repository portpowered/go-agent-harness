package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var (
	// ErrMeshClosed means that the room no longer accepts membership changes.
	ErrMeshClosed = errors.New("room participant mesh is closed")
	// ErrMeshEmptyParticipantID means that a membership operation did not name
	// a participant. IDs are the stable key for every room-owned resource.
	ErrMeshEmptyParticipantID = errors.New("room participant ID must not be empty")
	// ErrMeshInvalidParticipantID means that an ID contains a value that cannot
	// safely cross the local signaling boundary.
	ErrMeshInvalidParticipantID = errors.New("room participant ID contains a control character")
	// ErrMeshDuplicateParticipant means that an ID is already in the room.
	ErrMeshDuplicateParticipant = errors.New("room participant is already joined")
	// ErrMeshUnknownParticipant means that an ID is not in the room.
	ErrMeshUnknownParticipant = errors.New("room participant is not joined")
	// ErrMeshPairNotFound means that two joined IDs do not have a pair resource.
	ErrMeshPairNotFound = errors.New("room participant pair is not present")
	// ErrMeshInvalidPair means that a pair spec names the same participant twice.
	ErrMeshInvalidPair = errors.New("room participant pair must contain two distinct IDs")
	// ErrMeshNilPairResource means that a pair factory returned no usable owner.
	ErrMeshNilPairResource = errors.New("room pair factory returned a nil resource")
	// ErrMeshPairFactoryUnavailable means that a mesh was configured without a
	// pair factory and could not fall back to the built-in loopback factory.
	ErrMeshPairFactoryUnavailable = errors.New("room pair factory is unavailable")
)

// MeshError attributes a membership or pair failure to the participant IDs
// involved. It deliberately contains no provider or credential values.
type MeshError struct {
	Operation     string
	ParticipantID string
	RemoteID      string
	Cause         error
}

func (e *MeshError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "room mesh"
	if e.Operation != "" {
		message += " " + e.Operation
	}
	if e.ParticipantID != "" {
		message += fmt.Sprintf(" participant %q", e.ParticipantID)
	}
	if e.RemoteID != "" {
		message += fmt.Sprintf(" with %q", e.RemoteID)
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *MeshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// PairSpec is the canonical identity of one unordered participant pair.
// FirstID is lexicographically before SecondID, which makes map keys and
// diagnostics deterministic regardless of join order.
type PairSpec struct {
	FirstID  string
	SecondID string
}

// PairKey is a descriptive alias for callers that use graph terminology.
type PairKey = PairSpec

// NewPairSpec returns the canonical unordered pair for two participant IDs.
func NewPairSpec(firstID, secondID string) (PairSpec, error) {
	firstID, err := normalizeMeshParticipantID(firstID)
	if err != nil {
		return PairSpec{}, err
	}
	secondID, err = normalizeMeshParticipantID(secondID)
	if err != nil {
		return PairSpec{}, err
	}
	if firstID == secondID {
		return PairSpec{}, ErrMeshInvalidPair
	}
	if firstID > secondID {
		firstID, secondID = secondID, firstID
	}
	return PairSpec{FirstID: firstID, SecondID: secondID}, nil
}

func (s PairSpec) String() string {
	return fmt.Sprintf("%q<->%q", s.FirstID, s.SecondID)
}

// PairResource is the lifecycle owner for one logical participant pair.
// Connect must establish the resource before Join commits membership. Close
// must release both sides and unblock any operation waiting on the pair. The
// mesh invokes each resource's Close at most once, including on failed joins.
type PairResource interface {
	Connect(context.Context) error
	Close() error
}

// PairFactory creates one inert resource for one canonical unordered pair.
// Construction must not mutate mesh membership; Connect is called by the
// mesh after the resource is registered as pending so room cancellation can
// close a pair whose connection attempt is still blocked.
type PairFactory func(context.Context, PairSpec) (PairResource, error)

// MeshConfig configures a participant mesh. A nil Context uses
// context.Background, and a nil PairFactory selects NewLoopbackPairFactory.
type MeshConfig struct {
	Context     context.Context
	PairFactory PairFactory
}

// PeerView is the participant-local view of one remote pair. The same logical
// PairResource is visible from each endpoint, but Peers never returns a
// participant's own ID or a pair belonging to another participant.
type PeerView struct {
	LocalID  string
	RemoteID string
	Pair     PairResource
}

// PairSnapshot is a read-only mesh snapshot entry. Resource ownership remains
// with the mesh; callers must not close it directly.
type PairSnapshot struct {
	Spec     PairSpec
	Resource PairResource
}

type meshPair struct {
	resource PairResource

	closeOnce sync.Once
	closeErr  error
}

func (p *meshPair) close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.closeErr = p.resource.Close()
	})
	return p.closeErr
}

type pendingPair struct {
	pair *meshPair
}

// Mesh owns the participant registry and exactly one pair resource for every
// unordered pair of joined IDs. It is safe for concurrent membership reads,
// cancellation, and close calls.
type Mesh struct {
	mu       sync.RWMutex
	mutateMu sync.Mutex

	participants map[string]struct{}
	pairs        map[PairSpec]*meshPair
	pending      []*pendingPair
	closed       bool
	done         chan struct{}

	ctx        context.Context
	cancel     context.CancelFunc
	stopParent func() bool
	factory    PairFactory

	closeOnce sync.Once
	closeErr  error
}

// NewMesh constructs a mesh without creating any pair or signaling resource.
// It accepts an optional config to keep the zero-argument local loopback path
// convenient while leaving dependency injection explicit for tests and the
// later room runtime.
func NewMesh(config ...MeshConfig) *Mesh {
	cfg := MeshConfig{}
	if len(config) > 0 {
		cfg = config[0]
	}
	parent := cfg.Context
	if parent == nil {
		parent = context.Background()
	}
	meshContext, cancel := context.WithCancel(parent)
	factory := cfg.PairFactory
	if factory == nil {
		factory = NewLoopbackPairFactory()
	}
	mesh := &Mesh{
		participants: make(map[string]struct{}),
		pairs:        make(map[PairSpec]*meshPair),
		done:         make(chan struct{}),
		ctx:          meshContext,
		cancel:       cancel,
		factory:      factory,
	}
	mesh.stopParent = context.AfterFunc(parent, func() { _ = mesh.Close() })
	return mesh
}

// NewParticipantMesh is the explicit constructor used by room composition
// code that already has a caller context and pair factory.
func NewParticipantMesh(ctx context.Context, factory PairFactory) *Mesh {
	return NewMesh(MeshConfig{Context: ctx, PairFactory: factory})
}

// NewLoopbackMesh constructs a mesh backed by the provider-neutral loopback
// signaling and rtc.Peer primitives.
func NewLoopbackMesh(ctx context.Context) *Mesh {
	return NewParticipantMesh(ctx, NewLoopbackPairFactory())
}

// Context returns the mesh-owned cancellation context. It is useful for
// binding participant mixers and sessions to the room lifetime.
func (m *Mesh) Context() context.Context {
	if m == nil {
		return context.Background()
	}
	return m.ctx
}

// Done closes when the mesh is closed or its parent context is canceled.
func (m *Mesh) Done() <-chan struct{} {
	if m == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return m.done
}

// Join atomically adds one participant and connects it to every participant
// already in the room. If any pair fails, all newly created pairs are closed
// and the registry is left unchanged.
func (m *Mesh) Join(ctx context.Context, participantID string) error {
	if m == nil {
		return &MeshError{Operation: "join", ParticipantID: participantID, Cause: ErrMeshClosed}
	}
	normalizedID, err := normalizeMeshParticipantID(participantID)
	if err != nil {
		return &MeshError{Operation: "join", ParticipantID: participantID, Cause: err}
	}

	// Serializing mutations prevents two concurrent joins from both observing
	// the same old membership and omitting their pair with one another.
	m.mutateMu.Lock()
	defer m.mutateMu.Unlock()

	m.mu.RLock()
	closed := m.closed
	_, duplicate := m.participants[normalizedID]
	existing := make([]string, 0, len(m.participants))
	for id := range m.participants {
		existing = append(existing, id)
	}
	m.mu.RUnlock()
	if closed {
		return &MeshError{Operation: "join", ParticipantID: normalizedID, Cause: ErrMeshClosed}
	}
	if duplicate {
		return &MeshError{Operation: "join", ParticipantID: normalizedID, Cause: ErrMeshDuplicateParticipant}
	}
	sort.Strings(existing)

	joinContext, stopContext := m.operationContext(ctx)
	defer stopContext()
	created := make([]*meshPair, 0, len(existing))
	for _, existingID := range existing {
		if err := joinContext.Err(); err != nil {
			return m.joinFailure(normalizedID, "connect pair", err, created)
		}
		spec, specErr := NewPairSpec(normalizedID, existingID)
		if specErr != nil {
			return m.joinFailure(normalizedID, "create pair", &MeshError{
				Operation:     "create pair",
				ParticipantID: normalizedID,
				RemoteID:      existingID,
				Cause:         specErr,
			}, created)
		}
		resource, factoryErr := m.factory(joinContext, spec)
		if factoryErr != nil {
			return m.joinFailure(normalizedID, "create pair", &MeshError{
				Operation:     "create pair",
				ParticipantID: normalizedID,
				RemoteID:      existingID,
				Cause:         factoryErr,
			}, created)
		}
		if nilPairResource(resource) {
			return m.joinFailure(normalizedID, "create pair", &MeshError{
				Operation:     "create pair",
				ParticipantID: normalizedID,
				RemoteID:      existingID,
				Cause:         ErrMeshNilPairResource,
			}, created)
		}
		pair := &meshPair{resource: resource}
		pending := &pendingPair{pair: pair}
		if err := m.addPending(pending); err != nil {
			_ = pair.close()
			return m.joinFailure(normalizedID, "connect pair", err, created)
		}
		if connectErr := resource.Connect(joinContext); connectErr != nil {
			m.removePending(pending)
			_ = pair.close()
			return m.joinFailure(normalizedID, "connect pair", &MeshError{
				Operation:     "connect pair",
				ParticipantID: normalizedID,
				RemoteID:      existingID,
				Cause:         connectErr,
			}, created)
		}
		m.removePending(pending)
		created = append(created, pair)
	}
	if err := joinContext.Err(); err != nil {
		return m.joinFailure(normalizedID, "join", err, created)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return m.joinFailure(normalizedID, "join", ErrMeshClosed, created)
	}
	if _, exists := m.participants[normalizedID]; exists {
		m.mu.Unlock()
		return m.joinFailure(normalizedID, "join", ErrMeshDuplicateParticipant, created)
	}
	m.participants[normalizedID] = struct{}{}
	for index, existingID := range existing {
		spec, _ := NewPairSpec(normalizedID, existingID)
		m.pairs[spec] = created[index]
	}
	m.mu.Unlock()
	return nil
}

func (m *Mesh) joinFailure(participantID, operation string, cause error, created []*meshPair) error {
	cleanupErr := closeMeshPairs(created)
	if cleanupErr != nil {
		if meshErr, ok := cause.(*MeshError); ok {
			copyOf := *meshErr
			copyOf.Cause = errors.Join(meshErr.Cause, cleanupErr)
			return &copyOf
		}
		cause = errors.Join(cause, cleanupErr)
	}
	if meshErr, ok := cause.(*MeshError); ok {
		return meshErr
	}
	return &MeshError{Operation: operation, ParticipantID: participantID, Cause: cause}
}

// AddParticipant is an alias for Join for callers using registry vocabulary.
func (m *Mesh) AddParticipant(ctx context.Context, participantID string) error {
	return m.Join(ctx, participantID)
}

// Remove removes one participant and only the pair resources incident to that
// participant. Other pair resources remain connected and discoverable.
func (m *Mesh) Remove(participantID string) error {
	if m == nil {
		return &MeshError{Operation: "remove", ParticipantID: participantID, Cause: ErrMeshClosed}
	}
	normalizedID, err := normalizeMeshParticipantID(participantID)
	if err != nil {
		return &MeshError{Operation: "remove", ParticipantID: participantID, Cause: err}
	}
	m.mutateMu.Lock()
	defer m.mutateMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return &MeshError{Operation: "remove", ParticipantID: normalizedID, Cause: ErrMeshClosed}
	}
	if _, exists := m.participants[normalizedID]; !exists {
		m.mu.Unlock()
		return &MeshError{Operation: "remove", ParticipantID: normalizedID, Cause: ErrMeshUnknownParticipant}
	}
	delete(m.participants, normalizedID)
	incident := make([]*meshPair, 0)
	for spec, pair := range m.pairs {
		if spec.FirstID != normalizedID && spec.SecondID != normalizedID {
			continue
		}
		incident = append(incident, pair)
		delete(m.pairs, spec)
	}
	m.mu.Unlock()
	if err := closeMeshPairs(incident); err != nil {
		return &MeshError{Operation: "remove", ParticipantID: normalizedID, Cause: err}
	}
	return nil
}

// RemoveParticipant is an alias for Remove.
func (m *Mesh) RemoveParticipant(participantID string) error {
	return m.Remove(participantID)
}

// Leave is a lifecycle-oriented alias for Remove.
func (m *Mesh) Leave(participantID string) error {
	return m.Remove(participantID)
}

// Participants returns a sorted snapshot of joined IDs.
func (m *Mesh) Participants() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	ids := make([]string, 0, len(m.participants))
	for id := range m.participants {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	return ids
}

// ParticipantIDs is an alias for Participants.
func (m *Mesh) ParticipantIDs() []string {
	return m.Participants()
}

// Peers returns the local participant's remote-peer map. The returned map is a
// copy and can be safely mutated by the caller.
func (m *Mesh) Peers(participantID string) (map[string]PeerView, error) {
	if m == nil {
		return nil, &MeshError{Operation: "inspect peers", ParticipantID: participantID, Cause: ErrMeshClosed}
	}
	normalizedID, err := normalizeMeshParticipantID(participantID)
	if err != nil {
		return nil, &MeshError{Operation: "inspect peers", ParticipantID: participantID, Cause: err}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, &MeshError{Operation: "inspect peers", ParticipantID: normalizedID, Cause: ErrMeshClosed}
	}
	if _, exists := m.participants[normalizedID]; !exists {
		return nil, &MeshError{Operation: "inspect peers", ParticipantID: normalizedID, Cause: ErrMeshUnknownParticipant}
	}
	peers := make(map[string]PeerView)
	for spec, pair := range m.pairs {
		remoteID := ""
		switch normalizedID {
		case spec.FirstID:
			remoteID = spec.SecondID
		case spec.SecondID:
			remoteID = spec.FirstID
		default:
			continue
		}
		peers[remoteID] = PeerView{LocalID: normalizedID, RemoteID: remoteID, Pair: pair.resource}
	}
	return peers, nil
}

// RemotePeers is an alias for Peers.
func (m *Mesh) RemotePeers(participantID string) (map[string]PeerView, error) {
	return m.Peers(participantID)
}

// Pair returns the resource for one joined unordered pair.
func (m *Mesh) Pair(firstID, secondID string) (PairResource, error) {
	if m == nil {
		return nil, &MeshError{Operation: "inspect pair", ParticipantID: firstID, RemoteID: secondID, Cause: ErrMeshClosed}
	}
	spec, err := NewPairSpec(firstID, secondID)
	if err != nil {
		return nil, &MeshError{Operation: "inspect pair", ParticipantID: firstID, RemoteID: secondID, Cause: err}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, &MeshError{Operation: "inspect pair", ParticipantID: spec.FirstID, RemoteID: spec.SecondID, Cause: ErrMeshClosed}
	}
	pair, exists := m.pairs[spec]
	if !exists {
		return nil, &MeshError{Operation: "inspect pair", ParticipantID: spec.FirstID, RemoteID: spec.SecondID, Cause: ErrMeshPairNotFound}
	}
	return pair.resource, nil
}

// Pairs returns all pair resources in deterministic pair-key order.
func (m *Mesh) Pairs() []PairSnapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	snapshots := make([]PairSnapshot, 0, len(m.pairs))
	for spec, pair := range m.pairs {
		snapshots = append(snapshots, PairSnapshot{Spec: spec, Resource: pair.resource})
	}
	m.mu.RUnlock()
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].Spec.FirstID == snapshots[j].Spec.FirstID {
			return snapshots[i].Spec.SecondID < snapshots[j].Spec.SecondID
		}
		return snapshots[i].Spec.FirstID < snapshots[j].Spec.FirstID
	})
	return snapshots
}

// PairCount returns the number of connected logical unordered pairs.
func (m *Mesh) PairCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.pairs)
}

// Close prevents new membership changes, cancels in-flight pair operations,
// and closes every connected or pending pair. It is safe to call repeatedly.
func (m *Mesh) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		all := make([]*meshPair, 0, len(m.pairs)+len(m.pending))
		seen := make(map[*meshPair]struct{}, len(m.pairs)+len(m.pending))
		for _, pair := range m.pairs {
			if _, exists := seen[pair]; !exists {
				seen[pair] = struct{}{}
				all = append(all, pair)
			}
		}
		for _, pending := range m.pending {
			if pending == nil || pending.pair == nil {
				continue
			}
			if _, exists := seen[pending.pair]; !exists {
				seen[pending.pair] = struct{}{}
				all = append(all, pending.pair)
			}
		}
		m.participants = make(map[string]struct{})
		m.pairs = make(map[PairSpec]*meshPair)
		m.pending = nil
		close(m.done)
		stopParent := m.stopParent
		cancel := m.cancel
		m.mu.Unlock()

		if stopParent != nil {
			stopParent()
		}
		if cancel != nil {
			cancel()
		}
		m.closeErr = closeMeshPairs(all)
	})
	return m.closeErr
}

func (m *Mesh) operationContext(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationContext, cancel := context.WithCancel(m.ctx)
	stop := context.AfterFunc(ctx, cancel)
	return operationContext, func() {
		stop()
		cancel()
	}
}

func (m *Mesh) addPending(pending *pendingPair) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrMeshClosed
	}
	m.pending = append(m.pending, pending)
	return nil
}

func (m *Mesh) removePending(want *pendingPair) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, pending := range m.pending {
		if pending != want {
			continue
		}
		copy(m.pending[index:], m.pending[index+1:])
		m.pending[len(m.pending)-1] = nil
		m.pending = m.pending[:len(m.pending)-1]
		return
	}
}

func closeMeshPairs(pairs []*meshPair) error {
	var closeErr error
	for index := len(pairs) - 1; index >= 0; index-- {
		if pairs[index] == nil {
			continue
		}
		closeErr = errors.Join(closeErr, pairs[index].close())
	}
	return closeErr
}

func normalizeMeshParticipantID(participantID string) (string, error) {
	normalized := strings.TrimSpace(participantID)
	if normalized == "" {
		return "", ErrMeshEmptyParticipantID
	}
	if strings.ContainsAny(normalized, "\x00\r\n\t") {
		return "", ErrMeshInvalidParticipantID
	}
	return normalized, nil
}

func nilPairResource(resource PairResource) bool {
	if resource == nil {
		return true
	}
	value := reflect.ValueOf(resource)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// LoopbackPeerPair is the built-in pair resource. It owns one rtc.Peer and
// both endpoints of one rtc.LoopbackSignaling exchange. The peer uses a
// provider-neutral in-memory data connection after the signaling handshake;
// media tracks remain a separate room-owned seam.
type LoopbackPeerPair struct {
	spec     PairSpec
	peer     *rtc.Peer
	offerer  *rtc.LoopbackEndpoint
	answerer *rtc.LoopbackEndpoint

	connectOnce sync.Once
	connectErr  error
	closeOnce   sync.Once
	closeErr    error
}

var _ PairResource = (*LoopbackPeerPair)(nil)

// NewLoopbackPairFactory returns a factory that creates one local signaling
// and rtc.Peer resource per unordered PairSpec.
func NewLoopbackPairFactory() PairFactory {
	return func(ctx context.Context, spec PairSpec) (PairResource, error) {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		return newLoopbackPeerPair(spec)
	}
}

func newLoopbackPeerPair(spec PairSpec) (*LoopbackPeerPair, error) {
	canonical, err := NewPairSpec(spec.FirstID, spec.SecondID)
	if err != nil {
		return nil, err
	}
	offerer, answerer, err := rtc.NewLoopbackSignalingPair()
	if err != nil {
		return nil, fmt.Errorf("create loopback signaling for %s: %w", canonical, err)
	}
	pair := &LoopbackPeerPair{spec: canonical, offerer: offerer, answerer: answerer}
	pair.peer = rtc.NewPeer(rtc.PeerConfig{
		Dialer: &loopbackPeerDialer{
			offerer:  offerer,
			answerer: answerer,
		},
		Endpoint: "loopback://room/" + canonical.String(),
		Retry:    rtc.RetryPolicy{MaxAttempts: 1},
	})
	return pair, nil
}

func (p *LoopbackPeerPair) Connect(ctx context.Context) error {
	if p == nil || p.peer == nil {
		return ErrMeshNilPairResource
	}
	p.connectOnce.Do(func() {
		p.connectErr = p.peer.Connect(ctx)
	})
	return p.connectErr
}

func (p *LoopbackPeerPair) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		var errs []error
		if p.peer != nil {
			errs = append(errs, p.peer.Close())
		}
		if p.offerer != nil {
			errs = append(errs, p.offerer.Close())
		}
		if p.answerer != nil {
			errs = append(errs, p.answerer.Close())
		}
		p.closeErr = errors.Join(errs...)
	})
	return p.closeErr
}

// Spec returns this pair's canonical identity.
func (p *LoopbackPeerPair) Spec() PairSpec {
	if p == nil {
		return PairSpec{}
	}
	return p.spec
}

// Peer returns the owned provider-neutral rtc.Peer.
func (p *LoopbackPeerPair) Peer() *rtc.Peer {
	if p == nil {
		return nil
	}
	return p.peer
}

// State returns the underlying rtc.Peer state.
func (p *LoopbackPeerPair) State() rtc.State {
	if p == nil || p.peer == nil {
		return rtc.StateClosed
	}
	return p.peer.State()
}

// Signaling returns the offerer and answerer endpoints owned by this pair.
func (p *LoopbackPeerPair) Signaling() (rtc.Signaling, rtc.Signaling) {
	if p == nil {
		return nil, nil
	}
	return p.offerer, p.answerer
}

type loopbackPeerDialer struct {
	offerer  *rtc.LoopbackEndpoint
	answerer *rtc.LoopbackEndpoint
}

func (d *loopbackPeerDialer) DialContext(ctx context.Context, _ string, _ map[string]string) (rtc.Conn, error) {
	if d == nil || d.offerer == nil || d.answerer == nil {
		return nil, ErrMeshPairFactoryUnavailable
	}
	if err := completeLoopbackExchange(ctx, d.offerer, d.answerer); err != nil {
		return nil, err
	}
	return &loopbackConn{}, nil
}

func completeLoopbackExchange(ctx context.Context, offerer, answerer *rtc.LoopbackEndpoint) error {
	offer := rtc.SessionDescription{Type: "offer", SDP: loopbackSDP("offer")}
	answer := rtc.SessionDescription{Type: "answer", SDP: loopbackSDP("answer")}
	if err := offerer.SendOffer(ctx, offer); err != nil {
		return fmt.Errorf("send offer: %w", err)
	}
	if err := offerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "offer-candidate"}); err != nil {
		return fmt.Errorf("send offer candidate: %w", err)
	}
	if err := offerer.CompleteCandidateGathering(ctx); err != nil {
		return fmt.Errorf("complete offer gathering: %w", err)
	}
	if _, err := answerer.ReceiveOffer(ctx); err != nil {
		return fmt.Errorf("receive offer: %w", err)
	}
	if _, err := answerer.ReceiveCandidate(ctx); err != nil {
		return fmt.Errorf("receive offer candidate: %w", err)
	}
	if _, err := answerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		return fmt.Errorf("finish offer candidates: %w", err)
	}
	if err := answerer.SendAnswer(ctx, answer); err != nil {
		return fmt.Errorf("send answer: %w", err)
	}
	if err := answerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "answer-candidate"}); err != nil {
		return fmt.Errorf("send answer candidate: %w", err)
	}
	if err := answerer.CompleteCandidateGathering(ctx); err != nil {
		return fmt.Errorf("complete answer gathering: %w", err)
	}
	if _, err := offerer.ReceiveAnswer(ctx); err != nil {
		return fmt.Errorf("receive answer: %w", err)
	}
	if _, err := offerer.ReceiveCandidate(ctx); err != nil {
		return fmt.Errorf("receive answer candidate: %w", err)
	}
	if _, err := offerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		return fmt.Errorf("finish answer candidates: %w", err)
	}
	if err := offerer.WaitCandidateGathering(ctx); err != nil {
		return fmt.Errorf("wait offer gathering: %w", err)
	}
	if err := answerer.WaitCandidateGathering(ctx); err != nil {
		return fmt.Errorf("wait answer gathering: %w", err)
	}
	return nil
}

func loopbackSDP(name string) string {
	return "v=0\r\no=- " + name + " 1 IN IP4 127.0.0.1\r\ns=" + name + "\r\nt=0 0"
}

type loopbackConn struct {
	mu     sync.Mutex
	closed bool
}

func (c *loopbackConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, ErrLoopbackConnectionClosed
	}
	return 0, nil, io.EOF
}

func (c *loopbackConn) WriteMessage(int, []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrLoopbackConnectionClosed
	}
	return nil
}

func (c *loopbackConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

var ErrLoopbackConnectionClosed = errors.New("loopback pair connection is closed")
