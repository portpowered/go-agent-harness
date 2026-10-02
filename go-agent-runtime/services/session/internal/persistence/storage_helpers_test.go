package session

// NewStorage creates a Storage using the workspace directory (e.g. ~/.agent-cli).
// Sessions are stored in <workspace>/sessions/.
func NewStorage(workspaceDir string) *Storage {
	return NewStorageWithWorkspace(workspaceDir, workspaceDir)
}
