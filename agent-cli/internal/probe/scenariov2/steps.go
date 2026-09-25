package scenariov2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

type stepHandler func(*executor, context.Context, probe.ScenarioV2Step) error

// browserStepHandlers are the steps that drive the browser broker.
func browserStepHandlers() map[probe.ScenarioV2StepType]stepHandler {
	return map[probe.ScenarioV2StepType]stepHandler{
		probe.ScenarioV2StepBrowserConnect:         (*executor).discover,
		probe.ScenarioV2StepBrowserDiscover:        (*executor).discover,
		probe.ScenarioV2StepBrowserSelect:          (*executor).selectTarget,
		probe.ScenarioV2StepBrowserActivate:        (*executor).activateTarget,
		probe.ScenarioV2StepWebMCPWaitReady:        (*executor).waitReady,
		probe.ScenarioV2StepWebMCPListTools:        (*executor).listTools,
		probe.ScenarioV2StepWebMCPInvoke:           (*executor).invoke,
		probe.ScenarioV2StepWebMCPCancel:           (*executor).cancelInvocation,
		probe.ScenarioV2StepBrowserNavigateFixture: (*executor).navigate,
		probe.ScenarioV2StepBrowserDisconnect:      (*executor).deferClose,
		probe.ScenarioV2StepCloseTab:               (*executor).deferClose,
	}
}

// sessionStepHandlers are the provider-session and control steps that do not
// require a configured browser.
func sessionStepHandlers() map[probe.ScenarioV2StepType]stepHandler {
	return map[probe.ScenarioV2StepType]stepHandler{
		probe.ScenarioV2StepSendText:      (*executor).appendProviderStep,
		probe.ScenarioV2StepSendAudio:     (*executor).appendProviderStep,
		probe.ScenarioV2StepInterrupt:     (*executor).interrupt,
		probe.ScenarioV2StepSleepFake:     (*executor).sleepFake,
		probe.ScenarioV2StepClose:         (*executor).markClosed,
		probe.ScenarioV2StepOpenTab:       (*executor).unsupportedSingleBrowserStep,
		probe.ScenarioV2StepSwitchBrowser: (*executor).unsupportedSingleBrowserStep,
	}
}

func (e *executor) dispatchStep(ctx context.Context, step probe.ScenarioV2Step) error {
	if handler, ok := browserStepHandlers()[step.Type]; ok {
		if e.broker == nil {
			return errors.New("browser executor is not configured")
		}
		return handler(e, ctx, step)
	}
	if handler, ok := sessionStepHandlers()[step.Type]; ok {
		return handler(e, ctx, step)
	}
	return fmt.Errorf("unsupported probe.scenario.v2 step %q", step.Type)
}

func (e *executor) discover(ctx context.Context, step probe.ScenarioV2Step) error {
	if err := e.recordDiscoveryStarted(); err != nil {
		return err
	}
	candidates, err := e.broker.Discover(ctx, webmcp.DiscoverOptions{BrowserID: webmcp.BrowserID(step.BrowserID), ExplicitOnly: true})
	if err != nil {
		return err
	}
	e.discovered = candidates
	return e.recordDiscoveryEvidence(ctx)
}

func (e *executor) selectTarget(ctx context.Context, step probe.ScenarioV2Step) error {
	browserID := webmcp.BrowserID(step.BrowserID)
	if browserID == "" && len(e.discovered) == 1 {
		browserID = e.discovered[0].ID
	}
	selector := webmcp.TargetSelector{BrowserID: browserID, TargetID: webmcp.TargetID(step.TargetID)}
	page, err := e.broker.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: step.Activate})
	if err != nil {
		return err
	}
	e.selected = page
	return e.recordSelectionEvidence(page, "selected")
}

func (e *executor) activateTarget(ctx context.Context, step probe.ScenarioV2Step) error {
	browserID := webmcp.BrowserID(step.BrowserID)
	if browserID == "" {
		browserID = e.selected.Key.BrowserID
	}
	targetID := webmcp.TargetID(step.TargetID)
	if targetID == "" {
		targetID = e.selected.Key.TargetID
	}
	page, err := e.broker.SelectWithOptions(ctx, webmcp.TargetSelector{BrowserID: browserID, TargetID: targetID}, webmcp.SelectOptions{Activate: true})
	if err != nil {
		return err
	}
	e.selected = page
	return e.recordSelectionEvidence(page, "activated")
}

func (e *executor) waitReady(ctx context.Context, _ probe.ScenarioV2Step) error {
	page, err := e.broker.Selected(ctx)
	if err != nil {
		return err
	}
	e.selected = page
	return nil
}

func (e *executor) listTools(ctx context.Context, step probe.ScenarioV2Step) error {
	catalog, err := e.broker.ListTools(ctx, webmcp.ListToolsOptions{
		Refresh:        step.Refresh,
		NameContains:   step.NameContains,
		IncludeSchemas: step.IncludeSchemas,
		FrameID:        webmcp.FrameID(step.FrameID),
	})
	if err != nil {
		return err
	}
	e.catalog = catalog
	e.hasCatalog = true
	return e.recordCatalogEvidence(catalog)
}

func (e *executor) invoke(ctx context.Context, step probe.ScenarioV2Step) error {
	input := json.RawMessage(step.InputJSON)
	result, err := e.broker.Invoke(ctx, webmcp.InvokeRequest{ToolRef: webmcp.ToolRef(step.ToolRef), Input: input, Reason: step.Reason})
	current := invocation{PublicID: result.InvocationID, ToolRef: webmcp.ToolRef(step.ToolRef), Input: append(json.RawMessage(nil), input...), Result: result, Err: err}
	if descriptor, ok := e.toolForRef(step.ToolRef); ok {
		current.Name = descriptor.Name
	}
	e.invocations = append(e.invocations, current)
	if recordErr := e.recordInvocationAdmission(current); recordErr != nil {
		return recordErr
	}
	if current.Err != nil && !e.toleratedInvocationError(current.Err) {
		return current.Err
	}
	return nil
}

func (e *executor) cancelInvocation(ctx context.Context, step probe.ScenarioV2Step) error {
	if err := e.broker.Cancel(ctx, webmcp.CancelRequest{InvocationID: webmcp.InvocationID(step.InvocationID), Reason: step.Reason}); err != nil {
		return err
	}
	return e.recordInvocationCancel(step)
}

func (e *executor) navigate(ctx context.Context, step probe.ScenarioV2Step) error {
	if e.mode == BrowserExecutorReal {
		return e.navigateReal(ctx, step)
	}
	targetURL := step.URL
	if step.FixturePath != "" {
		script, err := testkit.LoadBrowserScriptFile(step.FixturePath)
		if err != nil {
			return fmt.Errorf("load navigation fixture: %w", err)
		}
		if len(script.Endpoint.Targets) == 0 {
			return errors.New("navigation fixture has no target")
		}
		targetURL = script.Endpoint.Targets[0].URL
	}
	if err := e.adapter.Navigate(ctx, targetURL); err != nil {
		return err
	}
	return e.refreshSelectedGeneration(ctx)
}

func (e *executor) navigateReal(ctx context.Context, step probe.ScenarioV2Step) error {
	if step.FixturePath != "" {
		return newBrowserExecutorError(e.mode, phaseNavigate, webmcp.ErrorUnsupportedWebMCP, errors.New("real browser execution does not consume navigation fixtures"))
	}
	if e.browserNavigate == nil {
		return newBrowserExecutorError(e.mode, phaseNavigate, webmcp.ErrorUnsupportedWebMCP, errors.New("real browser runtime does not provide navigation"))
	}
	if err := e.browserNavigate(ctx, step.URL); err != nil {
		return browserOperationError(e.mode, phaseNavigate, err)
	}
	return e.refreshSelectedGeneration(ctx)
}

func (e *executor) refreshSelectedGeneration(ctx context.Context) error {
	page, err := e.broker.Selected(ctx)
	if err != nil {
		return err
	}
	previous := e.selected.Generation
	e.selected = page
	return e.recordGenerationChange(previous, page.Generation)
}

// deferClose marks the browser closed but defers physical teardown until
// terminal invocation evidence has been drained. The broker keeps its bounded
// terminal cache available until Close, which lets a preceding invoke and
// this lifecycle step coexist.
func (e *executor) deferClose(context.Context, probe.ScenarioV2Step) error {
	e.closed = true
	return nil
}

func (e *executor) appendProviderStep(_ context.Context, step probe.ScenarioV2Step) error {
	if step.Type == probe.ScenarioV2StepSendAudio {
		e.providerSteps = append(e.providerSteps, sendAudioStep(step))
		return nil
	}
	e.providerSteps = append(e.providerSteps, sendTextStep(step))
	return nil
}

// interrupt timing is authored for the provider replay. The capture is the
// source of the actual cancel event; retaining the step keeps the provider
// projection aligned without introducing a live provider call.
func (e *executor) interrupt(context.Context, probe.ScenarioV2Step) error {
	return nil
}

func (e *executor) sleepFake(_ context.Context, step probe.ScenarioV2Step) error {
	e.clock.Advance(time.Duration(step.DurationMS) * time.Millisecond)
	return nil
}

func (e *executor) markClosed(context.Context, probe.ScenarioV2Step) error {
	e.closed = true
	return nil
}

func (e *executor) unsupportedSingleBrowserStep(_ context.Context, step probe.ScenarioV2Step) error {
	return fmt.Errorf("unsupported probe.scenario.v2 step %q for the single browser fixture executor", step.Type)
}
