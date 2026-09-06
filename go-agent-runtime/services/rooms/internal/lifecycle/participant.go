package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func (r Runner) openParticipant(ctx context.Context, state *runState, participant rooms.Participant, request rooms.RoomRunOptions, recorder *evidence.Recorder) (*activeParticipant, error) {
	kind := roommanifest.NormalizeParticipantKind(participant.Kind)
	if kind == rooms.ParticipantKindHuman && request.ReplayPlan != nil {
		return &activeParticipant{participant: participant, finished: make(chan struct{})}, nil
	}
	local, err := r.openLocalMedia(ctx, participant, kind, request)
	if err != nil {
		return nil, err
	}
	if kind == rooms.ParticipantKindHuman {
		return humanParticipant(participant, local)
	}
	return r.openAgent(ctx, state, participant, request, recorder, local)
}

func (r Runner) openLocalMedia(ctx context.Context, participant rooms.Participant, kind rooms.ParticipantKind, request rooms.RoomRunOptions) (rooms.MediaPorts, error) {
	if kind == rooms.ParticipantKindHuman && r.media == nil {
		return rooms.MediaPorts{}, fmt.Errorf("human participant media factory is unavailable")
	}
	if r.media == nil || request.ReplayPlan != nil {
		return rooms.MediaPorts{}, nil
	}
	local, err := r.media.OpenMedia(ctx, participant, request.AudioFormat)
	if err != nil {
		return rooms.MediaPorts{}, fmt.Errorf("open participant media: %w", err)
	}
	return local, nil
}

func humanParticipant(participant rooms.Participant, local rooms.MediaPorts) (*activeParticipant, error) {
	if local.Capture == nil && local.Playback == nil {
		return nil, closeLocalMedia(local, errors.New("human participant has no media endpoints"))
	}
	return &activeParticipant{participant: participant, media: local, finished: make(chan struct{})}, nil
}

func (r Runner) openAgent(ctx context.Context, state *runState, participant rooms.Participant, request rooms.RoomRunOptions, recorder *evidence.Recorder, local rooms.MediaPorts) (*activeParticipant, error) {
	liveRequest := newLiveRequest(participant)
	release, err := r.configureCapabilities(ctx, participant, request, &liveRequest)
	if err != nil {
		return nil, closeWithCapabilities(closeLocalMedia(local, err), release)
	}
	if request.ReplayPlan != nil {
		replay, replayErr := replayRequest(*request.ReplayPlan, participant)
		if replayErr != nil {
			return nil, closeWithCapabilities(closeLocalMedia(local, replayErr), release)
		}
		liveRequest.Replay = replay
	}
	if recorder != nil && strings.TrimSpace(liveRequest.Replay.OutputCapturePath) == "" {
		liveRequest.Replay.OutputCapturePath = recorder.CapturePath(participant.ID)
	}
	return r.admitLive(ctx, state, participant, request, local, liveRequest, release)
}

func newLiveRequest(participant rooms.Participant) session.LiveRequest {
	return session.LiveRequest{
		SessionID: participant.ID, ParticipantID: participant.ID,
		Provider: participant.Provider, Model: participant.Model,
		CredentialReference: participant.APIKeyEnv,
		Instructions:        participant.SystemPrompt, OpeningPrompt: participant.OpeningPrompt,
		Voice: participant.Voice, ToolNames: append([]string(nil), participant.Tools...),
		ProviderLiveness: session.LiveLivenessPolicy{Enabled: true},
	}
}

func (r Runner) configureCapabilities(ctx context.Context, participant rooms.Participant, request rooms.RoomRunOptions, liveRequest *session.LiveRequest) (func() error, error) {
	if request.LiveCapabilitiesFactory != nil {
		binding, err := request.LiveCapabilitiesFactory(ctx, *liveRequest)
		if err != nil {
			return nil, fmt.Errorf("resolve participant capabilities: %w", err)
		}
		release := ownCapabilities(&binding)
		liveRequest.Capabilities = &binding
		return release, nil
	}
	if participant.BrowserTools == nil {
		return nil, nil
	}
	if request.BrowserCapabilitiesFactory == nil {
		return nil, fmt.Errorf("participant %q browser capabilities are unavailable", participant.ID)
	}
	browser, err := request.BrowserCapabilitiesFactory(participant)
	if err != nil {
		return nil, fmt.Errorf("configure participant browser tools: %w", err)
	}
	owner := newBrowserCapabilityHandle(browser)
	liveRequest.Capabilities = &session.LiveCapabilities{
		Executor: browser.Executor, Definitions: append([]messages.ToolDefinition(nil), browser.Definitions...), Handle: owner,
	}
	return owner.Close, nil
}

func (r Runner) admitLive(ctx context.Context, state *runState, participant rooms.Participant, request rooms.RoomRunOptions, local rooms.MediaPorts, liveRequest session.LiveRequest, release func() error) (*activeParticipant, error) {
	handle, err := r.live.OpenLive(ctx, liveRequest)
	if err != nil {
		return nil, closeWithCapabilities(closeLocalMedia(local, fmt.Errorf("open live participant: %w", err)), release)
	}
	if handle == nil {
		return nil, closeWithCapabilities(closeLocalMedia(local, errors.New("live service returned a nil participant handle")), release)
	}
	active := &activeParticipant{participant: participant, handle: handle, endpoints: handle.Media(), media: local, finished: make(chan struct{})}
	active.events = newEventDrain(ctx, handle.Events(), participant.ID, request.OnDiagnostic, request.EventSink, func() { state.noteTurn(participant.ID) }, r.currentTime, func(event session.LiveEvent) {
		state.noteTerminal(participant.ID, event)
		if err := terminalLivenessFailure(event); err != nil {
			state.failParticipant(participant.ID, err)
		}
	}, state.setFailure)
	if err := handle.Start(ctx); err != nil {
		return nil, closeStartedParticipant(active, release, fmt.Errorf("start live participant: %w", err))
	}
	startCancellationWatcher(ctx, active)
	return active, nil
}

func closeStartedParticipant(active *activeParticipant, release func() error, runErr error) error {
	if active == nil {
		return closeWithCapabilities(runErr, release)
	}
	active.events.Stop()
	if eventErr := active.events.Wait(); eventErr != nil {
		runErr = errors.Join(runErr, eventErr)
	}
	if handleErr := active.handle.Close(); handleErr != nil {
		runErr = errors.Join(runErr, handleErr)
	}
	return closeWithCapabilities(closeLocalMedia(active.media, runErr), release)
}

func startCancellationWatcher(ctx context.Context, active *activeParticipant) {
	go func() {
		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if cause == nil {
				cause = ctx.Err()
			}
			active.handle.Cancel(cause)
		case <-active.finished:
		}
	}()
}

func closeLocalMedia(local rooms.MediaPorts, runErr error) error {
	if closeErr := local.Close(); closeErr != nil {
		return errors.Join(runErr, fmt.Errorf("close participant media: %w", closeErr))
	}
	return runErr
}

func ownCapabilities(binding *session.LiveCapabilities) func() error {
	if binding == nil {
		return nil
	}
	if binding.Handle != nil {
		return binding.Handle.Close
	}
	if binding.Close == nil {
		return nil
	}
	closeCapability := binding.Close
	var once sync.Once
	var closeErr error
	binding.Close = func() error {
		once.Do(func() { closeErr = closeCapability() })
		return closeErr
	}
	return binding.Close
}

func closeWithCapabilities(runErr error, release func() error) error {
	if release == nil {
		return runErr
	}
	if closeErr := release(); closeErr != nil {
		return errors.Join(runErr, fmt.Errorf("close participant capabilities: %w", closeErr))
	}
	return runErr
}
