package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenPeeDeeP/depguard/v2"
	"golang.org/x/tools/go/analysis"
	"gopkg.in/yaml.v3"
)

// golangciDepguard is the depguard section of .golangci.yml.
type golangciDepguard struct {
	Linters struct {
		Settings struct {
			Depguard struct {
				Rules map[string]struct {
					ListMode string   `yaml:"list-mode"`
					Files    []string `yaml:"files"`
					Allow    []string `yaml:"allow"`
					Deny     []struct {
						Pkg  string `yaml:"pkg"`
						Desc string `yaml:"desc"`
					} `yaml:"deny"`
				} `yaml:"rules"`
			} `yaml:"depguard"`
		} `yaml:"settings"`
	} `yaml:"linters"`
}

// depguardCase places one import in a repository source file and names the
// depguard list that must reject it ("" when every list must accept it).
type depguardCase struct {
	file, imported, list string
}

// TestDepguardImportRulesRejectViolations proves the module-direction and
// contract import rules that moved from the architecture policy to
// golangci-lint's depguard reject a violation, and that their files,
// test-file and exception scopes stay as narrow as the gate rules they
// replaced. It runs the depguard analyzer version golangci-lint v2.9.0 pins,
// configured from the repository's .golangci.yml the way golangci-lint
// configures it.
func TestDepguardImportRulesRejectViolations(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	analyzer := repositoryDepguard(t, root)
	for _, test := range depguardCases() {
		t.Run(test.file+" imports "+test.imported, func(t *testing.T) {
			messages := depguardMessages(t, analyzer, filepath.Join(root, filepath.FromSlash(test.file)), test.imported)
			if test.list == "" {
				if len(messages) != 0 {
					t.Fatalf("import was rejected: %q", messages)
				}
				return
			}
			want := "from list '" + test.list + "'"
			for _, message := range messages {
				if strings.Contains(message, want) {
					return
				}
			}
			t.Fatalf("import was not rejected %s: %q", want, messages)
		})
	}
}

func depguardCases() []depguardCase {
	const repo = "github.com/portpowered/go-agent-harness"
	cli, runtime, loop := repo+"/agent-cli", repo+"/go-agent-runtime", repo+"/go-agent-loop"
	device, provider := repo+"/go-device-gateway/pkg/devices", repo+"/go-llm-gateway/pkg/providers/openai"
	private := cli + "/internal/services/internal/devices"
	return []depguardCase{
		// Module dependency direction (production and tests).
		{"go-agent-loop/pkg/engine/engine.go", runtime + "/services/session", "reusable-modules"},
		{"go-audio/pkg/codec/codec_test.go", runtime, "reusable-modules"},
		{"go-device-gateway/pkg/devices/devices.go", cli + "/internal/config", "reusable-modules"},
		{"go-llm-gateway/pkg/providers/openai/session.go", cli, "reusable-modules"},
		{"go-agent-runtime/services/session/service.go", cli + "/internal/config", "runtime-module"},
		{"go-agent-runtime/runtime.go", provider, ""},
		{"go-agent-loop/pkg/engine/engine.go", provider, "agent-loop"},
		{"go-agent-loop/pkg/engine/engine_test.go", device, "agent-loop"},
		{"go-audio/pkg/codec/codec.go", loop + "/pkg/messages", "go-audio"},
		{"go-audio/pkg/codec/codec.go", device, "go-audio"},
		{"go-audio/pkg/codec/codec.go", provider, "go-audio"},
		{"go-llm-gateway/pkg/providers/openai/session.go", loop + "/pkg/engine", ""},
		{"go-llm-gateway/test/functional/gateway_test.go", loop + "/pkg/engine", "gateway-functional-tests"},
		{"go-llm-gateway/test/functional/gateway_test.go", loop + "/pkg/messages", ""},
		{"go-llm-gateway/test/functional/gateway_test.go", loop + "/pkg/messages/internal", "gateway-functional-tests"},
		{"go-llm-gateway/test/functional/gateway_test.go", "context", ""},
		{"go-llm-gateway/test/functional/nested/nested_test.go", loop + "/pkg/engine", ""},
		// Public service contracts and the device transports.
		{"agent-cli/internal/services/agentsession/interface.go", device, "session-contract"},
		{"agent-cli/internal/services/agentsession/interface.go", provider, "session-contract"},
		{"agent-cli/internal/services/agentsession/session.go", device, ""},
		{"agent-cli/internal/services/tools/interface.go", cli + "/internal/services/internal/tools", "tool-contract"},
		{"agent-cli/internal/services/devices/interface.go", device, "device-contract"},
		{"agent-cli/internal/services/devices/interface.go", private, "device-contract"},
		{"agent-cli/internal/services/devices/validation.go", device, ""},
		{"agent-cli/internal/transport/cli/devices.go", device, "device-contract"},
		{"agent-cli/internal/transport/cli/probe_output.go", device, "device-contract"},
		{"agent-cli/internal/transport/cli/probe.go", device, "device-contract"},
		{"agent-cli/internal/transport/cli/session.go", device, ""},
		{"agent-cli/internal/transport/cli/internal/livehost/files.go", private, "cli-transports"},
		{"agent-cli/internal/transport/cli/session_test.go", private, ""},
		// Audio payload access (production only).
		{"go-agent-loop/pkg/engine/engine.go", "encoding/binary", "agent-loop-production"},
		{"go-agent-loop/test/functional/media/harness.go", loop + "/pkg/platform/clock", "agent-loop-production"},
		{"go-agent-loop/pkg/engine/engine_test.go", "encoding/binary", ""},
		{"agent-cli/internal/room/room.go", repo + "/go-llm-gateway/pkg/wavio", "agent-cli-production"},
		{"agent-cli/internal/room/room.go", cli + "/internal/audio/pcm", "agent-cli-production"},
		{"agent-cli/internal/room/room.go", cli + "/internal/audiocapture", "agent-cli-production"},
		{"agent-cli/internal/room/room.go", loop + "/pkg/platform/clock", "agent-cli-production"},
		{"agent-cli/internal/webmcp/frames.go", "encoding/binary", "agent-cli-binary"},
		{"agent-cli/internal/webmcp/testkit/ids.go", "encoding/binary", ""},
		{"agent-cli/internal/room/room_test.go", "encoding/binary", ""},
		{"agent-cli/cmd/agent/main.go", "encoding/binary", ""},
	}
}

// repositoryDepguard builds the depguard analyzer from .golangci.yml,
// expanding ${config-path} and ${base-path} to the repository root as
// golangci-lint does for the root configuration.
func repositoryDepguard(t *testing.T, root string) *analysis.Analyzer {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var config golangciDepguard
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	rules := config.Linters.Settings.Depguard.Rules
	if len(rules) == 0 {
		t.Fatal(".golangci.yml has no depguard rules")
	}
	placeholders := strings.NewReplacer("${config-path}", filepath.ToSlash(root), "${base-path}", filepath.ToSlash(root))
	settings := depguard.LinterSettings{}
	for name, rule := range rules {
		list := &depguard.List{ListMode: rule.ListMode, Allow: rule.Allow, Deny: map[string]string{}}
		for _, file := range rule.Files {
			list.Files = append(list.Files, placeholders.Replace(file))
		}
		for _, deny := range rule.Deny {
			list.Deny[deny.Pkg] = deny.Desc
		}
		settings[name] = list
	}
	analyzer, err := depguard.NewAnalyzer(&settings)
	if err != nil {
		t.Fatal(err)
	}
	return analyzer
}

// depguardMessages runs the analyzer over a one-import file named name.
func depguardMessages(t *testing.T, analyzer *analysis.Analyzer, name, imported string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, "package fixture\n\nimport _ "+strconv.Quote(imported)+"\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	var messages []string
	pass := &analysis.Pass{Analyzer: analyzer, Fset: fset, Files: []*ast.File{file}, Report: func(diagnostic analysis.Diagnostic) {
		messages = append(messages, diagnostic.Message)
	}}
	if _, err := analyzer.Run(pass); err != nil {
		t.Fatal(err)
	}
	return messages
}
