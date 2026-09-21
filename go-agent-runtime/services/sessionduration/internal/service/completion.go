package service

import (
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func (s *Service) Complete(request sessionduration.CompletionRequest) error {
	err := request.RunError
	completionFacts := sessionduration.CompletionFacts{
		ProviderToolCallObserved:     request.ProviderToolCallObserved,
		AssistantResponseCompleted:   request.AssistantResponseCompleted,
		ScheduledAudioIncomplete:     request.ScheduledAudioIncomplete,
		ScheduledAudioCompleted:      request.ScheduledAudioCompleted,
		ScheduledAudioDispatched:     request.ScheduledAudioDispatched,
		ScheduledAudioCount:          request.ScheduledAudioCount,
		ProviderScheduledStatus:      request.ProviderScheduledStatus,
		ProviderScheduledErrorCode:   request.ProviderScheduledErrorCode,
		ProviderScheduledErrorDetail: request.ProviderScheduledErrorDetails,
	}
	if request.Observer != nil && request.Observer.Active() {
		completionFacts = request.Observer.CompletionFacts()
	}
	roomBoundCancellation := request.RoomBoundCancellation || channelClosed(request.BoundCancellation)
	checkResponse := !roomBoundCancellation && !request.DurationExpired &&
		(request.HasAudioInput || request.RequireAssistantResponse || request.RequireTerminalAssistantReply || request.ProviderToolCallObserved)
	if request.Observer != nil && request.Observer.Active() {
		checkResponse = !roomBoundCancellation && !request.DurationExpired &&
			(request.HasAudioInput || request.RequireAssistantResponse || request.RequireTerminalAssistantReply || completionFacts.ProviderToolCallObserved)
	}
	if checkResponse {
		if request.AudioOutputError != nil {
			err = errors.Join(err, request.AudioOutputError())
		}
		if !completionFacts.AssistantResponseCompleted && (request.RequireTerminalAssistantReply || completionFacts.ProviderToolCallObserved) {
			incomplete := request.AssistantResponseIncomplete
			if incomplete == nil {
				incomplete = sessionduration.ErrAssistantResponseIncomplete
			}
			err = errors.Join(err, incomplete)
		}
	}
	scheduledCause := request.ScheduledAudioIncompleteCause
	if scheduledCause == nil {
		scheduledCause = sessionduration.ErrScheduledAudioIncomplete
	}
	if !request.DurationExpired && request.CloseAfterScheduledAudio && completionFacts.ScheduledAudioIncomplete &&
		!errors.Is(err, scheduledCause) && !errors.Is(err, sessionduration.ErrScheduledAudioIncomplete) {
		err = errors.Join(err, &sessionduration.ScheduledAudioIncompleteError{
			Completed:         completionFacts.ScheduledAudioCompleted,
			Dispatched:        completionFacts.ScheduledAudioDispatched,
			Scheduled:         completionFacts.ScheduledAudioCount,
			ProviderStatus:    completionFacts.ProviderScheduledStatus,
			ProviderErrorCode: completionFacts.ProviderScheduledErrorCode,
			ProviderDetails:   completionFacts.ProviderScheduledErrorDetail,
			Cause:             scheduledCause,
		})
	}
	return err
}

func channelClosed(signal <-chan struct{}) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal:
		return true
	default:
		return false
	}
}
