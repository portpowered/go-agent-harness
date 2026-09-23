package browserconversation

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// BrowserCandidate is the host-neutral identity returned by discovery.
// Transport adapters own endpoints and protocol handles; the service only
// needs bounded identity and selection metadata.
type BrowserCandidate struct {
	ID string
}

type BrowserTarget struct {
	BrowserID string
	ID        string
}

type BrowserPageContext struct {
	BrowserID  string
	TargetID   string
	Generation uint64
	Connected  bool
}

type BrowserToolDescriptor struct {
	Ref         string
	Name        string
	InputSchema json.RawMessage
	FrameID     string
	Generation  uint64
}

type BrowserToolCatalog struct {
	Context    BrowserPageContext
	Generation uint64
	Tools      []BrowserToolDescriptor
}

type BrowserDiscoveryOptions struct {
	ExplicitOnly bool
}

type BrowserSelector struct{ BrowserID string }
type BrowserTargetSelector struct {
	BrowserID string
	TargetID  string
}
type BrowserListToolsOptions struct {
	Refresh        bool
	IncludeSchemas bool
}
type BrowserInvokeRequest struct {
	ToolRef     string
	Input       json.RawMessage
	Reason      string
	ModelCallID string
}
type BrowserInvokeResult struct {
	InvocationID string
	State        string
	Output       json.RawMessage
	ErrorCode    string
}
type BrowserCancelRequest struct {
	InvocationID string
	Reason       string
}

// BrowserEvent is a transport-neutral semantic browser observation. Input
// and output stay as copied JSON so recording policy, not the broker, decides
// whether they are retained.
type BrowserEvent struct {
	Type               string
	Sequence           uint64
	At                 time.Time
	BrowserID          string
	TargetID           string
	Generation         uint64
	PreviousGeneration uint64
	InvocationID       string
	ToolName           string
	FrameID            string
	State              string
	Status             string
	ErrorCode          string
	Reason             string
	Tools              []BrowserToolDescriptor
	RemovedToolNames   []string
	Input              json.RawMessage
	Output             json.RawMessage
	ToolCount          int
	ToolCountKnown     bool
	CatalogReady       bool
}

// Broker is the narrow browser capability consumed by the service. It is
// implemented by a host adapter and never exposes a CLI or protocol package
// through the reusable runtime.
type Broker interface {
	Discover(context.Context, BrowserDiscoveryOptions) ([]BrowserCandidate, error)
	ListTargets(context.Context, BrowserSelector) ([]BrowserTarget, error)
	Select(context.Context, BrowserTargetSelector) (BrowserPageContext, error)
	Selected(context.Context) (BrowserPageContext, error)
	ListTools(context.Context, BrowserListToolsOptions) (BrowserToolCatalog, error)
	Invoke(context.Context, BrowserInvokeRequest) (BrowserInvokeResult, error)
	Cancel(context.Context, BrowserCancelRequest) error
	Watch(context.Context) <-chan BrowserEvent
	Close() error
}

type InvocationWaiter interface {
	WaitInvocation(context.Context, string) (BrowserInvokeResult, error)
}

type BrowserEventWatcher interface {
	WatchBrowserEvents(context.Context) <-chan BrowserEvent
}

// Fixture owns one run-scoped declarative browser fixture. Its implementation
// is supplied by a host; the service owns lifecycle and never opens a browser
// while its constructor runs.
type Fixture interface {
	// Close must honor ctx so fixture shutdown cannot extend beyond the run's
	// bounded cleanup window.
	Close(context.Context) error
	Navigate(context.Context, BrowserCustomerNavigation) error
	ReadState(context.Context, string) (json.RawMessage, error)
	ProbeTab(context.Context, string) (BrowserConversationTabStateProbeResult, error)
}

type FixtureFactory func(context.Context, BrowserConversationScenario) (Fixture, error)

type BrowserConversationTabStateProbeResult struct {
	PageID            string
	BrowserID         string
	TargetID          string
	Alive             bool
	Responsive        bool
	AllowsMutation    bool
	ReadSucceeded     bool
	MutationSucceeded bool
}

type OracleReader interface {
	ReadState(context.Context, string) (json.RawMessage, error)
}

type CustomerNavigateFunc func(context.Context, Fixture, BrowserCustomerNavigation) error

type SessionRequest struct {
	Scenario           BrowserConversationScenario
	Fixture            Fixture
	Broker             Broker
	ToolExecutor       messages.ToolExecutor
	ToolDefinitions    []messages.ToolDefinition
	AudioInputs        []ScheduledAudioInput
	AudioInterruptions <-chan ScheduledAudioInput
	StreamObserver     func(messages.StreamMessage)
	CustomerNavigate   CustomerNavigateFunc
}

type SessionRunner func(context.Context, io.Writer, SessionRequest) error

// RunRequest is an effect boundary. All concrete browser, provider, fixture,
// and process choices are injected by the host or a test.
type RunRequest struct {
	Scenario         BrowserConversationScenario
	AudioByStep      map[string][]byte
	Broker           Broker
	FixtureFactory   FixtureFactory
	Fixture          Fixture
	Oracle           OracleReader
	PostSessionProbe func(context.Context, Fixture, string) (BrowserConversationTabStateProbeResult, error)
	SessionRunner    SessionRunner
	ToolExecutor     messages.ToolExecutor
	ToolDefinitions  []messages.ToolDefinition
	Validator        BrowserConversationValidator
	CustomerNavigate CustomerNavigateFunc
	Output           io.Writer
}
