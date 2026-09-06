package wire

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"testing"
)

type recordingToolService struct {
	resolves     int
	capabilities serviceTools.Capabilities
}

func (s *recordingToolService) Resolve(*config.Config) (serviceTools.Capabilities, error) {
	s.resolves++
	return s.capabilities, nil
}

func TestCompositionValuesWithPorts_SkipsDisplacedDefaultConstructors(t *testing.T) {
	for _, selected := range livePortDefinitions() {
		selected := selected
		t.Run(selected.descriptor.Name, func(t *testing.T) {
			definitions := livePortDefinitions()
			defaultCalls := make(map[string]int, len(definitions))
			for index := range definitions {
				name := definitions[index].descriptor.Name
				factory := definitions[index].defaultValue
				if factory == nil {
					continue
				}
				definitions[index].defaultValue = func(defaults toolDefaults) any {
					defaultCalls[name]++
					return factory(defaults)
				}
			}

			replacement := replacementForPortType(t, selected.descriptor.Type)
			values, err := compositionValuesWithPorts(
				definitions,
				toolDefaults{executor: &recordingToolExecutor{}},
				[]PortSwap{NewPortSwap(selected.descriptor.Name, replacement)},
			)
			if err != nil {
				t.Fatalf("compositionValuesWithPorts: %v", err)
			}
			definition, ok := findPortDefinitionIn(definitions, selected.descriptor.Name)
			if !ok {
				t.Fatalf("selected port %q disappeared from live definitions", selected.descriptor.Name)
			}
			if got := definition.value(&values); got != replacement {
				t.Fatalf("selected %q replacement identity changed: got %T/%p want %T/%p", selected.descriptor.Name, got, got, replacement, replacement)
			}

			assertDefaultConstructorCalls(t, definitions, selected.descriptor.Name, defaultCalls)
		})
	}
}

func TestToolServicePort_AdvertisesAndExecutesCompleteCustomSurface(t *testing.T) {
	const toolName = "composition_unique_tool"

	toolExecutor := &recordingToolExecutor{}
	toolService := &recordingToolService{capabilities: serviceTools.Capabilities{
		Executor: toolExecutor,
		Definitions: []messages.ToolDefinition{{
			Name:        toolName,
			Description: "A tool unique to the composition fixture.",
		}},
	}}
	inferencer := &recordingInferencer{results: []messages.InferenceResult{
		{
			Message:   messages.NewTextMessage(messages.RoleAssistant, "call the custom tool"),
			ToolCalls: []messages.ToolCall{{ID: "composition-tool-call", Name: toolName, Arguments: `{}`}},
		},
		{Message: messages.NewTextMessage(messages.RoleAssistant, "custom tool complete")},
	}}
	fallbackExecutor := &recordingToolExecutor{}
	root, err := composeTestAgentCLI(
		fallbackExecutor,
		WithToolService(toolService),
		WithInferencer(inferencer),
	)
	if err != nil {
		t.Fatalf("ComposeAgentCLI with custom tool service: %v", err)
	}
	if err := executeAskCommand(t, root); err != nil {
		t.Fatalf("ask with custom tool service: %v", err)
	}
	if toolService.resolves != 1 {
		t.Fatalf("custom tool service resolves = %d, want exactly 1", toolService.resolves)
	}
	if toolExecutor.calls != 1 {
		t.Fatalf("custom tool executor calls = %d, want exactly 1", toolExecutor.calls)
	}
	if fallbackExecutor.calls != 0 {
		t.Fatalf("fallback tool executor calls = %d, want zero when complete service is supplied", fallbackExecutor.calls)
	}
	if inferencer.calls != 2 {
		t.Fatalf("custom-tool inferencer calls = %d, want tool and completion turns", inferencer.calls)
	}
	for _, requestTools := range inferencer.toolRequests {
		for _, definition := range requestTools {
			if definition.Name == toolName {
				return
			}
		}
	}
	t.Fatalf("custom tool %q was never advertised to the inferencer: %#v", toolName, inferencer.toolRequests)
}

func assertDefaultConstructorCalls(t *testing.T, definitions []portDefinition, selectedName string, defaultCalls map[string]int) {
	t.Helper()
	for _, definition := range definitions {
		calls := defaultCalls[definition.descriptor.Name]
		if definition.defaultValue == nil {
			if calls != 0 {
				t.Fatalf("graph-provided %q default constructor calls = %d, want zero", definition.descriptor.Name, calls)
			}
			continue
		}
		if definition.descriptor.Name == selectedName {
			if calls != 0 {
				t.Fatalf("displaced %q default constructor calls = %d, want exactly 0", definition.descriptor.Name, calls)
			}
			continue
		}
		if calls != 1 {
			t.Fatalf("unswapped %q default constructor calls = %d, want exactly 1", definition.descriptor.Name, calls)
		}
	}
}
