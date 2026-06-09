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

// EnsureAgentsMD creates AGENTS.md in workspaceDir if it does not already exist.
// toolDefs describes the tools registered in the current session.
func EnsureAgentsMD(workspaceDir string, toolDefs []messages.ToolDefinition) error {
	path := filepath.Join(workspaceDir, AgentsMDFileName)
	if _, err := os.Stat(path); err == nil {
		return nil // already exists, leave it alone
	}
	content := generateAgentsMD(workspaceDir, toolDefs)
	return os.WriteFile(path, []byte(content), 0644)
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
	fmt.Fprintln(&sb, "## Available Tools")
	fmt.Fprintln(&sb)
	if len(toolDefs) == 0 {
		fmt.Fprintln(&sb, "No tools are currently registered.")
	} else {
		for _, t := range toolDefs {
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
			fmt.Fprintln(&sb)
		}
	}

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
	fmt.Fprintln(&sb, "agent session show <session-id>")
	fmt.Fprintln(&sb, "agent session delete <session-id>")
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
