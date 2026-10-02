package tools

import (
	"context"
	"testing"
)

func TestResolverFuncResolvesThroughTheFunction(t *testing.T) {
	resolver := ResolverFunc(func(context.Context, Request) (Capability, error) {
		return Capability{WorkspaceDir: "/work"}, nil
	})
	capability, err := resolver.Resolve(t.Context(), Request{})
	if err != nil || capability.WorkspaceDir != "/work" {
		t.Fatalf("Resolve() = %#v, %v", capability, err)
	}
	if _, err := ResolverFunc(nil).Resolve(t.Context(), Request{}); err == nil {
		t.Fatal("nil ResolverFunc resolved without error")
	}
	if summary, err := resolver.BuildSkillsSummary(t.Context(), SkillSummaryRequest{}); err == nil || summary != "" {
		t.Fatalf("BuildSkillsSummary() = %q, %v, want an unavailable error", summary, err)
	}
}
