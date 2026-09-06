package cli

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestProductionAudioOwnership prevents CLI packages from growing private
// PCM/WAV implementations as audio ownership moves to go-audio.
func TestProductionAudioOwnership(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	internalRoot := filepath.Dir(filename)
	fset := token.NewFileSet()
	var violations []string
	err := filepath.Walk(internalRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if (importPath == "encoding/binary" && !strings.Contains(path, string(filepath.Separator)+"webmcp"+string(filepath.Separator)+"testkit"+string(filepath.Separator))) ||
				strings.Contains(importPath, "/agent-cli/internal/audio") ||
				strings.Contains(importPath, "/go-llm-gateway/pkg/wavio") ||
				strings.Contains(importPath, "/go-agent-loop/pkg/platform/clock") ||
				strings.Contains(importPath, "/services/internal/") {
				violations = append(violations, path+" imports "+importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan CLI production imports: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("CLI production audio ownership violations:\n%s\nuse go-audio/pkg/codec and public device/audio boundaries", strings.Join(violations, "\n"))
	}
}

// TestMigratedDeviceProbeTransportOwnership keeps the real device probe
// implementation behind services/internal/devices. The command owns flags,
// scenario selection, and rendering; it must not regain gateway or registry
// dependencies as the remaining session transport is migrated.
func TestMigratedDeviceProbeTransportOwnership(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	for _, name := range []string{"probe.go", "device_probe.go"} {
		path := filepath.Join(filepath.Dir(filename), name)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse migrated device probe transport %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if strings.Contains(importPath, "/go-device-gateway/") || strings.Contains(importPath, "/services/internal/") {
				t.Fatalf("migrated device probe transport %s imports implementation dependency %q", name, importPath)
			}
		}
	}
}

// TestDeviceCommandCompositionUsesInjectedRegistry keeps host device
// discovery in the application graph. Room and router transports may carry
// the public registry seam, but they must not construct or mutate a process
// global/default registry behind their constructors.
func TestDeviceCommandCompositionUsesInjectedRegistry(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	for _, name := range []string{"core_router.go", "room.go"} {
		path := filepath.Join(filepath.Dir(filename), name)
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read device transport %s: %v", name, err)
		}
		for _, forbidden := range []string{
			"NewHostDeviceRegistry",
			"newDefaultDeviceRegistry",
			"NewRoomRunCommandWithDeviceRegistry",
			"SetDeviceRegistry",
		} {
			if strings.Contains(string(contents), forbidden) {
				t.Fatalf("device transport %s contains legacy registry construction/compatibility path %q", name, forbidden)
			}
		}
	}
}

// TestApplicationAudioPayloadOwnership scans the complete application, including
// service internals. Moving DSP below the transport must not evade the gate.
func TestApplicationAudioPayloadOwnership(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	internalRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	err := filepath.Walk(internalRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported := strings.Trim(spec.Path.Value, "\"")
			if strings.Contains(imported, "/services/servicetest") {
				t.Errorf("production file %s imports acceptance-test runtime access %s", path, imported)
			}
			// Browser testkit binary identifiers are unrelated to audio payloads.
			binaryIdentifier := strings.Contains(path, filepath.Join("webmcp", "testkit")+string(filepath.Separator))
			if (imported == "encoding/binary" && !binaryIdentifier) || strings.Contains(imported, "/agent-cli/internal/audio") || strings.Contains(imported, "/go-llm-gateway/pkg/wavio") || strings.Contains(imported, "/go-agent-loop/pkg/platform/clock") {
				t.Errorf("application audio ownership violation: %s imports %s", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
