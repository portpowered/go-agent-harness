package lifecycle

import duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"

const (
	MaxDurationReason     = duration.MaxDurationReason
	DefaultDrainPeriod    = duration.DefaultDrainPeriod
	DefaultCleanupTimeout = duration.DefaultCleanupTimeout
)

type Clock = duration.Clock
type Runner = duration.Runner
type Handle = duration.Handle
type ArtifactLifecycle = duration.ArtifactLifecycle
type MessageState = duration.MessageState
type MessageResult = duration.MessageResult
type MessageHandler = duration.MessageHandler
type RunRequest = duration.RunRequest
type Service = duration.Service

func New(source Clock) Service { return &service{clock: source} }

type service struct{ clock Clock }
