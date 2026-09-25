package customersim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	shippedBinaryName = "yui"
	executableBits    = 0o111
)

// Host supplies the process facts the run depends on. The CLI transport is
// the host boundary that injects them.
type Host struct {
	WorkingDir func() (string, error)
	HomeDir    func() (string, error)
	Executable func() (string, error)
}

func (h Host) workingDir() (string, error) {
	if h.WorkingDir == nil {
		return "", errors.New("working directory lookup is not configured")
	}
	return h.WorkingDir()
}

// locateBinary returns the shipped yui binary and a cleanup function. An
// explicit path must be an executable regular file; otherwise the running
// executable, then checkout-relative bin directories are tried, and finally
// a temporary copy is built from the checkout.
func (h Host) locateBinary(ctx context.Context, explicit string) (string, func(), error) {
	noop := func() {}
	if strings.TrimSpace(explicit) != "" {
		path, err := validateBinary(explicit)
		return path, noop, err
	}
	for _, candidate := range uniqueNonEmpty(h.binaryCandidates()...) {
		if path, err := validateBinary(candidate); err == nil {
			return path, noop, nil
		}
	}
	root, err := h.repositoryRoot()
	if err != nil {
		return "", noop, fmt.Errorf("locate shipped yui binary: %w", err)
	}
	return buildBinary(ctx, root)
}

func (h Host) binaryCandidates() []string {
	candidates := []string{}
	if h.Executable != nil {
		if executable, err := h.Executable(); err == nil && filepath.Base(executable) == shippedBinaryName {
			candidates = append(candidates, executable)
		}
	}
	if cwd, err := h.workingDir(); err == nil {
		candidates = append(candidates, checkoutBinaries(cwd)...)
		if root, rootErr := repositoryRoot(cwd); rootErr == nil {
			candidates = append(candidates, checkoutBinaries(root)...)
		}
	}
	return candidates
}

func checkoutBinaries(root string) []string {
	return []string{filepath.Join(root, "agent-cli", "bin", shippedBinaryName), filepath.Join(root, "bin", shippedBinaryName)}
}

// buildBinary builds a temporary shipped binary from the checkout root; the
// cleanup function removes it.
func buildBinary(ctx context.Context, root string) (string, func(), error) {
	noop := func() {}
	temporary, err := os.CreateTemp("", "yui-customer-simulation-binary-")
	if err != nil {
		return "", noop, fmt.Errorf("create temporary shipped binary: %w", err)
	}
	path := temporary.Name()
	remove := func() {
		_ = os.Remove(path) //nolint:errcheck // Best-effort removal of a private temporary binary.
	}
	if err := temporary.Close(); err != nil {
		remove()
		return "", noop, fmt.Errorf("prepare temporary shipped binary: %w", err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", path, "./agent-cli/cmd/yui")
	build.Dir = root
	build.Stdout = io.Discard
	build.Stderr = io.Discard
	if err := build.Run(); err != nil {
		remove()
		return "", noop, fmt.Errorf("build shipped yui binary: %w", err)
	}
	return path, remove, nil
}

func validateBinary(raw string) (string, error) {
	path, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("binary %q is not a regular file", path)
	}
	if info.Mode().Perm()&executableBits == 0 {
		return "", fmt.Errorf("binary %q is not executable", path)
	}
	return path, nil
}

// ensureRunRootOutsideCheckout rejects an evidence root inside the checkout
// the host runs from. Outside a checkout any root is accepted.
func (h Host) ensureRunRootOutsideCheckout(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		return fmt.Errorf("resolve --run-root: %w", err)
	}
	repository, err := h.repositoryRoot()
	if err != nil {
		return nil //nolint:nilerr // Outside a checkout there is nothing to protect; any run root is accepted.
	}
	relative, err := filepath.Rel(repository, root)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("--run-root %q must be outside checkout %q", root, repository)
	}
	return nil
}

func (h Host) repositoryRoot() (string, error) {
	cwd, err := h.workingDir()
	if err != nil {
		return "", err
	}
	return repositoryRoot(cwd)
}

func repositoryRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("repository root not found")
		}
		current = parent
	}
}
