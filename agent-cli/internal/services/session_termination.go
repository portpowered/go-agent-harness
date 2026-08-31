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
// an error: wait for stragglers during the bounded quiet period, cancel and
// stop owned resources, then flush messages already buffered after the stop.
// Waiting first is essential because cancellation makes provider messages that
// have not reached the consumer-facing delta buffer unrecoverable.
type sessionTerminationBoundary struct {
	waitForStragglers  func(time.Duration) error
	stopOwnedResources func() error
	flushBuffered      func() error
}

// terminate applies the shared terminal-drain contract and joins cleanup
// failures with the initiating error without masking either one.
func (b sessionTerminationBoundary) terminate(primary error) error {
	var waitErr, stopErr, flushErr error
	if b.waitForStragglers != nil {
		waitErr = b.waitForStragglers(sessionStragglerDrainQuietPeriod)
	}
	if b.stopOwnedResources != nil {
		stopErr = b.stopOwnedResources()
	}
	if b.flushBuffered != nil {
		flushErr = b.flushBuffered()
	}
	return errors.Join(primary, waitErr, stopErr, flushErr)
}
