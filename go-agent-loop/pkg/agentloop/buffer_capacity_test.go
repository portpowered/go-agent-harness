package agentloop

import (
	"testing"
)

func TestNew_DefaultBufferCapacity(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	outputs := loop.engine.State().LoopState.Outputs
	if got := outputs.ModelInbox.Cap(); got != 64 {
		t.Errorf("ModelInbox capacity: got %d, want 64", got)
	}
	if got := outputs.ToolInbox.Cap(); got != 64 {
		t.Errorf("ToolInbox capacity: got %d, want 64", got)
	}
	if got := outputs.UserInbox.Cap(); got != 64 {
		t.Errorf("UserInbox capacity: got %d, want 64", got)
	}
	if got := outputs.KernelDeltaInbox.Cap(); got != 64 {
		t.Errorf("KernelDeltaInbox capacity: got %d, want 64", got)
	}
}

func TestNew_GlobalBufferCapacityOverride(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(WithInferencer(inf), WithBufferCapacity(128))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	outputs := loop.engine.State().LoopState.Outputs
	if got := outputs.ModelInbox.Cap(); got != 128 {
		t.Errorf("ModelInbox capacity: got %d, want 128", got)
	}
	if got := outputs.ToolInbox.Cap(); got != 128 {
		t.Errorf("ToolInbox capacity: got %d, want 128", got)
	}
	if got := outputs.UserInbox.Cap(); got != 128 {
		t.Errorf("UserInbox capacity: got %d, want 128", got)
	}
	if got := outputs.KernelDeltaInbox.Cap(); got != 128 {
		t.Errorf("KernelDeltaInbox capacity: got %d, want 128", got)
	}
}

func TestNew_PerParticipantBufferCapacity(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(
		WithInferencer(inf),
		WithModelBufferCapacity(256),
		WithToolBufferCapacity(32),
		WithUserBufferCapacity(16),
		WithKernelBufferCapacity(512),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	outputs := loop.engine.State().LoopState.Outputs
	if got := outputs.ModelInbox.Cap(); got != 256 {
		t.Errorf("ModelInbox capacity: got %d, want 256", got)
	}
	if got := outputs.ToolInbox.Cap(); got != 32 {
		t.Errorf("ToolInbox capacity: got %d, want 32", got)
	}
	if got := outputs.UserInbox.Cap(); got != 16 {
		t.Errorf("UserInbox capacity: got %d, want 16", got)
	}
	if got := outputs.KernelDeltaInbox.Cap(); got != 512 {
		t.Errorf("KernelDeltaInbox capacity: got %d, want 512", got)
	}
}

func TestNew_PerParticipantOverridesGlobal(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(
		WithInferencer(inf),
		WithBufferCapacity(128),
		WithModelBufferCapacity(256),
		// Tool, User, Kernel not overridden — should use global 128
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	outputs := loop.engine.State().LoopState.Outputs
	if got := outputs.ModelInbox.Cap(); got != 256 {
		t.Errorf("ModelInbox capacity: got %d, want 256 (per-participant override)", got)
	}
	if got := outputs.ToolInbox.Cap(); got != 128 {
		t.Errorf("ToolInbox capacity: got %d, want 128 (global fallback)", got)
	}
	if got := outputs.UserInbox.Cap(); got != 128 {
		t.Errorf("UserInbox capacity: got %d, want 128 (global fallback)", got)
	}
	if got := outputs.KernelDeltaInbox.Cap(); got != 128 {
		t.Errorf("KernelDeltaInbox capacity: got %d, want 128 (global fallback)", got)
	}
}

func TestNew_UserRunnerOutboxCapacity(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(
		WithInferencer(inf),
		WithUserBufferCapacity(32),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	// UserRunner is accessible via GetUserRunner; verify both Inbox and Outbox.
	userRunner := loop.engine.GetUserRunner()
	if got := userRunner.Inbox.Cap(); got != 32 {
		t.Errorf("UserRunner.Inbox capacity: got %d, want 32", got)
	}
	if got := userRunner.Outbox.Cap(); got != 32 {
		t.Errorf("UserRunner.Outbox capacity: got %d, want 32", got)
	}
}

func TestNew_KernelRunnerDeltaInboxCapacity(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(
		WithInferencer(inf),
		WithKernelBufferCapacity(128),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	kernelRunner := loop.engine.GetKernelRunner()
	if got := kernelRunner.DeltaInbox.Cap(); got != 128 {
		t.Errorf("KernelRunner.DeltaInbox capacity: got %d, want 128", got)
	}
}
