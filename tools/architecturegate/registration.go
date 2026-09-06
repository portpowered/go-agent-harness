package main

import (
	"os"
	"path/filepath"
	"strings"
)

// classifyGenerated resolves a registered generated file relative to the
// owning module. Registration is module-scoped so a wildcard in one module
// cannot silently exempt a generated file in another.
func classifyGenerated(file string, module *Module, policy Policy) string {
	for _, rule := range policy.GeneratedFiles {
		if rule.Module != "" && !matchesAny([]string{rule.Module}, module.Path, module.Dir, filepath.Base(module.Dir)) {
			continue
		}
		rel, err := filepath.Rel(module.Dir, file)
		if err == nil && (globMatch(rule.Pattern, filepath.ToSlash(rel)) || globMatch(rule.Pattern, filepath.ToSlash(file))) {
			return rule.Generator
		}
	}
	return ""
}

func registeredGenerated(file string, module *Module, policy Policy) bool {
	generator := classifyGenerated(file, module, policy)
	if !hasGeneratedHeader(file) || generator == "" {
		return false
	}
	for _, rule := range policy.GeneratedFiles {
		if rule.Generator != generator || (rule.Module != "" && !matchesAny([]string{rule.Module}, module.Path, module.Dir, filepath.Base(module.Dir))) {
			continue
		}
		rel, err := filepath.Rel(module.Dir, file)
		if err != nil || (!globMatch(rule.Pattern, filepath.ToSlash(rel)) && !globMatch(rule.Pattern, filepath.ToSlash(file))) {
			continue
		}
		if rule.Header == "" || generatedHeaderPresent(file, rule.Header) {
			return true
		}
	}
	return false
}

func hasGeneratedHeader(file string) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	for _, line := range firstLines(data, 10) {
		if strings.Contains(line, "Code generated") && strings.Contains(line, "DO NOT EDIT") {
			return true
		}
	}
	return false
}

func generatedHeaderPresent(file, expected string) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	for _, line := range firstLines(data, 10) {
		if strings.TrimSpace(line) == expected {
			return true
		}
	}
	return false
}

func firstLines(data []byte, limit int) []string {
	lines := strings.Split(string(data), "\n")
	if len(lines) > limit {
		return lines[:limit]
	}
	return lines
}
