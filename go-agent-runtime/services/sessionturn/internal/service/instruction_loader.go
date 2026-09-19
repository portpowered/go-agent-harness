package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type instructionLoader struct {
	ctx         context.Context
	workspace   string
	configDir   string
	toolService tools.Service
}

func (s *Service) withInstructionLoader(ctx context.Context, request sessionturn.InstructionRequest) (sessionturn.InstructionRequest, error) {
	if request.Request.Loader != nil {
		return request, nil
	}
	if workspace := request.Request.WorkspaceDir; workspace != "" {
		info, err := os.Stat(workspace)
		if err != nil {
			return request, fmt.Errorf("invalid filesystem root: resolve filesystem scope: invalid workdir %q: %w", workspace, err)
		}
		if !info.IsDir() {
			return request, fmt.Errorf("resolve filesystem scope: workdir %q is not a directory", workspace)
		}
	}
	if request.Request.Prompt == "" && request.Request.WorkspaceDir == "" {
		return request, nil
	}
	var service tools.Service
	if s != nil {
		service = s.deps.ToolService
	}
	request.Request.Loader = instructionLoader{
		ctx: ctx, workspace: request.Request.WorkspaceDir,
		configDir: request.Request.ConfigDir, toolService: service,
	}
	return request, nil
}

func (l instructionLoader) Stat(path string) error { _, err := os.Stat(path); return err }

func (l instructionLoader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (l instructionLoader) SkillsSummary() (string, error) {
	if l.toolService == nil || (l.workspace == "" && l.configDir == "") {
		return "", nil
	}
	roots := make([]tools.SkillRoot, 0, 2)
	if l.workspace != "" {
		roots = append(roots, tools.SkillRoot{Directory: filepath.Join(l.workspace, "skills")})
	}
	if l.configDir != "" {
		roots = append(roots, tools.SkillRoot{Directory: filepath.Join(l.configDir, "skills")})
	}
	return l.toolService.BuildSkillsSummary(l.ctx, tools.SkillSummaryRequest{SkillRoots: roots})
}

var _ session.InstructionLoader = instructionLoader{}
