package chrome

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/chromedp/cdproto/emulation"
)

func (s *targetSession) installPageScript(ctx context.Context, source string) error {
	if source == "" {
		return errors.New("site adapter source is empty")
	}
	return s.run(ctx, pageScriptAction(source))
}

type focusExecutor struct {
	mu             sync.Mutex
	enabled        []bool
	failNextEnable bool
}

func (e *focusExecutor) Execute(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if method != emulation.CommandSetFocusEmulationEnabled {
		return errors.New("unexpected CDP method")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	focus, ok := params.(*emulation.SetFocusEmulationEnabledParams)
	if !ok {
		return errors.New("unexpected focus parameters")
	}
	e.enabled = append(e.enabled, focus.Enabled)
	if e.failNextEnable && focus.Enabled {
		e.failNextEnable = false
		return errors.New("focus response lost after enabling")
	}
	return nil
}

func TestTargetFocusFailedAcquisitionCleanupPreservesLaterLease(t *testing.T) {
	e := &focusExecutor{failNextEnable: true}
	s := newInvocationTestSession(t, e)
	failedCleanup, err := s.AcquirePageFocus(context.Background())
	if err == nil || failedCleanup == nil {
		t.Fatal("expected failed acquisition with cleanup")
	}
	release, err := s.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := failedCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, true}) {
		t.Fatalf("failed cleanup disabled active lease: %v", e.enabled)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := failedCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, true, false}) {
		t.Fatalf("calls=%v", e.enabled)
	}
}

func TestTargetFocusFailedAcquisitionCleanupRestoresOnce(t *testing.T) {
	e := &focusExecutor{failNextEnable: true}
	s := newInvocationTestSession(t, e)
	cleanup, err := s.AcquirePageFocus(context.Background())
	if err == nil || cleanup == nil {
		t.Fatal("expected failed acquisition with cleanup")
	}
	for range 2 {
		if err := cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, false}) {
		t.Fatalf("calls=%v", e.enabled)
	}
}
func TestTargetFocusLeaseRestoresOnceAfterOperationCancellation(t *testing.T) {
	e := &focusExecutor{}
	s := newInvocationTestSession(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	release, err := s.AcquirePageFocus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, false}) {
		t.Fatalf("calls=%v", e.enabled)
	}
}
func TestTargetFocusOverlappingLeasesRestoreAfterLastRelease(t *testing.T) {
	e := &focusExecutor{}
	s := newInvocationTestSession(t, e)
	first, err := s.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := first(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true}) {
		t.Fatalf("premature release=%v", e.enabled)
	}
	if err := second(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, false}) {
		t.Fatalf("calls=%v", e.enabled)
	}
}
