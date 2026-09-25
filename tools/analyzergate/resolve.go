package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type analyzerSpec struct {
	binaryName     string
	versionArgs    []string
	installPackage string
}

var analyzerSpecs = map[string]analyzerSpec{
	"golangci-lint": {
		binaryName:     "golangci-lint",
		versionArgs:    []string{"version"},
		installPackage: "github.com/golangci/golangci-lint/v2/cmd/golangci-lint",
	},
	"staticcheck": {
		binaryName:     "staticcheck",
		versionArgs:    []string{"-version"},
		installPackage: "honnef.co/go/tools/cmd/staticcheck",
	},
}

type config struct {
	tool             string
	expectedVersion  string
	pinnedVersion    string
	candidate        string
	goBinary         string
	installPackage   string
	binaryName       string
	versionArgs      []string
	toolDirectory    string
	workingDirectory string
}

type resolutionError struct {
	Tool      string
	Expected  string
	Observed  string
	Attempted string
	Recovery  string
	Cause     error
}

func (e *resolutionError) Error() string {
	observed := e.Observed
	if strings.TrimSpace(observed) == "" {
		observed = "no usable version was observed"
	}
	message := fmt.Sprintf(
		"%s resolver failed: expected version %s; observed %s; attempted remedy: %s; recovery: %s",
		e.Tool, e.Expected, observed, e.Attempted, e.Recovery,
	)
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *resolutionError) Unwrap() error { return e.Cause }

var reportedVersionPattern = regexp.MustCompile(`(^|[^0-9A-Za-z])(v?[0-9]+(\.[0-9]+){1,2}([-+][0-9A-Za-z.-]+)?)($|[^0-9A-Za-z])`)
var pinnedVersionPattern = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){1,2}([-+][0-9A-Za-z.-]+)?$`)

func normalizeVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !pinnedVersionPattern.MatchString(value) {
		return "", fmt.Errorf("version %q must be a numeric version such as v2.3.0 or 2025.1.1", value)
	}
	return strings.TrimPrefix(value, "v"), nil
}

func reportedVersion(output string) (string, error) {
	matches := reportedVersionPattern.FindStringSubmatch(output)
	if len(matches) == 0 {
		return "", fmt.Errorf("version probe output did not contain a numeric version")
	}
	return normalizeVersion(matches[2])
}

// diagnosticLog forwards resolver progress to the caller's writer and keeps
// the first write failure so resolution can report it instead of dropping it.
type diagnosticLog struct {
	writer io.Writer
	err    error
}

func (d *diagnosticLog) printf(format string, args ...any) {
	if d.err != nil {
		return
	}
	if _, err := fmt.Fprintf(d.writer, format, args...); err != nil {
		d.err = fmt.Errorf("write analyzer diagnostics: %w", err)
	}
}

func resolve(ctx context.Context, cfg config, diagnostics io.Writer) (string, error) {
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	log := &diagnosticLog{writer: diagnostics}
	resolved, err := resolveWithLog(ctx, cfg, log)
	if err != nil {
		return "", err
	}
	if log.err != nil {
		return "", log.err
	}
	return resolved, nil
}

func resolveWithLog(ctx context.Context, cfg config, log *diagnosticLog) (string, error) {
	cachePath := installedPath(cfg)
	observed := make([]string, 0, 3)
	if resolved, ok := resolveCandidate(ctx, cfg, log, &observed); ok {
		return resolved, nil
	}
	if resolved, ok := resolveCached(ctx, cfg, cachePath, log, &observed); ok {
		return resolved, nil
	}
	return installPinned(ctx, cfg, cachePath, log, observed)
}

func resolveCandidate(ctx context.Context, cfg config, log *diagnosticLog, observed *[]string) (string, bool) {
	candidatePath, err := candidateExecutable(cfg)
	if err != nil {
		*observed = append(*observed, fmt.Sprintf("candidate %q was unavailable or not executable", cfg.candidate))
		log.printf("analyzergate: %s candidate %q is unavailable or not executable\n", cfg.tool, cfg.candidate)
		return "", false
	}
	version, probeErr := probeVersion(ctx, candidatePath, cfg)
	if probeErr != nil {
		*observed = append(*observed, fmt.Sprintf("candidate %q version probe failed (%v)", candidatePath, probeErr))
		log.printf("analyzergate: %s candidate %q could not be version-checked: %v\n", cfg.tool, candidatePath, probeErr)
		return "", false
	}
	if version == cfg.expectedVersion {
		return candidatePath, true
	}
	*observed = append(*observed, fmt.Sprintf("candidate %q reported %s", candidatePath, version))
	log.printf("analyzergate: %s candidate %q reports %s, expected %s\n", cfg.tool, candidatePath, version, cfg.expectedVersion)
	return "", false
}

func resolveCached(ctx context.Context, cfg config, cachePath string, log *diagnosticLog, observed *[]string) (string, bool) {
	if cachePath == "" || cachePath == cfg.candidate || !executableFile(cachePath) {
		return "", false
	}
	version, probeErr := probeVersion(ctx, cachePath, cfg)
	if probeErr != nil {
		*observed = append(*observed, fmt.Sprintf("cached executable %q version probe failed (%v)", cachePath, probeErr))
		log.printf("analyzergate: cached %s could not be version-checked: %v\n", cfg.tool, probeErr)
		return "", false
	}
	if version == cfg.expectedVersion {
		log.printf("analyzergate: using cached %s %s at %s\n", cfg.tool, cfg.expectedVersion, cachePath)
		return cachePath, true
	}
	*observed = append(*observed, fmt.Sprintf("cached executable %q reported %s", cachePath, version))
	log.printf("analyzergate: cached %s reports %s, expected %s\n", cfg.tool, version, cfg.expectedVersion)
	return "", false
}

func installPinned(ctx context.Context, cfg config, cachePath string, log *diagnosticLog, observed []string) (string, error) {
	installPackage := cfg.installPackage + "@" + cfg.pinnedVersion
	installDirectory := filepath.Dir(cachePath)
	attempted := fmt.Sprintf("install %s into %s", installPackage, installDirectory)
	if err := os.MkdirAll(installDirectory, 0o755); err != nil {
		return "", &resolutionError{
			Tool:      cfg.tool,
			Expected:  cfg.expectedVersion,
			Observed:  strings.Join(observed, "; "),
			Attempted: attempted,
			Recovery:  fmt.Sprintf("make %s writable or set ANALYZER_TOOL_DIR to a writable directory", cfg.tool),
			Cause:     fmt.Errorf("create deterministic tool directory %q: %w", installDirectory, err),
		}
	}

	log.printf("analyzergate: installing pinned %s %s into %s\n", cfg.tool, cfg.expectedVersion, installDirectory)
	installOutput, err := install(ctx, cfg, installPackage, installDirectory)
	if err != nil {
		if detail := strings.TrimSpace(installOutput); detail != "" {
			observed = append(observed, "go install output: "+detail)
		}
		return "", &resolutionError{
			Tool:      cfg.tool,
			Expected:  cfg.expectedVersion,
			Observed:  strings.Join(observed, "; "),
			Attempted: attempted,
			Recovery:  fmt.Sprintf("verify network/module access and rerun, or set ANALYZER_TOOL_DIR to a writable cache directory; the pinned command is %s", installPackage),
			Cause:     fmt.Errorf("go install failed: %w", err),
		}
	}
	return verifyInstalled(ctx, cfg, cachePath, attempted, observed)
}

func verifyInstalled(ctx context.Context, cfg config, cachePath, attempted string, observed []string) (string, error) {
	if !executableFile(cachePath) {
		return "", &resolutionError{
			Tool:      cfg.tool,
			Expected:  cfg.expectedVersion,
			Observed:  strings.Join(append(observed, fmt.Sprintf("go install did not create executable %q", cachePath)), "; "),
			Attempted: attempted,
			Recovery:  fmt.Sprintf("check that the pinned package builds a %s executable and that GOBIN is writable", cfg.binaryName),
			Cause:     errors.New("installed analyzer executable is missing or not executable"),
		}
	}

	installedVersion, err := probeVersion(ctx, cachePath, cfg)
	if err != nil {
		return "", &resolutionError{
			Tool:      cfg.tool,
			Expected:  cfg.expectedVersion,
			Observed:  strings.Join(append(observed, fmt.Sprintf("installed executable version probe failed (%v)", err)), "; "),
			Attempted: attempted + " and verify the installed executable",
			Recovery:  fmt.Sprintf("remove the corrupted cache entry %q and rerun the pinned install", cachePath),
			Cause:     fmt.Errorf("installed executable version probe failed: %w", err),
		}
	}
	if installedVersion != cfg.expectedVersion {
		return "", &resolutionError{
			Tool:      cfg.tool,
			Expected:  cfg.expectedVersion,
			Observed:  strings.Join(append(observed, fmt.Sprintf("installed executable %q reported %s", cachePath, installedVersion)), "; "),
			Attempted: attempted + " and verify the installed executable",
			Recovery:  fmt.Sprintf("check the package version suffix and remove the stale cache entry %q before retrying", cachePath),
			Cause:     errors.New("installed analyzer reported a version different from the pinned version"),
		}
	}
	return cachePath, nil
}

func candidateExecutable(cfg config) (string, error) {
	candidate := cfg.candidate
	if strings.ContainsRune(candidate, os.PathSeparator) || (runtime.GOOS == windowsGOOS && strings.ContainsAny(candidate, `/\\`)) {
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(cfg.workingDirectory, candidate)
		}
		candidate, err := absolutePath(candidate)
		if err != nil {
			return "", err
		}
		if !executableFile(candidate) {
			return "", fmt.Errorf("candidate %q is not executable", candidate)
		}
		return candidate, nil
	}
	path, err := exec.LookPath(candidate)
	if err != nil {
		return "", err
	}
	return absolutePath(path)
}

func installedPath(cfg config) string {
	if cfg.toolDirectory == "" || cfg.binaryName == "" || cfg.expectedVersion == "" {
		return ""
	}
	return filepath.Join(
		cfg.toolDirectory,
		cfg.binaryName,
		cfg.expectedVersion,
		runtime.GOOS+"-"+runtime.GOARCH,
		cfg.binaryName+runtimeExecutableSuffix(),
	)
}

// windowsGOOS is the GOOS value whose executables and paths need special handling.
const windowsGOOS = "windows"

func runtimeExecutableSuffix() string {
	if runtime.GOOS == windowsGOOS {
		return ".exe"
	}
	return ""
}

func executableFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == windowsGOOS {
		return true
	}
	return info.Mode()&0o111 != 0
}

func probeVersion(ctx context.Context, path string, cfg config) (string, error) {
	command := exec.CommandContext(ctx, path, cfg.versionArgs...)
	command.Dir = cfg.workingDirectory
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("command exited with %v: %s", err, compactOutput(output.String()))
	}
	version, err := reportedVersion(output.String())
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, compactOutput(output.String()))
	}
	return version, nil
}

func install(ctx context.Context, cfg config, packageVersion, installDirectory string) (string, error) {
	command := exec.CommandContext(ctx, cfg.goBinary, "install", packageVersion)
	command.Dir = cfg.workingDirectory
	command.Env = setEnvironment(os.Environ(), "GOBIN", installDirectory)
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output)
	command.Stderr = io.MultiWriter(&output)
	err := command.Run()
	return output.String(), err
}

func setEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, prefix+value)
}

func absolutePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func compactOutput(output string) string {
	output = strings.Join(strings.Fields(output), " ")
	if len(output) > 400 {
		return output[:400] + "..."
	}
	return output
}
