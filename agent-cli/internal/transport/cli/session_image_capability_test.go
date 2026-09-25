package cli

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionImageRefusedWhenModelCannotAcceptImageInput pins the session
// --image admission contract: an image is refused before any provider session
// opens when the selected provider/model cannot accept image input, whether
// the provider lacks image input or configured model metadata omits it.
func TestSessionImageRefusedWhenModelCannotAcceptImageInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		modelsYML string
		want      string
	}{
		{
			name: "provider without image input",
			args: []string{"--provider", config.ProviderGrok, "--model", "grok-voice"},
			want: `model "grok-voice" does not support image input capability`,
		},
		{
			name:      "configured model without image modality",
			args:      []string{"--provider", config.ProviderOpenAI, "--model", "gpt-realtime-2.1"},
			modelsYML: "models:\n  - name: gpt-realtime-2.1\n    providers: [openai]\n    input_modalities: [text, audio]\n",
			want:      `model "gpt-realtime-2.1" does not support image input capability`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			configDir := filepath.Join(root, "config")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatalf("create config directory: %v", err)
			}
			if test.modelsYML != "" {
				if err := os.WriteFile(filepath.Join(configDir, config.ModelsFileName), []byte(test.modelsYML), 0o600); err != nil {
					t.Fatalf("write models.yaml: %v", err)
				}
			}
			imagePath := filepath.Join(root, "photo.png")
			writeSessionCapabilityPNG(t, imagePath)
			inferencer := &connectRecordingInferencer{}
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = configDir
			command := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, inferencer, nil).Generate()
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			command.SetArgs(append(append([]string(nil), test.args...), "--api-key", "test-key", "--image", imagePath))

			err := command.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("session --image error = %v, want %q\nstdout=%q\nstderr=%q", err, test.want, stdout.String(), stderr.String())
			}
			if inferencer.opened.Load() {
				t.Fatal("provider session opened for a model without image input")
			}
		})
	}
}

func writeSessionCapabilityPNG(t *testing.T, path string) {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

// connectRecordingInferencer records whether a provider session was requested
// and refuses it, so a test can prove admission failed before connection.
type connectRecordingInferencer struct{ opened atomic.Bool }

func (i *connectRecordingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.opened.Store(true)
	return nil, errors.New("provider session must not open")
}
