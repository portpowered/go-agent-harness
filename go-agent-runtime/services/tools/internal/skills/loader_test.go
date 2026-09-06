package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoader_List_and_BuildSummary(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillMD := filepath.Join(skillDir, SkillFileName)
	content := `---
name: test-skill
description: A test skill for unit tests.
---
# Test skill
Use this when testing.
`
	if err := os.WriteFile(skillMD, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	loader := NewLoader(dir, "")
	list, err := loader.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(list))
	}
	if list[0].Meta.Name != "test-skill" || list[0].Meta.Description != "A test skill for unit tests." {
		t.Errorf("unexpected meta: %+v", list[0].Meta)
	}

	summary, err := loader.BuildSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "test-skill") || !strings.Contains(summary, "A test skill for unit tests.") {
		t.Errorf("summary missing skill info: %s", summary)
	}
}

func TestLoader_LoadSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "pdf-help")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `---
name: pdf-help
description: Help with PDFs.
---
# PDF help
Step 1: Open the file.
Step 2: Process it.
`
	if err := os.WriteFile(filepath.Join(skillDir, SkillFileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	loader := NewLoader(dir, "")
	body, err := loader.LoadSkill("pdf-help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Step 1") || !strings.Contains(body, "Step 2") {
		t.Errorf("expected instructions in body: %s", body)
	}
	if strings.Contains(body, "name: pdf-help") {
		t.Error("body should not contain frontmatter")
	}
}

func TestLoader_LoadSkillWithPath_resource(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "ref-skill")
	refDir := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(refDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, SkillFileName), []byte("---\nname: ref-skill\ndescription: Ref skill.\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "REFERENCE.md"), []byte("# Reference\nDetails here."), 0644); err != nil {
		t.Fatal(err)
	}

	loader := NewLoader(dir, "")
	content, err := loader.LoadSkillWithPath("ref-skill", "references/REFERENCE.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Details here.") {
		t.Errorf("expected reference content: %s", content)
	}
}

func TestLoaderFromRootsUsesExplicitOrder(t *testing.T) {
	dir := t.TempDir()
	workspaceRoot := filepath.Join(dir, "workspace-skills")
	configRoot := filepath.Join(dir, "config-skills")
	for _, root := range []string{workspaceRoot, configRoot} {
		if err := os.MkdirAll(filepath.Join(root, "shared"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill := func(root, description string) {
		t.Helper()
		content := "---\nname: shared\ndescription: " + description + "\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(root, "shared", SkillFileName), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(workspaceRoot, "workspace wins")
	writeSkill(configRoot, "config loses")

	loader := NewLoaderFromRoots(workspaceRoot, configRoot)
	list, err := loader.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Meta.Description != "workspace wins" {
		t.Fatalf("ordered roots = %#v, want workspace metadata", list)
	}
}
