package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeRoomsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// These aliases preserve the CLI room package while runtime owns the public
// contract and private membership lifecycle.
var (
	ErrMeshClosed                 error = runtimeRooms.ErrMeshClosed
	ErrMeshEmptyParticipantID     error = runtimeRooms.ErrMeshEmptyParticipantID
	ErrMeshInvalidParticipantID   error = runtimeRooms.ErrMeshInvalidParticipantID
	ErrMeshDuplicateParticipant   error = runtimeRooms.ErrMeshDuplicateParticipant
	ErrMeshUnknownParticipant     error = runtimeRooms.ErrMeshUnknownParticipant
	ErrMeshPairNotFound           error = runtimeRooms.ErrMeshPairNotFound
	ErrMeshInvalidPair            error = runtimeRooms.ErrMeshInvalidPair
	ErrMeshNilPairResource        error = runtimeRooms.ErrMeshNilPairResource
	ErrMeshPairFactoryUnavailable error = runtimeRooms.ErrMeshPairFactoryUnavailable
)

type MeshError = runtimeRooms.MeshError
type PairSpec = runtimeRooms.PairSpec
type PairKey = runtimeRooms.PairKey
type PairResource = runtimeRooms.PairResource
type PairFactory = runtimeRooms.PairFactory
type MeshConfig = runtimeRooms.MeshConfig
type PeerView = runtimeRooms.PeerView
type PairSnapshot = runtimeRooms.PairSnapshot

func NewPairSpec(firstID, secondID string) (PairSpec, error) {
	return runtimeRoomsWire.NewPairSpec(firstID, secondID)
}

// Mesh keeps the pre-existing CLI pointer shape while delegating all state to
// the pair-neutral runtime lifecycle service.
type Mesh struct{ delegate runtimeRooms.Mesh }

func NewMesh(config ...MeshConfig) *Mesh {
	cfg := MeshConfig{}
	if len(config) > 0 {
		cfg = config[0]
	}
	if cfg.Context == nil {
		cfg.Context = context.Background()
	}
	if cfg.PairFactory == nil {
		cfg.PairFactory = NewLoopbackPairFactory()
	}
	return &Mesh{delegate: runtimeRoomsWire.NewMesh(cfg)}
}

func NewParticipantMesh(ctx context.Context, factory PairFactory) *Mesh {
	return NewMesh(MeshConfig{Context: ctx, PairFactory: factory})
}

func NewLoopbackMesh(ctx context.Context) *Mesh {
	return NewParticipantMesh(ctx, NewLoopbackPairFactory())
}

func (m *Mesh) Context() context.Context {
	if m == nil || m.delegate == nil {
		return context.Background()
	}
	return m.delegate.Context()
}

func (m *Mesh) Done() <-chan struct{} {
	if m == nil || m.delegate == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return m.delegate.Done()
}

func (m *Mesh) Join(ctx context.Context, id string) error {
	if m == nil || m.delegate == nil {
		return &MeshError{Operation: "join", ParticipantID: id, Cause: ErrMeshClosed}
	}
	return m.delegate.Join(ctx, id)
}

func (m *Mesh) AddParticipant(ctx context.Context, id string) error { return m.Join(ctx, id) }

func (m *Mesh) Remove(id string) error {
	if m == nil || m.delegate == nil {
		return &MeshError{Operation: "remove", ParticipantID: id, Cause: ErrMeshClosed}
	}
	return m.delegate.Remove(id)
}

func (m *Mesh) RemoveParticipant(id string) error { return m.Remove(id) }
func (m *Mesh) Leave(id string) error             { return m.Remove(id) }

func (m *Mesh) Participants() []string {
	if m == nil || m.delegate == nil {
		return nil
	}
	return m.delegate.Participants()
}

func (m *Mesh) ParticipantIDs() []string { return m.Participants() }

func (m *Mesh) Peers(id string) (map[string]PeerView, error) {
	if m == nil || m.delegate == nil {
		return nil, &MeshError{Operation: "inspect peers", ParticipantID: id, Cause: ErrMeshClosed}
	}
	return m.delegate.Peers(id)
}

func (m *Mesh) RemotePeers(id string) (map[string]PeerView, error) { return m.Peers(id) }

func (m *Mesh) Pair(firstID, secondID string) (PairResource, error) {
	if m == nil || m.delegate == nil {
		return nil, &MeshError{Operation: "inspect pair", ParticipantID: firstID, RemoteID: secondID, Cause: ErrMeshClosed}
	}
	return m.delegate.Pair(firstID, secondID)
}

func (m *Mesh) Pairs() []PairSnapshot {
	if m == nil || m.delegate == nil {
		return nil
	}
	return m.delegate.Pairs()
}

func (m *Mesh) PairCount() int {
	if m == nil || m.delegate == nil {
		return 0
	}
	return m.delegate.PairCount()
}

func (m *Mesh) Close() error {
	if m == nil || m.delegate == nil {
		return nil
	}
	return m.delegate.Close()
}

// LoopbackPeerPair is the CLI's provider-neutral RTC compatibility resource.
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
		Dialer:   &loopbackPeerDialer{offerer: offerer, answerer: answerer},
		Endpoint: "loopback://room/" + canonical.String(), Retry: rtc.RetryPolicy{MaxAttempts: 1},
	})
	return pair, nil
}

func (p *LoopbackPeerPair) Connect(ctx context.Context) error {
	if p == nil || p.peer == nil {
		return ErrMeshNilPairResource
	}
	p.connectOnce.Do(func() { p.connectErr = p.peer.Connect(ctx) })
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

func (p *LoopbackPeerPair) Spec() PairSpec {
	if p == nil {
		return PairSpec{}
	}
	return p.spec
}

func (p *LoopbackPeerPair) Peer() *rtc.Peer {
	if p == nil {
		return nil
	}
	return p.peer
}

func (p *LoopbackPeerPair) State() rtc.State {
	if p == nil || p.peer == nil {
		return rtc.StateClosed
	}
	return p.peer.State()
}

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

type loopbackError string

func (e loopbackError) Error() string { return string(e) }

var ErrLoopbackConnectionClosed error = loopbackError("loopback pair connection is closed")
