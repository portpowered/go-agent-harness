package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

const phaseTargetActivation = "target_activation"

// optionSelector is implemented by brokers that can activate the tab they
// select.
type optionSelector interface {
	SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
}

// contextRefresher is implemented by brokers that can refresh the selected
// page context on request.
type contextRefresher interface {
	SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error)
}

// targetActivator is implemented by brokers that can activate a target
// without selecting it.
type targetActivator interface {
	Activate(context.Context, webmcp.TargetSelector) error
}

// SelectTarget selects the exact target. Activation requires a broker that
// supports selection options.
func SelectTarget(ctx context.Context, broker webmcp.Broker, selector webmcp.TargetSelector, activate bool) (webmcp.PageContext, error) {
	if withOptions, ok := broker.(optionSelector); ok {
		return withOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	}
	if activate {
		return webmcp.PageContext{}, direct.RuntimeUnavailableError(phaseTargetActivation)
	}
	return broker.Select(ctx, selector)
}

// EnsureSelection resolves the exact target (honoring the persisted
// selection) and selects it with the configured activation.
func EnsureSelection(ctx context.Context, broker webmcp.Broker, selector Selector) (webmcp.PageContext, error) {
	resolution, err := ResolveTarget(ctx, broker, selector)
	if err != nil {
		return webmcp.PageContext{}, err
	}
	return SelectTarget(ctx, broker, resolution.selector(), selector.Browser.Selection.ActivateTab)
}

// SelectedContext returns the broker's selected page context, refreshed
// when requested and supported.
func SelectedContext(ctx context.Context, broker webmcp.Broker, refresh bool) (webmcp.PageContext, error) {
	if refresher, ok := broker.(contextRefresher); ok {
		return refresher.SelectedWithRefresh(ctx, refresh)
	}
	return broker.Selected(ctx)
}

// ContextWithCatalog describes page with its current catalog summary.
func ContextWithCatalog(ctx context.Context, broker webmcp.Broker, page webmcp.PageContext, refresh bool) (Context, error) {
	if refresh {
		refreshed, err := SelectedContext(ctx, broker, true)
		if err != nil {
			return Context{}, err
		}
		page = refreshed
	}
	snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: false})
	if err != nil {
		return Context{}, err
	}
	if snapshot.Context.Key.BrowserID != "" {
		page = snapshot.Context
	}
	return contextDataWithCatalog(page, snapshot), nil
}

// DescribeContext ensures the selection and describes it with its catalog.
func DescribeContext(ctx context.Context, broker webmcp.Broker, selector Selector, refresh bool) (Context, error) {
	page, err := EnsureSelection(ctx, broker, selector)
	if err != nil {
		return Context{}, err
	}
	return ContextWithCatalog(ctx, broker, page, refresh)
}

// SelectRequest is one explicit select. Activate is the effective activation
// choice; SaveSelection persists the record when the configuration asks for
// persistence.
type SelectRequest struct {
	Selector      Selector
	Activate      bool
	SaveSelection func(selectionstore.Selection) error
}

// Select replaces the selection with the exact live target, describes it,
// and persists the redacted record when configured. A failed selection or
// description never overwrites the prior persisted record.
func Select(ctx context.Context, broker webmcp.Broker, request SelectRequest) (Context, error) {
	resolution, err := ResolveReplacementTarget(ctx, broker, request.Selector)
	if err != nil {
		return Context{}, err
	}
	page, err := SelectTarget(ctx, broker, resolution.selector(), request.Activate)
	if err != nil {
		return Context{}, err
	}
	page = completeSelectedPage(page, resolution)
	data, err := ContextWithCatalog(ctx, broker, page, false)
	if err != nil {
		return Context{}, err
	}
	if request.Selector.Browser.Selection.Persist {
		if err := saveSelection(request.SaveSelection, selectionRecord(page, resolution)); err != nil {
			return Context{}, fmt.Errorf("persist WebMCP selection: %w", err)
		}
	}
	return data, nil
}

func saveSelection(save func(selectionstore.Selection) error, selection selectionstore.Selection) error {
	if save == nil {
		return errors.New("WebMCP selection store is unavailable")
	}
	return save(selection)
}

// completeSelectedPage fills identity the broker left empty from the
// resolved target.
func completeSelectedPage(page webmcp.PageContext, resolution Resolution) webmcp.PageContext {
	if page.Key.BrowserID == "" {
		page.Key.BrowserID = resolution.Candidate.ID
	}
	if page.Key.TargetID == "" {
		page.Key.TargetID = resolution.Target.ID
	}
	if page.Origin == "" {
		page.Origin = resolution.Target.Origin
	}
	if page.Generation == 0 {
		page.Generation = resolution.Target.Generation
	}
	return page
}

func selectionRecord(page webmcp.PageContext, resolution Resolution) selectionstore.Selection {
	return selectionstore.Selection{
		Version:           selectionstore.Version,
		EndpointID:        string(resolution.Candidate.ID),
		BrowserID:         string(page.Key.BrowserID),
		BrowserInstanceID: resolution.Candidate.BrowserInstanceID,
		TargetID:          string(page.Key.TargetID),
		Origin:            normalize.RedactedOrigin(page.Origin),
		ContinuityMarker:  resolution.Target.ContinuityMarker,
		Generation:        page.Generation,
		SelectedAt:        time.Now().UTC(),
	}
}

// Activate brings the exact target to the foreground. A broker without a
// direct activator activates through selection options.
func Activate(ctx context.Context, broker webmcp.Broker, selector Selector) (Context, error) {
	resolution, err := ResolveTarget(ctx, broker, selector)
	if err != nil {
		return Context{}, err
	}
	if activator, ok := broker.(targetActivator); ok {
		if err := activator.Activate(ctx, resolution.selector()); err != nil {
			return Context{}, err
		}
		target := resolution.Target
		return contextData(webmcp.PageContext{
			Key:       webmcp.PageKey{BrowserID: resolution.Candidate.ID, TargetID: target.ID},
			Title:     target.Title,
			URL:       target.URL,
			Origin:    target.Origin,
			Connected: target.Attached,
		}), nil
	}
	withOptions, ok := broker.(optionSelector)
	if !ok {
		return Context{}, direct.RuntimeUnavailableError(phaseTargetActivation)
	}
	page, err := withOptions.SelectWithOptions(ctx, resolution.selector(), webmcp.SelectOptions{Activate: true})
	if err != nil {
		return Context{}, err
	}
	return contextData(page), nil
}
