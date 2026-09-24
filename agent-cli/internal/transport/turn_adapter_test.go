package transport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/stretchr/testify/require"
)

const (
	testBrowserID  = "browser"
	testTargetID   = "tab"
	projectedBound = time.Second
)

func TestSessionTurnToolPresentationProjectsCLIFailures(t *testing.T) {
	presentation := SessionTurnToolPresentation()
	require.True(t, presentation.DisplayTool(cliTools.ScreenToolID))
	require.False(t, presentation.DisplayTool(cliTools.PageSightToolID))
	require.Equal(t, sight.SourceScreen, presentation.DisplaySource)
	require.Equal(t, sight.SourceBrowserPage, presentation.PageSightSource)

	denied := presentation.DisplayPermissionDenied(runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionDenied, Reason: "revoked"})
	var captureErr *cliTools.ScreenCaptureError
	require.ErrorAs(t, denied, &captureErr)
	require.Equal(t, cliTools.ScreenCaptureDenied, captureErr.State)
	require.Equal(t, "revoked", captureErr.Reason)
	require.Equal(t, cliTools.ScreenRecordingPermissionDeniedErrorCode, presentation.DisplayErrorCode(denied))

	result, err := sight.Decode([]byte(presentation.DisplayFailure(denied)))
	require.NoError(t, err)
	require.Equal(t, "Screen sight is unavailable.", result.Error, "display failure must stay customer safe")

	page, err := sight.Decode([]byte(presentation.PageSightFailure()))
	require.NoError(t, err)
	require.Equal(t, sight.SourceBrowserPage, page.Source)
	require.Equal(t, sessionturn.PageSightUnavailableErrorCode, page.ErrorCode)
	require.Equal(t, pageSightUnavailable, page.Error)
}

func TestSessionTurnToolPresentationRecognizesFilesystemRefusals(t *testing.T) {
	presentation := SessionTurnToolPresentation()
	refusal, err := cliTools.MarshalFilesystemRefusal(cliTools.FilesystemRefusal{
		Type: cliTools.FilesystemRefusalType, Version: cliTools.FilesystemRefusalVersion, Status: cliTools.FilesystemRefusalStatus,
		Operation: "read", Path: "/outside", WorkDir: "/work", Reason: cliTools.FilesystemRefusalOutsidePermittedRoots,
		Message: "refused", Remediation: "stay inside the workspace",
	})
	require.NoError(t, err)
	require.True(t, presentation.FailedContent(string(refusal)))
	require.False(t, presentation.FailedContent(`{"ok":true}`))
}

func TestSessionTurnBrowserWatchProjectsBrokerEvents(t *testing.T) {
	require.Nil(t, SessionTurnBrowserWatch(nil))
	require.Nil(t, SessionTurnBrowserWatch(func(context.Context) <-chan webmcp.BrokerEvent { return nil })(context.Background()))

	source := make(chan webmcp.BrokerEvent, 2)
	projected := SessionTurnBrowserWatch(func(context.Context) <-chan webmcp.BrokerEvent { return source })(context.Background())
	require.Equal(t, cap(source), cap(projected), "projection must keep the source burst buffer")
	source <- webmcp.BrokerEvent{Type: webmcp.BrokerEventSelected, BrowserID: testBrowserID, TargetID: testTargetID, Generation: 3, Sequence: 7}
	close(source)
	require.Equal(t, sessionturn.BrowserEvent{Type: sessionturn.BrowserEventSelected, BrowserID: testBrowserID, TargetID: testTargetID, Generation: 3, Sequence: 7}, receiveProjected(t, projected))
	select {
	case _, ok := <-projected:
		require.False(t, ok, "projection must close when the source closes")
	case <-time.After(projectedBound):
		t.Fatal("projection did not close after the source closed")
	}
}

func TestSessionTurnBrowserWatchStopsWithContextWithoutClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := make(chan webmcp.BrokerEvent)
	projected := SessionTurnBrowserWatch(func(context.Context) <-chan webmcp.BrokerEvent { return source })(ctx)
	cancel()
	select {
	case event, ok := <-projected:
		t.Fatalf("cancelled projection delivered %#v (open=%v), want no watch-closed signal", event, ok)
	case <-time.After(10 * time.Millisecond):
	}
}

func receiveProjected(t *testing.T, projected <-chan sessionturn.BrowserEvent) sessionturn.BrowserEvent {
	t.Helper()
	select {
	case event := <-projected:
		return event
	case <-time.After(projectedBound):
		t.Fatal("projection did not deliver the broker event")
		return sessionturn.BrowserEvent{}
	}
}

func TestSessionTurnInteractiveSettingsCopiesConfiguredBudgets(t *testing.T) {
	settings := config.DefaultInteractiveToolConfig()
	require.Equal(t, &runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout: settings.FastReadTimeout, LongRunningTimeout: settings.LongRunningTimeout, AcknowledgementThreshold: settings.AcknowledgementThreshold,
	}, SessionTurnInteractiveSettings(settings))
}

func TestSessionTurnInstructionLoaderReadsWorkspaceFiles(t *testing.T) {
	workspace, configDir := t.TempDir(), t.TempDir()
	path := filepath.Join(workspace, "AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte("instructions"), 0o600))
	loader := SessionTurnInstructionLoader(configDir)(workspace)
	require.NoError(t, loader.Stat(path))
	require.ErrorIs(t, loader.Stat(filepath.Join(workspace, "missing")), os.ErrNotExist)
	data, err := loader.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "instructions", string(data))
	_, err = loader.SkillsSummary()
	require.NoError(t, err)
}

func TestSessionTurnWorkspaceResolverUsesFilesystemPolicy(t *testing.T) {
	workspace := t.TempDir()
	root, err := SessionTurnWorkspaceResolver(nil)(workspace)
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.Equal(t, resolved, root)
	_, err = SessionTurnWorkspaceResolver(nil)(filepath.Join(workspace, "missing"))
	require.Error(t, err)
}

func TestSessionTurnConfiguredImageModelReadsModelsConfig(t *testing.T) {
	configDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, config.ModelsFileName), []byte(`
models:
  - name: vision-model
    providers: [openai]
    input_modalities: [text, image]
    supportedInputMimeTypes: [image/png]
`), 0o600))
	lookup := SessionTurnConfiguredImageModel(configDir)
	metadata, err := lookup("vision-model")
	require.NoError(t, err)
	require.Equal(t, &sessionturn.ImageModelMetadata{InputModalities: []string{"text", "image"}, SupportedInputMIMETypes: []string{"image/png"}}, metadata)
	metadata, err = lookup("unconfigured-model")
	require.NoError(t, err)
	require.Nil(t, metadata)

	notDir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notDir, nil, 0o600))
	_, err = SessionTurnConfiguredImageModel(notDir)("vision-model")
	require.ErrorContains(t, err, "load model capability metadata")
}

func TestSessionTurnStagingRootResolvesAbsoluteConfigDir(t *testing.T) {
	configDir := t.TempDir()
	root, err := SessionTurnStagingRoot("  " + configDir + "  ")()
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(configDir), root)
	require.True(t, filepath.IsAbs(root))
	root, err = SessionTurnStagingRoot("relative-config")()
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(root) && strings.HasSuffix(root, "relative-config"))
}
