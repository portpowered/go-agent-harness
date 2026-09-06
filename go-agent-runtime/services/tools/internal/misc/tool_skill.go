package misc

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/skills"
)

// LoadSkillTool lets the model load a skill's full instructions or a resource file from a skill.
type LoadSkillTool struct {
	loader *skills.Loader
}

func NewLoadSkillTool() *LoadSkillTool {
	return NewLoadSkillToolFromRoots()
}

// NewLoadSkillToolFromRoots binds one immutable request-scoped loader to the
// tool. Roots directly contain skill subdirectories and are ordered by
// precedence. Execution never reads workspace or config paths from context.
func NewLoadSkillToolFromRoots(skillRoots ...string) *LoadSkillTool {
	return &LoadSkillTool{loader: skills.NewLoaderFromRoots(skillRoots...)}
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

func (t *LoadSkillTool) Execute(_ context.Context, args map[string]any) ([]messages.Message, error) {
	skillName, ok := args["skill_name"].(string)
	if !ok || skillName == "" {
		return core.ErrorAsToolMessage(fmt.Errorf("skill_name is required"))
	}
	resourcePath, ok := args["resource_path"].(string)
	if !ok {
		resourcePath = ""
	}

	if t == nil || t.loader == nil {
		return core.ErrorAsToolMessage(fmt.Errorf("skill roots are not configured for this tool surface"))
	}
	content, err := t.loader.LoadSkillWithPath(skillName, resourcePath)
	if err != nil {
		return core.ErrorAsToolMessage(err)
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, content)}, nil
}
