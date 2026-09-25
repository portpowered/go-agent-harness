package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const customFast = 7 * time.Second

type fakePolicy struct{ tools.InteractiveToolPolicy }

type fakeFactory struct {
	request tools.InteractiveToolPolicyRequest
}

func (f *fakeFactory) Resolve(request tools.InteractiveToolPolicyRequest) (tools.InteractiveToolPolicy, error) {
	f.request = request
	return fakePolicy{}, nil
}

func (f *fakeFactory) ValidateSettings(tools.InteractiveToolPolicySettings) error { return nil }

func TestResolveInteractivePolicyDefaultsAndBrowserNames(t *testing.T) {
	factory := &fakeFactory{}
	service := New(factory)
	if _, err := service.ResolveInteractivePolicy(sessionturn.InteractivePolicyRequest{DynamicLongRunning: true}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := tools.InteractiveToolPolicySettings{
		FastReadTimeout: tools.DefaultInteractiveFastReadTimeout, LongRunningTimeout: tools.DefaultInteractiveLongRunningTimeout,
		AcknowledgementThreshold: tools.DefaultInteractiveAcknowledgementThreshold,
	}
	if factory.request.Settings != want || !factory.request.DynamicLongRunning || !slices.Contains(factory.request.ExplicitLongRunningNames, tools.InvokeToolName) || len(factory.request.ExplicitLongRunningNames) != len(browserLongRunningToolNames()) {
		t.Fatalf("request = %#v", factory.request)
	}
	custom := tools.InteractiveToolPolicySettings{FastReadTimeout: customFast}
	if _, err := service.ResolveInteractivePolicy(sessionturn.InteractivePolicyRequest{Settings: &custom}); err != nil || factory.request.Settings != custom {
		t.Fatalf("custom settings = %#v, %v", factory.request.Settings, err)
	}
	if _, err := New(nil).ResolveInteractivePolicy(sessionturn.InteractivePolicyRequest{}); !errors.Is(err, errNoPolicyFactory) {
		t.Fatalf("nil factory = %v", err)
	}
}

type panickingExecutor struct{}

func (panickingExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	panic("tool failure stays inside the call")
}

func TestToolExecutorIsolatesPanicsAsCorrelatedResults(t *testing.T) {
	executor := New(nil).NewToolExecutor(sessionturn.ToolExecutorRequest{Inner: panickingExecutor{}, Timeout: time.Second})
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-7", Name: "boom"})
	if err != nil || response.ToolCallID != "call-7" || response.Name != "boom" || !strings.Contains(response.Content, "panicked") {
		t.Fatalf("panic result = %#v, %v", response, err)
	}
}
