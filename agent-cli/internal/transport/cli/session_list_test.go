package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
)

func TestSessionListCommandBoundsAndComposesMetadataFilters(t *testing.T) {
	configDir := t.TempDir()
	storage := session.NewStorage(configDir)
	base := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("bulk-%03d", i)
		if err := storage.Save(id, nil); err != nil {
			t.Fatalf("Save %q: %v", id, err)
		}
		path := filepath.Join(configDir, "sessions", "session-"+id+".json")
		if err := os.Chtimes(path, base.Add(time.Duration(i)*time.Second), base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Chtimes %q: %v", id, err)
		}
	}

	list := func(extra ...string) cliResult {
		args := []string{"--config-dir", configDir, "session", "list"}
		return executeGeneratedCLI(context.Background(), configDir, append(args, extra...)...)
	}

	defaultResult := list()
	if defaultResult.err != nil {
		t.Fatalf("default list: %v", defaultResult.err)
	}
	defaultIDs := sessionListOutputIDs(defaultResult.stdout)
	if len(defaultIDs) != session.DefaultSessionListLimit {
		t.Fatalf("default list count: got %d, want %d", len(defaultIDs), session.DefaultSessionListLimit)
	}
	if defaultIDs[0] != "bulk-104" || defaultIDs[len(defaultIDs)-1] != "bulk-005" {
		t.Fatalf("default list bounds: first=%q last=%q", defaultIDs[0], defaultIDs[len(defaultIDs)-1])
	}

	customResult := list("--limit", "3")
	if customResult.err != nil {
		t.Fatalf("custom limit list: %v", customResult.err)
	}
	if got := sessionListOutputIDs(customResult.stdout); !equalStrings(got, []string{"bulk-104", "bulk-103", "bulk-102"}) {
		t.Fatalf("custom limit IDs: got %#v", got)
	}

	sinceResult := list("--since", base.Add(100*time.Second).Format(time.RFC3339))
	if sinceResult.err != nil {
		t.Fatalf("since list: %v", sinceResult.err)
	}
	if got := sessionListOutputIDs(sinceResult.stdout); !equalStrings(got, []string{"bulk-104", "bulk-103", "bulk-102", "bulk-101", "bulk-100"}) {
		t.Fatalf("since IDs: got %#v", got)
	}

	filterResult := list("--filter", "BuLk-00")
	if filterResult.err != nil {
		t.Fatalf("filter list: %v", filterResult.err)
	}
	if got := sessionListOutputIDs(filterResult.stdout); !equalStrings(got, []string{"bulk-009", "bulk-008", "bulk-007", "bulk-006", "bulk-005", "bulk-004", "bulk-003", "bulk-002", "bulk-001", "bulk-000"}) {
		t.Fatalf("filter IDs: got %#v", got)
	}

	composedResult := list("--limit", "2", "--since", base.Add(100*time.Second).Format(time.RFC3339), "--filter", "BULK-10")
	if composedResult.err != nil {
		t.Fatalf("composed list: %v", composedResult.err)
	}
	if got := sessionListOutputIDs(composedResult.stdout); !equalStrings(got, []string{"bulk-104", "bulk-103"}) {
		t.Fatalf("composed IDs: got %#v", got)
	}
}

func TestSessionListCommandRejectsInvalidQueriesBeforeOutput(t *testing.T) {
	configDir := t.TempDir()
	storage := session.NewStorage(configDir)
	if err := storage.Save("existing", nil); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "zero limit", args: []string{"--limit", "0"}, want: "--limit must be between 1 and 1000"},
		{name: "negative limit", args: []string{"--limit", "-1"}, want: "--limit must be between 1 and 1000"},
		{name: "non numeric limit", args: []string{"--limit", "many"}, want: "--limit must be an integer between 1 and 1000"},
		{name: "over maximum limit", args: []string{"--limit", "1001"}, want: "--limit must be between 1 and 1000"},
		{name: "invalid since", args: []string{"--since", "not-a-timestamp"}, want: "--since must be an RFC3339 timestamp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--config-dir", configDir, "session", "list"}, tt.args...)
			result := executeGeneratedCLI(context.Background(), configDir, args...)
			if result.err == nil {
				t.Fatal("expected invalid query error")
			}
			if !strings.Contains(result.err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", result.err, tt.want)
			}
			if result.stdout != "" {
				t.Fatalf("stdout = %q, want no partial rows", result.stdout)
			}
		})
	}
}

func TestSessionListCommandEmptyStateIsUnchanged(t *testing.T) {
	result := executeGeneratedCLI(context.Background(), t.TempDir(), "session", "list")
	if result.err != nil {
		t.Fatalf("empty list: %v", result.err)
	}
	if result.stdout != "No sessions found.\n" {
		t.Fatalf("empty list output = %q, want %q", result.stdout, "No sessions found.\n")
	}
}

func sessionListOutputIDs(output string) []string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 1 && lines[0] == "No sessions found." {
		return nil
	}
	ids := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
