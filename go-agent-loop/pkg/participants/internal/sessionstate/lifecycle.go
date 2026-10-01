package sessionstate

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// State is the mutable lifecycle state owned by one persistent session
// runner goroutine.
type State struct {
	Session      SessionPhase
	Response     Response
	Continuation Continuation
	Ack          Acknowledgement
	ResponseIDs  ResponseIDLedger
	ToolBatch    ToolBatch
	// Deferred holds control events waiting for the active response's
	// terminal boundary; they are replayed in order once it is observed.
	Deferred []messages.StreamMessage
	Onset    HeldOnset
}

// CancelPending reports whether a RESPONSE.CANCEL is still unacknowledged for
// the active response or the outstanding acknowledgement.
func (s *State) CancelPending() bool {
	return s.Response.Phase == ResponseCancelling || s.Ack.Cancelled()
}

// ResponseActive reports whether a response is live or an acknowledgement is
// outstanding: either is a barge-in target.
func (s *State) ResponseActive() bool {
	return s.Response.InFlight() || s.Ack.Outstanding()
}

// CancelTarget reports whether interrupting speech should cancel the active
// response. The bound tool continuation is exempt: nothing re-requests a
// cancelled continuation, so its obligation would be left unresolved. A cancel
// is never sent twice for one response.
func (s *State) CancelTarget() bool {
	return s.ResponseActive() && !s.Continuation.Bound() && !s.CancelPending()
}

// NoteCancelSent applies one accepted RESPONSE.CANCEL, automatic or explicit.
func (s *State) NoteCancelSent() {
	s.Response.Cancel()
	s.Ack.Cancel()
	s.ResponseIDs.Mark(s.Response.ID, ResponseIDCancelled)
}

// StartResponse applies a provider response start.
func (s *State) StartResponse(msgID string, acknowledgement, tagged bool) {
	if !s.beginResponse(msgID) {
		return
	}
	s.Response.Start(msgID)
	// A provider that echoes the request purpose identifies the continuation
	// exactly; the adapter owns which request each response answers. Without
	// that echo, the first response opened after the continuation request --
	// which is only sent while nothing is in flight -- is the continuation.
	s.Continuation.OnResponseStart(msgID, tagged, acknowledgement)
	if acknowledgement && s.Ack.Cancelled() {
		s.Response.Cancel()
		s.ResponseIDs.Mark(msgID, ResponseIDCancelled)
	}
}

// beginResponse reports whether a start event opens a new response,
// retiring a live response it replaces.
func (s *State) beginResponse(msgID string) bool {
	if msgID != "" && s.ResponseIDs.Closed(msgID) {
		return false
	}
	if s.Response.InFlight() {
		current := s.Response.ID
		if current == msgID {
			// Duplicate starts for the same response must not reset
			// cancellation or output state.
			return false
		}
		if current != "" && msgID == "" {
			// An untagged start cannot claim an identified response.
			return false
		}
		if current != "" {
			s.retireResponse()
		}
	}
	return true
}

// retireResponse retires the current response when a replacement response
// starts before its terminal boundary was observed. The retired response's
// own MESSAGE.END will never be owned, so the acknowledgement and
// continuation it carried are finalized here; a stranded outstanding
// acknowledgement would make every later contentful frame look like a live
// barge-in target and produce a RESPONSE.CANCEL the provider rejects with
// response_cancel_not_active.
func (s *State) retireResponse() {
	s.ResponseIDs.Mark(s.Response.ID, ResponseIDRetired)
	s.Ack.End()
	s.Continuation.End()
}

// EndResponse applies a provider MESSAGE.END and reports whether it ended the
// current response. An ended cancelled response's value is rewritten as
// interrupted.
func (s *State) EndResponse(msg *messages.StreamMessage, msgID string, acknowledgement bool) bool {
	if !s.ownsResponseEnd(msgID) {
		return false
	}
	ownedID := msgID
	if ownedID == "" {
		ownedID = s.Response.ID
	}
	if s.Response.CancelSent() {
		// Realtime providers normally acknowledge RESPONSE.CANCEL with a
		// response.done event. Preserve that wire boundary so the next input
		// can proceed, but mark it as interrupted rather than a normally
		// completed assistant turn.
		msg.Value = interruptedMessageEndValue(msg.Value, s.Response.HasOutput)
	}
	// A progress acknowledgement is never the assistant turn that satisfies
	// a user input or a tool continuation.
	s.Response.End(acknowledgement)
	s.ResponseIDs.Mark(ownedID, ResponseIDTerminal)
	s.Continuation.End()
	if acknowledgement {
		s.Ack.End()
	}
	return true
}

func (s *State) ownsResponseEnd(msgID string) bool {
	if msgID == "" {
		// Compatible providers may omit response_id on response.done. The
		// sole active response owns that terminal event.
		return s.Response.InFlight() || !s.Response.Completed()
	}
	switch s.ResponseIDs.Get(msgID) {
	case ResponseIDRetired, ResponseIDTerminal:
		return false
	case ResponseIDCancelled:
		if s.Response.ID != msgID {
			return false
		}
	case ResponseIDUnknown:
	}
	if s.Response.InFlight() {
		return s.Response.ID == msgID
	}
	// A response.done without response.created is accepted once for
	// compatibility with providers that omit the opening event.
	return !s.Response.Completed()
}

// StaleCustomerOutput reports whether msg is customer-visible output of a
// cancelled, retired, ended, or superseded response. MESSAGE.END is never
// stale, so a cancelled response can still close.
func (s *State) StaleCustomerOutput(msg messages.StreamMessage) bool {
	if !isCustomerOutputDelta(msg) {
		return false
	}
	msgID := ResponseID(msg.ResponseID)
	if msgID == "" {
		return s.Response.CancelSent() || s.Ack.Cancelled()
	}
	if s.ResponseIDs.Get(msgID) != ResponseIDUnknown {
		return true
	}
	return s.Response.ID != "" && s.Response.ID != msgID
}

// TagContinuation marks provider output that belongs to the bound
// continuation response so downstream tool-obligation accounting resolves the
// obligation against that response and never against an unrelated one.
func (s *State) TagContinuation(msg *messages.StreamMessage, msgID string) {
	if !IsResponseStreamType(msg.Type) || msg.ResponsePurpose != "" || !s.Continuation.Owns(msgID) {
		return
	}
	msg.ResponsePurpose = messages.ResponsePurposeToolContinuation
}

// DeferResponseRequest reports whether evt, which may ask the provider for a
// new response, must wait for the active response's terminal boundary. A
// RESPONSE.CANCEL that is still unacknowledged holds both the end-of-turn
// boundary and a continuation: otherwise the provider sees a second response
// request before the first ended (response_cancel_not_active). A tool
// continuation is never requested over an active response: the provider
// rejects a second active response, and the response that is playing must
// stay interruptible.
func (s *State) DeferResponseRequest(evt messages.StreamMessage) bool {
	continuation := IsContinuationCreate(evt)
	if (continuation || evt.Type == messages.StreamTypeMessageEnd) && s.CancelPending() {
		return true
	}
	return continuation && s.Response.InFlight()
}

// HoldForAcknowledgement applies acknowledgement ordering and reports
// whether it consumed evt. A progress acknowledgement is dropped while a
// response is active or requested (the provider rejects a second response
// request, and the acknowledgement is only useful while nothing is speaking);
// an ordinary continuation is deferred until an outstanding acknowledgement
// has ended.
func (s *State) HoldForAcknowledgement(evt messages.StreamMessage) bool {
	if evt.Type != messages.StreamTypeResponseCreate {
		return false
	}
	if IsAcknowledgementCreate(evt) {
		return s.Response.InFlight() || s.Continuation.Requested() || s.Continuation.Bound() || s.Ack.Outstanding()
	}
	if s.Ack.Outstanding() {
		s.Deferred = append(s.Deferred, evt)
		return true
	}
	return false
}

// TakeDeferred returns and clears the deferred control events.
func (s *State) TakeDeferred() []messages.StreamMessage {
	deferred := s.Deferred
	s.Deferred = nil
	return deferred
}
