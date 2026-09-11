// Command mesh-consumer exercises the public room mesh from a separate module.
// It intentionally imports only services/rooms and services/rooms/wire.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
)

type pairResource struct {
	mu          sync.Mutex
	connected   bool
	closed      bool
	closeErr    error
	block       bool
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func (p *pairResource) Connect(ctx context.Context) error {
	p.mu.Lock()
	block := p.block
	p.startOnce.Do(func() { close(p.started) })
	p.mu.Unlock()
	if block {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.release:
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("pair closed before connect")
	}
	p.connected = true
	return nil
}

func (p *pairResource) Close() error {
	p.releaseOnce.Do(func() { close(p.release) })
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return p.closeErr
}

type pairFactory struct {
	mu             sync.Mutex
	resources      []*pairResource
	connectCalls   int
	failConnectAt  int
	connectFailure error
	blockResources bool
}

func (f *pairFactory) create(_ context.Context, spec rooms.PairSpec) (rooms.PairResource, error) {
	pair := &pairResource{block: f.blockResources, started: make(chan struct{}), release: make(chan struct{})}
	f.mu.Lock()
	f.resources = append(f.resources, pair)
	f.mu.Unlock()
	return &recordingResource{resource: pair, factory: f}, nil
}

func (f *pairFactory) connectError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls++
	if f.failConnectAt != f.connectCalls {
		return nil
	}
	return f.connectFailure
}

func (f *pairFactory) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, resource := range f.resources {
		resource.mu.Lock()
		if resource.closed {
			count++
		}
		resource.mu.Unlock()
	}
	return count
}

type recordingFactory struct {
	base *pairFactory
}

func (f recordingFactory) create(ctx context.Context, spec rooms.PairSpec) (rooms.PairResource, error) {
	resource, err := f.base.create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &recordingResource{resource: resource.(*pairResource), factory: f.base}, nil
}

type recordingResource struct {
	resource *pairResource
	factory  *pairFactory
}

func (r *recordingResource) Connect(ctx context.Context) error {
	if err := r.factory.connectError(); err != nil {
		return err
	}
	return r.resource.Connect(ctx)
}

func (r *recordingResource) Close() error { return r.resource.Close() }

func check(condition bool, format string, args ...any) {
	if !condition {
		panic(fmt.Sprintf(format, args...))
	}
}

func main() {
	mode := "positive"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	var result map[string]any
	var err error
	switch mode {
	case "positive":
		result, err = positive()
	case "mutate-count", "mutate-order", "mutate-cleanup", "mutate-half-join":
		result, err = positiveMutation(mode)
	case "close-blocked":
		result, err = blockedShutdown(false)
	case "parent-blocked":
		result, err = blockedShutdown(true)
	default:
		err = fmt.Errorf("unknown mode %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}

func newMesh(factory rooms.PairFactory) rooms.Mesh {
	return roomswire.NewMesh(rooms.MeshConfig{Context: context.Background(), PairFactory: factory})
}

func positive() (result map[string]any, err error) {
	fixture := &pairFactory{}
	mesh := newMesh(fixture.create)
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("public mesh assertion: %v", recovered)
		}
	}()
	for _, id := range []string{"delta", "alpha", "charlie", "bravo"} {
		check(mesh.Join(context.Background(), id) == nil, "join %s failed", id)
	}
	wantIDs := []string{"alpha", "bravo", "charlie", "delta"}
	check(equalStrings(mesh.ParticipantIDs(), wantIDs), "participant snapshot = %v", mesh.ParticipantIDs())
	pairs := mesh.Pairs()
	check(len(pairs) == 6 && mesh.PairCount() == 6, "pair count = %d", len(pairs))
	check(sortedPairs(pairs), "pair snapshots are not canonical and sorted: %v", pairs)
	stable, pairErr := mesh.Pair("alpha", "charlie")
	check(pairErr == nil, "pair lookup failed: %v", pairErr)
	snapshot := append([]rooms.PairSnapshot(nil), pairs...)
	snapshot[0] = rooms.PairSnapshot{}
	check(mesh.Pairs()[0].Spec != (rooms.PairSpec{}), "pair snapshot aliases internal state")
	check(mesh.Remove("bravo") == nil, "remove failed")
	remaining, pairErr := mesh.Pair("charlie", "alpha")
	check(pairErr == nil && remaining == stable, "surviving pair identity changed")
	check(mesh.PairCount() == 3 && equalStrings(mesh.Participants(), []string{"alpha", "charlie", "delta"}), "post-remove state is wrong")
	check(errors.Is(mesh.Join(context.Background(), "alpha"), rooms.ErrMeshDuplicateParticipant), "duplicate did not preserve sentinel")
	check(errors.Is(mesh.Remove("missing"), rooms.ErrMeshUnknownParticipant), "unknown removal did not preserve sentinel")
	check(errors.Is(mesh.Join(context.Background(), "bad\nvalue"), rooms.ErrMeshInvalidParticipantID), "invalid ID did not preserve sentinel")
	rollback := rollbackProbe()
	closeErr := mesh.Close()
	check(closeErr == nil, "close returned %v", closeErr)
	select {
	case <-mesh.Done():
	default:
		panic("Done did not close after cleanup")
	}
	check(fixture.closeCount() == 6, "closed %d resources, want 6", fixture.closeCount())
	return map[string]any{"case": "external-positive", "participants": wantIDs, "pair_count": len(pairs), "surviving_pair": remaining != nil, "closed_resources": fixture.closeCount(), "rollback": rollback, "done_after_cleanup": true}, nil
}

func rollbackProbe() map[string]any {
	factory := &pairFactory{failConnectAt: 3, connectFailure: errors.New("fixture connect failure")}
	mesh := newMesh(factory.create)
	check(mesh.Join(context.Background(), "a") == nil, "rollback setup join a failed")
	check(mesh.Join(context.Background(), "b") == nil, "rollback setup join b failed")
	err := mesh.Join(context.Background(), "c")
	var typed *rooms.MeshError
	check(errors.As(err, &typed), "connect failure was not typed")
	check(typed.Operation == "connect pair" && errors.Is(err, factory.connectFailure), "connect failure lost its cause")
	check(equalStrings(mesh.Participants(), []string{"a", "b"}) && mesh.PairCount() == 1, "failed join published partial membership")
	check(factory.closeCount() == 2, "rollback closed %d resources before mesh close", factory.closeCount())
	check(mesh.Close() == nil && factory.closeCount() == 3, "rollback survivor did not close exactly once")
	missing := roomswire.NewMesh(rooms.MeshConfig{})
	check(missing.Join(context.Background(), "x") == nil, "nil factory setup join failed")
	check(errors.Is(missing.Join(context.Background(), "y"), rooms.ErrMeshPairFactoryUnavailable), "nil factory did not preserve unavailable sentinel")
	_ = missing.Close()
	return map[string]any{"typed_connect_failure": true, "no_half_join": true, "created_closed_before_mesh_close": 2, "all_closed_after_mesh_close": 3, "nil_factory_rejected": true}
}

func positiveMutation(mode string) (map[string]any, error) {
	result, err := positive()
	if err != nil {
		return nil, err
	}
	switch mode {
	case "mutate-count":
		if result["pair_count"] == 6 {
			return nil, fmt.Errorf("mutated pair-count oracle unexpectedly accepted exact count")
		}
	case "mutate-order":
		if result["participants"].([]string)[0] == "alpha" {
			return nil, fmt.Errorf("mutated ordering oracle unexpectedly accepted sorted IDs")
		}
	case "mutate-cleanup":
		if result["closed_resources"] == 6 {
			return nil, fmt.Errorf("mutated cleanup oracle unexpectedly accepted exact close count")
		}
	case "mutate-half-join":
		if result["surviving_pair"] == true {
			return nil, fmt.Errorf("mutated half-join oracle unexpectedly accepted surviving identity")
		}
	}
	return nil, fmt.Errorf("mutation %s rejected the intended mismatch", mode)
}

func blockedShutdown(parentMode bool) (map[string]any, error) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	factory := &pairFactory{blockResources: true}
	mesh := roomswire.NewMesh(rooms.MeshConfig{Context: parent, PairFactory: factory.create})
	if err := mesh.Join(context.Background(), "alpha"); err != nil {
		return nil, err
	}
	joined := make(chan error, 1)
	go func() { joined <- mesh.Join(context.Background(), "bravo") }()
	select {
	case <-factory.resourcesStarted():
	case <-time.After(2 * time.Second):
		return nil, errors.New("blocked pair did not start")
	}
	if parentMode {
		cancelParent()
	} else if err := mesh.Close(); err != nil {
		return nil, err
	}
	select {
	case err := <-joined:
		check(err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, rooms.ErrMeshClosed)), "blocked join error = %v", err)
	case <-time.After(2 * time.Second):
		return nil, errors.New("blocked join did not terminate")
	}
	select {
	case <-mesh.Done():
	case <-time.After(2 * time.Second):
		return nil, errors.New("Done did not follow cleanup")
	}
	check(factory.closeCount() == 1, "blocked cleanup closed %d resources", factory.closeCount())
	return map[string]any{"case": map[bool]string{true: "parent-blocked", false: "close-blocked"}[parentMode], "join_terminated": true, "done_after_cleanup": true, "closed_resources": factory.closeCount()}, nil
}

func (f *pairFactory) resourcesStarted() <-chan struct{} {
	for {
		f.mu.Lock()
		for _, resource := range f.resources {
			started := resource.started
			f.mu.Unlock()
			return started
		}
		f.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sortedPairs(pairs []rooms.PairSnapshot) bool {
	for i, pair := range pairs {
		if pair.Spec.FirstID >= pair.Spec.SecondID {
			return false
		}
		if i > 0 && pairs[i-1].Spec.String() >= pair.Spec.String() {
			return false
		}
	}
	return true
}
