package room

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestMeshJoinCreatesOnePairPerUnorderedParticipantPair(t *testing.T) {
	var (
		mu    sync.Mutex
		specs []PairSpec
	)
	loopbackFactory := NewLoopbackPairFactory()
	factory := func(ctx context.Context, spec PairSpec) (PairResource, error) {
		mu.Lock()
		specs = append(specs, spec)
		mu.Unlock()
		return loopbackFactory(ctx, spec)
	}
	mesh := NewMesh(MeshConfig{PairFactory: factory})
	defer func() { _ = mesh.Close() }()

	for _, id := range []string{"zeta", "alpha", "beta"} {
		if err := mesh.Join(context.Background(), id); err != nil {
			t.Fatalf("Join(%q): %v", id, err)
		}
	}

	if got := mesh.PairCount(); got != 3 {
		t.Fatalf("PairCount = %d, want 3", got)
	}
	mu.Lock()
	gotSpecs := append([]PairSpec(nil), specs...)
	mu.Unlock()
	if len(gotSpecs) != 3 {
		t.Fatalf("pair factory calls = %d, want 3", len(gotSpecs))
	}
	wantPairs := []PairSpec{{FirstID: "alpha", SecondID: "zeta"}, {FirstID: "alpha", SecondID: "beta"}, {FirstID: "beta", SecondID: "zeta"}}
	for index, want := range wantPairs {
		if got := gotSpecs[index]; got != want {
			t.Fatalf("pair factory spec %d = %#v, want %#v", index, got, want)
		}
	}

	for _, test := range []struct {
		id      string
		remotes []string
	}{
		{id: "alpha", remotes: []string{"beta", "zeta"}},
		{id: "beta", remotes: []string{"alpha", "zeta"}},
		{id: "zeta", remotes: []string{"alpha", "beta"}},
	} {
		peers, err := mesh.Peers(test.id)
		if err != nil {
			t.Fatalf("Peers(%q): %v", test.id, err)
		}
		got := make([]string, 0, len(peers))
		for remoteID, view := range peers {
			if remoteID == test.id || view.LocalID != test.id || view.RemoteID != remoteID || view.Pair == nil {
				t.Fatalf("peer view for %q/%q = %#v", test.id, remoteID, view)
			}
			got = append(got, remoteID)
		}
		sortStrings(got)
		if !equalStrings(got, test.remotes) {
			t.Fatalf("%s remote IDs = %#v, want %#v", test.id, got, test.remotes)
		}
	}
	for _, ids := range [][2]string{{"alpha", "beta"}, {"alpha", "zeta"}, {"beta", "zeta"}} {
		resource, err := mesh.Pair(ids[0], ids[1])
		if err != nil {
			t.Fatalf("Pair(%q, %q): %v", ids[0], ids[1], err)
		}
		loopback, ok := resource.(*LoopbackPeerPair)
		if !ok {
			t.Fatalf("Pair(%q, %q) type = %T, want *LoopbackPeerPair", ids[0], ids[1], resource)
		}
		if loopback.State() != rtc.StateConnected {
			t.Fatalf("pair %q/%q state = %s, want connected", ids[0], ids[1], loopback.State())
		}
	}
}

func TestLoopbackPairClosesPeerAndBothSignalingEndpoints(t *testing.T) {
	resource, err := newLoopbackPeerPair(PairSpec{FirstID: "alpha", SecondID: "beta"})
	if err != nil {
		t.Fatalf("newLoopbackPeerPair: %v", err)
	}
	if err := resource.Connect(context.Background()); err != nil {
		t.Fatalf("LoopbackPeerPair.Connect: %v", err)
	}
	if resource.State() != rtc.StateConnected {
		t.Fatalf("loopback peer state = %s, want connected", resource.State())
	}
	offerer, answerer := resource.Signaling()
	// The loopback signaling exchange closes Done after both sides finish ICE
	// gathering; PairResource.Close still owns and closes both endpoints.
	awaitClosed(t, offerer.Done())
	awaitClosed(t, answerer.Done())
	if err := resource.Close(); err != nil {
		t.Fatalf("LoopbackPeerPair.Close: %v", err)
	}
	if resource.State() != rtc.StateClosed {
		t.Fatalf("closed loopback peer state = %s, want closed", resource.State())
	}
	awaitClosed(t, offerer.Done())
	awaitClosed(t, answerer.Done())
	if err := resource.Close(); err != nil {
		t.Fatalf("repeated LoopbackPeerPair.Close: %v", err)
	}
}

func TestMeshRejectsDuplicateAndUnknownMembershipOperations(t *testing.T) {
	mesh := NewMesh()
	defer func() { _ = mesh.Close() }()
	if err := mesh.Join(context.Background(), "alpha"); err != nil {
		t.Fatalf("first Join: %v", err)
	}
	if err := mesh.Join(context.Background(), " alpha "); !errors.Is(err, ErrMeshDuplicateParticipant) {
		t.Fatalf("duplicate Join error = %v, want ErrMeshDuplicateParticipant", err)
	} else if !containsErrorText(err, "alpha") {
		t.Fatalf("duplicate Join error = %v, want participant ID", err)
	}
	if err := mesh.RemoveParticipant("missing"); !errors.Is(err, ErrMeshUnknownParticipant) {
		t.Fatalf("unknown RemoveParticipant error = %v, want ErrMeshUnknownParticipant", err)
	} else if !containsErrorText(err, "missing") {
		t.Fatalf("unknown removal error = %v, want participant ID", err)
	}
	if got := mesh.Participants(); !equalStrings(got, []string{"alpha"}) {
		t.Fatalf("membership after rejected operations = %#v", got)
	}
}

func TestMeshRemovalClosesOnlyRemovedPairAndLeavesSurvivors(t *testing.T) {
	resources := make(map[PairSpec]*countingPair)
	var resourcesMu sync.Mutex
	factory := func(_ context.Context, spec PairSpec) (PairResource, error) {
		resource := &countingPair{spec: spec}
		resourcesMu.Lock()
		resources[spec] = resource
		resourcesMu.Unlock()
		return resource, nil
	}
	mesh := NewMesh(MeshConfig{PairFactory: factory})
	defer func() { _ = mesh.Close() }()
	for _, id := range []string{"a", "b", "c"} {
		if err := mesh.Join(context.Background(), id); err != nil {
			t.Fatalf("Join(%q): %v", id, err)
		}
	}
	_, err := mesh.Pair("a", "b")
	if err != nil {
		t.Fatalf("Pair(a,b): %v", err)
	}
	ac, err := mesh.Pair("a", "c")
	if err != nil {
		t.Fatalf("Pair(a,c): %v", err)
	}
	if err := mesh.Remove("b"); err != nil {
		t.Fatalf("Remove(b): %v", err)
	}
	if got := mesh.PairCount(); got != 1 {
		t.Fatalf("PairCount after removal = %d, want 1", got)
	}
	if _, err := mesh.Pair("a", "b"); !errors.Is(err, ErrMeshPairNotFound) {
		t.Fatalf("removed Pair(a,b) error = %v, want ErrMeshPairNotFound", err)
	}
	peers, err := mesh.Peers("a")
	if err != nil {
		t.Fatalf("Peers(a) after removal: %v", err)
	}
	if len(peers) != 1 || peers["c"].Pair != ac {
		t.Fatalf("a peers after b removal = %#v, want only surviving a/c pair", peers)
	}
	peers, err = mesh.Peers("c")
	if err != nil {
		t.Fatalf("Peers(c) after removal: %v", err)
	}
	if len(peers) != 1 || peers["a"].Pair != ac {
		t.Fatalf("c peers after b removal = %#v, want only surviving c/a pair", peers)
	}
	if got := resources[PairSpec{FirstID: "a", SecondID: "b"}].closeCount.Load(); got != 1 {
		t.Fatalf("removed a/b close count = %d, want 1", got)
	}
	if got := resources[PairSpec{FirstID: "a", SecondID: "c"}].closeCount.Load(); got != 0 {
		t.Fatalf("surviving a/c close count = %d, want 0", got)
	}
}

func TestMeshContextCancellationClosesAllPairsAndRepeatedCloseIsSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	resources := make([]*countingPair, 0, 3)
	var resourcesMu sync.Mutex
	factory := func(_ context.Context, spec PairSpec) (PairResource, error) {
		resource := &countingPair{spec: spec}
		resourcesMu.Lock()
		resources = append(resources, resource)
		resourcesMu.Unlock()
		return resource, nil
	}
	mesh := NewMesh(MeshConfig{Context: ctx, PairFactory: factory})
	for _, id := range []string{"a", "b", "c"} {
		if err := mesh.Join(context.Background(), id); err != nil {
			t.Fatalf("Join(%q): %v", id, err)
		}
	}
	cancel()
	awaitClosed(t, mesh.Done())
	resourcesMu.Lock()
	gotResources := append([]*countingPair(nil), resources...)
	resourcesMu.Unlock()
	for _, resource := range gotResources {
		if got := resource.closeCount.Load(); got != 1 {
			t.Fatalf("pair %s close count = %d, want 1", resource.spec, got)
		}
	}
	if err := mesh.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := mesh.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}
}

func TestMeshCancellationUnblocksAnInFlightJoinAndClosesPendingPair(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pending := &blockingPair{started: make(chan struct{})}
	factory := func(_ context.Context, _ PairSpec) (PairResource, error) { return pending, nil }
	mesh := NewMesh(MeshConfig{Context: ctx, PairFactory: factory})
	defer func() { _ = mesh.Close() }()
	if err := mesh.Join(context.Background(), "first"); err != nil {
		t.Fatalf("first Join: %v", err)
	}

	joinErr := make(chan error, 1)
	go func() { joinErr <- mesh.Join(context.Background(), "second") }()
	awaitClosed(t, pending.started)
	cancel()
	select {
	case err := <-joinErr:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight Join error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight Join did not unblock after mesh cancellation")
	}
	awaitClosed(t, mesh.Done())
	if got := pending.closeCount.Load(); got != 1 {
		t.Fatalf("pending pair close count = %d, want 1", got)
	}
	if got := mesh.Participants(); len(got) != 0 {
		t.Fatalf("membership after mesh cancellation = %#v, want empty", got)
	}
}

type countingPair struct {
	spec       PairSpec
	closeCount atomic.Int32
}

func (p *countingPair) Connect(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (p *countingPair) Close() error {
	p.closeCount.Add(1)
	return nil
}

type blockingPair struct {
	started    chan struct{}
	startOnce  sync.Once
	closeCount atomic.Int32
}

func (p *blockingPair) Connect(ctx context.Context) error {
	p.startOnce.Do(func() { close(p.started) })
	<-ctx.Done()
	return ctx.Err()
}

func (p *blockingPair) Close() error {
	p.closeCount.Add(1)
	return nil
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func containsErrorText(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}

func awaitClosed(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal("channel did not close")
	}
}
