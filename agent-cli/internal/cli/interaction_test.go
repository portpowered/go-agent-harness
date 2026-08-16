package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

func TestInteractionReplayS2FlagMatrix(t *testing.T) {
	fixture := interactionFixture()
	fixturePath := writeInteractionFixture(t, fixture)
	missingPath := filepath.Join(t.TempDir(), "missing.interaction.json")
	invalidPath := filepath.Join(t.TempDir(), "invalid.interaction.json")
	if err := os.WriteFile(invalidPath, []byte(`{"version":"gateway.interaction.v1","events":[{"interactionId":"int-123","sequence":1,"type":"text.delta"}]}`), 0600); err != nil {
		t.Fatalf("write invalid fixture: %v", err)
	}

	tests := []struct {
		name     string
		args     []string
		wantErr  string
		wantHelp bool
	}{
		{name: "group help", args: []string{"interaction"}, wantHelp: true},
		{name: "replay ordered events", args: []string{"interaction", "replay", fixturePath}},
		{name: "missing replay argument", args: []string{"interaction", "replay"}, wantErr: "accepts 1 arg(s), received 0"},
		{name: "extra replay argument", args: []string{"interaction", "replay", fixturePath, "extra"}, wantErr: "accepts 1 arg(s), received 2"},
		{name: "missing fixture", args: []string{"interaction", "replay", missingPath}, wantErr: "replay interaction fixture"},
		{name: "invalid fixture payload", args: []string{"interaction", "replay", invalidPath}, wantErr: "replay interaction fixture"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := executeGeneratedCLI(context.Background(), t.TempDir(), tc.args...)
			if tc.wantErr != "" {
				if got.err == nil {
					t.Fatalf("expected error containing %q", tc.wantErr)
				}
				if !strings.Contains(got.err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want context %q", got.err, tc.wantErr)
				}
				if tc.name == "missing replay argument" || tc.name == "extra replay argument" {
					if got.err.Error() != tc.wantErr {
						t.Fatalf("argument error = %q, want exact %q", got.err, tc.wantErr)
					}
				}
				if tc.name == "missing fixture" {
					var pathErr *os.PathError
					if !errors.As(got.err, &pathErr) || pathErr.Path != missingPath {
						t.Fatalf("error = %v, want wrapped PathError for %q", got.err, missingPath)
					}
				}
				if tc.name == "invalid fixture payload" {
					var validationErr gateway.InteractionFixtureValidationError
					if !errors.As(got.err, &validationErr) {
						t.Fatalf("error = %v, want InteractionFixtureValidationError", got.err)
					}
					if validationErr.File != invalidPath || validationErr.FieldPath != "events[0].textDelta" {
						t.Fatalf("validation error = %+v, want file and textDelta field", validationErr)
					}
				}
				return
			}
			if got.err != nil {
				t.Fatalf("execute %s: %v", tc.name, got.err)
			}
			if got.stderr != "" {
				t.Fatalf("stderr = %q, want empty", got.stderr)
			}
			if tc.wantHelp {
				for _, want := range []string{"Usage:", "replay", "one JSON object per line", "without provider credentials"} {
					if !strings.Contains(got.stdout, want) {
						t.Fatalf("help missing %q:\n%s", want, got.stdout)
					}
				}
				return
			}
			assertReplayedEvents(t, got.stdout, fixture.Events)
		})
	}
}

func TestInteractionReplayCancellationAndOutputFailure(t *testing.T) {
	cancelFixture := interactionFixture()
	cancelFixture.Events = cancelFixture.Events[:1]
	fixturePath := writeInteractionFixture(t, cancelFixture)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := runInteractionReplay(ctx, &out, fixturePath); err != nil {
		t.Fatalf("cancelled replay: %v", err)
	}
	if out.Len() > 0 {
		assertReplayedEvents(t, out.String(), cancelFixture.Events)
	}

	wantErr := errors.New("event output failed")
	err := runInteractionReplay(context.Background(), errWriter{err: wantErr}, fixturePath)
	if !errors.Is(err, wantErr) {
		t.Fatalf("output error = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "write interaction event") {
		t.Fatalf("output error = %v, want command context", err)
	}
}

func interactionFixture() gateway.InteractionFixture {
	return gateway.InteractionFixture{
		Version: gateway.InteractionFixtureVersion,
		Events: []gateway.InteractionEvent{
			{
				InteractionID: "int-123",
				Sequence:      1,
				Type:          gateway.InteractionEventStart,
				Provider:      "fixture-provider",
				Model:         "fixture-model",
			},
			{
				InteractionID: "int-123",
				Sequence:      2,
				Type:          gateway.InteractionEventTextDelta,
				Provider:      "fixture-provider",
				Model:         "fixture-model",
				TextDelta:     &gateway.TextDeltaEvent{Content: "hello"},
			},
			{
				InteractionID: "int-123",
				Sequence:      3,
				Type:          gateway.InteractionEventEnd,
				Provider:      "fixture-provider",
				Model:         "fixture-model",
			},
		},
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

func assertReplayedEvents(t *testing.T, output string, want []gateway.InteractionEvent) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != len(want) {
		t.Fatalf("replay emitted %d lines, want %d: %q", len(lines), len(want), output)
	}
	got := make([]gateway.InteractionEvent, 0, len(lines))
	for i, line := range lines {
		var event gateway.InteractionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode replay line %d: %v", i, err)
		}
		got = append(got, event)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed events = %#v, want %#v", got, want)
	}
}
