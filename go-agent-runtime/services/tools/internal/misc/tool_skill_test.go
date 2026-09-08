package misc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillUsesBoundSkillRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "bound-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: bound-skill\ndescription: Bound test skill\n---\nBound instructions.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewLoadSkillToolFromRoots(root)
	messages, err := tool.Execute(context.Background(), map[string]any{"skill_name": "bound-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].TextContent(), "Bound instructions") {
		t.Fatalf("bound skill result = %#v", messages)
	}
}

func TestLoadSkillWithoutRootsFailsClosed(t *testing.T) {
	tool := NewLoadSkillTool()
	messages, err := tool.Execute(context.Background(), map[string]any{"skill_name": "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].TextContent(), "skill \"missing\" not found") {
		t.Fatalf("unbound skill result = %#v", messages)
	}
}
