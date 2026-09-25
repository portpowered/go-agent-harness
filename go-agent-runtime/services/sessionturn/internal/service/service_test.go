package service

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	composedSuffix = " +policy"
	userPrompt     = "describe this"
	customFast     = 7 * time.Second
)

type fakePolicy struct{ tools.InteractiveToolPolicy }

type fakeFactory struct {
	request tools.InteractiveToolPolicyRequest
}

func (f *fakeFactory) Resolve(request tools.InteractiveToolPolicyRequest) (tools.InteractiveToolPolicy, error) {
	f.request = request
	return fakePolicy{}, nil
}

func (f *fakeFactory) ValidateSettings(tools.InteractiveToolPolicySettings) error { return nil }

type fakeInstructions struct{}

func (fakeInstructions) Resolve(_ context.Context, request session.InstructionRequest) (session.InstructionResult, error) {
	return session.InstructionResult{Instructions: request.Prompt}, nil
}

func (fakeInstructions) Compose(composition session.InstructionComposition) string {
	return composition.Instructions + composedSuffix
}

type nopInferencer struct{}

func (nopInferencer) ConnectSession(context.Context) (messages.Session, error) { return nil, nil }

type streamOnly struct{}

func (streamOnly) Send(context.Context, messages.StreamMessage) bool      { return true }
func (streamOnly) Receive() *messages.TypedBuffer[messages.StreamMessage] { return nil }
func (streamOnly) Done() <-chan struct{}                                  { return nil }
func (streamOnly) Close() error                                           { return nil }

func TestResolveInteractivePolicyDefaultsAndBrowserNames(t *testing.T) {
	factory := &fakeFactory{}
	service := New(nil, factory, nil)
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
	if _, err := New(nil, nil, nil).ResolveInteractivePolicy(sessionturn.InteractivePolicyRequest{}); !errors.Is(err, errNoPolicyFactory) {
		t.Fatalf("nil factory = %v", err)
	}
}

func TestTurnsToolsAndPublicationDelegation(t *testing.T) {
	service := New(nil, nil, nil)
	if service.NewTurns(sessionturn.TurnsOptions{}).NextTurnIndex() != 1 {
		t.Fatal("new turns did not start at index one")
	}
	if service.NewToolExecutor(sessionturn.ToolExecutorRequest{}) == nil {
		t.Fatal("tool executor was nil")
	}
	inert := service.StartPublication(context.Background(), sessionturn.PublicationRequest{})
	if inert.Errors() != nil {
		t.Fatal("publication without watch was not inert")
	}
	events := make(chan sessionturn.BrowserEvent)
	live := service.StartPublication(context.Background(), sessionturn.PublicationRequest{
		Watch:   func(context.Context) <-chan sessionturn.BrowserEvent { return events },
		Refresh: func(context.Context) ([]messages.ToolDefinition, error) { return nil, nil },
	})
	live.Stop()
	if live.State().Lifecycle != sessionturn.PublicationStopped {
		t.Fatalf("live publication lifecycle = %s", live.State().Lifecycle)
	}
	merged := service.MergeToolDefinitions([]messages.ToolDefinition{{Name: "base"}}, []messages.ToolDefinition{{Name: "base"}, {Name: "page"}})
	if digest, err := service.ToolDefinitionDigest(merged); len(merged) != 2 || err != nil || digest == "" {
		t.Fatalf("merge/digest = %#v, %v", merged, err)
	}
}

func TestSeedAndInstructionDelegation(t *testing.T) {
	service := New(fakeInstructions{}, nil, nil)
	first, second := service.NextWirePrompt(), service.NextWirePrompt()
	if first == second || !strings.HasPrefix(first, sessionturn.TextSeedWirePrefix) {
		t.Fatalf("wire prompts = %q, %q", first, second)
	}
	if service.NewTextSeedInferencer(nopInferencer{}, first, "seed") == nil || service.NewInstructionsInferencer(nopInferencer{}, "", nil) == nil {
		t.Fatal("wrapper inferencer was nil")
	}
	var buffer bytes.Buffer
	if _, err := service.NewOutput(&buffer).Write([]byte("x")); err != nil || buffer.String() != "x" {
		t.Fatalf("output = %q, %v", buffer.String(), err)
	}
	if got, err := service.ResolveInstructions(context.Background(), sessionturn.InstructionsRequest{Prompt: userPrompt}); err != nil || got != userPrompt {
		t.Fatalf("instructions = %q, %v", got, err)
	}
	if got := service.ComposeInstructions(session.InstructionComposition{Instructions: userPrompt}); got != userPrompt+composedSuffix {
		t.Fatalf("composed = %q", got)
	}
	if got := New(nil, nil, nil).ComposeInstructions(session.InstructionComposition{Instructions: userPrompt}); got != userPrompt {
		t.Fatalf("fallback compose = %q", got)
	}
}

func TestAttachImagesSelectsLoopPrompt(t *testing.T) {
	service := New(nil, nil, nil)
	cases := []struct {
		name       string
		request    sessionturn.ImageAttachRequest
		wantPrompt string
		seeded     bool
	}{
		{name: "seed", request: sessionturn.ImageAttachRequest{Seed: sessionturn.Seed{Present: true}, Prompt: userPrompt}, seeded: true},
		{name: "image only", request: sessionturn.ImageAttachRequest{}, wantPrompt: sessionturn.ImageOnlyPrompt},
		{name: "prompt kept", request: sessionturn.ImageAttachRequest{Prompt: userPrompt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.request.Inferencer = nopInferencer{}
			attachment, err := service.AttachImages(tc.request)
			if err != nil || attachment.Inferencer == nil || attachment.FirstTurn == nil {
				t.Fatalf("attachment = %#v, %v", attachment, err)
			}
			if tc.seeded && (attachment.WirePrompt == "" || attachment.Prompt != attachment.WirePrompt) {
				t.Fatalf("seeded attachment = %#v", attachment)
			}
			if !tc.seeded && (attachment.Prompt != tc.wantPrompt || attachment.WirePrompt != "") {
				t.Fatalf("attachment = %#v, want prompt %q", attachment, tc.wantPrompt)
			}
		})
	}
	if _, err := service.AttachImages(sessionturn.ImageAttachRequest{}); !errors.Is(err, sessionturn.ErrMissingInferencer) {
		t.Fatalf("nil inferencer = %v", err)
	}
}

func TestImageDelegation(t *testing.T) {
	service := New(nil, nil, nil)
	if err := service.SendImageTurn(context.Background(), streamOnly{}, userPrompt, nil); !errors.Is(err, sessionturn.ErrImageSend) {
		t.Fatalf("stream-only image turn = %v", err)
	}
	if _, err := service.ResolveImageCapabilities(sessionturn.ImageCapabilityRequest{Provider: "grok"}); !errors.Is(err, sessionturn.ErrImageCapability) {
		t.Fatalf("capability = %v", err)
	}
	if _, err := service.PrepareImageParts(sessionturn.ImagePartsRequest{}); !errors.Is(err, sessionturn.ErrImageCapability) {
		t.Fatalf("prepare = %v", err)
	}
	if service.BindImageTools(sessionturn.ImageToolBinding{}) != nil {
		t.Fatal("binding without executor produced one")
	}
	staged, err := service.StageImageTools(context.Background(), sessionturn.ImageStagingRequest{})
	if err != nil || staged.Cleanup() != nil {
		t.Fatalf("stage = %v", err)
	}
	if complete, deferred := service.CompleteMessageSupport(streamOnly{}); complete || deferred {
		t.Fatal("stream-only session reported complete messages")
	}
}
