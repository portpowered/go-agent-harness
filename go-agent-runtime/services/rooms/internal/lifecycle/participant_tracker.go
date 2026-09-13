package lifecycle

import (
	"context"
	"strings"
	"sync"
	"time"

	m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	rm "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	p "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const participantResponseCancelTimeout = 50 * time.Millisecond
const fc, fd, fsc, foc, fso, fx, fte, frs, fbs, fbc, frd, fri, frc, frt, fbr, fcont, ft, ff uint32 = 1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072
const tc, tt, tk, tcl, tp, to, ti, tb, ts, ec, es, et = 0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2

type participantLifecycle struct {
	mu    sync.Mutex
	flags uint32
	x     [9]string
	e     [3]error
	s     rm.TrackedSession
	d     Ch
	q     func() error
	h     chan<- struct{}
	z     Ch
	n     int
	p, a  map[string]struct{}
	o     obs
}
type pl = participantLifecycle
type obs struct {
	a, b, c, r, p, o string
	e                error
	f                bool
}
type state struct {
	p          string
	e          error
	s          PS
	d          Ch
	q          func() error
	ok         bool
	c, t, v, o string
}

const sStart, sAudio, sToolStart, sToolDelta, sToolEnd, sMessageEnd, sOpen, sClose, sCancel, sCreate = m.StreamTypeMessageStart, m.StreamTypeAudioStart, m.StreamTypeToolCallStart, m.StreamTypeToolCallDelta, m.StreamTypeToolCallEnd, m.StreamTypeMessageEnd, m.StreamTypeSessionOpen, m.StreamTypeSessionClose, m.StreamTypeResponseCancel, m.StreamTypeResponseCreate
const rSessionClose, rProviderClose, rFailure, rCancel, rProviderComplete, rLoopComplete, rReplayComplete, rReplayDivergence, rReplayIncomplete, rPartial = m.TerminalReasonSessionClose, m.TerminalReasonProviderClose, m.TerminalReasonTerminalFailure, m.TerminalReasonCancellation, m.TerminalReasonProviderAuthoredCompletion, m.TerminalReasonLoopSynthesizedCompletion, m.TerminalReasonReplayComplete, m.TerminalReasonReplayDivergence, m.TerminalReasonReplayIncomplete, m.TerminalReasonPartialOutput
const xFailure, xFailed, xClose, xDisconnected, xCompleted, xGrace, xCancelled, xStopped, providerClosed = "session_failure", "failed", "provider_close", "disconnected", "completed", "completed_during_grace", "cancelled_after_grace", "stopped", "provider_closed"
const pError, pDisconnected, pEnded = rm.ParticipantTerminationError, rm.ParticipantTerminationDisconnected, rm.ParticipantTerminationEnded
const oNone, oNA = m.TerminalOutputNone, m.TerminalOutputNotApplicable

func NewParticipantLifecycle(o O) L     { return &pl{h: o.StateChanged, z: o.AdmissionClosed} }
func (l *pl) mutate(f func())           { l.mu.Lock(); f(); l.notify(); l.mu.Unlock() }
func update[T any](l *pl, f func() T) T { l.mu.Lock(); v := f(); l.notify(); l.mu.Unlock(); return v }
func read[T any](l *pl, f func() T) T   { l.mu.Lock(); defer l.mu.Unlock(); return f() }
func (l *pl) has(f uint32) bool         { return l.flags&f != 0 }
func (l *pl) set(f uint32, v bool)      { l.flags = l.flags&^f | pick(v, f, uint32(0)) }
func (l *pl) notify() {
	select {
	case l.h <- struct{}{}:
	default:
	}
}
func errorClass(err error) string { return first(p.ErrorClassification(err), p.ErrorClassUnknown) }
func observation(a, b string, o E, err error, failure bool) obs {
	return obs{a: a, b: b, c: o.Classification, r: o.TerminalReason, p: o.TerminalProvenance, o: o.OutputState, e: err, f: failure}
}
func (l *pl) setObservation(o obs) {
	o.r, o.o = first(o.r, string(rSessionClose)), first(o.o, string(oNone))
	doIf(o.p == "" && (o.a != "" || o.b != "" || o.r != "" || o.f), func() { o.p = termProv(o.b, o.r) })
	l.o = o
	l.set(ft, true)
	l.x[tcl], l.x[tt], l.x[tp], l.x[to] = o.c, o.r, o.p, o.o
}
func errorObservation(l *pl, err error) obs {
	return observation(xFailure, xFailed, E{Classification: errorClass(err), TerminalReason: string(rFailure), TerminalProvenance: string(m.TerminalProvenanceSession), OutputState: outputState(l.has(fso), l.n)}, err, true)
}
func closeObservation(v *CV) obs {
	provider := v.Reason == providerClosed
	return obs{a: pick(provider, "provider_close", "participant_completion"), b: pick(provider, "disconnected", "completed"), c: v.Classification, r: first(string(v.TerminalReason), pick(provider, string(rProviderClose), string(rSessionClose))), p: string(v.TerminalProvenance), o: first(string(v.OutputState), string(oNA))}
}
func (l *pl) fail(err error) {
	doIf(err != nil && !cancelOnly(err) && !l.has(ff) && !l.has(fbc), func() { l.flags |= ff; l.setObservation(errorObservation(l, err)) })
}
func (l *pl) term(reason rm.ParticipantTerminationReason, err error) {
	doIf(l.x[tk] == "" && reason != "", func() { l.x[tk], l.e[et] = string(reason), err })
}
func (l *pl) disconnected() {
	doIf(!l.has(ff) && !l.has(ft), func() {
		l.setObservation(observation(xClose, xDisconnected, E{TerminalReason: string(rProviderClose), TerminalProvenance: string(m.TerminalProvenanceSession), OutputState: outputState(l.has(fso), l.n)}, nil, false))
	})
}

func (l *pl) MarkConnected(err error) {
	l.mutate(func() { l.set(fc, err == nil); l.e[ec] = err; l.fail(err) })
}
func (l *pl) MarkDeviceReady()     { l.mutate(func() { l.flags |= fc | fd; l.e[ec] = nil }) }
func (l *pl) DeviceHasReady() bool { return read(l, func() bool { return l.has(fd) }) }
func (l *pl) MarkSessionCreated()  { l.mutate(func() { l.set(fsc, true) }) }
func (l *pl) SetOwnedSession(s SS) { l.mutate(func() { l.s = s }) }
func closeSession(s SS) error {
	return pickCall(s != nil, func() error { return s.Close() }, func() error { return nil })
}
func (l *pl) CloseOwnedSession() error         { return closeSession(read(l, func() SS { return l.s })) }
func (l *pl) MarkOwnedSessionClosed(err error) { l.mutate(func() { l.flags |= foc; l.e[es] = err }) }
func (l *pl) MarkParticipantFailure(err error) {
	l.mutate(func() { doIf(!l.has(frs), func() { l.term(pError, err) }) })
}
func (l *pl) MarkLivenessFailure(err error, meta rm.ParticipantLivenessMetadata) {
	l.mutate(func() {
		doIf(l.x[tk] == "", func() {
			l.x[tk], l.e[et], l.x[tcl], l.x[tt], l.x[tp], l.x[to] = string(pError), err, meta.Classification, string(meta.TerminalReason), string(meta.TerminalProvenance), string(meta.OutputState)
		})
	})
}
func (l *pl) TransportTerminalError() error { return read(l, func() error { return call(l.q) }) }

func (l *pl) clearTools()             { l.p, l.a = nil, nil; l.set(fcont, false) }
func (l *pl) ToolCallID(msg M) string { return toolCallID(msg) }
func (l *pl) recordTool(id string, continuation bool) {
	l.a = pick(l.a == nil, map[string]struct{}{}, l.a)
	delete(l.p, id)
	l.a[id] = struct{}{}
	doIf(l.has(fbs) && !l.has(fbc) && continuation, func() { l.set(fcont, true) })
}
func (l *pl) RecordToolResultSend(id string, accepted, continuation bool) {
	l.mutate(func() { doIf(accepted && validID(id), func() { l.recordTool(id, continuation) }) })
}
func (l *pl) RecordToolContinuationRequest(accepted bool) {
	l.mutate(func() { doIf(accepted && l.has(fbs) && !l.has(fbc) && len(l.a) > 0, func() { l.set(fcont, true) }) })
}
func (l *pl) RecordResponseCancellation() {
	l.mutate(func() { doIf(l.has(fri), func() { l.set(frc, true) }) })
}

func sendCancel(s PS) {
	ctx, cancel := context.WithTimeout(context.Background(), participantResponseCancelTimeout)
	defer cancel()
	_ = m.SendSessionWithOutcome(ctx, s, M{Type: sCancel, Value: m.NewResponseCancelValue()})
}
func (l *pl) CancelActiveResponse() {
	st := update(l, func() state {
		ok := l.has(fbc) && l.has(fbr) && l.has(fri) && !l.has(frc) && l.s != nil
		doIf(ok, func() { l.set(frc, true) })
		return state{s: l.s, ok: ok}
	})
	doIf(st.ok, func() { sendCancel(st.s) })
}
func (l *pl) AdmitResponseTerminal() bool {
	return read(l, func() bool { return !l.has(frs) || l.has(fbs) && !l.has(fbc) && l.has(fbr) })
}
func (l *pl) boundMessage(msg M) bool {
	return pickCall(msg.Type == sToolEnd, func() bool { id := toolCallID(msg); _, ok := l.p[id]; return id != "" && ok }, func() bool { return msg.Type == sCreate && len(l.a) > 0 && !l.has(fcont) })
}
func (l *pl) AdmitSessionMessageAfterBound(msg M) bool {
	return read(l, func() bool { return l.has(fbs) && !l.has(fbc) && l.boundMessage(msg) })
}
func (l *pl) AdmitCompleteToolResultAfterBound(msg N) bool {
	return read(l, func() bool {
		_, ok := l.p[msg.ToolCallID]
		return l.has(fbs) && !l.has(fbc) && validID(msg.ToolCallID) && ok
	})
}

func (l *pl) ObserveTerminal(o E) bool {
	return pickCall(o.TerminalReason != "" || o.Classification != "" || o.OutputState != "" || o.Err != nil, func() bool {
		return update(l, func() bool {
			return pickCall(o.Failure, func() bool { return l.observeFailure(o) }, func() bool { return l.observeSuccess(o) })
		})
	}, func() bool { return false })
}
func (l *pl) disconnectTerminal() bool { l.disconnected(); l.term(pDisconnected, nil); return false }
func (l *pl) observeFailure(o E) bool {
	return pickCall(o.TerminalReason == string(rProviderClose) && o.FailingEvent == string(sClose) && call(l.q) == nil, func() bool { return l.disconnectTerminal() }, func() bool {
		return pickCall(!l.has(fbc), func() bool {
			return pickCall(l.has(ff), func() bool { return pick(specific(o, l.o), l.replaceFailure(o), false) }, func() bool { return l.newFailure(o) })
		}, func() bool { return false })
	})
}
func (l *pl) newFailure(o E) bool {
	l.flags |= ff
	l.set(fbr, false)
	l.set(frt, false)
	l.clearTools()
	l.setObservation(observation(xFailure, xFailed, o, o.Err, true))
	return true
}
func (l *pl) replaceFailure(o E) bool {
	l.set(frt, false)
	err := pick(o.Err != nil && o.Err.Error() == "session stream error" && l.o.e != nil, l.o.e, o.Err)
	err = pick(l.x[tk] == string(pError) && l.e[et] != nil, l.e[et], err)
	l.setObservation(observation(xFailure, xFailed, o, err, true))
	doIf(l.x[tk] == string(pError), func() { l.e[et] = err })
	return true
}
func specific(in E, old obs) bool {
	return (in.Classification != "" && in.Classification != p.ErrorClassUnknown && in.Classification != p.ErrorClassCancellation && (old.c == "" || old.c == p.ErrorClassUnknown || old.c == p.ErrorClassCancellation)) || old.p == string(m.TerminalProvenanceCLI) && in.TerminalProvenance != "" && in.TerminalProvenance != string(m.TerminalProvenanceCLI)
}
func (l *pl) finishSuccess(o E) bool {
	t := observation("", "", o, nil, false)
	l.set(frt, false)
	grace := l.has(fbr)
	doIf(grace, func() {
		t.a, t.b = l.x[tb], xGrace
		l.set(fbr, false)
		doIf(l.has(fbc) || o.RoomBound || o.TerminalReason == string(rCancel), func() {
			t.b, t.c, t.r, t.p, t.o = xCancelled, rm.RoomBoundCancelledClassification, string(rCancel), string(m.TerminalProvenanceRoom), first(t.o, string(oNone))
		})
	})
	doIf(!grace && l.has(fbs), func() { t.a, t.b = l.x[tb], xCompleted })
	doIf(!grace && !l.has(fbs) && l.x[ts] != "", func() { t.a, t.b = l.x[ts], xStopped })
	l.clearTools()
	l.setObservation(t)
	return true
}
func (l *pl) observeSuccess(o E) bool {
	ok := !l.has(ff) && !(l.has(fbs) && l.has(fbc) && !l.has(fbr) && l.has(ft)) && !(l.has(fbs) && !l.has(fbr) && l.has(fte)) && !(l.has(fbs) && l.has(fbc) && !o.RoomBound && o.TerminalReason != string(rCancel))
	return pickCall(ok, func() bool { return l.finishSuccess(o) }, func() bool { return false })
}

func (l *pl) observeResponse(msg M) {
	doIf(msg.Role != m.RoleTool, func() {
		old := l.x[ti]
		fresh := msg.Type == sStart || !l.has(fri) || strings.TrimSpace(msg.ResponseID) != "" && strings.TrimSpace(msg.ResponseID) != old
		l.set(fri, true)
		l.x[ti] = msg.ResponseID
		l.set(frt, false)
		doIf(fresh, func() { l.set(frc, false) })
	})
}
func (l *pl) observeTool(msg M) {
	doIf(msg.Role != m.RoleTool && (!l.has(fbs) || !l.has(fbc) && l.has(fbr)), func() {
		id := toolCallID(msg)
		doIf(id != "", func() { l.p = pick(l.p == nil, map[string]struct{}{}, l.p); l.p[id] = struct{}{} })
	})
}
func (l *pl) observeEnd(msg M) {
	doIf(msg.Role != m.RoleTool, func() { l.set(fri, false); l.x[ti] = ""; l.set(frt, !l.has(fbs)) })
}
func (l *pl) closeValue(v *CV) {
	l.x[tc], l.x[tt] = v.Reason, string(v.TerminalReason)
	doIf(l.x[tt] == "" && v.Reason == providerClosed, func() { l.x[tt] = string(rProviderClose) })
	doIf(!l.has(ff) && !l.has(ft) && !(l.has(fbs) && l.has(fbc)), func() { l.setObservation(closeObservation(v)) })
}
func (l *pl) observeClose(msg M) {
	v, ok := msg.Value.(*CV)
	l.set(fx, true)
	doIf(ok && v != nil, func() { l.closeValue(v) })
	doIf(!l.has(frs) || l.has(fbs) && !l.has(fbc), l.finishClose)
}
func (l *pl) observeLocked(msg M) {
	doIf(msg.Type == sStart || msg.Type == sAudio, func() { l.observeResponse(msg) })
	doIf(msg.Type == sToolStart || msg.Type == sToolDelta || msg.Type == sToolEnd, func() { l.observeTool(msg) })
	doIf(msg.Type == sMessageEnd, func() { l.observeEnd(msg) })
	doIf(msg.Type == sOpen, func() { l.set(fso, true) })
	doIf(msg.Type == sClose, func() { l.observeClose(msg) })
}
func (l *pl) Observe(msg M) int { return update(l, func() int { l.observeLocked(msg); return l.n }) }
func (l *pl) finishClose() {
	err := call(l.q)
	doIf(err != nil && !cancelOnly(err), func() { l.finishFailure(err) })
	doIf(err == nil || cancelOnly(err), func() { l.term(classClose(l.x[tc], m.TerminalReason(l.x[tt])), nil) })
}
func (l *pl) finishFailure(err error)  { l.fail(err); l.term(pError, err) }
func (l *pl) ObserveAdmittedTurn() int { return update(l, func() int { l.n++; return l.n }) }

func (l *pl) finishTransport(err error) {
	doIf(err != nil && !cancelOnly(err), func() { l.finishFailure(err) })
	doIf(err == nil || cancelOnly(err), func() { l.disconnected(); l.term(pDisconnected, nil) })
}
func (l *pl) MarkTransportEndedWithError(err error) {
	l.mutate(func() {
		l.set(fte, true)
		doIf(!l.has(frs) || l.has(fbs) && !l.has(fbc), func() { l.finishTransport(err) })
	})
}
func (l *pl) SetTransportDone(done Ch, terminalError func() error) {
	l.mutate(func() { l.d, l.q = done, terminalError })
	doIf(channelClosed(done), func() { l.MarkTransportEndedWithError(call(terminalError)) })
}
func (l *pl) transportState() state { return state{ok: l.has(fte), d: l.d, q: l.q} }
func (l *pl) TransportHasEnded() bool {
	st := read(l, l.transportState)
	closed := st.d != nil && channelClosed(st.d)
	doIf(closed, func() { l.MarkTransportEndedWithError(call(st.q)) })
	return st.ok || closed
}
func (l *pl) stopTransport() { l.set(fte, true); l.finishTransport(call(l.q)) }
func boundReason(r []rm.RoomTerminationReason) rm.RoomTerminationReason {
	return pickCall(len(r) > 0, func() rm.RoomTerminationReason { return r[0] }, func() rm.RoomTerminationReason { return "" })
}
func (l *pl) boundDone() {
	doIf(l.has(ft), func() { l.o.a, l.o.b = l.x[tb], xCompleted })
	doIf(!l.has(ft), func() {
		l.setObservation(obs{a: l.x[tb], b: xCompleted, r: l.x[tt], p: string(m.TerminalProvenanceLoop), o: string(oNone)})
	})
}
func (l *pl) boundStop(reason []rm.RoomTerminationReason) {
	mid := l.has(fri) || l.has(frt) || len(l.p) > 0 || len(l.a) > 0
	l.x[tb] = boundTrigger(boundReason(reason), mid)
	l.set(fbr, mid)
	doIf(!mid && !l.has(ff) && l.x[tk] == "", l.boundDone)
}
func (l *pl) stop(bound bool, reason []rm.RoomTerminationReason) {
	doIf(channelClosed(l.d), l.stopTransport)
	l.set(frs, true)
	l.set(fbs, bound)
	doIf(len(reason) > 0, func() { l.x[ts] = string(reason[0]) })
	doIf(bound, func() { l.boundStop(reason) })
}
func (l *pl) MarkCoordinatorStopping(bound bool, reason ...rm.RoomTerminationReason) {
	l.mutate(func() { l.stop(bound, reason) })
}
func (l *pl) cancelBound() {
	l.o.a, l.o.b, l.o.c = l.x[tb], xCancelled, rm.RoomBoundCancelledClassification
	l.o.r, l.o.p, l.o.o = string(rCancel), string(m.TerminalProvenanceRoom), string(oNone)
	l.set(ft, true)
}
func (l *pl) MarkBoundCancellation() {
	l.mutate(func() { l.set(fbc, true); doIf(l.has(fbr) && !l.has(ff), l.cancelBound) })
}
func (l *pl) runDone(err error) {
	l.set(frd, true)
	doIf(err != nil && !cancelOnly(err), func() { l.finishFailure(err) })
	doIf((err == nil || cancelOnly(err)) && l.has(ff), func() { l.term(pError, l.o.e) })
}
func (l *pl) MarkRunDone(err error) {
	l.mutate(func() { l.runDone(err) })
}
func (l *pl) RunHasFinished() bool { return read(l, func() bool { return l.has(frd) }) }

func (l *pl) Snapshot() rm.ParticipantLifecycleSnapshot {
	return read(l, func() rm.ParticipantLifecycleSnapshot {
		return rm.ParticipantLifecycleSnapshot{Connected: l.has(fc), SessionOpened: l.has(fso), SessionClosed: l.has(fx), CloseReason: l.x[tc], TerminalReason: m.TerminalReason(l.x[tt]), Turns: l.n, ConnectErr: l.e[ec]}
	})
}
func (l *pl) Terminal() (rm.ParticipantTerminationReason, error, bool) {
	s := read(l, func() state {
		return pickCall(l.has(ff) && l.x[tk] == "", func() state { return state{p: string(pError), e: l.o.e, ok: true} }, func() state { return state{p: l.x[tk], e: l.e[et], ok: l.x[tk] != ""} })
	})
	return rm.ParticipantTerminationReason(s.p), s.e, s.ok
}
func (l *pl) TerminalMetadata() (string, m.TerminalReason, m.TerminalProvenance, m.TerminalOutputState) {
	s := read(l, func() state { return state{c: l.x[tcl], t: l.x[tt], v: l.x[tp], o: l.x[to]} })
	return s.c, m.TerminalReason(s.t), m.TerminalProvenance(s.v), m.TerminalOutputState(s.o)
}
func (l *pl) snapshotLocked() V {
	o := l.o
	doIf(l.has(ff), func() { o.f = true; o.a, o.b = first(o.a, xFailure), first(o.b, xFailed) })
	doIf(!l.has(ff) && l.has(fbs), func() {
		o.a = first(o.a, l.x[tb])
		doIf(l.has(fbr) && l.has(fbc), func() {
			o.b, o.c, o.r, o.p = xCancelled, rm.RoomBoundCancelledClassification, string(rCancel), string(m.TerminalProvenanceRoom)
		})
		doIf(o.b == "", func() { o.b = pick(l.has(fbr) && l.has(fbc), xCancelled, xCompleted) })
	})
	doIf(o.a == "" && l.x[ts] != "", func() { o.a, o.b = l.x[ts], xStopped })
	o.r, o.o, o.p = first(o.r, l.x[tt]), first(o.o, string(oNone)), first(o.p, termProv(o.b, o.r))
	return V{TerminationTrigger: o.a, TerminationDisposition: o.b, Classification: o.c, TerminalReason: o.r, TerminalProvenance: o.p, OutputState: o.o, Err: o.e, Failure: o.f}
}
func (l *pl) TerminalObservationSnapshot() V { return read(l, l.snapshotLocked) }
func (l *pl) OwnedSessionSnapshot() rm.ParticipantOwnedSessionSnapshot {
	return read(l, func() rm.ParticipantOwnedSessionSnapshot {
		return rm.ParticipantOwnedSessionSnapshot{Created: l.has(fsc), Closed: l.has(foc), TransportDone: l.d, CloseErr: l.e[es]}
	})
}

var _ L = (*pl)(nil)
