package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

func TestSessionToolExecutor_ScreenTimeoutDeniedRecheckUsesOneCorrelatedPermissionResult(t *testing.T) {
	executor := newTimeoutScreenPermissionExecutor(cliTools.DisplayPermission{State: cliTools.DisplayPermissionGranted})
	call := messages.ToolCall{ID: "screen-timeout-denied", Name: cliTools.ScreenToolID, Arguments: `{"action":"screenshot"}`}
	go func() {
		<-executor.started
		executor.setPermission(cliTools.DisplayPermission{State: cliTools.DisplayPermissionDenied, Reason: "permission became ineffective during capture"})
	}()

	response, err := newTestSessionToolExecutor(executor, 15*time.Millisecond).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if response.ToolCallID != call.ID || response.Name != call.Name || len(response.ContentParts) != 0 {
		t.Fatalf("screen timeout denial response = %#v, want correlated text-only result", response)
	}
	result, err := sight.Decode([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode screen timeout denial: %v", err)
	}
	if result.Version != sight.ResultVersion || result.Status != sight.StatusError || result.Source != sight.SourceScreen || result.ErrorCode != cliTools.ScreenRecordingPermissionDeniedErrorCode {
		t.Fatalf("screen timeout denial result = %+v, want version 2 permission denial", result)
	}
	for _, forbidden := range []string{
		"System Settings → Privacy & Security → Screen & System Audio Recording",
		"hosting application",
		"completely quit and restart",
		"macOS Sequoia",
		"monthly re-confirmation",
	} {
		if strings.Contains(result.Error, forbidden) {
			t.Errorf("screen timeout denial error %q contains operator-only text %q", result.Error, forbidden)
		}
	}
	if result.Error != screenSightUnavailable {
		t.Fatalf("screen timeout denial error = %q, want concise customer-safe message", result.Error)
	}
	if executor.recheckCount() != 1 {
		t.Fatalf("screen permission rechecks = %d, want exactly one", executor.recheckCount())
	}
	select {
	case <-executor.exited:
	case <-time.After(time.Second):
		t.Fatal("timed-out screen worker did not exit")
	}
}

type screenRecheckCase struct {
	name       string
	permission cliTools.DisplayPermission
	recheckErr error
	wait       bool
	callName   string
}

func TestSessionToolExecutor_ScreenTimeoutRecheckPreservesTimeoutForNonDenial(t *testing.T) {
	cases := []screenRecheckCase{
		{name: "granted", permission: cliTools.DisplayPermission{State: cliTools.DisplayPermissionGranted}},
		{name: "unavailable", permission: cliTools.DisplayPermission{State: cliTools.DisplayPermissionUnavailable}},
		{name: "failed", recheckErr: errors.New("permission service failed")},
		{name: "unrelated tool", permission: cliTools.DisplayPermission{State: cliTools.DisplayPermissionDenied}, callName: "show_page"},
		{name: "bounded inconclusive", wait: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runScreenRecheckCase(t, tc) })
	}
}

// runScreenRecheckCase times out one call and asserts that an inconclusive or
// unrelated permission re-check keeps the ordinary timeout presentation.
func runScreenRecheckCase(t *testing.T, tc screenRecheckCase) {
	t.Helper()
	executor := newTimeoutScreenPermissionExecutor(tc.permission)
	executor.recheckErr = tc.recheckErr
	if tc.wait {
		unblock := make(chan struct{})
		executor.recheckWait = unblock
		defer close(unblock)
	}
	name := tc.callName
	if name == "" {
		name = cliTools.ScreenToolID
	}
	call := messages.ToolCall{ID: "screen-timeout-" + tc.name, Name: name, Arguments: `{}`}
	startedAt := time.Now()
	response, err := newTestSessionToolExecutor(executor, 15*time.Millisecond).Execute(context.Background(), call)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if response.ToolCallID != call.ID || response.Name != call.Name || len(response.ContentParts) != 0 {
		t.Fatalf("timeout response = %#v, want correlated text-only result", response)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("timeout/recheck took %s, want bounded completion", elapsed)
	}
	wantRechecks := 1
	if tc.callName != "" {
		wantRechecks = 0
		assertGenericToolTimeout(t, response.Content)
	} else {
		assertSafeScreenTimeout(t, response.Content)
	}
	if got := executor.recheckCount(); got != wantRechecks {
		t.Fatalf("permission rechecks = %d, want %d", got, wantRechecks)
	}
	select {
	case <-executor.exited:
	case <-time.After(time.Second):
		t.Fatal("timed-out worker did not exit")
	}
}

func assertGenericToolTimeout(t *testing.T, content string) {
	t.Helper()
	if !strings.Contains(content, "classification="+sessionturn.ToolTimeoutClassification) || !strings.Contains(content, "tool execution timed out") {
		t.Fatalf("unrelated timeout result = %q, want unchanged generic timeout failure", content)
	}
}

func assertSafeScreenTimeout(t *testing.T, content string) {
	t.Helper()
	result, err := sight.Decode([]byte(content))
	if err != nil {
		t.Fatalf("decode timeout result: %v", err)
	}
	if result.ErrorCode == cliTools.ScreenRecordingPermissionDeniedErrorCode || result.Error != screenSightUnavailable {
		t.Fatalf("timeout result = %+v, want safe non-denial timeout failure", result)
	}
}

// screenSightUnavailable is the customer-safe display failure text.
const screenSightUnavailable = "Screen sight is unavailable."
