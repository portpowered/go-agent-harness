package scenariov2

import (
	"encoding/json"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func (e *executor) recordInvocationAdmission(current invocation) error {
	browserID := e.selected.Key.BrowserID
	targetID := e.selected.Key.TargetID
	generation := e.selected.Generation
	if current.PublicID == "" {
		fields := map[string]any{fieldCode: ErrorCode(current.Err)}
		if current.ToolRef != "" {
			fields[fieldToolRef] = string(current.ToolRef)
		}
		if current.Name != "" {
			fields[fieldToolName] = current.Name
		}
		return e.recordBrowserEvent(testkit.EventBrowserInvocationError, browserID, targetID, generation, fields)
	}
	fields := map[string]any{
		fieldInvocationID: string(current.PublicID),
		fieldToolRef:      string(current.ToolRef),
	}
	if current.Name != "" {
		fields[fieldToolName] = current.Name
	}
	if descriptor, ok := e.toolForRef(string(current.ToolRef)); ok && descriptor.FrameID != "" {
		fields["frame_id"] = string(descriptor.FrameID)
	}
	if err := e.recordBrowserEvent(testkit.EventBrowserInvocationCreated, browserID, targetID, generation, fields); err != nil {
		return err
	}
	if current.Err != nil {
		return e.recordInvocationError(string(current.PublicID), current.Err)
	}
	return e.recordBrowserEvent(testkit.EventBrowserInvocationDispatched, browserID, targetID, generation, map[string]any{
		fieldInvocationID: string(current.PublicID),
		fieldToolRef:      string(current.ToolRef),
		"input":           json.RawMessage(append([]byte(nil), current.Input...)),
	})
}

// recordInvocationError records a failed invocation against the selected page.
func (e *executor) recordInvocationError(invocationID string, err error) error {
	fields := map[string]any{fieldCode: ErrorCode(err)}
	if invocationID != "" {
		fields[fieldInvocationID] = invocationID
	}
	return e.recordBrowserEvent(testkit.EventBrowserInvocationError, e.selected.Key.BrowserID, e.selected.Key.TargetID, e.selected.Generation, fields)
}

func (e *executor) recordInvocationTerminal(current invocation) error {
	if current.PublicID == "" {
		return nil
	}
	if current.Err != nil {
		return e.recordInvocationError(string(current.PublicID), current.Err)
	}
	state := current.Result.State
	if state == webmcp.InvocationCanceled || state == webmcp.InvocationTimedOut {
		return e.recordBrowserEvent(testkit.EventBrowserInvocationCanceled, e.selected.Key.BrowserID, e.selected.Key.TargetID, e.selected.Generation, map[string]any{
			fieldInvocationID: string(current.PublicID),
			fieldSource:       "browser",
			fieldReason:       string(state),
		})
	}
	if state == webmcp.InvocationError || state == webmcp.InvocationOrphaned || state == webmcp.InvocationPolicyDenied {
		return e.recordInvocationError(string(current.PublicID), errors.New(string(current.Result.ErrorCode)))
	}
	return e.recordBrowserEvent(testkit.EventBrowserInvocationCompleted, e.selected.Key.BrowserID, e.selected.Key.TargetID, e.selected.Generation, map[string]any{
		fieldInvocationID: string(current.PublicID),
		"status":          string(state),
		"output":          json.RawMessage(append([]byte(nil), current.Result.Output...)),
	})
}

func (e *executor) recordInvocationCancel(step probe.ScenarioV2Step) error {
	return e.recordBrowserEvent(testkit.EventBrowserInvocationCancel, e.selected.Key.BrowserID, e.selected.Key.TargetID, e.selected.Generation, map[string]any{
		fieldInvocationID: step.InvocationID,
		fieldSource:       "scenario",
		fieldReason:       step.Reason,
	})
}

func (e *executor) recordGenerationChange(previous, current uint64) error {
	return e.recordBrowserEvent(testkit.EventBrowserPageGenerationChanged, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, map[string]any{
		"previous_generation": previous,
		"current_generation":  current,
		fieldReason:           "fixture_navigation",
	})
}

func (e *executor) recordCleanupEvidence() error {
	if e == nil || e.selected.Key.BrowserID == "" || e.selected.Key.TargetID == "" {
		return nil
	}
	cleanup := map[string]any{fieldReason: reasonBrokerClose, fieldOwnership: ownershipHarness}
	if err := e.recordBrowserEvent(testkit.EventBrowserTargetDetached, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, cleanup); err != nil {
		return err
	}
	return e.recordBrowserEvent(testkit.EventBrowserChromeTargetClosed, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, cleanup)
}
