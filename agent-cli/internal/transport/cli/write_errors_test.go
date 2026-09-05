package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/spf13/cobra"
)

type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestToolCommandListToolsPropagatesWriterError(t *testing.T) {
	cmd := NewToolCommand(flags.NewGlobalFlags())
	registry := tools.NewToolRegistry()
	wantErr := errors.New("write failed")

	err := cmd.listTools(errWriter{err: wantErr}, registry)
	if !errors.Is(err, wantErr) {
		t.Fatalf("listTools error = %v, want %v", err, wantErr)
	}
}

func TestConfigAddLocalCommandRunPropagatesSummaryWriteError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()

	cmd := &cobra.Command{}
	cmd.SetOut(errWriter{err: errors.New("summary write failed")})
	cmd.SetErr(errWriter{err: errors.New("stderr write failed")})

	configCmd := NewConfigAddLocalCommand(globalFlags)
	configCmd.baseURL = server.URL
	configCmd.model = "test-model"

	err := configCmd.run(cmd)
	if err == nil {
		t.Fatal("expected summary write error, got nil")
	}
	if !strings.Contains(err.Error(), "write config summary") {
		t.Fatalf("error = %v, want write config summary context", err)
	}
}
