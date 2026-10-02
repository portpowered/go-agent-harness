package livesession

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Bounds of the transcript ring a delegation carries.
const (
	// transcriptRingSize is how many transcript spans the ring keeps.
	transcriptRingSize = 32
	// maxSpanText caps one span's text; a longer run starts a new span.
	maxSpanText = 2048
)

// pendingDelegation is a client delegation waiting for the user transcript
// that covers it, or for the settle window to pass.
type pendingDelegation struct {
	id       string
	offsetMS int64
	task     string
	deadline time.Time
}

// delegationTracker turns a provider's client delegation into
// DELEGATION.CREATED. The public event carries no task text, and it may arrive before the user's request
// is transcribed, so the tracker keeps a bounded ring of recent transcript
// spans and reports a delegation once a user transcript fragment ending at or
// after its offset has arrived, or once the settle window has passed on the
// session clock, whichever comes first. Each delegation is reported exactly
// once, with no ResponseID, so it never touches the open speech segment.
//
// It does no I/O and is not safe for concurrent use; the session serializes
// it with the segmenter.
type delegationTracker struct {
	settle time.Duration
	// gap joins consecutive fragments of one speaker into one span.
	gap time.Duration

	ring []messages.TranscriptFragment
	// inputEndMS is the latest end of a user transcript fragment; hasInput
	// says whether one arrived.
	inputEndMS int64
	hasInput   bool

	pending []pendingDelegation
}

func newDelegationTracker(settle, gap time.Duration) *delegationTracker {
	return &delegationTracker{settle: settle, gap: gap}
}

// created maps one client delegation: reported at once when the user
// transcript already covers its offset, otherwise held until it does or the
// settle window passes.
func (d *delegationTracker) created(now time.Time, id string, offsetMS int64, task string) []messages.StreamMessage {
	if d.covered(offsetMS) {
		return []messages.StreamMessage{d.report(pendingDelegation{id: id, offsetMS: offsetMS, task: task})}
	}
	d.pending = append(d.pending, pendingDelegation{id: id, offsetMS: offsetMS, task: task, deadline: now.Add(d.settle)})
	return nil
}

// transcript records one transcript fragment. A user fragment may release
// held delegations whose offset it covers.
func (d *delegationTracker) transcript(speaker messages.Role, fragment Transcript) []messages.StreamMessage {
	d.remember(speaker, fragment)
	if speaker != messages.RoleUser {
		return nil
	}
	if !d.hasInput || fragment.EndMS > d.inputEndMS {
		d.inputEndMS, d.hasInput = fragment.EndMS, true
	}
	return d.release(func(p pendingDelegation) bool { return d.covered(p.offsetMS) })
}

// due reports the held delegations whose settle window has passed by now.
func (d *delegationTracker) due(now time.Time) []messages.StreamMessage {
	return d.release(func(p pendingDelegation) bool { return !now.Before(p.deadline) })
}

// nextDeadline is the earliest settle deadline, if a delegation is held.
// Every delegation waits the same settle window and pending keeps arrival
// order, so the first one held falls due first.
func (d *delegationTracker) nextDeadline() (time.Time, bool) {
	if len(d.pending) == 0 {
		return time.Time{}, false
	}
	return d.pending[0].deadline, true
}

// finish reports every held delegation at the end of the session, so none
// is lost to a close inside its settle window.
func (d *delegationTracker) finish() []messages.StreamMessage {
	return d.release(func(pendingDelegation) bool { return true })
}

func (d *delegationTracker) covered(offsetMS int64) bool {
	return d.hasInput && d.inputEndMS >= offsetMS
}

// release reports, in arrival order, the held delegations ready accepts.
func (d *delegationTracker) release(ready func(pendingDelegation) bool) []messages.StreamMessage {
	var out []messages.StreamMessage
	kept := d.pending[:0]
	for _, p := range d.pending {
		if ready(p) {
			out = append(out, d.report(p))
			continue
		}
		kept = append(kept, p)
	}
	clear(d.pending[len(kept):])
	d.pending = kept
	return out
}

func (d *delegationTracker) report(p pendingDelegation) messages.StreamMessage {
	var transcript []messages.TranscriptFragment
	if len(d.ring) > 0 {
		transcript = append([]messages.TranscriptFragment(nil), d.ring...)
	}
	value := messages.NewDelegationCreatedValue(p.id, messages.DelegationTargetClient, p.offsetMS, transcript)
	value.Task = p.task
	return messages.StreamMessage{Type: messages.StreamTypeDelegationCreated, Value: value}
}

// remember adds fragment to the ring, joining it to the last span when the
// same speaker continues within gap on the server timeline.
func (d *delegationTracker) remember(speaker messages.Role, fragment Transcript) {
	if fragment.Delta == "" {
		return
	}
	if n := len(d.ring); n > 0 {
		last := &d.ring[n-1]
		if last.Speaker == speaker && fragment.StartMS-last.EndMS < d.gap.Milliseconds() && len(last.Text)+len(fragment.Delta) <= maxSpanText {
			last.Text += fragment.Delta
			last.EndMS = max(last.EndMS, fragment.EndMS)
			return
		}
	}
	if len(d.ring) == transcriptRingSize {
		copy(d.ring, d.ring[1:])
		d.ring = d.ring[:transcriptRingSize-1]
	}
	d.ring = append(d.ring, messages.TranscriptFragment{Speaker: speaker, Text: fragment.Delta, StartMS: fragment.StartMS, EndMS: fragment.EndMS})
}
