package clock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

func TestTimerDomainContextsRejectNilParent(t *testing.T) {
	source := NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	var parent context.Context

	ctx, cancel, err := WithDeadline(parent, source, source.Now().Add(time.Second))
	if !errors.Is(err, contract.ErrNilContext) || ctx != nil || cancel != nil {
		t.Fatalf("WithDeadline(nil) = (%v, cancel=%t, %v), want ErrNilContext", ctx, cancel != nil, err)
	}
	ctx, cancel, err = WithTimeout(parent, source, time.Second)
	if !errors.Is(err, contract.ErrNilContext) || ctx != nil || cancel != nil {
		t.Fatalf("WithTimeout(nil) = (%v, cancel=%t, %v), want ErrNilContext", ctx, cancel != nil, err)
	}
}
