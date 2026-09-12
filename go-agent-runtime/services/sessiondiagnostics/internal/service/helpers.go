package service

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

const continuationCompletedStatus = "completed"

func recordContinuationTerminal(state *sessiondiagnostics.ContinuationState, terminal *sessiondiagnostics.Terminal, output bool) bool {
	if state == nil || !state.ToolResponseComplete {
		return false
	}
	if !state.ContinuationTerminalSeen {
		state.ContinuationTerminalSeen = true
		applyContinuationTerminalMetadata(state, terminal, output)
		if supersededByTurn(state) {
			state.ContinuationComplete = true
			return true
		}
		markContinuationFailure(state)
	}
	if continuationCanComplete(*state) {
		state.ContinuationComplete = true
		return true
	}
	return state.ResultAccepted && state.ContinuationRequested
}

func applyContinuationTerminalMetadata(state *sessiondiagnostics.ContinuationState, terminal *sessiondiagnostics.Terminal, output bool) {
	if terminal != nil {
		state.ContinuationStatus = normalize(terminal.Status)
		state.ContinuationErrorCode = sanitize(providerErrorCode(terminal))
		state.ContinuationStatusDetails = sanitize(terminal.StatusDetails)
		if state.ContinuationStatusDetails == "" {
			state.ContinuationStatusDetails = sanitize(providerErrorMessage(terminal))
		}
		state.ContinuationReason = terminal.Reason
	}
	state.ContinuationOutput = output
}

func markContinuationFailure(state *sessiondiagnostics.ContinuationState) {
	status := normalize(state.ContinuationStatus)
	failed := !state.ContinuationOutput || (status != "" && status != continuationCompletedStatus) || (state.ContinuationReason != "" && state.ContinuationReason != "provider_authored_completion" && state.ContinuationReason != "loop_synthesized_completion")
	if failed && state.ContinuationStatusDetails == "" && state.ContinuationReason != "" && !state.ContinuationOutput {
		state.ContinuationStatusDetails = "assistant continuation produced no observable output"
	}
	if failed {
		state.ContinuationFailure = true
	}
}

func continuationCanComplete(state sessiondiagnostics.ContinuationState) bool {
	if !state.ResultAccepted || !state.ContinuationRequested || !state.ToolResponseComplete || !state.ContinuationTerminalSeen {
		return false
	}
	status := normalize(state.ContinuationStatus)
	if state.ContinuationFailure || (status != "" && status != continuationCompletedStatus) {
		return false
	}
	if state.ContinuationReason != "" && state.ContinuationReason != "provider_authored_completion" && state.ContinuationReason != "loop_synthesized_completion" {
		return false
	}
	return state.ContinuationOutput
}

func supersededByTurn(state *sessiondiagnostics.ContinuationState) bool {
	if state == nil {
		return false
	}
	status := normalize(state.ContinuationStatus)
	if status != "cancelled" && status != "canceled" {
		return false
	}
	for _, field := range strings.FieldsFunc(state.ContinuationStatusDetails, func(r rune) bool { return r == ',' || r == ';' }) {
		if strings.TrimSpace(field) == "reason=turn_detected" {
			return true
		}
	}
	return false
}

func clearContinuationTerminal(state *sessiondiagnostics.ContinuationState) {
	state.ContinuationTerminalSeen = false
	state.ContinuationStatus = ""
	state.ContinuationErrorCode = ""
	state.ContinuationStatusDetails = ""
	state.ContinuationReason = ""
	state.ContinuationOutput = false
	state.ContinuationFailure = false
	state.ContinuationComplete = false
}

func providerFailure(terminal *sessiondiagnostics.Terminal) bool {
	if terminal == nil || terminal.ProviderCancellation || normalize(terminal.Reason) == "cancellation" {
		return false
	}
	status := normalize(terminal.Status)
	switch status {
	case "", continuationCompletedStatus:
		return normalize(terminal.Reason) == "terminal_failure"
	case "cancelled", "canceled":
		return false
	default:
		return true
	}
}

func retryDecision(terminal *sessiondiagnostics.Terminal) (time.Duration, bool) {
	if terminal == nil || normalize(terminal.Status) != "failed" || normalize(terminal.Reason) == "cancellation" || providerErrorCode(terminal) != rateLimitRetryCode {
		return 0, false
	}
	message := providerErrorMessage(terminal)
	match := regexp.MustCompile(rateLimitRetryDelayPattern).FindStringSubmatch(message)
	if len(match) != 2 {
		return defaultRateLimitRetryDelay, true
	}
	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return defaultRateLimitRetryDelay, true
	}
	if seconds > maxRateLimitRetryDelay.Seconds() {
		return maxRateLimitRetryDelay, true
	}
	delay := time.Duration(math.Round(seconds * float64(time.Second)))
	if delay <= 0 {
		return time.Nanosecond, true
	}
	return delay, true
}

func providerErrorCode(terminal *sessiondiagnostics.Terminal) string {
	if terminal == nil {
		return ""
	}
	if code := strings.TrimSpace(terminal.ErrorCode); code != "" {
		return code
	}
	return legacyDetail(terminal.StatusDetails, "code")
}

func providerErrorMessage(terminal *sessiondiagnostics.Terminal) string {
	if terminal == nil {
		return ""
	}
	if message := strings.TrimSpace(terminal.ErrorMessage); message != "" {
		return message
	}
	return legacyDetail(terminal.StatusDetails, "message")
}

func legacyDetail(details, wanted string) string {
	parts := strings.Split(details, ",")
	for index, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(key) != wanted {
			continue
		}
		value = strings.TrimSpace(value)
		if wanted == "message" && index+1 < len(parts) {
			value = strings.TrimSpace(strings.Join(append([]string{value}, parts[index+1:]...), ","))
		}
		return sanitize(value)
	}
	return ""
}

func normalize(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func sanitize(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maxStatusDetailBytes {
		return value[:maxStatusDetailBytes]
	}
	return value
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneIndexMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneScheduled(values []sessiondiagnostics.ScheduledState) []sessiondiagnostics.ScheduledState {
	if values == nil {
		return nil
	}
	result := make([]sessiondiagnostics.ScheduledState, len(values))
	for index, value := range values {
		result[index] = value
		result[index].ResponseIDs = append([]string(nil), value.ResponseIDs...)
	}
	return result
}

func cloneContinuations(values map[string]sessiondiagnostics.ContinuationState) []sessiondiagnostics.ContinuationState {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]sessiondiagnostics.ContinuationState, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
