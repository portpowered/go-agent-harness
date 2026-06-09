package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

func TestInteractionCommand_HelpDocumentsReplayOutputAndCredentialFreeBehavior(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"interaction", "--help"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute help: %v", err)
	}

	help := testWriter.StdoutString()
	for _, want := range []string{"replay", "one JSON object per line", "without provider credentials"} {
		if !strings.Contains(help, want) {
			t.Fatalf("interaction help missing %q:\n%s", want, help)
		}
	}
}

func TestInteractionReplay_PrintsNormalizedEventsAsNDJSON(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	fixturePath := writeInteractionFixture(t, gateway.InteractionFixture{
		Version: gateway.InteractionFixtureVersion,
		Events: []gateway.InteractionEvent{
			{
				InteractionID: "int-123",
				Sequence:      1,
				Type:          gateway.InteractionEventStart,
				Provider:      "fixture",
				Model:         "demo-model",
			},
			{
				InteractionID: "int-123",
				Sequence:      2,
				Type:          gateway.InteractionEventTextDelta,
				Provider:      "fixture",
				Model:         "demo-model",
				TextDelta:     &gateway.TextDeltaEvent{Content: "hello"},
			},
			{
				InteractionID: "int-123",
				Sequence:      3,
				Type:          gateway.InteractionEventEnd,
				Provider:      "fixture",
				Model:         "demo-model",
			},
		},
	})

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"interaction", "replay", fixturePath})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute interaction replay: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(testWriter.StdoutString()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 replayed event lines, got %d: %q", len(lines), testWriter.StdoutString())
	}

	var events []gateway.InteractionEvent
	for i, line := range lines {
		var event gateway.InteractionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal line %d: %v\nline: %s", i, err, line)
		}
		events = append(events, event)
	}

	if events[0].Type != gateway.InteractionEventStart || events[0].Sequence != 1 {
		t.Fatalf("event[0] = %+v, want interaction.start sequence 1", events[0])
	}
	if events[1].Type != gateway.InteractionEventTextDelta || events[1].TextDelta == nil || events[1].TextDelta.Content != "hello" {
		t.Fatalf("event[1] = %+v, want text.delta hello", events[1])
	}
	if events[2].Type != gateway.InteractionEventEnd || events[2].Sequence != 3 {
		t.Fatalf("event[2] = %+v, want interaction.end sequence 3", events[2])
	}
	if testWriter.StderrString() != "" {
		t.Fatalf("expected empty stderr, got %q", testWriter.StderrString())
	}
}

func TestInteractionReplay_InvalidFixtureReturnsActionableError(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	fixturePath := filepath.Join(t.TempDir(), "invalid.interaction.json")
	if err := os.WriteFile(fixturePath, []byte(`{"version":"gateway.interaction.v1","events":[{"interactionId":"int-123","sequence":1,"type":"text.delta"}]}`), 0600); err != nil {
		t.Fatalf("write invalid interaction fixture: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{"interaction", "replay", fixturePath})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected invalid interaction fixture error")
	}
	if !strings.Contains(err.Error(), "replay interaction fixture") || !strings.Contains(err.Error(), fixturePath) || !strings.Contains(err.Error(), "textDelta") {
		t.Fatalf("invalid fixture error should include command context, path, and field, got: %v", err)
	}
}

func writeInteractionFixture(t *testing.T, fixture gateway.InteractionFixture) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fixture.interaction.json")
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatalf("marshal interaction fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write interaction fixture: %v", err)
	}
	return path
}
