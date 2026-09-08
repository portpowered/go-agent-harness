package session

import (
	"context"
	"time"
)

// FileStoreOptions contains explicit host-resolved paths. Opening a store is
// inert; filesystem access begins when a storage operation is invoked.
type FileStoreOptions struct {
	Directory          string
	WorkspaceDirectory string
}

// FileStoreFactory constructs the built-in durable adapter for hosts that
// choose filesystem persistence. Other hosts can inject their own ports.
type FileStoreFactory interface {
	Open(FileStoreOptions) (ManagedStore, error)
}

// ManagedStore adds history administration to the invocation storage ports.
type ManagedStore interface {
	SessionStore
	TraceStore
	List(context.Context, SessionListOptions) ([]SessionInfo, error)
	Delete(context.Context, string) error
	ListTraces(context.Context) ([]TraceInfo, error)
}

type SessionListOptions struct {
	Limit  int
	Since  *time.Time
	Filter string
}

type SessionInfo struct {
	ID      string
	ModTime time.Time
}

type TraceInfo struct {
	ID      string
	ModTime time.Time
}

const (
	DefaultSessionListLimit = 100
	MaxSessionListLimit     = 1000
)
