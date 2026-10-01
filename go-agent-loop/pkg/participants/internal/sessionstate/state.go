// Package sessionstate holds the session model runner's lifecycle state
// machines and the barge-in onset detector. It has no I/O: the runner feeds
// it provider messages and user audio and acts on the decisions it returns.
//
// Each concern is an explicit state machine whose transitions are small
// methods, so the combinations the barge-in rules rely on (for example
// "cancel sent but response still in flight") are named phases rather than
// coincidences of independent flags.
package sessionstate

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// SessionPhase tracks the provider session itself.
type SessionPhase uint8

const (
	// SessionAwaitingOpen: no SESSION.OPEN or SESSION.CREATED observed yet.
	SessionAwaitingOpen SessionPhase = iota
	// SessionOpen: the first lifecycle event was observed; the initial
	// configuration (if any) has been sent.
	SessionOpen
	// SessionClosed: the provider reported SESSION.CLOSE; no response follows.
	SessionClosed
)

// ObserveOpen reports whether this is the first open of the session.
func (p *SessionPhase) ObserveOpen() bool {
	if *p != SessionAwaitingOpen {
		return false
	}
	*p = SessionOpen
	return true
}

// ResponsePhase is the provider response lifecycle. A response is live from
// MESSAGE.START through its owned MESSAGE.END.
type ResponsePhase uint8

const (
	ResponseIdle        ResponsePhase = iota // nothing started, or an acknowledgement ended
	ResponseInFlight                         // started, no cancel sent
	ResponseCancelling                       // started, RESPONSE.CANCEL sent, terminal not yet observed
	ResponseCompleted                        // ended normally
	ResponseInterrupted                      // ended after a cancel, or a cancel was sent while idle
)

// Response is the current provider response.
type Response struct {
	Phase ResponsePhase
	// ID is the provider identity of the live response ("" when untagged or idle).
	ID string
	// HasOutput records whether the current response produced output deltas.
	HasOutput bool
}

func (r *Response) InFlight() bool {
	return r.Phase == ResponseInFlight || r.Phase == ResponseCancelling
}

// CancelSent reports whether a cancel targets the current or just-ended
// response, so its late untagged output is stale.
func (r *Response) CancelSent() bool {
	return r.Phase == ResponseCancelling || r.Phase == ResponseInterrupted
}

func (r *Response) Completed() bool { return r.Phase == ResponseCompleted }

func (r *Response) Start(id string) {
	r.Phase, r.ID, r.HasOutput = ResponseInFlight, id, false
}

// Cancel records an accepted RESPONSE.CANCEL. A completed response stays
// completed: the provider has nothing left to cancel and its terminal
// accounting must not change.
func (r *Response) Cancel() {
	switch r.Phase {
	case ResponseInFlight:
		r.Phase = ResponseCancelling
	case ResponseIdle:
		r.Phase = ResponseInterrupted
	case ResponseCancelling, ResponseCompleted, ResponseInterrupted:
	}
}

// ClearCancel forgets a cancel that targeted an earlier response once a new
// acknowledgement request is accepted.
func (r *Response) ClearCancel() {
	switch r.Phase {
	case ResponseCancelling:
		r.Phase = ResponseInFlight
	case ResponseInterrupted:
		r.Phase = ResponseIdle
	case ResponseIdle, ResponseInFlight, ResponseCompleted:
	}
}

// End applies an owned terminal boundary. An acknowledgement never counts as
// the assistant turn, so it returns the response to idle.
func (r *Response) End(acknowledgement bool) {
	switch {
	case acknowledgement:
		r.Phase, r.HasOutput = ResponseIdle, false
	case r.CancelSent():
		r.Phase = ResponseInterrupted
	default:
		r.Phase = ResponseCompleted
	}
	r.ID = ""
}

// ContinuationPhase tracks the response that answers an accepted tool
// result. Only the bound continuation response is exempt from barge-in.
type ContinuationPhase uint8

const (
	ContinuationNone ContinuationPhase = iota
	// ContinuationRequested: the RESPONSE.CREATE was accepted, its response
	// has not opened yet.
	ContinuationRequested
	// ContinuationBoundTagged: the provider echoed the continuation purpose
	// on the response it opened.
	ContinuationBoundTagged
	// ContinuationBoundGuessed: the provider does not echo the purpose (Grok,
	// LocalAI), so the first response opened after the request is assumed to
	// answer it.
	ContinuationBoundGuessed
	// ContinuationEnded: the bound response ended or was retired; consumed
	// once by TakeEnded.
	ContinuationEnded
)

// Continuation is the tool-continuation binding.
type Continuation struct {
	Phase ContinuationPhase
	// ResponseID is the bound response's identity.
	ResponseID string
	// PurposeEchoed records that the provider echoes the request purpose.
	// Once seen, untagged responses are never guessed to be continuations.
	PurposeEchoed bool
}

func (c *Continuation) Requested() bool { return c.Phase == ContinuationRequested }

func (c *Continuation) Bound() bool {
	return c.Phase == ContinuationBoundTagged || c.Phase == ContinuationBoundGuessed
}

// Owns reports whether an output event with msgID belongs to the bound response.
func (c *Continuation) Owns(msgID string) bool {
	return c.Bound() && (msgID == "" || msgID == c.ResponseID)
}

func (c *Continuation) Request() { c.Phase, c.ResponseID = ContinuationRequested, "" }

// OnResponseStart binds the continuation to a newly started response when
// the provider tagged it, or, without a purpose echo, when a request is
// pending. An acknowledgement response never binds.
func (c *Continuation) OnResponseStart(id string, tagged, acknowledgement bool) {
	if tagged {
		c.PurposeEchoed = true
	}
	guessed := !tagged && !c.PurposeEchoed && c.Requested()
	if acknowledgement || (!tagged && !guessed) {
		return
	}
	c.Phase, c.ResponseID = ContinuationBoundGuessed, id
	if tagged {
		c.Phase = ContinuationBoundTagged
	}
}

// End finishes a bound continuation response.
func (c *Continuation) End() {
	if c.Bound() {
		c.Phase, c.ResponseID = ContinuationEnded, ""
	}
}

// Rejected releases a guessed binding when the provider rejected the request
// because another response was active: the guessed response was the
// provider's own, and the adapter retries the request once it ends.
func (c *Continuation) Rejected() {
	if c.Phase == ContinuationBoundGuessed {
		c.Request()
	}
}

func (c *Continuation) TakeEnded() bool {
	if c.Phase != ContinuationEnded {
		return false
	}
	c.Phase = ContinuationNone
	return true
}

func (c *Continuation) Reset() { c.Phase, c.ResponseID = ContinuationNone, "" }

// AcknowledgementPhase tracks a progress acknowledgement response requested
// while a long-running tool executes.
type AcknowledgementPhase uint8

const (
	AcknowledgementNone AcknowledgementPhase = iota
	AcknowledgementOutstanding
	// AcknowledgementCancelled: outstanding, and a cancel targets it, so its
	// response is cancelled as soon as it starts.
	AcknowledgementCancelled
	// AcknowledgementEnded: finished; consumed once by TakeEnded.
	AcknowledgementEnded
)

// Acknowledgement is the outstanding progress acknowledgement, if any.
type Acknowledgement struct {
	Phase AcknowledgementPhase
	// Start is an untagged response start attributed to the outstanding
	// acknowledgement, re-announced without that purpose if the provider
	// rejects the acknowledgement request.
	Start *messages.StreamMessage
}

func (a *Acknowledgement) Outstanding() bool {
	return a.Phase == AcknowledgementOutstanding || a.Phase == AcknowledgementCancelled
}

func (a *Acknowledgement) Cancelled() bool { return a.Phase == AcknowledgementCancelled }

// Request records an accepted acknowledgement RESPONSE.CREATE.
func (a *Acknowledgement) Request() { a.Phase = AcknowledgementOutstanding }

// ObserveTagged records provider output tagged with the acknowledgement purpose.
func (a *Acknowledgement) ObserveTagged() {
	if !a.Outstanding() {
		a.Phase = AcknowledgementOutstanding
	}
}

func (a *Acknowledgement) Cancel() {
	if a.Phase == AcknowledgementOutstanding {
		a.Phase = AcknowledgementCancelled
	}
}

func (a *Acknowledgement) End() {
	if a.Outstanding() {
		a.Phase = AcknowledgementEnded
	}
}

func (a *Acknowledgement) Reject() { a.Phase = AcknowledgementNone }

func (a *Acknowledgement) TakeEnded() bool {
	if a.Phase != AcknowledgementEnded {
		return false
	}
	a.Phase = AcknowledgementNone
	return true
}

// ToolBatch records provider-boundary failures inside one tool-result batch
// until the batch's continuation boundary.
type ToolBatch struct {
	Failures []messages.StreamMessage
	// Rejected marks that a result in the batch was not delivered, so its
	// continuation must not be requested.
	Rejected bool
}

func (b *ToolBatch) TakeFailures() []messages.StreamMessage {
	failures := b.Failures
	b.Failures = nil
	return failures
}

// ResponseIDStatus is what the runner knows about a past response identity.
type ResponseIDStatus uint8

const (
	ResponseIDUnknown   ResponseIDStatus = iota
	ResponseIDCancelled                  // a cancel targeted it; it may still end
	ResponseIDRetired                    // a replacement started before it ended
	ResponseIDTerminal                   // its terminal boundary was owned
)

// ResponseIDRetention bounds the response identity ledger. Only recent
// responses can still deliver a late event; a provider never interleaves
// output from dozens of responses back.
const ResponseIDRetention = 128

// ResponseIDLedger remembers the most recent response identities and their
// status, evicting the oldest so bookkeeping does not grow with session length.
type ResponseIDLedger struct {
	status map[string]ResponseIDStatus
	order  []string
}

// Mark records status for id. A closed status (retired or terminal) is final.
func (l *ResponseIDLedger) Mark(id string, status ResponseIDStatus) {
	if id == "" {
		return
	}
	if current, ok := l.status[id]; ok {
		if !current.Closed() {
			l.status[id] = status
		}
		return
	}
	if l.status == nil {
		l.status = make(map[string]ResponseIDStatus, ResponseIDRetention+1)
	}
	l.status[id] = status
	l.order = append(l.order, id)
	if len(l.order) > ResponseIDRetention {
		delete(l.status, l.order[0])
		l.order = append(l.order[:0], l.order[1:]...)
	}
}

func (l *ResponseIDLedger) Get(id string) ResponseIDStatus { return l.status[id] }

// Closed reports whether id can no longer own lifecycle events.
func (l *ResponseIDLedger) Closed(id string) bool { return l.Get(id).Closed() }

func (l *ResponseIDLedger) Len() int { return len(l.status) }

func (s ResponseIDStatus) Closed() bool {
	return s == ResponseIDRetired || s == ResponseIDTerminal
}
