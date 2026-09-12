package sessioncontinuation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"
)

func TestUnresolvedToolResultsErrorOwnsSnapshots(t *testing.T) {
	err := &sessioncontinuation.UnresolvedToolResultsError{
		CallIDs:      []string{"call-a", "call-b"},
		SendStatuses: map[string]messages.SessionSendStatus{"call-a": messages.SessionSendClosed},
	}
	if got := err.Error(); !strings.Contains(got, "call-a, call-b") || !strings.Contains(got, "call-a=closed") {
		t.Fatalf("unresolved error = %q, want IDs and send status", got)
	}
	if !errors.Is(err, sessioncontinuation.ErrUnresolvedToolResults) {
		t.Fatalf("unresolved error lost sentinel identity: %v", err)
	}
	ids := err.UnresolvedCallIDs()
	statuses := err.SendStatusSnapshot()
	ids[0] = "mutated"
	statuses["call-a"] = messages.SessionSendTimedOut
	if err.CallIDs[0] != "call-a" || err.SendStatuses["call-a"] != messages.SessionSendClosed {
		t.Fatalf("error snapshot aliases were exposed: ids=%v statuses=%v", ids, statuses)
	}
	var nilErr *sessioncontinuation.UnresolvedToolResultsError
	if nilErr.Error() != sessioncontinuation.ErrUnresolvedToolResults.Error() || nilErr.UnresolvedCallIDs() != nil || nilErr.SendStatusSnapshot() != nil {
		t.Fatal("nil unresolved error did not return safe empty snapshots")
	}
}

func TestContinuationSentinelsRetainLiveRuntimeIdentity(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
	}{
		{
			name:   "image",
			err:    &runtimesession.LiveImageContinuationError{CallIDs: []string{"image-call"}},
			target: sessioncontinuation.ErrImageContinuationIncomplete,
		},
		{
			name:   "tool",
			err:    &runtimesession.LiveToolContinuationError{CallIDs: []string{"tool-call"}},
			target: sessioncontinuation.ErrToolContinuationIncomplete,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !errors.Is(test.err, test.target) {
				t.Fatalf("live runtime error %v does not match continuation sentinel %v", test.err, test.target)
			}
		})
	}
}

func TestContinuationErrorsExposeLegacyRuntimeViews(t *testing.T) {
	image := &sessioncontinuation.ImageContinuationError{CallIDs: []string{"image-call"}}
	var legacyImage *runtimesession.LiveImageContinuationError
	if !errors.As(image, &legacyImage) || legacyImage == nil || legacyImage.CallIDs[0] != "image-call" {
		t.Fatalf("image continuation did not expose its legacy view: %v", image)
	}

	tool := &sessioncontinuation.ToolContinuationError{CallIDs: []string{"tool-call"}}
	var legacyTool *runtimesession.LiveToolContinuationError
	if !errors.As(tool, &legacyTool) || legacyTool == nil || legacyTool.CallIDs[0] != "tool-call" {
		t.Fatalf("tool continuation did not expose its legacy view: %v", tool)
	}
}
