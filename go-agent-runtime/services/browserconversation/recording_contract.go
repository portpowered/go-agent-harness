package browserconversation

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

type RecordingRequest struct {
	Watch             func(context.Context) <-chan BrowserEvent
	IncludeArguments  bool
	IncludeResults    bool
	RedactURLQuery    bool
	RedactURLFragment bool
	Credentials       []string
	MaxEvents         int
	MaxBytes          int
	Now               func() time.Time
}

type RecordedBrowserEvent struct {
	Sequence           uint64    `json:"sequence"`
	At                 time.Time `json:"at,omitempty"`
	Type               string    `json:"type"`
	BrowserID          string    `json:"browser_id,omitempty"`
	TargetID           string    `json:"target_id,omitempty"`
	Generation         uint64    `json:"generation,omitempty"`
	PreviousGeneration uint64    `json:"previous_generation,omitempty"`
	InvocationID       string    `json:"invocation_id,omitempty"`
	FrameID            string    `json:"frame_id,omitempty"`
	ToolName           string    `json:"tool_name,omitempty"`
	State              string    `json:"state,omitempty"`
	Status             string    `json:"status,omitempty"`
	ErrorCode          string    `json:"error_code,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	Input              any       `json:"input,omitempty"`
	Output             any       `json:"output,omitempty"`
	Tools              []string  `json:"tools,omitempty"`
	RemovedToolNames   []string  `json:"removed_tools,omitempty"`
}

type RecordingSnapshot struct {
	Events   []RecordedBrowserEvent      `json:"events,omitempty"`
	Artifact *transcript.BrowserArtifact `json:"-"`
}

type Recorder interface {
	Start(context.Context)
	Close() error
	Snapshot() (RecordingSnapshot, error)
}
