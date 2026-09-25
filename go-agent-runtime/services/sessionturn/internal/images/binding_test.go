package images

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const stagedRoot = "/staging-root"

type plainExecutor struct{}

func (plainExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{}, nil
}

// binderExecutor captures the bound preparer.
type binderExecutor struct {
	plainExecutor
	preparer tools.ImagePartPreparer
}

func (b *binderExecutor) WithSessionImagePreparer(preparer tools.ImagePartPreparer) messages.ToolExecutor {
	b.preparer = preparer
	return plainExecutor{}
}

func readImageDefinitions() []messages.ToolDefinition {
	return []messages.ToolDefinition{{Name: "unrelated"}, {Name: tools.ReadImageToolID}}
}

func TestBindToolsLeavesUnboundExecutors(t *testing.T) {
	if got := BindTools(sessionturn.ImageToolBinding{Definitions: readImageDefinitions()}); got != nil {
		t.Fatalf("nil executor = %#v", got)
	}
	binder := &binderExecutor{}
	unresolved := func() (sessionturn.ImageCapabilities, error) {
		t.Fatal("capabilities resolved for an executor that cannot bind read_image")
		return sessionturn.ImageCapabilities{}, nil
	}
	if got := BindTools(sessionturn.ImageToolBinding{Executor: binder, Definitions: []messages.ToolDefinition{{Name: "unrelated"}}, Resolve: unresolved}); got != binder || binder.preparer != nil {
		t.Fatal("executor without read_image was bound")
	}
	var plain messages.ToolExecutor = plainExecutor{}
	if got := BindTools(sessionturn.ImageToolBinding{Executor: plain, Definitions: readImageDefinitions(), Resolve: unresolved}); got != plain {
		t.Fatal("non-binder executor was replaced")
	}
}

func TestBindToolsUsesCapabilitySnapshot(t *testing.T) {
	binder := &binderExecutor{}
	snapshot := capabilities()
	resolved := 0
	resolve := func() (sessionturn.ImageCapabilities, error) {
		resolved++
		return snapshot, nil
	}
	bound := BindTools(sessionturn.ImageToolBinding{Executor: binder, Definitions: readImageDefinitions(), Resolve: resolve, Load: fixtureLoader(t)})
	if bound == nil || binder.preparer == nil || resolved != 1 {
		t.Fatalf("read_image executor bound=%v resolved=%d, want bound once", binder.preparer != nil, resolved)
	}
	snapshot.SupportedInputMIMETypes[0] = gifType
	parts, err := binder.preparer([]string{validPNG})
	if err != nil || len(parts) != 1 || parts[0].MediaType != mimePNG {
		t.Fatalf("prepared = %#v, %v", parts, err)
	}
	failing := &binderExecutor{}
	resolveErr := errors.New("capability resolution failed")
	BindTools(sessionturn.ImageToolBinding{Executor: failing, Definitions: readImageDefinitions(), Resolve: func() (sessionturn.ImageCapabilities, error) {
		return sessionturn.ImageCapabilities{}, resolveErr
	}})
	if _, err := failing.preparer([]string{validPNG}); !errors.Is(err, resolveErr) {
		t.Fatalf("capability error = %v", err)
	}
}

type fakeStaging struct {
	request tools.ImageStagingRequest
	result  tools.ImageStagingResult
	err     error
	calls   int
}

func (s *fakeStaging) Stage(_ context.Context, request tools.ImageStagingRequest) (tools.ImageStagingResult, error) {
	s.calls++
	s.request = request
	return s.result, s.err
}

func stagingRequest(root func() (string, error)) sessionturn.ImageStagingRequest {
	return sessionturn.ImageStagingRequest{
		SourcePaths: []string{"source.png"}, Parts: []messages.ImagePart{{Bytes: []byte{1}, MediaType: mimePNG}},
		StagingRoot: root, ToolDefinitions: readImageDefinitions(),
	}
}

func rootAt(path string) func() (string, error) { return func() (string, error) { return path, nil } }

func TestStageSkipsSessionsWithoutReadImage(t *testing.T) {
	staging := &fakeStaging{}
	request := stagingRequest(func() (string, error) {
		t.Fatal("staging root resolved without read_image")
		return "", nil
	})
	request.ToolDefinitions = []messages.ToolDefinition{{Name: "unrelated"}}
	got, err := Stage(context.Background(), staging, request)
	if err != nil || staging.calls != 0 || len(got.ToolDefinitions) != 1 || got.Cleanup() != nil {
		t.Fatalf("stage without read_image = %#v, %v", got, err)
	}
}

func TestStageFailures(t *testing.T) {
	rootErr := errors.New("home unavailable")
	stageErr := errors.New("disk full")
	mismatch := stagingRequest(rootAt(stagedRoot))
	mismatch.Parts = nil
	cases := []struct {
		name    string
		staging tools.ImageStaging
		request sessionturn.ImageStagingRequest
		want    string
	}{
		{name: "count mismatch", staging: &fakeStaging{}, request: mismatch, want: "does not match image part count"},
		{name: "no staging root", staging: &fakeStaging{}, request: stagingRequest(nil), want: string(errNoStaging)},
		{name: "no staging", request: stagingRequest(rootAt(stagedRoot)), want: string(errNoStaging)},
		{name: "root error", staging: &fakeStaging{}, request: stagingRequest(func() (string, error) { return "", rootErr }), want: rootErr.Error()},
		{name: "stage error", staging: &fakeStaging{err: stageErr}, request: stagingRequest(rootAt(stagedRoot)), want: stageErr.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Stage(context.Background(), tc.staging, tc.request)
			if err == nil || !strings.Contains(err.Error(), tc.want) || got.Cleanup == nil || got.Cleanup() != nil {
				t.Fatalf("stage = %v, want %q with no-op cleanup", err, tc.want)
			}
		})
	}
}

func TestStageDelegatesWithResolvedRoot(t *testing.T) {
	staged := []messages.ToolDefinition{{Name: tools.ReadImageToolID, Description: "staged"}}
	staging := &fakeStaging{result: tools.ImageStagingResult{ToolDefinitions: staged}}
	got, err := Stage(context.Background(), staging, stagingRequest(rootAt(stagedRoot)))
	if err != nil || staging.request.StagingRoot != stagedRoot || len(staging.request.ImageParts) != 1 || got.ToolDefinitions[0].Description != "staged" {
		t.Fatalf("stage = %#v, %v", got, err)
	}
	if got.Cleanup == nil || got.Cleanup() != nil {
		t.Fatal("nil staging cleanup was not replaced with a no-op")
	}
	if HasTool(nil, tools.ReadImageToolID) {
		t.Fatal("empty definitions reported read_image")
	}
}
