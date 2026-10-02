package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
)

// TestToolCommandBroadAllowPathRefusesHomeCredentials proves the CLI injects
// the host home directory, so --allow-path / or a parent of HOME can neither
// read ~/.ssh nor plant ~/.ssh/authorized_keys, while ordinary files under the
// same root stay readable.
func TestToolCommandBroadAllowPathRefusesHomeCredentials(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "user")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(home, ".ssh", "id_rsa")
	if err := os.WriteFile(key, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(notes, []byte("ordinary notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.VolumeName(base) + string(filepath.Separator), base} {
		t.Run(root, func(t *testing.T) {
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = t.TempDir()
			globalFlags.WorkDirPath = t.TempDir()
			globalFlags.AllowPathList = []string{root}
			globalFlags.Host.HomeDir = func() (string, error) { return home, nil }
			command := NewToolCommand(globalFlags)

			authorized := filepath.Join(home, ".ssh", "authorized_keys")
			for _, args := range [][]string{
				{"read_file", "path=" + key},
				{"list_dir", "path=" + filepath.Dir(key)},
				{"write_file", "path=" + authorized, "content=ssh-ed25519 ATTACKER"},
				{"append_file", "path=" + authorized, "content=ssh-ed25519 ATTACKER"},
			} {
				assertToolCommandRefused(t, command, args)
			}
			if _, err := os.Lstat(authorized); !os.IsNotExist(err) {
				t.Fatalf("authorized_keys was planted (err %v)", err)
			}
			var out bytes.Buffer
			if err := runToolTestCommand(t, command, []string{"read_file", "path=" + notes}, &out); err != nil || !strings.Contains(out.String(), "ordinary notes") {
				t.Fatalf("ordinary read under the broad root = %q, %v", out.String(), err)
			}
		})
	}
}

// assertToolCommandRefused runs one tool command and requires a filesystem
// refusal whose output does not disclose the credential.
func assertToolCommandRefused(t *testing.T, command *ToolCommand, args []string) {
	t.Helper()
	var out bytes.Buffer
	err := runToolTestCommand(t, command, args, &out)
	if err == nil || !errors.Is(err, tools.ErrFilesystemRefused) {
		t.Fatalf("%s error = %v, want a filesystem refusal", args[0], err)
	}
	if strings.Contains(out.String(), "PRIVATE KEY") || strings.Contains(out.String(), "id_rsa") {
		t.Fatalf("%s leaked the credential: %q", args[0], out.String())
	}
}
