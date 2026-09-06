package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	SkillFileName         = "SKILL.md"
	frontmatterMatchCount = 3
	// Dir names from Agent Skills spec (optional directories).
	ScriptsDir    = "scripts"
	ReferencesDir = "references"
	AssetsDir     = "assets"
)

// Meta holds the required frontmatter fields for a skill (metadata only, ~100 tokens).
type Meta struct {
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	License       string `yaml:"license,omitempty"`
	Compatibility string `yaml:"compatibility,omitempty"`
}

// Skill holds parsed skill metadata and the path to its directory.
type Skill struct {
	Meta Meta
	// Dir is the absolute path to the skill directory (parent of SKILL.md).
	Dir string
	// SkillPath is the absolute path to SKILL.md.
	SkillPath string
}

func frontmatterMatches(content string) []string {
	pattern := regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n(.*)$`)
	return pattern.FindStringSubmatch(content)
}

// ParseSkillFile reads skillPath (SKILL.md) and returns metadata and body.
// The body is the Markdown content after the frontmatter (instructions).
func ParseSkillFile(skillPath string) (meta Meta, body string, err error) {
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return Meta{}, "", err
	}
	content := string(data)
	matches := frontmatterMatches(content)
	if len(matches) != frontmatterMatchCount {
		return Meta{}, "", fmt.Errorf("SKILL.md must have YAML frontmatter between --- delimiters")
	}
	if err := yaml.Unmarshal([]byte(matches[1]), &meta); err != nil {
		return Meta{}, "", fmt.Errorf("invalid frontmatter: %w", err)
	}
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.TrimSpace(meta.Description)
	if meta.Name == "" || meta.Description == "" {
		return Meta{}, "", fmt.Errorf("frontmatter must have name and description")
	}
	body = strings.TrimSpace(matches[2])
	return meta, body, nil
}

// ParseSkillFileMetadataOnly reads only the frontmatter of SKILL.md (for startup listing).
func ParseSkillFileMetadataOnly(skillPath string) (Meta, error) {
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return Meta{}, err
	}
	content := string(data)
	matches := frontmatterMatches(content)
	if len(matches) != frontmatterMatchCount {
		return Meta{}, fmt.Errorf("SKILL.md must have YAML frontmatter between --- delimiters")
	}
	var meta Meta
	if err := yaml.Unmarshal([]byte(matches[1]), &meta); err != nil {
		return Meta{}, fmt.Errorf("invalid frontmatter: %w", err)
	}
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.TrimSpace(meta.Description)
	if meta.Name == "" || meta.Description == "" {
		return Meta{}, fmt.Errorf("frontmatter must have name and description")
	}
	return meta, nil
}

// ValidateName checks skill name per Agent Skills spec: lowercase, numbers, hyphens; 1-64 chars; no leading/trailing hyphen; no consecutive hyphens.
func ValidateName(name, dirName string) error {
	if len(name) == 0 || len(name) > 64 {
		return fmt.Errorf("name must be 1-64 characters")
	}
	if name != dirName {
		return fmt.Errorf("name %q must match directory name %q", name, dirName)
	}
	allowed := regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)
	if !allowed.MatchString(name) {
		return fmt.Errorf("name must be lowercase letters, numbers, and hyphens only; no leading/trailing or consecutive hyphens")
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("name must not contain consecutive hyphens")
	}
	return nil
}

// AllowedResourcePath returns true if path is under scripts/, references/, or assets/ and is local (no ..).
func AllowedResourcePath(rel string) bool {
	rel = filepath.Clean(rel)
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return false
	}
	base := filepath.ToSlash(rel)
	return strings.HasPrefix(base, ScriptsDir+"/") ||
		strings.HasPrefix(base, ReferencesDir+"/") ||
		strings.HasPrefix(base, AssetsDir+"/") ||
		base == ScriptsDir || base == ReferencesDir || base == AssetsDir
}

// ReadResource reads a file from the skill directory. rel must be under scripts/, references/, or assets/.
func ReadResource(skillDir, rel string) ([]byte, error) {
	if !AllowedResourcePath(rel) {
		return nil, fmt.Errorf("resource path must be under scripts/, references/, or assets/")
	}
	path := filepath.Join(skillDir, filepath.FromSlash(rel))
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dirAbs, err := filepath.Abs(skillDir)
	if err != nil {
		return nil, err
	}
	// Ensure no escape outside skill dir
	if !strings.HasPrefix(abs, dirAbs+string(filepath.Separator)) && abs != dirAbs {
		return nil, fmt.Errorf("resource path escapes skill directory")
	}
	return os.ReadFile(abs)
}
