package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
	sessioninstructionswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions/wire"
)

const (
	workspacePath       = "/consumer/workspace"
	secondWorkspacePath = "/consumer/second-workspace"
	agentsText          = "consumer AGENTS instructions"
	secondAgentsText    = "independent alternate AGENTS instructions"
	summaryText         = "consumer skill summary"
	secondSummaryText   = "second consumer skill summary"
	filePrompt          = "consumer file prompt"
)

type memoryLoader struct {
	files   map[string][]byte
	stat    map[string]error
	summary string
}

func (l *memoryLoader) Stat(path string) error {
	if err, ok := l.stat[path]; ok {
		return err
	}
	if _, ok := l.files[path]; ok {
		return nil
	}
	return fs.ErrNotExist
}

func (l *memoryLoader) ReadFile(path string) ([]byte, error) {
	data, ok := l.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (l *memoryLoader) SkillsSummary() (string, error) { return l.summary, nil }

func main() {
	mode := flag.String("mode", "positive", "verification mode")
	flag.Parse()
	report := map[string]any{"mode": *mode, "status": "accepted", "cli_imports": false, "private_imports": false, "credentials": false, "hidden_global_io": false}
	if err := run(*mode, report); err != nil {
		report["status"] = "rejected"
		report["error"] = err.Error()
		_ = json.NewEncoder(os.Stdout).Encode(report)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}

func run(mode string, report map[string]any) error {
	first := sessioninstructionswire.NewInstructionService()
	second := sessioninstructionswire.NewInstructionService()
	if first == nil || second == nil {
		return errors.New("instruction service construction returned nil")
	}
	report["service_instances"] = 2
	switch mode {
	case "positive":
		return runPositive(first, second, report)
	case "invalid":
		return runInvalid(first, report)
	case "print":
		return runPrint(first, report)
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
}

func runPositive(first, second sessioninstructions.InstructionService, report map[string]any) error {
	loader := &memoryLoader{
		files:   map[string][]byte{workspacePath + "/AGENTS.md": []byte(agentsText), workspacePath + "/prompt.md": []byte(filePrompt)},
		summary: summaryText,
	}
	resolved, err := first.Resolve(context.Background(), sessioninstructions.InstructionRequest{
		WorkspaceDir: workspacePath,
		Loader:       loader,
	})
	if err != nil {
		return fmt.Errorf("resolve AGENTS.md: %w", err)
	}
	want := agentsText + "\n\n---\n\n" + summaryText
	if resolved.Instructions != want {
		return fmt.Errorf("resolved instructions = %q, want %q", resolved.Instructions, want)
	}

	fileResolved, err := first.Resolve(context.Background(), sessioninstructions.InstructionRequest{
		Prompt:       workspacePath + "/prompt.md",
		WorkspaceDir: workspacePath,
		Loader:       loader,
	})
	if err != nil || fileResolved.Instructions != filePrompt+"\n\n---\n\n"+summaryText {
		return fmt.Errorf("explicit file resolution = %q, err=%v", fileResolved.Instructions, err)
	}

	literal, err := first.Resolve(context.Background(), sessioninstructions.InstructionRequest{Prompt: "literal consumer prompt"})
	if err != nil || literal.Instructions != "literal consumer prompt" {
		return fmt.Errorf("literal resolution = %q, err=%v", literal.Instructions, err)
	}
	none, err := first.Resolve(context.Background(), sessioninstructions.InstructionRequest{Prompt: "none", WorkspaceDir: workspacePath})
	if err != nil || none.Instructions != "" {
		return fmt.Errorf("none resolution = %q, err=%v", none.Instructions, err)
	}

	scoped, err := first.Resolve(context.Background(), sessioninstructions.InstructionRequest{
		Prompt:                     "literal consumer prompt",
		FilesystemScopeSet:         true,
		FilesystemScopeDescription: "root=/consumer/workspace",
	})
	if err != nil || !strings.HasSuffix(scoped.Instructions, "Filesystem scope: root=/consumer/workspace. Relative filesystem-tool paths resolve from this workdir.") {
		return fmt.Errorf("scope resolution = %q, err=%v", scoped.Instructions, err)
	}

	secondLoader := &memoryLoader{
		files:   map[string][]byte{secondWorkspacePath + "/AGENTS.md": []byte(secondAgentsText)},
		summary: secondSummaryText,
	}
	secondResolved, err := second.Resolve(context.Background(), sessioninstructions.InstructionRequest{
		WorkspaceDir: secondWorkspacePath,
		Loader:       secondLoader,
	})
	if err != nil {
		return fmt.Errorf("resolve second independent service: %w", err)
	}
	secondWant := secondAgentsText + "\n\n---\n\n" + secondSummaryText
	if secondResolved.Instructions != secondWant || strings.Contains(secondResolved.Instructions, agentsText) {
		return fmt.Errorf("second service leaked first service state: %q", secondResolved.Instructions)
	}

	composition := first.Compose(sessioninstructions.InstructionComposition{
		Instructions:           resolved.Instructions,
		ToolDefinitions:        []messages.ToolDefinition{{Name: "show_page"}, {Name: "webmcp_list_tabs"}},
		BrowserCapabilityState: sessioninstructions.BrowserCapabilityConnectedUnselected,
		BrowserToolsEnabled:    true,
	})
	for _, heading := range []string{
		"WebMCP browser selection:",
		"Tool-grounding requirements:",
		"WebMCP tab selection calibration:",
		"WebMCP ambiguity recovery:",
		"Sight routing requirements:",
	} {
		if strings.Count(composition, heading) != 1 {
			return fmt.Errorf("composition heading %q count=%d", heading, strings.Count(composition, heading))
		}
	}
	report["resolved"] = resolved.Instructions
	report["composition"] = composition
	return nil
}

func runInvalid(service sessioninstructions.InstructionService, report map[string]any) error {
	_, err := service.Resolve(context.Background(), sessioninstructions.InstructionRequest{WorkspaceDir: workspacePath})
	if !errors.Is(err, sessioninstructions.ErrLoaderRequired) {
		return fmt.Errorf("missing loader error = %v, want ErrLoaderRequired", err)
	}
	var resolutionErr *sessioninstructions.ResolutionError
	if !errors.As(err, &resolutionErr) || resolutionErr.Phase != sessioninstructions.PhaseValidation {
		return fmt.Errorf("missing loader attribution = %T/%v", err, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.Resolve(ctx, sessioninstructions.InstructionRequest{Prompt: "cancelled"})
	if !errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled resolution error = %v", err)
	}
	report["invalid_loader_phase"] = resolutionErr.Phase
	report["cancellation"] = "context.Canceled"
	return nil
}

func runPrint(service sessioninstructions.InstructionService, report map[string]any) error {
	composition := service.Compose(sessioninstructions.InstructionComposition{
		Instructions:    "customer instructions",
		ToolDefinitions: []messages.ToolDefinition{{Name: "read_file"}},
	})
	if !strings.HasPrefix(composition, "customer instructions\n\n") || !strings.Contains(composition, "Tool-grounding requirements:") {
		return fmt.Errorf("composition oracle missing customer prefix or tool policy")
	}
	report["composition"] = composition
	return nil
}
