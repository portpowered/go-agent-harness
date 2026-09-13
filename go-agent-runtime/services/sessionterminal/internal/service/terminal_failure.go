package service

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func terminalFailure(request sessionterminal.Request) *sessionterminal.FailureFacts {
	if request.Failure == nil && !hasLifecycleObligation(request) && contextOnlyError(request.RunError) {
		return nil
	}
	if request.RoomBoundCancellation && request.Failure == nil && request.RoomCancellationOnly {
		return nil
	}
	if request.Failure != nil {
		return request.Failure
	}
	if failure := hintedFailure(request); failure != nil {
		return failure
	}
	if failure := factsFromSessionRunError(request.RunError); failure != nil {
		return failure
	}
	if request.RunError == nil && noContinuationIDs(request) && !hasLifecycleObligation(request) {
		return nil
	}
	if failure := obligationFailure(request); failure != nil {
		return failure
	}
	return providerFailure(request)
}

func hasLifecycleObligation(request sessionterminal.Request) bool {
	lifecycle := request.Lifecycle
	return len(lifecycle.UnresolvedToolResultCallIDs) > 0 ||
		len(lifecycle.PendingContinuationCallIDs) > 0 ||
		len(lifecycle.PendingToolContinuationIDs) > 0 ||
		len(lifecycle.PendingImageContinuationIDs) > 0 ||
		continuationMetadataPresent(lifecycle.PendingContinuations) || lifecycle.Scheduled.Incomplete
}

func continuationMetadataPresent(snapshot sessionterminal.ContinuationSnapshot) bool {
	return len(snapshot.Statuses) > 0 || len(snapshot.Codes) > 0 || len(snapshot.Details) > 0
}

func contextOnlyError(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func noContinuationIDs(request sessionterminal.Request) bool {
	lifecycle := request.Lifecycle
	return len(lifecycle.UnresolvedToolResultCallIDs) == 0 &&
		len(lifecycle.PendingContinuationCallIDs) == 0 &&
		len(lifecycle.PendingToolContinuationIDs) == 0 &&
		len(lifecycle.PendingImageContinuationIDs) == 0
}

func hintedFailure(request sessionterminal.Request) *sessionterminal.FailureFacts {
	for _, hint := range request.Lifecycle.FailureHints {
		if failure := failureForHint(hint, request); failure != nil {
			return failure
		}
	}
	return nil
}

func failureForHint(hint string, request sessionterminal.Request) *sessionterminal.FailureFacts {
	lifecycle := request.Lifecycle
	switch hint {
	case sessionterminal.FailureHintUnresolvedToolResults:
		if len(lifecycle.UnresolvedToolResultCallIDs) > 0 {
			return lifecycleFailure("unresolved_tool_result", request)
		}
	case sessionterminal.FailureHintImageContinuationIncomplete:
		if len(lifecycle.PendingImageContinuationIDs) > 0 {
			return lifecycleFailure("image_tool_continuation", request)
		}
	case sessionterminal.FailureHintToolContinuationIncomplete:
		if len(lifecycle.PendingToolContinuationIDs) > 0 {
			return lifecycleFailure("tool_continuation", request)
		}
	case sessionterminal.FailureHintScheduledAudioIncomplete:
		if lifecycle.Scheduled.Incomplete {
			return lifecycleFailure("scheduled_audio_incomplete", request)
		}
	}
	return nil
}

func obligationFailure(request sessionterminal.Request) *sessionterminal.FailureFacts {
	lifecycle := request.Lifecycle
	for _, obligation := range []struct {
		ids            []string
		classification string
	}{
		{lifecycle.UnresolvedToolResultCallIDs, "unresolved_tool_result"},
		{lifecycle.PendingToolContinuationIDs, "tool_continuation"},
		{lifecycle.PendingImageContinuationIDs, "image_tool_continuation"},
		{lifecycle.PendingContinuationCallIDs, "tool_continuation"},
	} {
		if len(obligation.ids) > 0 {
			return lifecycleFailure(obligation.classification, request)
		}
	}
	if lifecycle.Scheduled.Incomplete {
		return lifecycleFailure("scheduled_audio_incomplete", request)
	}
	if continuationMetadataPresent(lifecycle.PendingContinuations) {
		return lifecycleFailure("tool_continuation", request)
	}
	return nil
}

func providerFailure(request sessionterminal.Request) *sessionterminal.FailureFacts {
	classification := providers.ErrorClassification(request.RunError)
	if classification == "" {
		classification = providers.ErrorClassUnknown
	}
	failingEvent := failingEventRun
	if !request.Output.SawSessionOpen {
		failingEvent = failingEventConnect
	}
	return &sessionterminal.FailureFacts{
		Classification: classification,
		TerminalReason: messages.TerminalReasonTerminalFailure,
		Provenance:     messages.TerminalProvenanceCLI,
		OutputState:    derivedOutputState(request.Output),
		FailingEvent:   failingEvent,
	}
}

func lifecycleFailure(classification string, request sessionterminal.Request) *sessionterminal.FailureFacts {
	return &sessionterminal.FailureFacts{
		Classification: classification,
		TerminalReason: messages.TerminalReasonTerminalFailure,
		Provenance:     messages.TerminalProvenanceSession,
		OutputState:    derivedOutputState(request.Output),
		FailingEvent:   failingEventRun,
	}
}

func derivedOutputState(snapshot sessionterminal.OutputSnapshot) messages.TerminalOutputState {
	if !snapshot.SawSessionOpen || snapshot.TurnsCompleted == 0 {
		return messages.TerminalOutputNone
	}
	return messages.TerminalOutputPartial
}

func factsFromSessionRunError(err error) *sessionterminal.FailureFacts {
	if err == nil {
		return nil
	}
	var deltaErr *engine.StreamDeltaError
	if !errors.As(err, &deltaErr) || deltaErr == nil || deltaErr.Value == nil {
		return nil
	}
	value := deltaErr.Value
	failure := &sessionterminal.FailureFacts{
		Classification:    value.Classification,
		TerminalReason:    value.TerminalReason,
		Provenance:        value.TerminalProvenance,
		OutputState:       value.OutputState,
		ProviderErrorType: value.ErrorType,
		ProviderErrorCode: value.Code,
		FailingEvent:      string(messages.StreamTypeError),
	}
	if failure.Classification == "" {
		failure.Classification = providers.ErrorClassUnknown
	}
	if failure.TerminalReason == "" {
		failure.TerminalReason = messages.TerminalReasonTerminalFailure
	}
	if failure.Provenance == "" {
		failure.Provenance = messages.TerminalProvenanceProvider
	}
	if failure.OutputState == "" {
		failure.OutputState = messages.TerminalOutputNone
	}
	return failure
}
