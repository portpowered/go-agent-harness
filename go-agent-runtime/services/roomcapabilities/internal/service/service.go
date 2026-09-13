// Package service contains the private room capability policy and lifecycle
// owner. Hosts only receive the roomcapabilities contract through wire.
package service

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
)

type Service struct{}

var _ public.Service = (*Service)(nil)

func New() *Service { return &Service{} }

func (s *Service) ValidateTools(participant public.Participant, capabilities public.ToolCapabilities) error {
	requested, err := requestedTools(participant)
	if err != nil {
		return mismatch(err, participant, "requested tool set is invalid")
	}
	if len(requested) == 0 {
		return nil
	}
	if isNilExecutor(capabilities.Executor) {
		return fmt.Errorf("%w: %w: participant %q has no executor for requested tools %s", public.ErrToolsUnavailable, public.ErrNilToolExecutor, participant.ID, strings.Join(participant.Tools, ", "))
	}
	return validateReturnedTools(participant, capabilities, requested)
}

func requestedTools(participant public.Participant) (map[string]struct{}, error) {
	requested := make(map[string]struct{}, len(participant.Tools))
	for _, name := range participant.Tools {
		if strings.TrimSpace(name) == "" {
			return nil, public.ErrEmptyToolName
		}
		if _, exists := requested[name]; exists {
			return nil, public.ErrDuplicateTool
		}
		requested[name] = struct{}{}
	}
	return requested, nil
}

func validateReturnedTools(participant public.Participant, capabilities public.ToolCapabilities, requested map[string]struct{}) error {
	seen := make(map[string]struct{}, len(capabilities.Definitions))
	for _, definition := range capabilities.Definitions {
		if strings.TrimSpace(definition.Name) == "" {
			return mismatch(public.ErrEmptyToolName, participant, "returned definition has an empty name")
		}
		if _, ok := requested[definition.Name]; !ok {
			return mismatch(public.ErrUnrequestedTool, participant, "received unrequested tool %q", definition.Name)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return mismatch(public.ErrDuplicateTool, participant, "received duplicate tool %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	if len(seen) != len(requested) {
		for _, name := range participant.Tools {
			if _, ok := seen[name]; !ok {
				return mismatch(public.ErrMissingTool, participant, "missing definition for requested tool %q", name)
			}
		}
	}
	return nil
}

func (s *Service) ValidateBrowser(capabilities public.BrowserCapabilities) error {
	if isNilExecutor(capabilities.Executor) {
		return fmt.Errorf("%w: %w: browser executor is nil", public.ErrBrowserToolsUnavailable, public.ErrNilToolExecutor)
	}
	if err := validateDefinitions(capabilities.Definitions, "browser"); err != nil {
		return fmt.Errorf("%w: %w", public.ErrBrowserToolMismatch, err)
	}
	if err := validateDefinitions(capabilities.ToolDefinitionBase, "browser base"); err != nil {
		return fmt.Errorf("%w: %w", public.ErrBrowserToolMismatch, err)
	}
	return nil
}

func (s *Service) Compose(ctx context.Context, participant public.Participant, static public.ToolCapabilities, browser *public.BrowserCapabilities) (public.Capability, error) {
	if s == nil {
		return public.Capability{}, public.ErrServiceUnavailable
	}
	ctx, cancel := normalizeContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return public.Capability{}, err
	}
	if err := s.ValidateTools(participant, static); err != nil {
		return public.Capability{}, err
	}
	if len(participant.Tools) == 0 {
		static = public.ToolCapabilities{}
	}
	if browser == nil {
		return public.Capability{Executor: static.Executor, Definitions: s.CloneDefinitions(static.Definitions), ToolDefinitionBase: s.CloneDefinitions(static.Definitions)}, nil
	}
	if err := s.ValidateBrowser(*browser); err != nil {
		return public.Capability{}, fmt.Errorf("participant %q browser capability: %w", participant.ID, err)
	}
	return s.composeBrowser(ctx, static, *browser)
}

func (s *Service) composeBrowser(ctx context.Context, static public.ToolCapabilities, browser public.BrowserCapabilities) (public.Capability, error) {
	staticSnapshot := public.ToolCapabilities{Executor: static.Executor, Definitions: s.CloneDefinitions(static.Definitions)}
	browserSnapshot := browser
	browserSnapshot.Definitions = s.CloneDefinitions(browser.Definitions)
	browserSnapshot.ToolDefinitionBase = s.CloneDefinitions(browser.ToolDefinitionBase)
	initial, err := composeSurface(staticSnapshot.Executor, staticSnapshot.Definitions, browserSnapshot.Executor, browserSnapshot.Definitions)
	if err != nil {
		return public.Capability{}, fmt.Errorf("compose participant browser tools: %w", err)
	}
	baseDefinitions := browserSnapshot.ToolDefinitionBase
	if len(baseDefinitions) == 0 {
		baseDefinitions = browserSnapshot.Definitions
	}
	base, err := composeSurface(staticSnapshot.Executor, staticSnapshot.Definitions, browserSnapshot.Executor, baseDefinitions)
	if err != nil {
		return public.Capability{}, fmt.Errorf("compose participant browser tool base: %w", err)
	}
	executor := newRefreshingExecutor(initial.Executor)
	owner := newLifecycle(browserSnapshot.Initialize, func(refreshCtx context.Context) ([]public.ToolDefinition, error) {
		browserDefinitions := s.CloneDefinitions(browserSnapshot.Definitions)
		if browserSnapshot.RefreshToolDefinitions != nil {
			refreshed, refreshErr := browserSnapshot.RefreshToolDefinitions(refreshCtx)
			if refreshErr != nil {
				return nil, refreshErr
			}
			browserDefinitions = s.CloneDefinitions(refreshed)
		}
		if err := validateDefinitions(browserDefinitions, "browser"); err != nil {
			return nil, fmt.Errorf("%w: %w", public.ErrBrowserToolMismatch, err)
		}
		refreshed, resolveErr := composeSurface(staticSnapshot.Executor, staticSnapshot.Definitions, browserSnapshot.Executor, browserDefinitions)
		if resolveErr != nil {
			return nil, resolveErr
		}
		executor.Replace(refreshed.Executor)
		return s.CloneDefinitions(refreshed.Definitions), nil
	}, browserSnapshot.Close, browserSnapshot.CloseTimeout)
	return public.Capability{
		Executor:               executor,
		Definitions:            s.CloneDefinitions(initial.Definitions),
		ToolDefinitionBase:     s.CloneDefinitions(base.Definitions),
		RefreshToolDefinitions: owner.RefreshDefinitions,
		Initialize:             owner.Initialize,
		Close:                  owner.Close,
	}, nil
}

func (s *Service) CloneDefinitions(definitions []public.ToolDefinition) []public.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func (s *Service) OrderDefinitions(definitions []public.ToolDefinition, requested []string) []public.ToolDefinition {
	if len(requested) == 0 {
		return nil
	}
	byName := make(map[string]public.ToolDefinition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	ordered := make([]public.ToolDefinition, 0, len(requested))
	for _, name := range requested {
		if definition, ok := byName[name]; ok {
			ordered = append(ordered, definition)
		}
	}
	return s.CloneDefinitions(ordered)
}

func validateDefinitions(definitions []public.ToolDefinition, namespace string) error {
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" {
			return fmt.Errorf("%w: %s definition has an empty name", public.ErrEmptyToolName, namespace)
		}
		if _, exists := seen[definition.Name]; exists {
			return fmt.Errorf("%w: %s surface repeats tool %q", public.ErrDuplicateDefinition, namespace, definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	return nil
}

func mismatch(cause error, participant public.Participant, format string, args ...any) error {
	return fmt.Errorf("%w: %w: participant %q %s", public.ErrToolMismatch, cause, participant.ID, fmt.Sprintf(format, args...))
}

func isNilExecutor(executor public.ToolExecutor) bool {
	if executor == nil {
		return true
	}
	value := reflect.ValueOf(executor)
	nilableKinds := map[reflect.Kind]struct{}{
		reflect.Chan: {}, reflect.Func: {}, reflect.Interface: {},
		reflect.Map: {}, reflect.Pointer: {}, reflect.Slice: {},
	}
	if _, ok := nilableKinds[value.Kind()]; !ok {
		return false
	}
	return value.IsNil()
}

func normalizeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithCancel(context.Background())
	}
	return ctx, func() {}
}
