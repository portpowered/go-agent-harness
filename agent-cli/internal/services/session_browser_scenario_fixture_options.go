package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// BrowserConversationFixtureOption configures a run-scoped declarative
// browser fixture. It never changes the production browser discovery path.
type BrowserConversationFixtureOption func(*browserConversationFixtureConfig)

type browserConversationFixtureConfig struct {
	runtimeOptions []testkit.FixtureRuntimeOption
	brokerOptions  webmcp.BrokerOptions
}

// WithBrowserConversationFixtureRuntimeOption adds one testkit runtime seam.
func WithBrowserConversationFixtureRuntimeOption(option testkit.FixtureRuntimeOption) BrowserConversationFixtureOption {
	return func(config *browserConversationFixtureConfig) {
		if option != nil {
			config.runtimeOptions = append(config.runtimeOptions, option)
		}
	}
}

// WithBrowserConversationFixtureRuntimeOptions adds testkit runtime seams.
func WithBrowserConversationFixtureRuntimeOptions(options ...testkit.FixtureRuntimeOption) BrowserConversationFixtureOption {
	return func(config *browserConversationFixtureConfig) {
		for _, option := range options {
			if option != nil {
				config.runtimeOptions = append(config.runtimeOptions, option)
			}
		}
	}
}

// WithBrowserConversationFixtureBrokerOption adds one broker seam.
func WithBrowserConversationFixtureBrokerOption(option webmcp.BrokerOptions) BrowserConversationFixtureOption {
	return func(config *browserConversationFixtureConfig) {
		config.brokerOptions = option
	}
}

// WithBrowserConversationFixtureBrokerOptions is the plural spelling retained
// for callers that construct option lists programmatically.
func WithBrowserConversationFixtureBrokerOptions(options webmcp.BrokerOptions) BrowserConversationFixtureOption {
	return WithBrowserConversationFixtureBrokerOption(options)
}

// BrowserConversationOracleReader reads state without consulting assistant
// prose or the broker result envelope.
type BrowserConversationOracleReader interface {
	ReadBrowserConversationState(context.Context, string) (json.RawMessage, error)
}

// BrowserConversationOracleFunc adapts a state reader function.
type BrowserConversationOracleFunc func(context.Context, string) (json.RawMessage, error)

func (f BrowserConversationOracleFunc) ReadBrowserConversationState(ctx context.Context, pageID string) (json.RawMessage, error) {
	if f == nil {
		return nil, errors.New("browser conversation oracle function is nil")
	}
	return f(ctx, pageID)
}

// BrowserConversationTabStateProbeResult is the independent post-session tab
// health result. It is separate from assistant language and page-tool output.
type BrowserConversationTabStateProbeResult struct {
	PageID         string
	Alive          bool
	Responsive     bool
	AllowsMutation bool
}

// BrowserConversationTabStateProbe is an injectable post-session health seam.
type BrowserConversationTabStateProbe func(context.Context, *BrowserConversationFixtureRun, string) (BrowserConversationTabStateProbeResult, error)

// BrowserConversationPostSessionProbe is a descriptive alias.
type BrowserConversationPostSessionProbe = BrowserConversationTabStateProbe

// BrowserConversationFixtureFactory starts one run-scoped fixture boundary.
type BrowserConversationFixtureFactory func(context.Context, BrowserConversationScenario) (*BrowserConversationFixtureRun, error)

// BrowserConversationSessionRequest is the exact composition handed to the
// shared session runner. Audio, tools, and stream observation all use the
// existing service seams.
type BrowserConversationSessionRequest struct {
	Scenario        BrowserConversationScenario
	Fixture         *BrowserConversationFixtureRun
	Broker          webmcp.Broker
	ToolExecutor    messages.ToolExecutor
	ToolDefinitions []messages.ToolDefinition
	AudioInputs     []ScheduledAudioInput
	SessionOptions  SessionRunOptions
	StreamObserver  SessionStreamObserver
}

// BrowserConversationSessionRunner executes one shared duplex session.
type BrowserConversationSessionRunner func(context.Context, io.Writer, BrowserConversationSessionRequest) error

// BrowserconversationRunOptions configures one hermetic run.
type BrowserConversationRunOptions struct {
	Scenario         BrowserConversationScenario
	AudioByStep      map[string][]byte
	FixtureScript    testkit.BrowserScript
	FixtureOptions   []BrowserConversationFixtureOption
	FixtureFactory   BrowserConversationFixtureFactory
	SessionOptions   SessionRunOptions
	SessionRunner    BrowserConversationSessionRunner
	Oracle           BrowserConversationOracleReader
	PostSessionProbe BrowserConversationTabStateProbe
	Validator        BrowserConversationValidator
	Output           io.Writer
}
