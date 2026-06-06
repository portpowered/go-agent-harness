package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Loader discovers and loads skills from workspace and config directories.
// Metadata (name, description) is loaded at startup; full SKILL.md body when a skill is activated.
type Loader struct {
	workspaceSkillsDir string
	configSkillsDir    string
}

// NewLoader creates a loader that looks for skills in workspaceDir/skills and configSkillsDir.
// configSkillsDir can be empty (only workspace skills are used).
func NewLoader(workspaceDir, configSkillsDir string) *Loader {
	ws := ""
	if workspaceDir != "" {
		ws = filepath.Join(workspaceDir, "skills")
	}
	cs := ""
	if configSkillsDir != "" {
		cs = filepath.Join(configSkillsDir, "skills")
	}
	return &Loader{workspaceSkillsDir: ws, configSkillsDir: cs}
}

// List returns all discovered skills (metadata only). Workspace skills take precedence over config skills when names collide.
func (l *Loader) List() ([]Skill, error) {
	seen := make(map[string]struct{})
	var out []Skill

	for _, dir := range []string{l.workspaceSkillsDir, l.configSkillsDir} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if _, ok := seen[name]; ok {
				continue
			}
			skillPath := filepath.Join(dir, name, SkillFileName)
			if _, err := os.Stat(skillPath); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			meta, err := ParseSkillFileMetadataOnly(skillPath)
			if err != nil {
				continue // skip invalid skills
			}
			if err := ValidateName(meta.Name, name); err != nil {
				continue
			}
			seen[name] = struct{}{}
			absDir, _ := filepath.Abs(filepath.Join(dir, name))
			absPath, _ := filepath.Abs(skillPath)
			out = append(out, Skill{
				Meta:      meta,
				Dir:       absDir,
				SkillPath: absPath,
			})
		}
	}
	return out, nil
}

// BuildSummary returns a markdown section listing available skills (name + description) for injection into the system prompt (~100 tokens per skill).
func (l *Loader) BuildSummary() (string, error) {
	list, err := l.List()
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("# Available skills\n\n")
	sb.WriteString("The following skills extend your capabilities. To use a skill, call the `load_skill` tool with the skill name to load its full instructions. Optionally request a resource path (e.g. `references/REFERENCE.md`) to load a file from the skill.\n\n")
	for _, s := range list {
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Meta.Name, s.Meta.Description))
	}
	return sb.String(), nil
}

// LoadSkill returns the full SKILL.md content (frontmatter stripped; body only) for the named skill.
func (l *Loader) LoadSkill(name string) (body string, err error) {
	list, err := l.List()
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Meta.Name == name {
			_, body, err := ParseSkillFile(s.SkillPath)
			return body, err
		}
	}
	return "", fmt.Errorf("skill %q not found", name)
}

// LoadSkillWithPath returns the full SKILL.md body. If resourcePath is non-empty, it returns that file's content from the skill (under scripts/, references/, or assets/).
func (l *Loader) LoadSkillWithPath(name, resourcePath string) (content string, err error) {
	list, err := l.List()
	if err != nil {
		return "", err
	}
	var skillDir string
	for _, s := range list {
		if s.Meta.Name == name {
			skillDir = s.Dir
			break
		}
	}
	if skillDir == "" {
		return "", fmt.Errorf("skill %q not found", name)
	}
	if resourcePath == "" {
		return l.LoadSkill(name)
	}
	data, err := ReadResource(skillDir, resourcePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// GetSkillDir returns the absolute directory path for a skill by name, or empty if not found.
func (l *Loader) GetSkillDir(name string) string {
	list, _ := l.List()
	for _, s := range list {
		if s.Meta.Name == name {
			return s.Dir
		}
	}
	return ""
}
