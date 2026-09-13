package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	rm "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type M = m.StreamMessage
type N = m.Message
type V = rm.ParticipantTerminalObservation
type E = rm.SessionTerminalObservation
type L = rm.ParticipantLifecycle
type O = rm.ParticipantLifecycleOptions
type PS = m.Session
type SS = rm.TrackedSession
type CV = m.SessionCloseValue
type Ch = <-chan struct{}
type Out = m.SessionSendOutcome

type csend interface{ SendMessage(context.Context, N) bool }
type csendNoResponse interface{ SendMessageWithoutResponse(context.Context, N) bool }
type ccaps interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}
type terr interface{ TerminalError() error }
type mediaForwarder interface {
	RTCMedia() (audio.MediaEndpoints, bool)
}

type trackedSession struct {
	PS
	lifecycle L
	a         Ch
	once      sync.Once
	closeErr  error
}
type connectionTracker struct {
	inner     m.SessionInferencer
	lifecycle L
	a         Ch
	once      sync.Once
	mu        sync.Mutex
	ready     bool
	err       error
	sink      func(error)
}
type tr = trackedSession
type ct = connectionTracker

func pick[T any](ok bool, yes, no T) T { return map[bool]T{true: yes, false: no}[ok] }
func pickCall[T any](ok bool, yes, no func() T) T {
	return map[bool]func() T{true: yes, false: no}[ok]()
}
func doIf(ok bool, f func())    { map[bool]func(){true: f, false: func() {}}[ok]() }
func first(a, b string) string  { return pick(a != "", a, b) }
func call(f func() error) error { return pickCall(f != nil, f, func() error { return nil }) }
func validID(id string) bool    { return strings.TrimSpace(id) != "" }
func channelClosed(ch Ch) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
func cancelOnly(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	j, ok := err.(interface{ Unwrap() []error })
	return ok && allCancel(j.Unwrap())
}
func allCancel(errs []error) bool {
	for _, err := range errs {
		if !cancelOnly(err) {
			return false
		}
	}
	return true
}

func NewTrackedSession(session PS, lifecycle L, admission Ch) SS {
	return &trackedSession{PS: session, lifecycle: lifecycle, a: admission}
}
func newTrackedSession(session PS, lifecycle L, admission Ch) *tr {
	return &trackedSession{PS: session, lifecycle: lifecycle, a: admission}
}
func (s *tr) SessionAdmissionClosed() bool { return s != nil && channelClosed(s.a) }
func (s *tr) SessionAdmissionAllows(msg M) bool {
	return s != nil && (!s.SessionAdmissionClosed() || msg.Type == sCancel || msg.Type == sClose || s.lifecycle != nil && s.lifecycle.AdmitSessionMessageAfterBound(msg))
}
func (s *tr) completeAfterBound(msg N) bool {
	return s.lifecycle != nil && s.lifecycle.AdmitCompleteToolResultAfterBound(msg)
}
func (s *tr) SessionAdmissionAllowsCompleteMessage(msg N) bool {
	return s != nil && (!s.SessionAdmissionClosed() || s.completeAfterBound(msg))
}
func (s *tr) Send(ctx context.Context, msg M) bool { return s.SendWithOutcome(ctx, msg).OK() }
func (s *tr) closeOnce() {
	s.closeErr = s.PS.Close()
	doIf(s.lifecycle != nil, func() { s.lifecycle.MarkOwnedSessionClosed(s.closeErr) })
}
func (s *tr) Close() error {
	return pickCall(s != nil && s.PS != nil, func() error { s.once.Do(s.closeOnce); return s.closeErr }, func() error { return nil })
}
func sessionOutcome(ctx context.Context, session PS, msg M) Out {
	sender, ok := session.(m.SessionSendOutcomeSender)
	return pickCall(ok, func() Out { return sender.SendWithOutcome(ctx, msg) }, func() Out { return m.SendSessionWithOutcome(ctx, session, msg) })
}
func cancelledOut() Out { return Out{Status: m.SessionSendCancelled, Err: context.Canceled} }
func (s *tr) recordSent(msg M) {
	doIf(msg.Type == sToolEnd, func() { s.lifecycle.RecordToolResultSend(s.lifecycle.ToolCallID(msg), true, false) })
	doIf(msg.Type == sCreate, func() { s.lifecycle.RecordToolContinuationRequest(true) })
	doIf(msg.Type == sCancel, func() { s.lifecycle.RecordResponseCancellation() })
}
func (s *tr) sendOutcome(ctx context.Context, msg M) Out {
	out := sessionOutcome(ctx, s.PS, msg)
	doIf(out.OK() && s.lifecycle != nil, func() { s.recordSent(msg) })
	return out
}
func (s *tr) SendWithOutcome(ctx context.Context, msg M) Out {
	return pickCall(s != nil && s.PS != nil && s.SessionAdmissionAllows(msg), func() Out { return s.sendOutcome(ctx, msg) }, func() Out { return cancelledOut() })
}
func (s *tr) request(ctx context.Context) Out {
	out := m.RequestSessionResponse(ctx, s.PS)
	doIf(out.OK() && s.lifecycle != nil, func() { s.lifecycle.RecordToolContinuationRequest(true) })
	return out
}
func (s *tr) RequestResponse(ctx context.Context) Out {
	return pickCall(s != nil && s.PS != nil && (!s.SessionAdmissionClosed() || s.lifecycle != nil && s.lifecycle.AdmitSessionMessageAfterBound(M{Type: sCreate})), func() Out { return s.request(ctx) }, func() Out { return cancelledOut() })
}
func (s *tr) SupportsResponseRequests() bool {
	return s != nil && m.SupportsSessionResponseRequests(s.PS)
}
func sendComplete(session PS, ctx context.Context, msg N, noResponse bool) bool {
	return pickCall(noResponse, func() bool {
		sender, ok := session.(csendNoResponse)
		return ok && sender.SendMessageWithoutResponse(ctx, msg)
	}, func() bool { sender, ok := session.(csend); return ok && sender.SendMessage(ctx, msg) })
}
func (s *tr) SendMessage(ctx context.Context, msg N) bool { return s.complete(ctx, msg, false) }
func (s *tr) SendMessageWithoutResponse(ctx context.Context, msg N) bool {
	return s.complete(ctx, msg, true)
}
func (s *tr) complete(ctx context.Context, msg N, noResponse bool) bool {
	return pickCall(s != nil && s.PS != nil && (!s.SessionAdmissionClosed() || s.completeAfterBound(msg)), func() bool {
		accepted := sendComplete(s.PS, ctx, msg, noResponse)
		doIf(accepted && s.lifecycle != nil, func() { s.lifecycle.RecordToolResultSend(msg.ToolCallID, true, !noResponse) })
		return accepted
	}, func() bool { return false })
}
func capabilities(session PS) (bool, bool) {
	c, ok := session.(ccaps)
	if ok {
		return c.SupportsCompleteMessages(), c.SupportsCompleteMessagesWithoutResponse()
	}
	_, complete := session.(csend)
	_, noResponse := session.(csendNoResponse)
	return complete, noResponse
}
func capFor(session PS, noResponse bool) bool {
	complete, withoutResponse := capabilities(session)
	return pick(noResponse, withoutResponse, complete)
}
func (s *tr) SupportsCompleteMessages() bool                { return s != nil && capFor(s.PS, false) }
func (s *tr) SupportsCompleteMessagesWithoutResponse() bool { return s != nil && capFor(s.PS, true) }
func (s *tr) TerminalError() error {
	return pickCall(s != nil && s.PS != nil, func() error {
		source, ok := s.PS.(terr)
		return pickCall(ok, func() error { return source.TerminalError() }, func() error { return nil })
	}, func() error { return nil })
}
func (s *tr) RTCMedia() (audio.MediaEndpoints, bool) {
	if s == nil || s.PS == nil {
		return audio.MediaEndpoints{}, false
	}
	if owner, ok := s.PS.(audio.MediaSession); ok {
		return owner.RTCMedia(), true
	}
	if forwarder, ok := s.PS.(mediaForwarder); ok {
		return forwarder.RTCMedia()
	}
	return audio.MediaEndpoints{}, false
}

func NewConnectionTracker(inner m.SessionInferencer, lifecycle L, admission Ch) rm.ParticipantConnectionTracker {
	return &connectionTracker{inner: inner, lifecycle: lifecycle, a: admission}
}
func (i *ct) SetOutcomeSink(sink func(error)) {
	doIf(i != nil, func() {
		i.mu.Lock()
		i.sink = sink
		ready, err := i.ready, i.err
		i.mu.Unlock()
		doIf(ready && sink != nil, func() { sink(err) })
	})
}
func (i *ct) publishOnce(err error) {
	i.mu.Lock()
	i.ready, i.err = true, err
	sink := i.sink
	i.mu.Unlock()
	doIf(sink != nil, func() { sink(err) })
}
func (i *ct) publish(err error) { doIf(i != nil, func() { i.once.Do(func() { i.publishOnce(err) }) }) }
func (i *ct) Outcome() (error, bool) {
	if i == nil {
		return nil, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.err, i.ready
}
func (i *ct) track(session PS) *tr {
	tracked := newTrackedSession(session, i.lifecycle, i.a)
	doIf(i.lifecycle != nil, func() {
		i.lifecycle.MarkSessionCreated()
		i.lifecycle.SetOwnedSession(tracked)
		i.lifecycle.SetTransportDone(tracked.Done(), tracked.TerminalError)
	})
	return tracked
}
func (i *ct) watch(ctx context.Context, session *tr) {
	doIf(i.lifecycle != nil, func() { go i.watchGo(ctx, session) })
}
func (i *ct) watchGo(ctx context.Context, session *tr) {
	select {
	case <-session.Done():
		i.lifecycle.MarkTransportEndedWithError(session.TerminalError())
	case <-ctx.Done():
		doIf(channelClosed(session.Done()), func() { i.lifecycle.MarkTransportEndedWithError(session.TerminalError()) })
		doIf(!channelClosed(session.Done()), func() { closeQuietly(session) })
	}
}
func closeQuietly(session PS) {
	if err := session.Close(); err != nil {
		return
	}
}
func (i *ct) invalid() (PS, error) {
	err := errors.New("room participant session inferencer is nil")
	i.publish(err)
	return nil, err
}
func (i *ct) connectFailure(session PS, err error) (PS, error) {
	if session != nil {
		tracked := i.track(session)
		closeErr := tracked.Close()
		err = pick(closeErr != nil, errors.Join(err, fmt.Errorf("close failed session: %w", closeErr)), err)
		i.publish(err)
		return nil, err
	}
	i.publish(err)
	return nil, err
}
func (i *ct) connect(ctx context.Context) (PS, error) {
	session, err := i.inner.ConnectSession(ctx)
	err = pick(err == nil && session == nil, errors.New("room participant session is nil"), err)
	if err != nil {
		return i.connectFailure(session, err)
	}
	tracked := i.track(session)
	i.publish(nil)
	i.watch(ctx, tracked)
	return tracked, nil
}
func (i *ct) ConnectSession(ctx context.Context) (PS, error) {
	if i == nil || i.inner == nil {
		return i.invalid()
	}
	return i.connect(ctx)
}

func toolCallID(msg M) string {
	return strings.TrimSpace(first(first(endID(msg), startID(msg)), msg.ToolCallId))
}
func endID(msg M) string {
	value, ok := msg.Value.(*m.ToolCallEndValue)
	return pickCall(ok && value != nil && validID(value.ToolCallID), func() string { return value.ToolCallID }, func() string { return "" })
}
func startID(msg M) string {
	value, ok := msg.Value.(*m.ToolCallStartValue)
	return pickCall(ok && value != nil && validID(value.ToolCallID), func() string { return value.ToolCallID }, func() string { return "" })
}
func outputState(opened bool, turns int) string {
	return pick(!opened || turns == 0, string(oNone), string(m.TerminalOutputPartial))
}
func termProv(disposition, reason string) string {
	byReason := map[string]string{string(rProviderComplete): string(m.TerminalProvenanceProvider), string(rLoopComplete): string(m.TerminalProvenanceLoop), string(rProviderClose): string(m.TerminalProvenanceSession), string(rFailure): string(m.TerminalProvenanceSession), string(rReplayComplete): string(m.TerminalProvenanceReplay), string(rReplayDivergence): string(m.TerminalProvenanceReplay), string(rReplayIncomplete): string(m.TerminalProvenanceReplay), string(rCancel): string(m.TerminalProvenanceLoop), string(rPartial): string(m.TerminalProvenanceLoop)}[reason]
	return pick(disposition == xCancelled, string(m.TerminalProvenanceRoom), pick(byReason != "", byReason, pick(disposition == xStopped, string(m.TerminalProvenanceLoop), string(m.TerminalProvenanceSession))))
}
func boundTrigger(reason rm.RoomTerminationReason, mid bool) string {
	base := map[rm.RoomTerminationReason]string{rm.RoomTerminationMaxTurnsReached: "max_turns_reached", rm.RoomTerminationMaxDurationReached: "max_duration_reached"}[reason]
	base = first(base, string(reason))
	return pick(mid && base != "stopped", base+"_mid_response", base)
}
func classClose(closeReason string, terminalReason m.TerminalReason) rm.ParticipantTerminationReason {
	return pick(closeReason == providerClosed || terminalReason == rProviderClose, pDisconnected, pick(terminalReason == rFailure, pError, pEnded))
}

var _ SS = (*tr)(nil)
var _ rm.ParticipantConnectionTracker = (*ct)(nil)
