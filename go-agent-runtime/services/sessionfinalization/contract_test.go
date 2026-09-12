package sessionfinalization

import (
	"errors"
	"testing"
)

type contractCloser struct{ calls int }

func (c *contractCloser) Close() error { c.calls++; return nil }

func TestContractHelpersAndDefaults(t *testing.T) {
	c := &contractCloser{}
	if cleanup := OptionalCloser(c); cleanup == nil {
		t.Fatal("closable value produced no cleanup")
	} else if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 {
		t.Fatalf("closer calls = %d, want one", c.calls)
	}
	if OptionalCloser(nil) != nil || OptionalCloser(struct{}{}) != nil {
		t.Fatal("non-closable values produced cleanup")
	}
	if err := Invoke(nil); err != nil {
		t.Fatalf("nil invoke = %v", err)
	}
	if err := Invoke(func() error { panic("contract panic") }); !errors.Is(err, ErrFinalizationPanic) {
		t.Fatalf("invoke panic = %v", err)
	}
	if got := DefaultDrainPolicy(); got.QuietPeriod != DefaultStragglerDrainQuietPeriod || got.WallSafety != DefaultStragglerDrainWallSafety {
		t.Fatalf("default policy = %#v", got)
	}
	first, second := DefaultSIGINTCancellationErrors(), DefaultSIGINTCancellationErrors()
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("default cancellation errors = %v/%v", first, second)
	}
	first[0] = errors.New("mutated")
	if errors.Is(second[0], first[0]) {
		t.Fatal("default cancellation errors share slice storage")
	}
}
