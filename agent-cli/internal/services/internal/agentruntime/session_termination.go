package agentruntime
import (
	"context"
	"sync"
	"time"
	sessionfinalization "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
	sessionfinalizationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization/wire"
)
const sessionStragglerDrainQuietPeriod = sessionfinalization.DefaultStragglerDrainQuietPeriod
const sessionStragglerDrainWallSafety = sessionfinalization.DefaultStragglerDrainWallSafety
type sessionStragglerDrainPolicy struct{ quietPeriod time.Duration }
func defaultSessionStragglerDrainPolicy() sessionStragglerDrainPolicy { return sessionStragglerDrainPolicy{quietPeriod: sessionStragglerDrainQuietPeriod} }
const errInvalidSessionStragglerDrainPolicy = sessionfinalization.ErrInvalidStragglerDrainPolicy
const errMissingSessionStragglerDrain = sessionfinalization.ErrMissingStragglerDrain
type sessionTerminationBoundary struct {
	ctx context.Context
	quiesceUpstream, stopOwnedResources, flushBuffered func() error
	waitForStragglers func(sessionStragglerDrainPolicy) error
	once sync.Once
	delegate sessionfinalization.TerminationBoundary
}
func (b *sessionTerminationBoundary) terminate(primary error) error {
	b.once.Do(func() {
		req := sessionfinalization.TerminationRequest{QuiesceUpstream: b.quiesceUpstream, StopOwnedResources: b.stopOwnedResources, FlushBuffered: b.flushBuffered}
		req.WaitForStragglers = sessionfinalization.AdaptDrain(b.waitForStragglers, func(p sessionfinalization.DrainPolicy) sessionStragglerDrainPolicy { return sessionStragglerDrainPolicy{quietPeriod: p.QuietPeriod} })
		b.delegate = sessionfinalizationwire.NewService().NewTerminationBoundary(b.ctx, req)
	})
	return b.delegate.Terminate(primary)
}
