package agentruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/stretchr/testify/require"
)

// These tests bind the runtime plan to the session-turn service through the
// CLI configuration, tools composition, and filesystem adapters.

const (
	toolNameExec     = "exec"
	toolNameReadFile = "read_file"
	unusedReplayPath = "unused.json"
)

func TestInteractiveToolPolicyDefaultsAndClassSelection(t *testing.T) {
	policy, err := NewInteractiveToolPolicyForSession(config.DefaultInteractiveToolConfig(), []messages.ToolDefinition{
		{Name: toolNameReadFile},
		{Name: toolNameExec},
		{Name: "unclassified"},
	}, nil, false)
	require.NoError(t, err)
	settings := policy.Settings()
	require.Equal(t, config.DefaultInteractiveFastReadTimeout, settings.FastReadTimeout)
	require.Equal(t, config.DefaultInteractiveLongRunningTimeout, settings.LongRunningTimeout)
	require.Equal(t, config.DefaultInteractiveAcknowledgementThreshold, settings.AcknowledgementThreshold)
	for name, want := range map[string]runtimeTools.InteractiveToolClass{
		toolNameReadFile: InteractiveToolClassFastRead,
		toolNameExec:     InteractiveToolClassBoundedLongRunning,
		"unclassified":   InteractiveToolClassFastRead,
		"not-advertised": InteractiveToolClassFastRead,
	} {
		require.Equal(t, want, policy.ClassForTool(name), "class for %s", name)
	}
	require.Equal(t, config.DefaultInteractiveFastReadTimeout, policy.TimeoutForTool(toolNameReadFile))
	require.Equal(t, config.DefaultInteractiveLongRunningTimeout, policy.TimeoutForTool(toolNameExec))
	require.Equal(t, config.DefaultInteractiveFastReadTimeout, policy.TimeoutForTool("not-advertised"))
}

func TestInteractiveToolPolicyHonorsOverridesAndRejectsInvalidSettings(t *testing.T) {
	const (
		fastRead    = 7 * time.Second
		longRunning = 15 * time.Second
		acknowledge = 1200 * time.Millisecond
	)
	settings := config.InteractiveToolConfig{FastReadTimeout: fastRead, LongRunningTimeout: longRunning, AcknowledgementThreshold: acknowledge}
	policy, err := NewInteractiveToolPolicyForSession(settings, []messages.ToolDefinition{{Name: "sleep"}}, nil, false)
	require.NoError(t, err)
	require.Equal(t, runtimeTools.InteractiveToolPolicySettings{FastReadTimeout: fastRead, LongRunningTimeout: longRunning, AcknowledgementThreshold: acknowledge}, policy.Settings())
	require.Equal(t, longRunning, policy.TimeoutForTool("sleep"))
	require.NoError(t, policy.Clone().Validate())

	invalid := settings
	invalid.FastReadTimeout = runtimeTools.InteractiveFastReadTimeoutLimit
	_, err = NewInteractiveToolPolicyForSession(invalid, nil, nil, false)
	require.ErrorContains(t, err, "fast_read_timeout")
}

func TestInteractiveToolExecutorUsesIndependentSessionBudgets(t *testing.T) {
	const (
		fastA = 3 * time.Second
		fastB = 7 * time.Second
	)
	definitions := []messages.ToolDefinition{{Name: toolNameReadFile}, {Name: toolNameExec}}
	policyA, err := NewInteractiveToolPolicyForSession(config.InteractiveToolConfig{FastReadTimeout: fastA, LongRunningTimeout: 11 * time.Second, AcknowledgementThreshold: time.Second}, definitions, nil, false)
	require.NoError(t, err)
	policyB, err := NewInteractiveToolPolicyForSession(config.InteractiveToolConfig{FastReadTimeout: fastB, LongRunningTimeout: 19 * time.Second, AcknowledgementThreshold: 2 * time.Second}, definitions, nil, false)
	require.NoError(t, err)

	inner := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return messages.ToolCallResponse{}, errors.New("missing per-call deadline")
		}
		return messages.ToolCallResponse{Content: time.Until(deadline).String()}, nil
	})
	for _, tc := range []struct {
		policy InteractiveToolPolicy
		budget time.Duration
	}{{policyA, fastA}, {policyB, fastB}} {
		executor := newSessionLoopToolExecutor(sessionLoopOptions{ToolExecutor: inner, InteractiveToolPolicy: tc.policy})
		response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "budget", Name: toolNameReadFile})
		require.NoError(t, err)
		remaining, err := time.ParseDuration(response.Content)
		require.NoError(t, err)
		require.True(t, remaining > tc.budget-time.Second && remaining <= tc.budget, "deadline remaining = %s, want close to %s", remaining, tc.budget)
	}
}

func testPolicyPlanOptions(opts SessionRunOptions) SessionRunOptions {
	opts.ModelCatalog = testModelCatalog()
	opts.AudioService = newTestAudioIOService()
	opts.ReplayPath = unusedReplayPath
	opts.SessionInferencer = stubPlanSessionInferencer{}
	opts.ToolExecutor = sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{}, nil
	})
	return opts
}

func TestPlanSessionRuntimeThreadsResolvedInteractivePolicyBeforeProviderSetup(t *testing.T) {
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 4 * time.Second
	settings.LongRunningTimeout = 18 * time.Second
	plan, err := planSessionRuntime(testPolicyPlanOptions(SessionRunOptions{
		LoadedConfig:    &config.Config{Tools: config.ToolsConfig{Interactive: settings}},
		ToolDefinitions: []messages.ToolDefinition{{Name: toolNameExec}, {Name: toolNameReadFile}},
	}))
	require.NoError(t, err)
	require.NotNil(t, plan.interactivePolicy, "plan did not retain the interactive policy snapshot")
	require.NotNil(t, plan.loop.InteractiveToolPolicy, "loop did not retain the interactive policy snapshot")
	require.Equal(t, settings.FastReadTimeout, plan.interactivePolicy.Settings().FastReadTimeout)
	require.Equal(t, settings.LongRunningTimeout, plan.loop.InteractiveToolPolicy.Settings().LongRunningTimeout)
}

func TestPlanSessionRuntimeLoadsInteractivePolicyFromConfigDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte(`tools:
  interactive:
    fast_read_timeout: 6s
    long_running_timeout: 16s
    acknowledgement_threshold: 1500ms
`), 0o600))
	plan, err := planSessionRuntime(testPolicyPlanOptions(SessionRunOptions{
		ConfigDir:       dir,
		ToolDefinitions: []messages.ToolDefinition{{Name: toolNameReadFile}},
	}))
	require.NoError(t, err)
	require.NotNil(t, plan.interactivePolicy)
	require.Equal(t, runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout: 6 * time.Second, LongRunningTimeout: 16 * time.Second, AcknowledgementThreshold: 1500 * time.Millisecond,
	}, plan.interactivePolicy.Settings())
}

func TestPlanSessionRuntimeRejectsInvalidInteractiveConfigBeforeProviderSetup(t *testing.T) {
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 10 * time.Second
	providerCalls := 0
	factory := defaultSessionRuntimeFactory()
	factory.newGrokSessionWithTools = func(config.GrokConfig, transport.Dialer, []messages.ToolDefinition) (messages.SessionInferencer, error) {
		providerCalls++
		return nil, errors.New("provider must not be built")
	}
	_, err := planSessionRuntimeWithFactory(SessionRunOptions{ModelCatalog: testModelCatalog(),
		AudioService:           newTestAudioIOService(),
		RecordingService:       newTestRecordingService(),
		ProviderCaptureService: newTestProviderCaptureService(),
		LoadedConfig:           &config.Config{Tools: config.ToolsConfig{Interactive: settings}},
		Provider:               config.ProviderGrok,
		RecordPath:             "capture.json",
		ToolDefinitions:        []messages.ToolDefinition{{Name: toolNameReadFile}},
	}, factory)
	if err == nil || !strings.Contains(err.Error(), "fast_read_timeout") {
		t.Fatalf("plan error = %v, want fast-read validation", err)
	}
	require.Zero(t, providerCalls, "provider setup calls after invalid config")
}

func TestPrepareSessionImageToolAccess_NoReadImageSkipsHostResolution(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	got, cleanup, err := prepareSessionImageToolAccess(context.Background(), SessionRunOptions{ConfigDir: "\x00invalid-config-path", ToolDefinitions: []messages.ToolDefinition{{Name: "unrelated"}}}, []string{"image.png"}, nil)
	require.NoError(t, err, "prepare without read_image")
	require.Len(t, got.ToolDefinitions, 1)
	require.Equal(t, "unrelated", got.ToolDefinitions[0].Name)
	cleanup()
	_, err = os.Stat(root)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestPrepareSessionImageToolAccess_StagesAndRefreshesThroughToolsContract(t *testing.T) {
	configDir, imageBytes := filepath.Join(t.TempDir(), "config"), []byte("literal-image")
	base := []messages.ToolDefinition{{Name: "unrelated", Description: "keep"}, {Name: runtimeTools.ReadImageToolID, Parameters: []messages.ToolParameter{{Name: "path", Type: "string", Description: "original", Required: true}}}}
	got, cleanup, err := prepareSessionImageToolAccess(context.Background(), SessionRunOptions{ConfigDir: configDir, ToolDefinitions: base}, []string{"source.any"}, []messages.ImagePart{{Bytes: imageBytes, MediaType: "image/png"}})
	require.NoError(t, err)
	require.Nil(t, got.RefreshToolDefinitions)
	path := stagedReadImagePath(got.ToolDefinitions)
	require.NotEmpty(t, path, "read_image definition missing staged path marker")
	require.True(t, filepath.IsAbs(path), "advertised path = %q, want absolute path", path)
	require.Equal(t, ".png", filepath.Ext(path))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, imageBytes, actual)
	_, err = os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	cleanup()
	cleanup()
	_, err = os.Stat(filepath.Dir(path))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func stagedReadImagePath(definitions []messages.ToolDefinition) string {
	const marker = "Session-staged image path(s) (use one of these exact absolute paths):\n- "
	for _, definition := range definitions {
		for _, parameter := range definition.Parameters {
			if definition.Name != runtimeTools.ReadImageToolID || parameter.Name != "path" {
				continue
			}
			if index := strings.Index(parameter.Description, marker); index >= 0 {
				return strings.TrimSpace(strings.Split(parameter.Description[index+len(marker):], "\n- ")[0])
			}
		}
	}
	return ""
}
