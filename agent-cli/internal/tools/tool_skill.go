package tools

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/execctx"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// LoadSkillTool lets the model load a skill's full instructions or a resource file from a skill.
type LoadSkillTool struct{}

func NewLoadSkillTool() *LoadSkillTool {
	return &LoadSkillTool{}
}

func (t *LoadSkillTool) Name() string {
	return "load_skill"
}

func (t *LoadSkillTool) Description() string {
	return "Load an Agent Skill by name. Call with skill_name to load the skill's full instructions (SKILL.md). Optionally provide resource_path (e.g. references/REFERENCE.md, scripts/foo.sh) to load a specific file from the skill's scripts/, references/, or assets/ directory. Use this when you need to follow a skill's procedures or read its reference material."
}

func (t *LoadSkillTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skill_name": map[string]any{
				"type":        "string",
				"description": "Name of the skill to load (e.g. pdf-processing, data-analysis)",
			},
			"resource_path": map[string]any{
				"type":        "string",
				"description": "Optional path relative to the skill directory, under scripts/, references/, or assets/ (e.g. references/REFERENCE.md)",
			},
		},
		"required": []string{"skill_name"},
	}
}

func (t *LoadSkillTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	skillName, ok := args["skill_name"].(string)
	if !ok || skillName == "" {
		return ErrorAsToolMessage(fmt.Errorf("skill_name is required"))
	}
	resourcePath, _ := args["resource_path"].(string)

	workspaceDir := execctx.WorkspaceDir(ctx)
	configDir := execctx.ConfigDir(ctx)
	// Default config dir when not set (e.g. in tests)
	if configDir == "" && workspaceDir != "" {
		configDir = workspaceDir
	}
	loader := skills.NewLoader(workspaceDir, configDir)

	content, err := loader.LoadSkillWithPath(skillName, resourcePath)
	if err != nil {
		return ErrorAsToolMessage(err)
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, content)}, nil
}
