package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const AgentsMDFileName = "AGENTS.md"

const (
	availableToolsHeading     = "## Available Tools"
	availableToolsStartMarker = "<!-- BEGIN AGENT CLI MANAGED AVAILABLE TOOLS -->"
	availableToolsEndMarker   = "<!-- END AGENT CLI MANAGED AVAILABLE TOOLS -->"
)

// EnsureAgentsMD creates AGENTS.md in workspaceDir when it does not already
// exist and reconciles the CLI-managed Available Tools section when it does.
// Content outside that section is left byte-for-byte unchanged. toolDefs
// describes the tools registered in the current session.
func EnsureAgentsMD(workspaceDir string, toolDefs []messages.ToolDefinition) error {
	path := filepath.Join(workspaceDir, AgentsMDFileName)
	if _, err := os.Stat(path); err != nil {
		// Keep creation/error behavior for a missing file (and for an invalid
		// parent path) identical to the original implementation.
		return os.WriteFile(path, []byte(generateAgentsMD(workspaceDir, toolDefs)), 0644)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		// Preserve the historical behavior for an existing but unreadable
		// AGENTS.md. Prompt loading treats its subsequent read failure as an
		// empty prompt, and reconciliation cannot safely modify bytes it could
		// not read.
		return nil
	}

	reconciled, changed := reconcileAvailableToolsSection(string(content), toolDefs)
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(reconciled), 0644)
}

// generateAgentsMD produces the full AGENTS.md content.
func generateAgentsMD(workspaceDir string, toolDefs []messages.ToolDefinition) string {
	var sb strings.Builder

	fmt.Fprintln(&sb, "# Agent CLI — System Instructions")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "You are a general-purpose AI agent running inside the `agent` CLI. You can answer")
	fmt.Fprintln(&sb, "questions, reason through problems, and use tools to interact with the host system.")
	fmt.Fprintln(&sb)

	// --- Environment ---
	fmt.Fprintln(&sb, "## Environment")
	fmt.Fprintln(&sb)
	fmt.Fprintf(&sb, "- **OS**: %s\n", runtime.GOOS)
	fmt.Fprintf(&sb, "- **Architecture**: %s\n", runtime.GOARCH)
	fmt.Fprintf(&sb, "- **Workspace**: `%s`\n", workspaceDir)
	fmt.Fprintln(&sb)

	// --- Available tools ---
	sb.WriteString(renderAvailableToolsSection(toolDefs))

	// --- Configuration ---
	fmt.Fprintln(&sb, "## Configuration")
	fmt.Fprintln(&sb)
	fmt.Fprintf(&sb, "Configuration is loaded from `%s/config.yaml`.\n", workspaceDir)
	fmt.Fprintln(&sb, "CLI flags override the file; environment variables (prefix `AGENT_`, `__` for nesting) override both.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "```yaml")
	fmt.Fprintln(&sb, "model:")
	fmt.Fprintln(&sb, "  provider: openrouter        # openai | openrouter")
	fmt.Fprintln(&sb, "  openrouter:")
	fmt.Fprintln(&sb, "    model: z-ai/glm-4.7")
	fmt.Fprintln(&sb, "    api_key: sk-or-v1-...")
	fmt.Fprintln(&sb, "    base_url: https://openrouter.ai/api/v1")
	fmt.Fprintln(&sb, "  openai:")
	fmt.Fprintln(&sb, "    model: gpt-4")
	fmt.Fprintln(&sb, "    api_key: sk-...")
	fmt.Fprintln(&sb, "    base_url: https://api.openai.com/v1  # optional")
	fmt.Fprintln(&sb, "  claude:")
	fmt.Fprintln(&sb, "    model: claude-opus-4.6")
	fmt.Fprintln(&sb, "    api_key: sk-ant-...")
	fmt.Fprintln(&sb, "tools:")
	fmt.Fprintln(&sb, "  web:")
	fmt.Fprintln(&sb, "    brave:")
	fmt.Fprintln(&sb, "      enabled: true")
	fmt.Fprintln(&sb, "      api_key: your-brave-key")
	fmt.Fprintln(&sb, "      max_results: 10")
	fmt.Fprintln(&sb, "    duckduckgo:")
	fmt.Fprintln(&sb, "      enabled: true")
	fmt.Fprintln(&sb, "      max_results: 10")
	fmt.Fprintln(&sb, "  exec:")
	fmt.Fprintln(&sb, "    enable_deny_patterns: true   # block dangerous shell patterns")
	fmt.Fprintln(&sb, "    custom_deny_patterns: []     # additional patterns to block")
	fmt.Fprintln(&sb, "```")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Environment variable examples:")
	fmt.Fprintln(&sb, "```")
	fmt.Fprintln(&sb, "AGENT_MODEL__PROVIDER=openai")
	fmt.Fprintln(&sb, "AGENT_MODEL__OPENAI__API_KEY=sk-...")
	fmt.Fprintln(&sb, "AGENT_MODEL__OPENROUTER__MODEL=custom-model")
	fmt.Fprintln(&sb, "```")
	fmt.Fprintln(&sb)

	// --- CLI Commands ---
	fmt.Fprintln(&sb, "## CLI Commands")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "```bash")
	fmt.Fprintln(&sb, "# One-shot query")
	fmt.Fprintln(&sb, "agent ask \"your question\"")
	fmt.Fprintln(&sb, "agent ask ./file.jpg \"describe this image\" --model google/gemini-3.1-pro-preview --provider openrouter")
	fmt.Fprintln(&sb, "agent ask \"follow up\" --continue-last-session")
	fmt.Fprintln(&sb, "agent ask \"resume\" --session-id <id>")
	fmt.Fprintln(&sb, "agent ask \"streamed response\" --stream")
	fmt.Fprintln(&sb, "agent ask \"show me the tools\" --show-tool-use")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "# Interactive chat")
	fmt.Fprintln(&sb, "agent chat")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "# Invoke a tool directly (for debugging)")
	fmt.Fprintln(&sb, "agent tool <tool-id> \"key=value\" ...")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "# Session management")
	fmt.Fprintln(&sb, "agent session list")
	fmt.Fprintln(&sb, "agent session list --limit 20 --since 2026-08-31T00:00:00Z --filter billing")
	fmt.Fprintln(&sb, "agent session show <session-id>")
	fmt.Fprintln(&sb, "agent session delete <session-id>")
	fmt.Fprintln(&sb, "# session list defaults to the newest 100; --limit accepts 1-1000")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "# Override config at runtime")
	fmt.Fprintln(&sb, "agent ask \"...\" --api-key <key> --model <model-id> --provider <provider>")
	fmt.Fprintln(&sb, "agent --config-dir /path/to/dir ask \"...\"")
	fmt.Fprintln(&sb, "```")
	fmt.Fprintln(&sb)

	// --- Workspace layout ---
	fmt.Fprintln(&sb, "## Workspace Layout")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "```")
	fmt.Fprintf(&sb, "%s/\n", workspaceDir)
	fmt.Fprintln(&sb, "    config.yaml     # model provider, API keys, tool settings")
	fmt.Fprintln(&sb, "    AGENTS.md       # this file — instructions for the agent")
	fmt.Fprintln(&sb, "    skills/         # Agent Skills (SKILL.md per skill); use load_skill tool to activate")
	fmt.Fprintln(&sb, "    sessions/       # conversation history (JSON per session)")
	fmt.Fprintln(&sb, "```")
	fmt.Fprintln(&sb)

	// --- Guidelines ---
	fmt.Fprintln(&sb, "## Guidelines")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "- Be direct and concise in responses.")
	fmt.Fprintln(&sb, "- Use tools when they would help answer the question accurately.")
	fmt.Fprintln(&sb, "- Prefer targeted file reads over broad directory listings.")
	fmt.Fprintln(&sb, "- When executing shell commands, prefer non-interactive commands.")
	fmt.Fprintln(&sb, "- For multi-step tasks, think step-by-step before acting.")

	return sb.String()
}

func renderAvailableToolsSection(toolDefs []messages.ToolDefinition) string {
	var sb strings.Builder
	toolDefs = messages.CanonicalToolDefinitions(toolDefs)

	fmt.Fprintln(&sb, availableToolsHeading)
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, availableToolsStartMarker)
	if len(toolDefs) == 0 {
		fmt.Fprintln(&sb, "No tools are currently registered.")
	} else {
		for _, t := range toolDefs {
			fmt.Fprintln(&sb)
			fmt.Fprintf(&sb, "### `%s`\n", t.Name)
			fmt.Fprintf(&sb, "%s\n", t.Description)
			if len(t.Parameters) > 0 {
				fmt.Fprintln(&sb)
				fmt.Fprintln(&sb, "| Parameter | Type | Required | Description |")
				fmt.Fprintln(&sb, "|-----------|------|----------|-------------|")
				for _, p := range t.Parameters {
					req := "no"
					if p.Required {
						req = "yes"
					}
					fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", p.Name, p.Type, req, p.Description)
				}
			}
		}
	}
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, availableToolsEndMarker)
	fmt.Fprintln(&sb)

	return sb.String()
}

// reconcileAvailableToolsSection replaces the complete level-two Available
// Tools section while preserving every byte before and after it. The heading
// fallback intentionally understands the pre-marker documents generated by
// older agent-cli versions, so one invocation migrates those documents to the
// marker-delimited format used for future updates.
func reconcileAvailableToolsSection(content string, toolDefs []messages.ToolDefinition) (string, bool) {
	start, end, ok := availableToolsSectionBounds(content)
	if !ok {
		return content, false
	}

	replacement := renderAvailableToolsSection(toolDefs)
	reconciled := content[:start] + replacement + content[end:]
	return reconciled, reconciled != content
}

func availableToolsSectionBounds(content string) (int, int, bool) {
	for offset := 0; offset < len(content); {
		lineStart := offset
		lineEnd := strings.IndexByte(content[offset:], '\n')
		if lineEnd < 0 {
			lineEnd = len(content)
		} else {
			lineEnd += offset + 1
		}
		line := strings.TrimSpace(strings.TrimSuffix(content[lineStart:lineEnd], "\r"))
		if line == availableToolsHeading {
			sectionEnd := len(content)
			for next := lineEnd; next < len(content); {
				nextStart := next
				nextEnd := strings.IndexByte(content[next:], '\n')
				if nextEnd < 0 {
					nextEnd = len(content)
				} else {
					nextEnd += next + 1
				}
				nextLine := strings.TrimSpace(strings.TrimSuffix(content[nextStart:nextEnd], "\r"))
				if strings.HasPrefix(nextLine, "## ") && !strings.HasPrefix(nextLine, "### ") {
					sectionEnd = nextStart
					break
				}
				if nextEnd >= len(content) {
					break
				}
				next = nextEnd
			}
			return lineStart, sectionEnd, true
		}
		if lineEnd >= len(content) {
			break
		}
		offset = lineEnd
	}
	return 0, 0, false
}
