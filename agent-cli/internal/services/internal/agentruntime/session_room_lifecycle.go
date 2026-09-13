package agentruntime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeRoomsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRoomOutputDir           = "room-run"
	DefaultRoomBoundShutdownGrace  = 250 * time.Millisecond
	roomBoundResponseCancelTimeout = 50 * time.Millisecond
	roomCleanupTimeout             = time.Second
	roomAdmissionTimeout           = 5 * time.Second
)

type roomParticipantPlan struct {
	manifest              room.Participant
	options               SessionRunOptions
	inferencer            messages.SessionInferencer
	startupErr            error
	replay                bool
	replayLoop            sessionLoopOptions
	secret                string
	tracker               *roomConnectTrackingInferencer
	participant           *roomParticipantRuntime
	inputAudioSampleRate  int
	capabilityCoordinator SessionCapabilityCoordinator
}
type roomParticipantRuntime struct {
	plan             *roomParticipantPlan
	ctx              context.Context
	cancel           context.CancelFunc
	admissionCtx     context.Context
	admissionCancel  context.CancelFunc
	loopReady        chan *agentloop.AgentLoop
	participantDone  chan struct{}
	mixerDone        chan struct{}
	observerDone     chan struct{}
	observerOnce     sync.Once
	replayFrameAcks  chan struct{}
	mixer            *room.PCM16Mixer
	ingress          *roomAudioIngressLedger
	input            *devicegw.DeviceSource
	output           *devicegw.DeviceSink
	lifecycle        *roomParticipantLifecycle
	diagnosticSink   SessionDiagnosticSink
	outboundLoudness *audio.LoudnessNormalizer
}

func (r *roomParticipantRuntime) markObserverDone() {
	if r == nil || r.observerDone == nil {
		return
	}
	r.observerOnce.Do(func() { close(r.observerDone) })
}

type roomCleanupWaiter struct {
	timer *time.Timer
}

func (w *roomCleanupWaiter) start() {
	if w == nil || w.timer != nil {
		return
	}
	w.timer = time.NewTimer(roomCleanupTimeout)
}
func (w *roomCleanupWaiter) done() <-chan time.Time {
	if w == nil || w.timer == nil {
		return nil
	}
	return w.timer.C
}
func (w *roomCleanupWaiter) stop() {
	if w == nil || w.timer == nil {
		return
	}
	if !w.timer.Stop() {
		select {
		case <-w.timer.C:
		default:
		}
	}
	w.timer = nil
}

type roomLifecycleWorkError struct {
	outstanding []string
}

func (e *roomLifecycleWorkError) Error() string {
	if e == nil || len(e.outstanding) == 0 {
		return "room lifecycle work did not complete"
	}
	return "room lifecycle work did not complete: " + strings.Join(e.outstanding, "; ")
}
func newRoomLifecycleWorkError(outstanding ...string) error {
	seen := make(map[string]struct{}, len(outstanding))
	ordered := make([]string, 0, len(outstanding))
	for _, item := range outstanding {
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		ordered = append(ordered, item)
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	return &roomLifecycleWorkError{outstanding: ordered}
}
func roomLifecycleWorkLabel(participantID, phase string) string {
	if participantID == "" {
		return phase
	}
	return fmt.Sprintf("participant %q phase %s", participantID, phase)
}
func roomParticipantOutstandingWork(runtime *roomParticipantRuntime) []string {
	if runtime == nil || runtime.plan == nil {
		return []string{"participant runtime"}
	}
	id := runtime.plan.manifest.ID
	outstanding := make([]string, 0, 6)
	if runtime.plan.startupErr != nil {
	} else if roomParticipantIsHuman(runtime.plan) {
		if runtime.lifecycle == nil || !runtime.lifecycle.deviceHasReady() {
			outstanding = append(outstanding, roomLifecycleWorkLabel(id, "devices"))
		}
	} else if runtime.plan.tracker == nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "connect"))
	} else if _, ready := runtime.plan.tracker.outcome(); !ready {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "connect"))
	}
	if runtime.lifecycle == nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "lifecycle"))
		return outstanding
	}
	created, closed, transportDone, closeErr := runtime.lifecycle.ownedSessionSnapshot()
	if created && !closed {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.close"))
	}
	if closeErr != nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.close.error"))
	}
	if created && !roomChannelClosed(transportDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.transport"))
	}
	if runtime.participantDone != nil && !roomChannelClosed(runtime.participantDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "participant.loop"))
	}
	if runtime.mixerDone != nil && !roomChannelClosed(runtime.mixerDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "mixer"))
	}
	if runtime.observerDone != nil && !roomChannelClosed(runtime.observerDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "observer"))
	}
	return outstanding
}

// Deprecated: retained only as a decision-free adapter for the legacy room host.
type roomParticipantLifecycle struct {
	stateChanged    chan<- struct{}
	admissionClosed <-chan struct{}
	backend         runtimeRooms.ParticipantLifecycle
	backendOnce     sync.Once
}

func (l *roomParticipantLifecycle) backendLifecycle() runtimeRooms.ParticipantLifecycle {
	if l == nil {
		return nil
	}
	l.backendOnce.Do(func() {
		l.backend = runtimeRoomsWire.NewParticipantLifecycle(runtimeRooms.ParticipantLifecycleOptions{StateChanged: l.stateChanged, AdmissionClosed: l.admissionClosed})
	})
	return l.backend
}
func (l *roomParticipantLifecycle) markConnected(err error) {
	if backend := l.backendLifecycle(); backend != nil {
		backend.MarkConnected(err)
	}
}
func (l *roomParticipantLifecycle) markDeviceReady() {
	if backend := l.backendLifecycle(); backend != nil {
		backend.MarkDeviceReady()
	}
}
func (l *roomParticipantLifecycle) deviceHasReady() bool {
	backend := l.backendLifecycle()
	return backend != nil && backend.DeviceHasReady()
}
func (l *roomParticipantLifecycle) setOwnedSession(session *roomTrackedSession) {
	if backend := l.backendLifecycle(); backend != nil {
		if session == nil {
			backend.SetOwnedSession(nil)
			return
		}
		backend.SetOwnedSession(session.delegateSession())
	}
}
func (l *roomParticipantLifecycle) closeOwnedSession() error {
	if backend := l.backendLifecycle(); backend != nil {
		return backend.CloseOwnedSession()
	}
	return nil
}
func (l *roomParticipantLifecycle) markParticipantFailure(err error) {
	if backend := l.backendLifecycle(); backend != nil {
		backend.MarkParticipantFailure(err)
	}
}
func (l *roomParticipantLifecycle) markLivenessFailure(err error) {
	if backend := l.backendLifecycle(); backend != nil {
		classification, reason, provenance, output := sessionLivenessMetadata(err)
		backend.MarkLivenessFailure(err, runtimeRooms.ParticipantLivenessMetadata{
			Classification: classification, TerminalReason: reason,
			TerminalProvenance: provenance, OutputState: output,
		})
	}
}
func (l *roomParticipantLifecycle) cancelActiveResponse() {
	if backend := l.backendLifecycle(); backend != nil {
		backend.CancelActiveResponse()
	}
}
func (l *roomParticipantLifecycle) admitResponseTerminal() bool {
	backend := l.backendLifecycle()
	return backend == nil || backend.AdmitResponseTerminal()
}
func (l *roomParticipantLifecycle) observeTerminal(observation sessionTerminalObservation) bool {
	backend := l.backendLifecycle()
	if backend == nil {
		return false
	}
	return backend.ObserveTerminal(runtimeRooms.SessionTerminalObservation{ResponseID: observation.ResponseID, Classification: observation.Classification, TerminalReason: observation.TerminalReason, TerminalProvenance: observation.TerminalProvenance, OutputState: observation.OutputState, Err: observation.Err, Failure: observation.Failure, RoomBound: observation.RoomBound, Code: observation.Code, FailingEvent: observation.FailingEvent})
}
func (l *roomParticipantLifecycle) observe(msg messages.StreamMessage) int {
	if backend := l.backendLifecycle(); backend != nil {
		return backend.Observe(msg)
	}
	return 0
}
func (l *roomParticipantLifecycle) observeAdmittedTurn() int {
	if backend := l.backendLifecycle(); backend != nil {
		return backend.ObserveAdmittedTurn()
	}
	return 0
}
func (l *roomParticipantLifecycle) transportHasEnded() bool {
	backend := l.backendLifecycle()
	return backend != nil && backend.TransportHasEnded()
}
func (l *roomParticipantLifecycle) transportTerminalErrorSnapshot() error {
	if backend := l.backendLifecycle(); backend != nil {
		return backend.TransportTerminalError()
	}
	return nil
}
func (l *roomParticipantLifecycle) markCoordinatorStopping(bound bool, reason ...RoomTerminationReason) {
	backend := l.backendLifecycle()
	if backend == nil {
		return
	}
	converted := make([]runtimeRooms.RoomTerminationReason, len(reason))
	for index, value := range reason {
		converted[index] = runtimeRooms.RoomTerminationReason(value)
	}
	backend.MarkCoordinatorStopping(bound, converted...)
}
func (l *roomParticipantLifecycle) markBoundCancellation() {
	if backend := l.backendLifecycle(); backend != nil {
		backend.MarkBoundCancellation()
	}
}
func (l *roomParticipantLifecycle) markRunDone(err error) {
	if backend := l.backendLifecycle(); backend != nil {
		backend.MarkRunDone(err)
	}
}
func (l *roomParticipantLifecycle) runHasFinished() bool {
	backend := l.backendLifecycle()
	return backend != nil && backend.RunHasFinished()
}
func (l *roomParticipantLifecycle) snapshot() (connected, sessionOpened, sessionClosed bool, closeReason string, terminalReason messages.TerminalReason, turns int, connectErr error) {
	backend := l.backendLifecycle()
	if backend == nil {
		return false, false, false, "", "", 0, nil
	}
	snapshot := backend.Snapshot()
	return snapshot.Connected, snapshot.SessionOpened, snapshot.SessionClosed, snapshot.CloseReason, snapshot.TerminalReason, snapshot.Turns, snapshot.ConnectErr
}
func (l *roomParticipantLifecycle) terminal() (ParticipantTerminationReason, error, bool) {
	backend := l.backendLifecycle()
	if backend == nil {
		return "", nil, false
	}
	reason, err, observed := backend.Terminal()
	return ParticipantTerminationReason(reason), err, observed
}
func (l *roomParticipantLifecycle) terminalMetadata() (string, messages.TerminalReason, messages.TerminalProvenance, messages.TerminalOutputState) {
	backend := l.backendLifecycle()
	if backend == nil {
		return "", "", "", ""
	}
	return backend.TerminalMetadata()
}
func (l *roomParticipantLifecycle) terminalObservationSnapshot() roomParticipantTerminalObservation {
	backend := l.backendLifecycle()
	if backend == nil {
		return roomParticipantTerminalObservation{}
	}
	observation := backend.TerminalObservationSnapshot()
	return roomParticipantTerminalObservation{
		terminationTrigger: observation.TerminationTrigger, terminationDisposition: observation.TerminationDisposition,
		classification: observation.Classification, terminalReason: observation.TerminalReason,
		terminalProvenance: observation.TerminalProvenance, outputState: observation.OutputState,
		err: observation.Err, failure: observation.Failure,
	}
}
func (l *roomParticipantLifecycle) ownedSessionSnapshot() (created, closed bool, transportDone <-chan struct{}, closeErr error) {
	backend := l.backendLifecycle()
	if backend == nil {
		return false, false, nil, nil
	}
	snapshot := backend.OwnedSessionSnapshot()
	return snapshot.Created, snapshot.Closed, snapshot.TransportDone, snapshot.CloseErr
}

type roomConnectTrackingInferencer struct {
	inner       messages.SessionInferencer
	outcomes    chan<- roomConnectionOutcome
	once        sync.Once
	mu          sync.Mutex
	ready       bool
	connectErr  error
	lifecycle   *roomParticipantLifecycle
	backend     runtimeRooms.ParticipantConnectionTracker
	backendOnce sync.Once
}
type roomConnectionOutcome struct {
	tracker *roomConnectTrackingInferencer
	err     error
}

func newRoomConnectTrackingInferencer(inner messages.SessionInferencer) *roomConnectTrackingInferencer {
	return &roomConnectTrackingInferencer{inner: inner}
}
func (i *roomConnectTrackingInferencer) setOutcomeSink(outcomes chan<- roomConnectionOutcome) {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.outcomes = outcomes
	i.mu.Unlock()
}
func (i *roomConnectTrackingInferencer) publish(err error) {
	if i == nil {
		return
	}
	i.once.Do(func() {
		i.mu.Lock()
		i.ready = true
		i.connectErr = err
		sink := i.outcomes
		i.mu.Unlock()
		if sink != nil {
			sink <- roomConnectionOutcome{tracker: i, err: err}
		}
	})
}
func (i *roomConnectTrackingInferencer) outcome() (error, bool) {
	if i == nil {
		return nil, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connectErr, i.ready
}
func (i *roomConnectTrackingInferencer) runtimeTracker() runtimeRooms.ParticipantConnectionTracker {
	if i == nil {
		return nil
	}
	i.backendOnce.Do(func() {
		var participant runtimeRooms.ParticipantLifecycle
		var admissionClosed <-chan struct{}
		if i.lifecycle != nil {
			participant = i.lifecycle.backendLifecycle()
			admissionClosed = i.lifecycle.admissionClosed
		}
		i.backend = runtimeRoomsWire.NewConnectionTracker(i.inner, participant, admissionClosed)
		i.backend.SetOutcomeSink(i.publish)
	})
	return i.backend
}
func (i *roomConnectTrackingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil {
		err := errors.New("room participant session inferencer is nil")
		return nil, err
	}
	backend := i.runtimeTracker()
	session, err := backend.ConnectSession(ctx)
	if err != nil || session == nil || i.lifecycle == nil {
		return session, err
	}
	tracked := &roomTrackedSession{
		Session: session, lifecycle: i.lifecycle, admissionClosed: i.lifecycle.admissionClosed,
	}
	i.lifecycle.setOwnedSession(tracked)
	return tracked, nil
}

var _ messages.SessionInferencer = (*roomConnectTrackingInferencer)(nil)
var _ = classifyRoomSessionClose
