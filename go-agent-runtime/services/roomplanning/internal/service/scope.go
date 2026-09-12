package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
)

func resolveScope(options roomplanning.Options) (roomplanning.FilesystemScope, error) {
	if options.Filesystem != nil {
		if strings.TrimSpace(options.Filesystem.PrimaryRoot) == "" {
			return roomplanning.FilesystemScope{}, roomplanning.ErrFilesystemScope
		}
		return cloneScope(*options.Filesystem), nil
	}
	resolver := options.ResolveFilesystem
	if resolver == nil {
		resolver = defaultFilesystemScope
	}
	scope, err := resolver(options.WorkDir, options.AllowPaths)
	if err != nil {
		return roomplanning.FilesystemScope{}, fmt.Errorf("resolve filesystem scope: %w", err)
	}
	if strings.TrimSpace(scope.PrimaryRoot) == "" {
		return roomplanning.FilesystemScope{}, roomplanning.ErrFilesystemScope
	}
	return cloneScope(scope), nil
}

func defaultFilesystemScope(workDir string, allow []string) (roomplanning.FilesystemScope, error) {
	primary, err := resolvePrimaryRoot(workDir)
	if err != nil {
		return roomplanning.FilesystemScope{}, err
	}
	return appendAllowedRoots(primary, allow)
}

func resolvePrimaryRoot(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	primary, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	primary = filepath.Clean(primary)
	info, err := os.Stat(primary)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("root is not a directory")
		}
		return "", err
	}
	return primary, nil
}

func appendAllowedRoots(primary string, allow []string) (roomplanning.FilesystemScope, error) {
	result := roomplanning.FilesystemScope{PrimaryRoot: primary}
	seen := map[string]struct{}{primary: {}}
	for _, candidate := range allow {
		resolved, err := resolveAllowedRoot(primary, candidate)
		if err != nil {
			return roomplanning.FilesystemScope{}, err
		}
		if _, exists := seen[resolved]; exists {
			continue
		}
		seen[resolved] = struct{}{}
		result.AdditionalRoots = append(result.AdditionalRoots, resolved)
	}
	return result, nil
}

func resolveAllowedRoot(primary, candidate string) (string, error) {
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(primary, candidate)
	}
	resolved, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if _, err := os.Stat(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}
