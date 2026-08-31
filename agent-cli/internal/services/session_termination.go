package services

import (
	"errors"
	"time"
)

// sessionStragglerDrainQuietPeriod is the bounded quiet period used before a
// session is stopped. Provider output and terminal signals travel through
// independent paths, so the terminal signal can overtake output the provider
// has already accepted. A terminal signal must never skip the straggler drain.
const sessionStragglerDrainQuietPeriod = 25 * time.Millisecond

// sessionTerminationBoundary is the one terminal shutdown boundary shared by
// the live and duration session loops. Its callbacks are loop-owned adapters:
// they retain the live renderer or duration artifact/terminal state while this
// boundary owns the ordering of terminal cleanup.
//
// Every terminal signal follows this order, even when the initiating path has
// an error: quiesce external producers, wait for stragglers during the bounded
// quiet period, cancel and stop owned resources, then flush messages already
// buffered after the stop. Quiescing is separate from stopping the session so
// room-owned mixer producers cannot create new outbound transport events while
// the session's provider output is being drained.
type sessionTerminationBoundary struct {
	quiesceUpstream    func() error
	waitForStragglers  func(time.Duration) error
	stopOwnedResources func() error
	flushBuffered      func() error
}

// terminate applies the shared terminal-drain contract and joins cleanup
// failures with the initiating error without masking either one.
func (b sessionTerminationBoundary) terminate(primary error) error {
	var quiesceErr, waitErr, stopErr, flushErr error
	if b.quiesceUpstream != nil {
		quiesceErr = b.quiesceUpstream()
	}
	if b.waitForStragglers != nil {
		waitErr = b.waitForStragglers(sessionStragglerDrainQuietPeriod)
	}
	if b.stopOwnedResources != nil {
		stopErr = b.stopOwnedResources()
	}
	if b.flushBuffered != nil {
		flushErr = b.flushBuffered()
	}
	return errors.Join(primary, quiesceErr, waitErr, stopErr, flushErr)
}
