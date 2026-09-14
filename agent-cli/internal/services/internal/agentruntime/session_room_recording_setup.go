package agentruntime

import (
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func openRoomEvidence(opts RoomRunOptions, validation room.ValidationOptions, replayMode bool, roomClock platformclock.Source) (roomevidence.Recorder, []string, RoomRunOptions, error) {
	if strings.TrimSpace(opts.OutputDir) == "" {
		return nil, nil, opts, nil
	}
	if opts.Evidence == nil {
		err := errors.New("room evidence service is unavailable")
		return nil, nil, opts, err
	}
	outputDir, err := opts.Evidence.PrepareOutput(opts.OutputDir)
	if err != nil {
		return nil, nil, opts, err
	}
	opts.OutputDir = outputDir
	var secrets []string
	if !replayMode {
		secrets = roomCredentialSecrets(opts.Manifest, validation)
	}
	recorder, err := opts.Evidence.Open(roomevidence.Options{
		Destination: outputDir,
		Manifest:    opts.Manifest,
		AudioFormat: runtimeAudioFormat(roomFormatForOptions(opts)),
		Secrets:     secrets,
		StartedAt:   roomClock.Now().UTC(),
		Clock:       roomClock,
	})
	if err != nil {
		return nil, secrets, opts, err
	}
	if opts.onRoomRecorderReady != nil {
		opts.onRoomRecorderReady(recorder)
	}
	return recorder, secrets, opts, nil
}

type roomParticipantFailureHandler struct {
	runtime     *roomParticipantRuntime
	evidence    roomevidence.Recorder
	coordinator *roomCoordinator
}

func (h roomParticipantFailureHandler) observe(observation sessionTerminalObservation) {
	if !observation.Failure || observation.Classification == providers.ErrorClassCancellation || h.providerClose(observation) {
		return
	}
	h.recordProviderError(observation)
	failure := roomParticipantFailure(h.runtime.plan.manifest.ID, h.failureError(observation), secretsForPlan(h.runtime.plan))
	if observation.TerminalProvenance == string(messages.TerminalProvenanceProvider) && observation.FailingEvent == string(messages.StreamTypeError) {
		h.coordinator.fail(failure)
		return
	}
	h.coordinator.failParticipant(h.runtime.plan.manifest.ID, failure)
}

func (h roomParticipantFailureHandler) providerClose(observation sessionTerminalObservation) bool {
	return observation.TerminalReason == string(messages.TerminalReasonProviderClose) && observation.FailingEvent == string(messages.StreamTypeSessionClose)
}

func (h roomParticipantFailureHandler) recordProviderError(observation sessionTerminalObservation) {
	if h.evidence == nil || (observation.TerminalProvenance != string(messages.TerminalProvenanceProvider) && observation.FailingEvent != string(messages.StreamTypeError)) {
		return
	}
	fields := map[string]string{"classification": observation.Classification}
	if observation.Code != "" {
		fields["code"] = observation.Code
	}
	_ = h.evidence.RecordProviderErrorTimeline(h.runtime.plan.manifest.ID, fields)
}

func (h roomParticipantFailureHandler) failureError(observation sessionTerminalObservation) error {
	if h.runtime.lifecycle != nil {
		if transportErr := h.runtime.lifecycle.transportTerminalErrorSnapshot(); transportErr != nil {
			return transportErr
		}
	}
	if observation.Err != nil {
		return observation.Err
	}
	return errors.New("session stream error")
}

func replayParticipantArtifact(participant RoomReplayParticipant, role string) (RoomReplayArtifact, bool) {
	for _, artifact := range participant.Artifacts {
		if artifact.Role == role || artifact.Name == role {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}
