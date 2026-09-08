package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Loader discovers and loads skills from explicitly supplied skill roots.
// Metadata (name, description) is loaded at startup; full SKILL.md body when a skill is activated.
type Loader struct {
	skillRoots []string
}

// NewLoader creates a loader using the historical workspaceDir/skills and
// configSkillsDir/skills layout. New service callers should prefer
// NewLoaderFromRoots so the host can resolve each root explicitly.
func NewLoader(workspaceDir, configSkillsDir string) *Loader {
	roots := make([]string, 0, 2)
	if workspaceDir != "" {
		roots = append(roots, filepath.Join(workspaceDir, "skills"))
	}
	if configSkillsDir != "" {
		roots = append(roots, filepath.Join(configSkillsDir, "skills"))
	}
	return NewLoaderFromRoots(roots...)
}

// NewLoaderFromRoots creates a loader over ordered directories that directly
// contain skill subdirectories. Earlier roots take precedence when skill names
// collide. The caller owns path resolution; this constructor never consults
// the process working directory, user home, or environment.
func NewLoaderFromRoots(skillRoots ...string) *Loader {
	roots := make([]string, 0, len(skillRoots))
	seen := make(map[string]struct{}, len(skillRoots))
	for _, root := range skillRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return &Loader{skillRoots: roots}
}

// List returns all discovered skills (metadata only). Workspace skills take precedence over config skills when names collide.
func (l *Loader) List() ([]Skill, error) {
	seen := make(map[string]struct{})
	var out []Skill

	for _, root := range l.skillRoots {
		rootSkills, err := listRoot(root, seen)
		if err != nil {
			return nil, err
		}
		out = append(out, rootSkills...)
	}
	return out, nil
}

func listRoot(root string, seen map[string]struct{}) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir %s: %w", root, err)
	}
	result := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		skill, include, err := skillFromEntry(root, entry, seen)
		if err != nil {
			return nil, err
		}
		if include {
			result = append(result, skill)
		}
	}
	return result, nil
}

func skillFromEntry(root string, entry os.DirEntry, seen map[string]struct{}) (Skill, bool, error) {
	if !entry.IsDir() {
		return Skill{}, false, nil
	}
	name := entry.Name()
	if _, ok := seen[name]; ok {
		return Skill{}, false, nil
	}
	skillPath := filepath.Join(root, name, SkillFileName)
	if err := requireSkillFile(skillPath); err != nil {
		if os.IsNotExist(err) {
			return Skill{}, false, nil
		}
		return Skill{}, false, err
	}
	meta, valid := readSkillMetadata(skillPath)
	if !valid {
		return Skill{}, false, nil
	}
	if !validSkillName(meta.Name, name) {
		return Skill{}, false, nil
	}
	absDir, err := filepath.Abs(filepath.Join(root, name))
	if err != nil {
		return Skill{}, false, err
	}
	absPath, err := filepath.Abs(skillPath)
	if err != nil {
		return Skill{}, false, err
	}
	seen[name] = struct{}{}
	return Skill{Meta: meta, Dir: absDir, SkillPath: absPath}, true, nil
}

func readSkillMetadata(path string) (Meta, bool) {
	meta, err := ParseSkillFileMetadataOnly(path)
	if err != nil {
		return Meta{}, false
	}
	return meta, true
}

func validSkillName(name, directory string) bool {
	return ValidateName(name, directory) == nil
}

func requireSkillFile(path string) error {
	_, err := os.Stat(path)
	return err
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
	list, err := l.List()
	if err != nil {
		return ""
	}
	for _, s := range list {
		if s.Meta.Name == name {
			return s.Dir
		}
	}
	return ""
}
