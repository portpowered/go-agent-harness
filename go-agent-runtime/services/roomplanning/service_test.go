package roomplanning_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
)

func TestPublicErrorCodesRemainComparable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "filesystem", err: roomplanning.ErrFilesystemScope, want: "room planning filesystem scope is unavailable"},
		{name: "session", err: roomplanning.ErrSessionFactory, want: "room planning session factory is unavailable"},
		{name: "replay", err: roomplanning.ErrReplayPlanner, want: "room planning replay planner is unavailable"},
		{name: "tools", err: roomplanning.ErrParticipantTools, want: "room participant tools are unavailable"},
		{name: "tool match", err: roomplanning.ErrParticipantToolMatch, want: "room participant tool capabilities do not match the manifest"},
		{name: "browser", err: roomplanning.ErrParticipantBrowser, want: "room participant browser capabilities are unavailable"},
		{name: "browser contract", err: roomplanning.ErrBrowserCapability, want: "room participant browser capabilities do not match the contract"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.err.Error() != test.want {
				t.Fatalf("error text = %q, want %q", test.err, test.want)
			}
			wrapped := fmt.Errorf("wrapped: %w", test.err)
			if !errors.Is(wrapped, test.err) {
				t.Fatalf("errors.Is(%q, %q) = false", wrapped, test.err)
			}
		})
	}
}

type serviceStub struct{}

func (serviceStub) Plan(_ context.Context, _ roomplanning.Options) (roomplanning.PlanResult, error) {
	return roomplanning.PlanResult{}, nil
}

func (serviceStub) Await(context.Context, roomplanning.AwaitOptions) error { return nil }

var _ roomplanning.Service = serviceStub{}
