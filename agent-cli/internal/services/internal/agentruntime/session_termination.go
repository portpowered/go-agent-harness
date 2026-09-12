package agentruntime

import (
	"context"
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
	sfw "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization/wire"
	"sync"
	"time"
)

const sessionStragglerDrainQuietPeriod, sessionStragglerDrainWallSafety, errInvalidSessionStragglerDrainPolicy, errMissingSessionStragglerDrain = sf.DefaultStragglerDrainQuietPeriod, sf.DefaultStragglerDrainWallSafety, sf.ErrInvalidStragglerDrainPolicy, sf.ErrMissingStragglerDrain

type sessionStragglerDrainPolicy struct{ quietPeriod time.Duration }

var defaultSessionStragglerDrainPolicy = sessionStragglerDrainPolicy{quietPeriod: sessionStragglerDrainQuietPeriod}

type sessionTerminationBoundary struct {
	ctx                                                context.Context
	quiesceUpstream, stopOwnedResources, flushBuffered func() error
	waitForStragglers                                  func(sessionStragglerDrainPolicy) error
	once                                               sync.Once
	delegate                                           sf.TerminationBoundary
}

func (b *sessionTerminationBoundary) terminate(primary error) error {
	b.once.Do(func() {
		req := sf.TerminationRequest{QuiesceUpstream: b.quiesceUpstream, StopOwnedResources: b.stopOwnedResources, FlushBuffered: b.flushBuffered}
		req.WaitForStragglers = sfw.AdaptDrain(b.waitForStragglers, func(p sf.DrainPolicy) sessionStragglerDrainPolicy {
			return sessionStragglerDrainPolicy{quietPeriod: p.QuietPeriod}
		})
		b.delegate = sfw.NewService().NewTerminationBoundary(b.ctx, req)
	})
	return b.delegate.Terminate(primary)
}
