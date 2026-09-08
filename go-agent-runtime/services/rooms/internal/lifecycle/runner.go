// Package lifecycle owns invocation-scoped room participants. It deliberately
// knows only the public session live contract and the room media ports.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var (
	errDurationBound = errors.New("room duration bound reached")
	errTurnsBound    = errors.New("room turn bound reached")
)

// Dependencies are the two runtime roles required by a live room. A nil
// MediaFactory is valid for headless provider sessions and keeps text-only
// hosts free of device initialization.
type Dependencies struct {
	Live  session.LiveService
	Media rooms.MediaFactory
	Clock platformclock.Scheduler
}

type Runner struct {
	live  session.LiveService
	media rooms.MediaFactory
	clock platformclock.Scheduler
	now   func() time.Time
}

func New(dependencies Dependencies) Runner {
	var now func() time.Time
	if dependencies.Clock != nil {
		now = dependencies.Clock.Now
	}
	return Runner{live: dependencies.Live, media: dependencies.Media, clock: dependencies.Clock, now: now}
}

func (r Runner) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Time{}
}

func (r Runner) Run(ctx context.Context, _ io.Writer, request rooms.RoomRunOptions) (rooms.RoomResult, error) {
	ctx = nonNilContext(ctx)
	manifest := requestManifest(request)
	if err := r.validateRun(manifest); err != nil {
		return rooms.RoomResult{}, err
	}
	recorder, err := r.newRecorder(request, manifest)
	if err != nil {
		return rooms.RoomResult{}, err
	}
	request = installRecorder(request, recorder)

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	state := newRunState(manifest, cancel)
	stopTimer := r.startDurationBound(runCtx, manifest.Room.MaxDuration, state)
	defer stopTimer()
	r.openParticipants(ctx, runCtx, state, manifest, request, recorder)
	graph := r.startGraph(runCtx, state, request, recorder)
	state.waitAll(runCtx, request, r.currentTime)
	return r.finishRun(runCtx, state, graph, manifest, request, recorder)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func requestManifest(request rooms.RoomRunOptions) rooms.Manifest {
	manifest := request.Manifest
	if isZeroManifest(manifest) && request.ReplayPlan != nil {
		manifest = request.ReplayPlan.Manifest()
	}
	if isZeroManifest(manifest) && request.LaunchPlan != nil {
		manifest = request.LaunchPlan.Manifest
	}
	return manifest
}

func (r Runner) validateRun(manifest rooms.Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if r.clock == nil {
		return rooms.ErrRoomClockUnavailable
	}
	if hasAgent(manifest) && r.live == nil {
		return rooms.ErrRoomServiceUnavailable
	}
	return nil
}

func newRunState(manifest rooms.Manifest, stop context.CancelCauseFunc) *runState {
	state := &runState{
		results:   make(map[string]rooms.RoomParticipantResult, len(manifest.Participants)),
		terminals: make(map[string]terminalMetadata, len(manifest.Participants)),
		turns:     make(map[string]int, len(manifest.Participants)), agentIDs: make(map[string]struct{}),
		turnsBound: manifest.Room.MaxTurns, stop: stop,
	}
	for _, participant := range manifest.Participants {
		if roommanifest.NormalizeParticipantKind(participant.Kind) != rooms.ParticipantKindAgent {
			continue
		}
		state.agentCount++
		state.agentIDs[participant.ID] = struct{}{}
	}
	return state
}

func (r Runner) startDurationBound(ctx context.Context, duration time.Duration, state *runState) func() {
	if duration <= 0 {
		return func() {}
	}
	durationCtx, cancel := r.clock.WithTimeout(ctx, duration)
	stop := make(chan struct{})
	go watchDurationBound(durationCtx, stop, state)
	return func() {
		close(stop)
		cancel()
	}
}

func watchDurationBound(ctx context.Context, stop <-chan struct{}, state *runState) {
	select {
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			state.setBound(rooms.RoomTerminationMaxDurationReached, errDurationBound)
		}
	case <-stop:
	}
}

func (r Runner) openParticipants(ctx, runCtx context.Context, state *runState, manifest rooms.Manifest, request rooms.RoomRunOptions, recorder *evidence.Recorder) {
	for _, participant := range manifest.Participants {
		if err := ctx.Err(); err != nil {
			return
		}
		r.openOneParticipant(runCtx, state, participant, request, recorder)
	}
}

func (r Runner) openOneParticipant(ctx context.Context, state *runState, participant rooms.Participant, request rooms.RoomRunOptions, recorder *evidence.Recorder) {
	active, err := r.openParticipant(ctx, state, participant, request, recorder)
	if err != nil {
		// Admission failures leave no viable participant runtime to retire. They
		// therefore remain room-scoped failures; media/provider faults after
		// admission are recorded on the participant and let surviving peers run.
		state.setFailure(err)
		state.finish(participant, rooms.ParticipantTerminationError, err)
		if request.OnDiagnostic != nil {
			request.OnDiagnostic(participant.ID, diagnostic("participant_open_failed", err, r.currentTime))
		}
		return
	}
	participantID := participant.ID
	var mediaFailureOnce sync.Once
	active.onMediaError = func(mediaErr error) {
		mediaFailureOnce.Do(func() {
			state.failParticipant(participantID, mediaErr)
			if request.OnDiagnostic != nil {
				request.OnDiagnostic(participantID, diagnostic("participant_media_failed", mediaErr, r.currentTime))
			}
		})
	}
	state.add(active)
	if recorder != nil {
		recorder.RecordTimeline("participant_joined", participant.ID, map[string]string{"kind": string(roommanifest.NormalizeParticipantKind(participant.Kind))})
	}
	if request.OnParticipantReady != nil {
		request.OnParticipantReady(rooms.RoomParticipantReady{
			ID: participant.ID, ParticipantID: participant.ID, Kind: roommanifest.NormalizeParticipantKind(participant.Kind),
			InputDevice: participant.InputDevice, OutputDevice: participant.OutputDevice,
			Provider: participant.Provider, Model: participant.Model,
		})
	}
}

func (r Runner) startGraph(ctx context.Context, state *runState, request rooms.RoomRunOptions, recorder *evidence.Recorder) *roomGraph {
	active := state.snapshotActive()
	if !needsRoomGraph(active) {
		return nil
	}
	graph, err := newRoomGraph(ctx, r.clock, request.AudioFormat, active, state.setFailure, recorder)
	if err == nil {
		for _, participant := range active {
			if participant == nil {
				continue
			}
			participantID := participant.participant.ID
			participant.retire = func() { graph.retire(participantID) }
		}
		return graph
	}
	state.setFailure(err)
	if request.OnDiagnostic != nil {
		request.OnDiagnostic("room", diagnostic("room_graph_failed", err, r.currentTime))
	}
	return graph
}

func (r Runner) finishRun(ctx context.Context, state *runState, graph *roomGraph, manifest rooms.Manifest, request rooms.RoomRunOptions, recorder *evidence.Recorder) (rooms.RoomResult, error) {
	graphErr := error(nil)
	if graph != nil {
		graphErr = graph.Close()
	}
	result, runErr := state.result(ctx)
	finishMissingParticipants(state, result, manifest)
	var refreshedErr error
	result, refreshedErr = state.result(ctx)
	runErr = errors.Join(runErr, refreshedErr)
	if graphErr != nil {
		runErr = errors.Join(runErr, graphErr)
		if result.TerminationReason != rooms.RoomTerminationFailed {
			result.TerminationReason, result.Reason = rooms.RoomTerminationFailed, rooms.RoomTerminationFailed
			result.Error = graphErr.Error()
		}
	}
	return r.finalizeRun(result, runErr, manifest, request, recorder)
}

func finishMissingParticipants(state *runState, result rooms.RoomResult, manifest rooms.Manifest) {
	for _, participant := range manifest.Participants {
		if _, ok := result.Participants[participant.ID]; ok {
			continue
		}
		state.finish(participant, rooms.ParticipantTerminationError, errors.New("participant did not enter the room"))
	}
}

func (r Runner) finalizeRun(result rooms.RoomResult, runErr error, manifest rooms.Manifest, request rooms.RoomRunOptions, recorder *evidence.Recorder) (rooms.RoomResult, error) {
	if request.OnParticipantTerminated != nil {
		for _, participant := range manifest.Participants {
			if value, ok := result.Participants[participant.ID]; ok {
				request.OnParticipantTerminated(value)
			}
		}
	}
	if recorder != nil {
		if finalizeErr := recorder.Finalize(result, runErr, r.currentTime()); finalizeErr != nil {
			// Finalization failures are represented by the recorder's health
			// projection; they must not replace the room's runtime result.
			recorder.ApplyResult(&result)
			return result, runErr
		}
		recorder.ApplyResult(&result)
	}
	return result, runErr
}

func (r Runner) newRecorder(request rooms.RoomRunOptions, manifest rooms.Manifest) (*evidence.Recorder, error) {
	if strings.TrimSpace(request.OutputDir) == "" || !manifest.Room.RecordingEnabled() {
		return nil, nil
	}
	return evidence.NewRecorder(request.OutputDir, manifest, request.AudioFormat, r.currentTime(), r.clock)
}

func installRecorder(request rooms.RoomRunOptions, recorder *evidence.Recorder) rooms.RoomRunOptions {
	if recorder == nil {
		return request
	}
	diagnosticCallback := request.OnDiagnostic
	request.OnDiagnostic = func(participantID string, record rooms.RoomDiagnosticRecord) {
		recorder.RecordDiagnostic(participantID, record)
		if diagnosticCallback != nil {
			diagnosticCallback(participantID, record)
		}
	}
	readyCallback := request.OnParticipantReady
	request.OnParticipantReady = func(value rooms.RoomParticipantReady) {
		recorder.SetReady(value)
		if readyCallback != nil {
			readyCallback(value)
		}
	}
	terminatedCallback := request.OnParticipantTerminated
	request.OnParticipantTerminated = func(value rooms.RoomParticipantResult) {
		recorder.SetTerminated(value)
		if terminatedCallback != nil {
			terminatedCallback(value)
		}
	}
	request.EventSink = recordingEventSink{host: request.EventSink, recorder: recorder}
	return request
}

type recordingEventSink struct {
	host     rooms.EventSink
	recorder *evidence.Recorder
}

func (s recordingEventSink) Publish(ctx context.Context, participantID string, event session.LiveEvent) error {
	var hostErr error
	if s.host != nil {
		hostErr = s.host.Publish(ctx, participantID, event)
	}
	if s.recorder != nil {
		// Recorder errors degrade evidence only. They must not interrupt a
		// healthy provider session or stall the bounded live event drain.
		if recordErr := s.recorder.Publish(ctx, participantID, event); recordErr != nil {
			// Publish records enqueue and artifact failures in the recorder's
			// health state. The host sink remains the only room-failure signal.
		}
	}
	return hostErr
}

func diagnostic(event string, err error, now func() time.Time) rooms.RoomDiagnosticRecord {
	fields := map[string]string{}
	if err != nil {
		fields["error"] = err.Error()
	}
	if now == nil {
		return rooms.RoomDiagnosticRecord{Event: event, Fields: fields}
	}
	return rooms.RoomDiagnosticRecord{Event: event, Fields: fields, At: now()}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func hasAgent(manifest rooms.Manifest) bool {
	for _, participant := range manifest.Participants {
		if roommanifest.NormalizeParticipantKind(participant.Kind) == rooms.ParticipantKindAgent {
			return true
		}
	}
	return false
}

func needsRoomGraph(participants []*activeParticipant) bool {
	for _, participant := range participants {
		if participant == nil {
			continue
		}
		if participant.media.Capture != nil || participant.media.Playback != nil {
			return true
		}
		if endpoints := participant.endpoints; endpoints.Inbound != nil || endpoints.Outbound != nil {
			return true
		}
	}
	return false
}

func isZeroManifest(manifest rooms.Manifest) bool {
	return manifest.SchemaVersion == 0 && manifest.Room.MaxTurns == 0 && manifest.Room.MaxDuration == 0 && !manifest.Room.Interactive && manifest.Room.Recording == nil && len(manifest.Participants) == 0
}

func replayRequest(plan rooms.RoomReplayPlan, participant rooms.Participant) (session.LiveReplayPolicy, error) {
	recorded, ok := plan.Participant(participant.ID)
	if !ok {
		return session.LiveReplayPolicy{}, fmt.Errorf("replay participant %q is missing", participant.ID)
	}
	if roommanifest.NormalizeParticipantKind(participant.Kind) == rooms.ParticipantKindAgent && recorded.CapturePath == "" {
		return session.LiveReplayPolicy{}, fmt.Errorf("replay participant %q has no input capture", participant.ID)
	}
	// The provider owns capture decoding. Fast timing keeps the session from
	// falling back to wall-clock sleeps; room-level evidence timing remains a
	// separate scheduler concern until the replay scheduler is injected here.
	return session.LiveReplayPolicy{InputCapturePath: recorded.CapturePath, Timing: session.LiveReplayTimingFast}, nil
}
