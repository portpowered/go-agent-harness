package agent

// This file owns request preparation after host admission: resolving the
// invocation's explicit storage and tool ports, and selecting initial history.
// Filesystem configuration, prompt files, environment variables, and skill
// root discovery belong to the host adapter.

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// getSessionStorage returns the invocation-scoped persistence port supplied by
// the host resolver. It deliberately has no config-directory fallback.
func (e *Executor) getSessionStorage() (Storage, error) {
	if e == nil || !e.resolved {
		return nil, fmt.Errorf("session execution requires host-resolved dependencies")
	}
	if e.resolvedStorage == nil {
		return nil, fmt.Errorf("session storage is required")
	}
	return e.resolvedStorage, nil
}

// resolveToolCapability asks the reusable tools owner for the request-scoped
// capability. The request carries only values and explicit ports; it never
// asks the tools service to discover a config directory or default registry.
func (e *Executor) resolveToolCapability(ctx context.Context, workDir string, allowPaths []string, inf messages.Inferencer) (runtimeTools.Capability, error) {
	if e == nil {
		return runtimeTools.Capability{}, fmt.Errorf("session executor is required")
	}
	request := runtimeTools.Request{
		WorkDir:     workDir,
		AllowPaths:  append([]string(nil), allowPaths...),
		SkillRoots:  append([]runtimeTools.SkillRoot(nil), e.resolvedSkillRoots...),
		Inferencer:  inf,
		Executor:    e.executor,
		Definitions: append([]messages.ToolDefinition(nil), e.toolDefs...),
		// An empty injected surface is meaningful for embedders. Host composition
		// can pass an explicit executor or definitions when tools are desired.
		UseDefaultTool: false,
	}
	if e.toolService == nil {
		return runtimeTools.Capability{
			Executor:       request.Executor,
			Definitions:    request.Definitions,
			WorkspaceDir:   workDir,
			AdditionalDirs: append([]string(nil), allowPaths...),
		}, nil
	}
	return e.toolService.Resolve(ctx, request)
}

// getInitialHistory resolves the session ID and loads initial history using
// the explicit storage port. The store remains private to the execution
// implementation and can be backed by memory, a database, or host files.
func (e *Executor) getInitialHistory(cfg *Config, sessionStorage Storage) ([]messages.Message, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("session execution config is required")
	}
	if sessionStorage == nil {
		return nil, "", fmt.Errorf("session storage is required")
	}
	if cfg.SessionID != "" {
		history, err := sessionStorage.Load(cfg.SessionID)
		if err != nil {
			return nil, "", fmt.Errorf("load session %s: %w", cfg.SessionID, err)
		}
		return history, cfg.SessionID, nil
	}
	if cfg.ContinueLastSession {
		return loadLatestHistory(sessionStorage)
	}
	id, err := newSessionID(sessionStorage)
	if err != nil {
		return nil, "", err
	}
	return append([]messages.Message(nil), cfg.InitialHistory...), id, nil
}

func loadLatestHistory(storage Storage) ([]messages.Message, string, error) {
	id, err := storage.Latest()
	if err != nil {
		return nil, "", fmt.Errorf("find latest session: %w", err)
	}
	if id == "" {
		return nil, "", fmt.Errorf("no previous session to continue (use --session-id or run an ask first)")
	}
	history, err := storage.Load(id)
	if err != nil {
		return nil, "", fmt.Errorf("load latest session: %w", err)
	}
	return history, id, nil
}

// NewChatSessionID returns a new ID from the explicitly injected store. The
// request parameter is retained for the service's small internal adapter but
// is intentionally ignored; host configuration is already resolved.
func (e *Executor) NewChatSessionID(_ *Config) (string, error) {
	storage, err := e.getSessionStorage()
	if err != nil {
		return "", err
	}
	return newSessionID(storage)
}
