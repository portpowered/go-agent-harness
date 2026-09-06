package session

import "time"

// TraceStatus represents the lifecycle state of a loop trace.
type TraceStatus string

const (
	TraceStatusRunning     TraceStatus = "running"
	TraceStatusInterrupted TraceStatus = "interrupted"
	TraceStatusCompleted   TraceStatus = "completed"
)

// IterationStatus represents the lifecycle state of a single loop iteration.
type IterationStatus string

const (
	IterationStatusRunning     IterationStatus = "running"
	IterationStatusCompleted   IterationStatus = "completed"
	IterationStatusInterrupted IterationStatus = "interrupted"
	IterationStatusFailed      IterationStatus = "failed"
)

// TraceConfig holds the configuration of an iterative loop run.
type TraceConfig struct {
	MaxIterations int    `json:"maxIterations"`
	StopWord      string `json:"stopWord,omitempty"`
	Prompt        string `json:"prompt,omitempty"`
}

// IterationTrace holds the lineage data for one iteration of a loop run.
type IterationTrace struct {
	Iteration          int             `json:"iteration"`
	SessionID          string          `json:"sessionID"`
	SubAgentSessionIDs []string        `json:"subAgentSessionIDs"`
	Status             IterationStatus `json:"status"`
}

// TraceRecord is the durable handle for an entire IterativeLoop run.
// It is stored as trace-<id>.json in the sessions directory alongside
// session-<id>.json files.
type TraceRecord struct {
	TraceID          string           `json:"traceID"`
	Status           TraceStatus      `json:"status"`
	Config           TraceConfig      `json:"config"`
	CurrentIteration int              `json:"currentIteration"`
	Iterations       []IterationTrace `json:"iterations"`
}

// TraceInfo holds summary information for a listed trace.
type TraceInfo struct {
	ID      string
	ModTime time.Time
}
