package scenariov2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

const (
	phaseComposition = "composition"
	phasePageState   = "page_state"
	phaseNavigate    = "navigate"
)

func (e *executor) execute(ctx context.Context) error {
	if e == nil {
		return errors.New("v2 executor is nil")
	}
	for index, step := range e.scenario.Steps {
		if err := e.dispatchStep(ctx, step); err != nil {
			if e.mode == BrowserExecutorReal {
				err = browserOperationError(e.mode, "step_"+string(step.Type), err)
			}
			return fmt.Errorf("step %d (%s): %w", index, step.Type, err)
		}
	}
	if err := e.resolveInvocations(ctx); err != nil {
		if e.mode == BrowserExecutorReal {
			err = browserOperationError(e.mode, "resolve_invocations", err)
		}
		return fmt.Errorf("resolve browser invocations: %w", err)
	}
	if err := e.replayProvider(ctx); err != nil {
		return err
	}
	if err := e.capturePageState(ctx); err != nil {
		return err
	}
	return e.finishBrowser(ctx)
}

func (e *executor) replayProvider(ctx context.Context) error {
	if e.providerPath == "" {
		if len(e.providerSteps) > 0 {
			return errors.New("provider session steps require provider_fixture")
		}
		return nil
	}
	if e.analyze == nil {
		return errors.New("provider capture analyzer is not configured")
	}
	providerScenario := probe.Scenario{ID: e.scenario.ID, Name: e.scenario.Name, Description: e.scenario.Description, Steps: e.providerSteps}
	request := runtimeReplay.CaptureProbeRequest{SourcePath: e.providerPath}
	report, observation, err := e.analyze(ctx, e.replayService, providerScenario, request)
	if err != nil {
		return fmt.Errorf("replay provider fixture %q: %w", e.scenario.ProviderFixture, err)
	}
	e.providerReport = &report
	e.provider = &observation
	return nil
}

func (e *executor) finishBrowser(ctx context.Context) error {
	if e.broker != nil {
		closeErr := e.closeBrowser(ctx)
		e.closed = true
		if recordErr := e.recordCleanupEvidence(); recordErr != nil {
			return recordErr
		}
		if closeErr != nil {
			return fmt.Errorf("close browser broker: %w", closeErr)
		}
	}
	if e.runtime != nil {
		if err := e.runtime.Complete(); err != nil {
			return fmt.Errorf("complete browser fixture: %w", err)
		}
	}
	return nil
}

// cleanup releases browser resources after a failed execution. Failures are
// retained on the executor: the execution error already describes the run.
func (e *executor) cleanup(ctx context.Context) {
	if e == nil {
		return
	}
	if e.broker != nil || e.browserClose != nil || e.adapter != nil {
		e.cleanupErr = errors.Join(e.cleanupErr, e.closeBrowser(ctx))
		e.closed = true
	}
	if e.runtime != nil {
		e.cleanupErr = errors.Join(e.cleanupErr, e.runtime.Complete())
	}
}

func (e *executor) closeBrowser(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.browserCloseOnce.Do(func() {
		switch {
		case e.browserClose != nil:
			e.browserCloseErr = e.browserClose()
		case e.broker != nil:
			e.browserCloseErr = e.broker.Close()
		case e.adapter != nil:
			e.browserCloseErr = e.adapter.Disconnect(context.WithoutCancel(ctx))
		}
	})
	return e.browserCloseErr
}

func (e *executor) capturePageState(ctx context.Context) error {
	if e == nil || e.browserPageState == nil {
		return nil
	}
	state, err := e.browserPageState(ctx)
	if err != nil {
		return browserOperationError(e.mode, phasePageState, err)
	}
	normalized, err := testkit.JSONValue(state)
	if err != nil {
		return newBrowserExecutorError(e.mode, phasePageState, webmcp.ErrorBrowserProtocol, err)
	}
	e.pageState = append(json.RawMessage(nil), normalized...)
	e.pageStateSet = true
	return nil
}

func (e *executor) resolveInvocations(ctx context.Context) error {
	if e == nil || e.broker == nil {
		return nil
	}
	for index := range e.invocations {
		current := &e.invocations[index]
		if current.Err != nil || current.PublicID == "" {
			continue
		}
		result, err := e.broker.WaitInvocation(ctx, current.PublicID)
		if err != nil {
			current.Err = err
			continue
		}
		current.Result = result
	}
	for _, current := range e.invocations {
		if err := e.recordInvocationTerminal(current); err != nil {
			return err
		}
	}
	for _, current := range e.invocations {
		if current.Err != nil && !e.toleratedInvocationError(current.Err) {
			return current.Err
		}
	}
	return nil
}

// toleratedInvocationError reports whether err is the stale-reference
// rejection the scenario explicitly expects.
func (e *executor) toleratedInvocationError(err error) bool {
	return e.scenarioExpects(probe.ScenarioV2ExpectationStaleToolRejected) && isStaleToolError(err)
}

func (e *executor) toolForRef(ref string) (webmcp.ToolDescriptor, bool) {
	if !e.hasCatalog {
		return webmcp.ToolDescriptor{}, false
	}
	for _, tool := range e.catalog.Tools {
		if tool.Ref == webmcp.ToolRef(ref) {
			return tool, true
		}
	}
	return webmcp.ToolDescriptor{}, false
}

func (e *executor) scenarioExpects(kind probe.ScenarioV2ExpectationType) bool {
	for _, expectation := range e.scenario.Expectations {
		if expectation.Type == kind {
			return true
		}
	}
	return false
}
