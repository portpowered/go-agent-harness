package service

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func participantError(options roomplanning.Options, id string, err error) error {
	if options.ParticipantError != nil {
		return options.ParticipantError(id, err)
	}
	return fmt.Errorf("room participant %q: %w", id, err)
}

func validateTools(participant rooms.Participant, capabilities roomplanning.ToolCapabilities) error {
	if len(participant.Tools) == 0 {
		return nil
	}
	if isNil(capabilities.Executor) {
		return fmt.Errorf("%w: participant %q has no executor", roomplanning.ErrParticipantTools, participant.ID)
	}
	requested := make(map[string]struct{}, len(participant.Tools))
	for _, name := range participant.Tools {
		requested[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(capabilities.Definitions))
	for _, definition := range capabilities.Definitions {
		if _, ok := requested[definition.Name]; !ok {
			return fmt.Errorf("%w: participant %q received unrequested tool %q", roomplanning.ErrParticipantToolMatch, participant.ID, definition.Name)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return fmt.Errorf("%w: participant %q received duplicate tool %q", roomplanning.ErrParticipantToolMatch, participant.ID, definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	return missingToolDefinition(participant, requested, seen)
}

func missingToolDefinition(participant rooms.Participant, requested, seen map[string]struct{}) error {
	if len(seen) == len(requested) {
		return nil
	}
	for name := range requested {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("%w: participant %q is missing definition for requested tool %q", roomplanning.ErrParticipantToolMatch, participant.ID, name)
		}
	}
	return nil
}

func validateBrowser(capabilities roomplanning.BrowserCapabilities) error {
	if isNil(capabilities.Executor) {
		return roomplanning.ErrParticipantBrowser
	}
	if err := validateDefinitions(capabilities.Definitions); err != nil {
		return errors.Join(roomplanning.ErrBrowserCapability, err)
	}
	if err := validateDefinitions(capabilities.ToolDefinitionBase); err != nil {
		return errors.Join(roomplanning.ErrBrowserCapability, err)
	}
	return nil
}

func validateDefinitions(definitions []messages.ToolDefinition) error {
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" {
			return errors.New("contains a definition with an empty name")
		}
		if _, exists := seen[definition.Name]; exists {
			return fmt.Errorf("contains duplicate definition %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	return nil
}

func cloneDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func cloneScope(scope roomplanning.FilesystemScope) roomplanning.FilesystemScope {
	scope.AdditionalRoots = append([]string(nil), scope.AdditionalRoots...)
	return scope
}

func closeCapabilities(plans []*roomplanning.ParticipantPlan) error {
	var closeErr error
	for _, plan := range plans {
		if plan != nil && plan.Options.CapabilityClose != nil {
			closeErr = errors.Join(closeErr, plan.Options.CapabilityClose())
		}
	}
	return closeErr
}

func closeCapability(close func() error) error {
	if close == nil {
		return nil
	}
	return close()
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.Array, reflect.String,
		reflect.Struct, reflect.UnsafePointer:
		return false
	}
	return false
}

func normalizeKind(kind rooms.ParticipantKind) rooms.ParticipantKind {
	return rooms.ParticipantKindNormalizer{}.Normalize(kind)
}
