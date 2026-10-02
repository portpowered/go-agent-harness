package probe

import "time"

// Snapshot returns the progress observed so far.
func (p *DuplexProgress) Snapshot() DuplexProgressSnapshot {
	if p == nil || p.state == nil {
		return DuplexProgressSnapshot{}
	}
	return p.state.snapshot()
}

// Elapsed returns the runner's monotonic elapsed time at the instant of the
// snapshot. Segment gates use this to drive event-based policies while the
// child remains open.
func (p *DuplexProgress) Elapsed() time.Duration {
	if p == nil || p.state == nil {
		return 0
	}
	return p.state.elapsed()
}
