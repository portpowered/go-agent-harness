package execctx

import "context"

type contextKey string

const (
	keyWorkspaceDir contextKey = "workspace_dir"
	keyConfigDir    contextKey = "config_dir"
)

// WithWorkspaceDir returns a context with the workspace directory set.
func WithWorkspaceDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, keyWorkspaceDir, dir)
}

// WorkspaceDir returns the workspace directory from the context, or empty string.
func WorkspaceDir(ctx context.Context) string {
	v, _ := ctx.Value(keyWorkspaceDir).(string)
	return v
}

// WithConfigDir returns a context with the config directory set.
func WithConfigDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, keyConfigDir, dir)
}

// ConfigDir returns the config directory from the context, or empty string.
func ConfigDir(ctx context.Context) string {
	v, _ := ctx.Value(keyConfigDir).(string)
	return v
}
