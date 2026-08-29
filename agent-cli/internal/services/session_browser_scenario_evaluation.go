package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func evaluateBrowserConversation(scenario BrowserConversationScenario, result BrowserConversationResult, rootErr error) BrowserConversationMechanicalEvaluation {
	var failures []string
	expectedCancellation := browserConversationExpectedCancellation(scenario, result, rootErr)
	if rootErr != nil && !expectedCancellation {
		failures = append(failures, "run: "+safeBrowserConversationError(rootErr))
	}
	if browserConversationHasInterruption(scenario) {
		if !result.Cancellation.Interrupted {
			failures = append(failures, "cancellation: declared interruption was not observed")
		}
		if result.Cancellation.InvocationID == "" {
			failures = append(failures, "cancellation: interrupted invocation identity is missing")
		}
		if result.Cancellation.FinalState != webmcp.InvocationCanceled {
			failures = append(failures, "cancellation: interrupted invocation did not reach canceled terminal state")
		}
		if !result.Cancellation.OverlappingAudioSent {
			failures = append(failures, "cancellation: overlapping customer audio was not sent")
		}
	}
	if browserConversationHasCancel(scenario) {
		if !result.Cancellation.Requested {
			failures = append(failures, "cancellation: explicit customer cancel was not observed")
		}
		if result.Cancellation.FinalState != webmcp.InvocationCanceled {
			failures = append(failures, "cancellation: explicit cancel did not preserve a canceled terminal state")
		}
	}
	for _, step := range scenario.Steps {
		customer, assistant := browserConversationTurnsForStep(result.Turns, step.ID)
		if customer == nil {
			failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": missing customer transcript")
		} else if !strings.EqualFold(strings.TrimSpace(customer.ObservedText), strings.TrimSpace(step.Utterance)) {
			failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": customer transcript does not match utterance")
		}
		assistantOptional := (result.Cancellation.Interrupted && result.Cancellation.InterruptedStepID == step.ID) ||
			(step.Interrupt != nil && result.Cancellation.Interrupted) ||
			(step.Cancel != nil && result.Cancellation.CancelStepID == step.ID)
		if assistant == nil && !assistantOptional {
			failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": missing assistant turn")
		}
		expectedState := browserConversationExpectedState(&step)
		if expectedState != nil {
			before := browserConversationOracleForStep(result.Oracles, step.ID, BrowserConversationOracleBefore)
			after := browserConversationOracleForStep(result.Oracles, step.ID, BrowserConversationOracleAfter)
			if before == nil {
				failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": missing before oracle")
			} else if !browserConversationJSONEqual(before.State, expectedState.Before) {
				failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": before oracle mismatch")
			}
			if after == nil {
				failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": missing after oracle")
			} else if !browserConversationJSONEqual(after.State, expectedState.After) {
				failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": after oracle mismatch")
			}
			terminalInvoke := browserConversationTerminalInvokeForStep(result.BrokerCalls, step.ID)
			if terminalInvoke == nil {
				failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": missing terminal tool result")
			} else if assistant != nil && assistant.Sequence <= terminalInvoke.Sequence {
				failures = append(failures, "step "+safeBrowserConversationText(step.ID)+": assistant turn was observed before the completed browser invocation")
			}
		}
	}
	failures = append(failures, browserConversationCorrectionFailures(scenario, result)...)
	failures = append(failures, browserConversationRecoveryFailures(scenario, result)...)
	post := browserConversationOracleForStep(result.Oracles, "", BrowserConversationOraclePostSession)
	if post == nil {
		failures = append(failures, "post-session: missing independent oracle")
	}
	required := scenario.PostSession
	if !result.Lifecycle.ExternalTabAlive && required.MustRemainAlive {
		failures = append(failures, "post-session: external tab is not alive")
	}
	if !result.Lifecycle.ExternalTabResponsive && required.MustBeResponsive {
		failures = append(failures, "post-session: external tab is not responsive")
	}
	if !result.Lifecycle.ExternalTabAllowsMutation && required.MustAllowMutation {
		failures = append(failures, "post-session: external tab does not allow mutation")
	}
	if result.Lifecycle.BrowserClosed {
		failures = append(failures, "lifecycle: externally owned browser was closed")
	}
	if result.Lifecycle.TargetClosed {
		failures = append(failures, "lifecycle: externally owned target was closed")
	}
	if result.Lifecycle.DetachRequired && result.Lifecycle.DetachCount != 1 {
		failures = append(failures, fmt.Sprintf("lifecycle: fixture detached %d times, want exactly once", result.Lifecycle.DetachCount))
	}
	if !result.Lifecycle.SessionStarted {
		failures = append(failures, "lifecycle: session was not started")
	}
	if !result.Lifecycle.SessionTerminated {
		failures = append(failures, "lifecycle: session was not terminated")
	}
	return BrowserConversationMechanicalEvaluation{
		Passed:   len(failures) == 0,
		Failures: failures,
	}
}

func browserConversationHasInterruption(scenario BrowserConversationScenario) bool {
	for _, step := range scenario.Steps {
		if step.Interrupt != nil {
			return true
		}
	}
	return false
}

func browserConversationHasCancel(scenario BrowserConversationScenario) bool {
	for _, step := range scenario.Steps {
		if step.Cancel != nil {
			return true
		}
	}
	return false
}

func browserConversationExpectedCancellation(scenario BrowserConversationScenario, result BrowserConversationResult, rootErr error) bool {
	if !browserConversationHasInterruption(scenario) && !browserConversationHasCancel(scenario) {
		return false
	}
	if result.Lifecycle.Outcome != BrowserConversationLifecycleCanceled ||
		!result.Cancellation.Requested || result.Cancellation.FinalState != webmcp.InvocationCanceled {
		return false
	}
	if rootErr == nil {
		return true
	}
	return errors.Is(rootErr, context.Canceled) || errors.Is(rootErr, ErrBrowserConversationSession)
}
