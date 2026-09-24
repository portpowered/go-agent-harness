// Package transport holds stateless projections from CLI host packages onto
// runtime service ports. Adapters retain no session state and make no
// session decisions; the runtime services own both.
package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	screenRecheckOperation = "show permission re-check"
	pageSightUnavailable   = "Browser-page sight is unavailable."
	pageSightCause         = "browser-page sight unavailable"
	pageSightFallback      = `{"version":2,"status":"error","source":"browser_page","error_code":"page_sight_unavailable","error":"Browser-page sight is unavailable."}`
)

// SessionTurnToolPresentation projects CLI display and page-sight failures
// onto the session-turn executor.
func SessionTurnToolPresentation() sessionturn.ToolPresentation {
	return sessionturn.ToolPresentation{
		DisplayTool:             cliTools.IsPhysicalDisplayToolName,
		DisplayFailure:          cliTools.ScreenToolSessionErrorResult,
		DisplayErrorCode:        cliTools.ScreenToolErrorCode,
		DisplayPermissionDenied: screenPermissionDenied,
		FailedContent:           filesystemRefusal,
		PageSightFailure:        pageSightFailure,
		DisplaySource:           sight.SourceScreen,
		PageSightSource:         sight.SourceBrowserPage,
	}
}

func screenPermissionDenied(permission runtimeTools.DisplayPermission) error {
	return &cliTools.ScreenCaptureError{State: cliTools.ScreenCaptureDenied, Operation: screenRecheckOperation, Reason: permission.Reason}
}

func filesystemRefusal(content string) bool {
	refusal, refused := cliTools.FilesystemRefusalFromContent(content)
	return refused && refusal.Validate() == nil
}

func pageSightFailure() string {
	result := sight.NewError(sight.SourceBrowserPage, errors.New(pageSightCause))
	result.Error = pageSightUnavailable
	result.ErrorCode = sessionturn.PageSightUnavailableErrorCode
	encoded, err := sight.Encode(result)
	if err != nil {
		return pageSightFallback
	}
	return string(encoded)
}

// SessionTurnBrowserWatch projects broker observations onto the publication
// watch. The projection preserves the source buffer so a queued burst stays
// visible to the publisher's settle drain; it closes only when the source
// closes.
func SessionTurnBrowserWatch(watch func(context.Context) <-chan webmcp.BrokerEvent) func(context.Context) <-chan sessionturn.BrowserEvent {
	if watch == nil {
		return nil
	}
	return func(ctx context.Context) <-chan sessionturn.BrowserEvent {
		source := watch(ctx)
		if source == nil {
			return nil
		}
		projected := make(chan sessionturn.BrowserEvent, cap(source))
		go forwardBrowserEvents(ctx, source, projected)
		return projected
	}
}

func forwardBrowserEvents(ctx context.Context, source <-chan webmcp.BrokerEvent, projected chan<- sessionturn.BrowserEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-source:
			if !ok {
				close(projected)
				return
			}
			select {
			case projected <- browserEvent(event):
			case <-ctx.Done():
				return
			}
		}
	}
}

func browserEvent(event webmcp.BrokerEvent) sessionturn.BrowserEvent {
	return sessionturn.BrowserEvent{
		Type: sessionturn.BrowserEventType(event.Type), BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, Sequence: event.Sequence,
	}
}

// SessionTurnInteractiveSettings converts the loaded interactive config.
func SessionTurnInteractiveSettings(settings config.InteractiveToolConfig) *runtimeTools.InteractiveToolPolicySettings {
	return &runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout:          settings.FastReadTimeout,
		LongRunningTimeout:       settings.LongRunningTimeout,
		AcknowledgementThreshold: settings.AcknowledgementThreshold,
	}
}

// SessionTurnInstructionLoader supplies workspace file reads and the skills
// summary for one resolved workspace.
func SessionTurnInstructionLoader(configDir string) func(string) runtimeSession.InstructionLoader {
	return func(workspaceDir string) runtimeSession.InstructionLoader {
		return instructionLoader{workspaceDir: workspaceDir, configDir: configDir}
	}
}

type instructionLoader struct{ workspaceDir, configDir string }

func (l instructionLoader) Stat(path string) error { _, err := os.Stat(path); return err }

func (l instructionLoader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (l instructionLoader) SkillsSummary() (string, error) {
	return skills.NewLoader(l.workspaceDir, l.configDir).BuildSummary()
}

// SessionTurnWorkspaceResolver validates a host-selected workspace against the
// CLI filesystem policy and returns its primary root.
func SessionTurnWorkspaceResolver(allowPaths []string) func(string) (string, error) {
	return func(workDir string) (string, error) {
		policy, err := cliTools.ResolveFilesystemPolicy(workDir, allowPaths...)
		if err != nil {
			return "", err
		}
		return policy.PrimaryRoot(), nil
	}
}

// SessionTurnConfiguredImageModel reads configured model metadata.
func SessionTurnConfiguredImageModel(configDir string) func(string) (*sessionturn.ImageModelMetadata, error) {
	return func(model string) (*sessionturn.ImageModelMetadata, error) {
		storage, err := config.NewModelsConfigStorage(configDir)
		if err != nil {
			return nil, fmt.Errorf("initialize model capability metadata: %w", err)
		}
		models, err := storage.Load()
		if err != nil {
			return nil, fmt.Errorf("load model capability metadata: %w", err)
		}
		info := models.Lookup(model)
		if info == nil {
			return nil, nil
		}
		return &sessionturn.ImageModelMetadata{
			InputModalities:         append([]string(nil), info.InputModalities...),
			SupportedInputMIMETypes: append([]string(nil), info.SupportedInputMimeTypes...),
		}, nil
	}
}

// SessionTurnStagingRoot resolves the absolute config directory that holds
// session-staged images.
func SessionTurnStagingRoot(configDir string) func() (string, error) {
	return func() (string, error) {
		storage, err := config.NewDefaultConfigStorage(strings.TrimSpace(configDir))
		if err != nil {
			return "", err
		}
		return filepath.Clean(filepath.Dir(storage.Path())), nil
	}
}
