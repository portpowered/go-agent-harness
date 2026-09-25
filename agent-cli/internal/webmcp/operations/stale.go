package operations

import (
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

// SelectionRecoveryCommand is the command that replaces a stale persisted
// selection.
const SelectionRecoveryCommand = "agent webmcp select"

// Stale-selection reasons shared by resolution and its error details.
const (
	reasonBrowserNotFound   = "browser_not_found"
	reasonTargetNotFound    = "target_not_found"
	reasonTargetListFailed  = "target_list_failed"
	reasonEndpointChanged   = "endpoint_changed"
	reasonInstanceChanged   = "browser_instance_changed"
	reasonOriginChanged     = "origin_changed"
	reasonContinuityChanged = "continuity_changed"
	reasonGenerationChanged = "generation_changed"

	detailBrowserID          = "browser_id"
	detailTargetID           = "target_id"
	detailSelectedGeneration = "selected_generation"
	detailReason             = "reason"
	detailPhase              = "phase"
	detailInvocationID       = "invocation_id"
	detailToolRef            = "tool_ref"
	detailCancelSource       = "cancel_source"
	detailSideEffectUnknown  = "side_effect_unknown"

	phaseDiscovery = "discovery"
	phaseTargets   = "targets"

	maxPhaseLength = 32
)

// browserLossMessages are the transport failures that mean the remembered
// browser is gone rather than that the selection is merely stale.
func browserLossMessages() []string {
	return []string{
		"connection refused",
		"connection reset",
		"connection lost",
		"browser disconnected",
		"endpoint not found",
	}
}

// StaleSelectionError classifies a persisted selection that no longer
// matches the live browser. A cause that shows the browser itself is gone is
// reported as browser_disconnected with the phase that observed the loss.
func StaleSelectionError(browserID, targetID string, generation uint64, reason string, cause error) error {
	if phase, disconnected := persistedBrowserLossPhase(reason, cause); disconnected {
		err := webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, webmcp.DefaultErrorMessage(webmcp.ErrorBrowserDisconnected), map[string]any{
			detailBrowserID:      browserID,
			detailTargetID:       targetID,
			detailPhase:          phase,
			"reconnect_required": true,
		})
		err.Cause = cause
		return err
	}
	details := map[string]any{
		detailBrowserID:          browserID,
		detailTargetID:           targetID,
		detailSelectedGeneration: generation,
		detailReason:             reason,
	}
	err := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, staleSelectionMessage(reason), withSelectionRecovery(details))
	err.Cause = cause
	return err
}

func staleSelectionMessage(reason string) string {
	switch reason {
	case reasonInstanceChanged, reasonEndpointChanged:
		return "the selected browser was replaced; run agent webmcp select with a live browser target to replace the persisted selection"
	default:
		return "the persisted browser target selection is no longer current; run agent webmcp select with a live target to replace it"
	}
}

func persistedSelectionError(message, reason string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, message+"; run "+SelectionRecoveryCommand+" to replace it", withSelectionRecovery(map[string]any{
		detailReason: reason,
	}))
}

func withSelectionRecovery(details map[string]any) map[string]any {
	result := make(map[string]any, len(details)+1)
	for key, value := range details {
		result[key] = value
	}
	result["recovery"] = map[string]any{
		"action":      "replace_stale_selection",
		"command":     SelectionRecoveryCommand,
		"instruction": "Run agent webmcp select with a live browser or target selector to replace the stale persisted selection.",
	}
	return result
}

func persistedGeneration(selection *selectionstore.Selection) uint64 {
	if selection == nil {
		return 0
	}
	return selection.Generation
}

func persistedBrowserLossPhase(reason string, cause error) (string, bool) {
	if reason == reasonTargetNotFound {
		return "", false
	}
	fallback := phaseTargets
	if reason == reasonBrowserNotFound {
		fallback = phaseDiscovery
	}
	return browserLossPhase(cause, fallback)
}

// browserLossPhase reports whether err shows the browser itself was lost,
// and the bounded phase that observed it. Classified, joined, and wrapped
// causes are searched before the transport message is consulted.
func browserLossPhase(err error, fallback string) (string, bool) {
	if err == nil {
		return "", false
	}
	if phase, found := classifiedLossPhase(err, fallback); found {
		return phase, true
	}
	if phase, found := nestedLossPhase(err, fallback); found {
		return phase, true
	}
	message := strings.ToLower(err.Error())
	for _, loss := range browserLossMessages() {
		if strings.Contains(message, loss) {
			return fallback, true
		}
	}
	return "", false
}

func classifiedLossPhase(err error, fallback string) (string, bool) {
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified == nil {
		return "", false
	}
	if browserLossCode(classified.Code) {
		if phase, ok := safePhase(classified.Details[detailPhase]); ok {
			return phase, true
		}
		return fallback, true
	}
	if classified.Code == webmcp.ErrorStaleSelection && classified.Details != nil && classified.Details[detailReason] == reasonBrowserNotFound {
		return fallback, true
	}
	return "", false
}

func browserLossCode(code webmcp.ErrorCode) bool {
	return code == webmcp.ErrorBrowserDisconnected || code == webmcp.ErrorEndpointNotFound || code == webmcp.ErrorEndpointUnreachable
}

func nestedLossPhase(err error, fallback string) (string, bool) {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, nested := range joined.Unwrap() {
			if phase, found := browserLossPhase(nested, fallback); found {
				return phase, true
			}
		}
	}
	return browserLossPhase(errors.Unwrap(err), fallback)
}

// safePhase accepts only a short identifier-shaped phase so a broker detail
// never carries free text into the result.
func safePhase(value any) (string, bool) {
	phase, ok := value.(string)
	if !ok {
		return "", false
	}
	phase = strings.TrimSpace(phase)
	if phase == "" || len(phase) > maxPhaseLength {
		return "", false
	}
	for _, character := range phase {
		if !phaseCharacter(character) {
			return "", false
		}
	}
	return phase, true
}

func phaseCharacter(character rune) bool {
	switch {
	case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9':
		return true
	default:
		return character == '_' || character == '-' || character == '.'
	}
}
