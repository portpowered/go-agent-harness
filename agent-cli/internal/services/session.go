package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-llm-gateway/pkg/testing"
)

const (
	sessionProviderGrok             = config.ProviderGrok
	sessionProviderOpenAI           = config.ProviderOpenAI
	openAIRealtimeModel             = "gpt-realtime"
	openAIRealtimeBaseURL           = "wss://api.openai.com/v1/realtime"
	sessionClosedEventType          = "session.closed"
	sessionReplayDoneDrainIdleDelay = 25 * time.Millisecond
)

// SessionRunOptions contains the user-facing agent session command options.
type SessionRunOptions struct {
	RecordPath string
	ReplayPath string
	Provider   string
	Model      string
	APIKey     string
	BaseURL    string
	ConfigDir  string
	Prompt     string

	SessionInferencer messages.SessionInferencer
	WebSocketDialer   grok.WebSocketDialer
}

// RunSession validates and runs the session inference command surface.
func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) error {
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

func validateSessionRunOptions(opts SessionRunOptions) error {
	if opts.RecordPath == "" && opts.ReplayPath == "" {
		return fmt.Errorf("agent session requires --record <file>.json or --replay <file>.json")
	}
	if opts.RecordPath != "" && opts.ReplayPath != "" {
		return fmt.Errorf("agent session does not support --record and --replay together; choose one capture mode")
	}
	if opts.RecordPath != "" && !isJSONCapturePath(opts.RecordPath) {
		return fmt.Errorf("--record path %q must end with .json", opts.RecordPath)
	}
	if opts.ReplayPath != "" && !isJSONCapturePath(opts.ReplayPath) {
		return fmt.Errorf("--replay path %q must end with .json", opts.ReplayPath)
	}
	return nil
}

func isJSONCapturePath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

func replaySessionCapture(ctx context.Context, out io.Writer, path string) error {
	replayer, err := gwtesting.NewSessionReplayer(path, gwtesting.WithReplayOutboundValidation(false), gwtesting.WithReplayContext(ctx))
	if err != nil {
		return fmt.Errorf("replay session capture %s: %w", path, err)
	}
	for {
		select {
		case <-ctx.Done():
			_ = replayer.Close()
			return ctx.Err()
		case <-replayer.Done():
			return drainSessionReplayMessages(out, replayer)
		case msg, ok := <-replayer.Receive().Chan():
			if !ok {
				continue
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
		}
	}
}

func drainSessionReplayMessages(out io.Writer, replayer *gwtesting.SessionReplayer) error {
	for {
		msg, ok := replayer.Receive().Read()
		if !ok {
			return nil
		}
		if err := writeSessionReplayMessage(out, msg); err != nil {
			return err
		}
	}
}

func writeSessionReplayMessage(out io.Writer, msg messages.StreamMessage) error {
	switch v := msg.Value.(type) {
	case *messages.TextDeltaValue:
		_, err := fmt.Fprint(out, v.Content)
		return err
	case *messages.SessionCloseValue:
		if v.Reason != "" {
			_, err := fmt.Fprintf(out, "\n[session closed: %s]\n", v.Reason)
			return err
		}
	case *messages.ErrorValue:
		if v.Message != "" {
			return fmt.Errorf("session error: %s", v.Message)
		}
		return fmt.Errorf("session error")
	}
	return nil
}

type sessionLoopOptions struct {
	Prompt         string
	CloseAfterOpen bool
	WaitForClose   bool
	MaxDuration    time.Duration
	Done           <-chan struct{}
	DoneErr        func() error
}

func runAgentLoopSession(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	observedInferencer := newObservedSessionInferencer(sessionInferencer)
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(observedInferencer),
	)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- loop.Run(runCtx)
	}()
	timeout := make(<-chan time.Time)
	if opts.MaxDuration > 0 {
		timeout = time.After(opts.MaxDuration)
	}

	promptSent := false
	closeSent := false
	done := opts.Done
	for {
		select {
		case <-done:
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			if doneErr == nil {
				if drainErr := drainSessionLoopMessagesUntilIdle(out, loop, sessionReplayDoneDrainIdleDelay); drainErr != nil {
					return drainErr
				}
			}
			cancel()
			err := <-runErrCh
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				return drainErr
			}
			if doneErr != nil {
				return doneErr
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		case <-timeout:
			cancel()
			err := <-runErrCh
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				return drainErr
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			return nil
		case <-ctx.Done():
			cancel()
			err := <-runErrCh
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			return ctx.Err()
		case <-observedInferencer.Done():
			if drainErr := drainSessionLoopMessagesUntilQuiet(out, loop, 25*time.Millisecond); drainErr != nil {
				cancel()
				<-runErrCh
				return drainErr
			}
			cancel()
			err := <-runErrCh
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				return drainErr
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			return nil
		case err := <-runErrCh:
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				return drainErr
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			return nil
		case msg := <-loop.Deltas().Chan():
			if err := writeSessionReplayMessage(out, msg); err != nil {
				cancel()
				<-runErrCh
				return err
			}
			if msg.Type == messages.StreamTypeSessionOpen {
				if opts.Prompt != "" && !promptSent {
					promptSent = true
					userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
					if err := loop.Send(runCtx, []messages.Message{userMsg}); err != nil {
						cancel()
						<-runErrCh
						return fmt.Errorf("send session message: %w", err)
					}
				}
				if opts.CloseAfterOpen && opts.Prompt == "" && !closeSent {
					closeSent = true
					if err := sendSessionClose(runCtx, loop); err != nil {
						cancel()
						<-runErrCh
						return err
					}
				}
			}
			if opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !closeSent {
				closeSent = true
				if err := sendSessionClose(runCtx, loop); err != nil {
					cancel()
					<-runErrCh
					return err
				}
			}
			if shouldStopSessionLoop(msg, opts, closeSent) {
				cancel()
				err := <-runErrCh
				if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
					return drainErr
				}
				if err != nil && !errors.Is(err, context.Canceled) {
					return fmt.Errorf("session error: %w", err)
				}
				return nil
			}
		}
	}
}

type observedSessionInferencer struct {
	inner messages.SessionInferencer
	done  chan struct{}
	once  sync.Once
}

var _ messages.SessionInferencer = (*observedSessionInferencer)(nil)

func newObservedSessionInferencer(inner messages.SessionInferencer) *observedSessionInferencer {
	return &observedSessionInferencer{
		inner: inner,
		done:  make(chan struct{}),
	}
}

func (i *observedSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.closeDone()
		return nil, err
	}
	go func() {
		select {
		case <-session.Done():
			i.closeDone()
		case <-ctx.Done():
		}
	}()
	return &observedSession{Session: session, closeDone: i.closeDone}, nil
}

func (i *observedSessionInferencer) Done() <-chan struct{} {
	return i.done
}

func (i *observedSessionInferencer) closeDone() {
	i.once.Do(func() {
		close(i.done)
	})
}

type observedSession struct {
	messages.Session
	closeDone func()
	once      sync.Once
}

var _ messages.Session = (*observedSession)(nil)

func (s *observedSession) Close() error {
	err := s.Session.Close()
	s.markDone()
	return err
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}

func drainSessionLoopMessagesUntilIdle(out io.Writer, loop *agentloop.AgentLoop, idleDelay time.Duration) error {
	if idleDelay <= 0 {
		return drainSessionLoopMessages(out, loop)
	}

	idle := time.NewTimer(idleDelay)
	defer idle.Stop()
	for {
		select {
		case msg := <-loop.Deltas().Chan():
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(idleDelay)
		case <-idle.C:
			return nil
		}
	}
}

func shouldStopSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, closeSent bool) bool {
	if opts.CloseAfterOpen {
		return closeSent && msg.Type == messages.StreamTypeSessionClose
	}
	if opts.WaitForClose {
		return msg.Type == messages.StreamTypeSessionClose
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd, messages.StreamTypeTextEnd, messages.StreamTypeSessionClose:
		return true
	default:
		return false
	}
}

func drainSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if err := writeSessionReplayMessage(out, msg); err != nil {
			return err
		}
	}
}

func drainSessionLoopMessagesUntilQuiet(out io.Writer, loop *agentloop.AgentLoop, quiet time.Duration) error {
	timer := time.NewTimer(quiet)
	defer timer.Stop()

	for {
		select {
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return nil
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quiet)
		case <-timer.C:
			return nil
		}
	}
}

func sendSessionClose(ctx context.Context, loop *agentloop.AgentLoop) error {
	msg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}
	if err := loop.Send(ctx, []messages.Message{msg}); err != nil {
		return fmt.Errorf("close session loop: %w", err)
	}
	return nil
}

func wrapSessionPhaseError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", phase, err)
}

func grokReplayCaptureHasSessionClose(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "session.closed" {
			return true
		}
	}
	return false
}

func validateInjectedLiveSession(opts SessionRunOptions) error {
	switch strings.ToLower(effectiveSessionProvider(opts)) {
	case sessionProviderOpenAI:
		_, err := resolveOpenAIRealtimeSessionConfig(opts)
		return err
	case sessionProviderGrok:
		_, err := resolveGrokSessionConfig(opts)
		return err
	default:
		return fmt.Errorf("--record supports session providers %q and %q; got %q", sessionProviderGrok, sessionProviderOpenAI, effectiveSessionProvider(opts))
	}
}

func effectiveSessionProvider(opts SessionRunOptions) string {
	if strings.TrimSpace(opts.Provider) != "" {
		return opts.Provider
	}
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return ""
	}
	loadedCfg, err := storage.Load()
	if err != nil {
		return ""
	}
	return loadedCfg.Model.Provider
}

func usesWebSocketCapture(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.PayloadType == gwtesting.SessionPayloadTypeWebSocketMessage {
			return true
		}
	}
	return false
}

func usesOpenAIWebSocketCapture(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	return strings.EqualFold(capture.Provider.Name, sessionProviderOpenAI)
}

func captureHasEvent(path string, eventType string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.Type == eventType {
			return true
		}
	}
	return false
}

func resolveGrokSessionConfig(opts SessionRunOptions) (config.GrokConfig, error) {
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return config.GrokConfig{}, fmt.Errorf("failed to initialize config: %w", err)
	}

	loadedCfg, err := storage.Load()
	if err != nil {
		return config.GrokConfig{}, fmt.Errorf("failed to load config: %w", err)
	}
	if strings.TrimSpace(opts.Provider) == "" && !strings.EqualFold(loadedCfg.Model.Provider, sessionProviderGrok) {
		return config.GrokConfig{}, fmt.Errorf("--record requires --provider grok for live session inference")
	}

	effective := loadedCfg.ApplyOverrides(opts.APIKey, opts.Model, opts.Provider, opts.BaseURL)
	if strings.TrimSpace(effective.Model.Provider) == "" {
		return config.GrokConfig{}, fmt.Errorf("--record requires --provider grok for live session inference")
	}
	if !strings.EqualFold(effective.Model.Provider, sessionProviderGrok) {
		return config.GrokConfig{}, fmt.Errorf("--record supports provider %q only; got %q", sessionProviderGrok, effective.Model.Provider)
	}
	if err := effective.ValidateGrokSession(); err != nil {
		return config.GrokConfig{}, err
	}
	active, err := effective.ActiveGrokConfig()
	if err != nil {
		return config.GrokConfig{}, err
	}
	return *active, nil
}

func resolveOpenAIRealtimeSessionConfig(opts SessionRunOptions) (config.OpenAIConfig, error) {
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return config.OpenAIConfig{}, fmt.Errorf("failed to initialize config: %w", err)
	}

	loadedCfg, err := storage.Load()
	if err != nil {
		return config.OpenAIConfig{}, fmt.Errorf("failed to load config: %w", err)
	}
	if strings.TrimSpace(opts.Provider) == "" && !strings.EqualFold(loadedCfg.Model.Provider, sessionProviderOpenAI) {
		return config.OpenAIConfig{}, fmt.Errorf("--record requires --provider openai for OpenAI realtime session inference")
	}

	effective := loadedCfg.ApplyOverrides(opts.APIKey, opts.Model, opts.Provider, opts.BaseURL)
	if strings.TrimSpace(effective.Model.Provider) == "" {
		return config.OpenAIConfig{}, fmt.Errorf("--record requires --provider openai for OpenAI realtime session inference")
	}
	if !strings.EqualFold(effective.Model.Provider, sessionProviderOpenAI) {
		return config.OpenAIConfig{}, fmt.Errorf("--record supports provider %q only for OpenAI realtime sessions; got %q", sessionProviderOpenAI, effective.Model.Provider)
	}
	active, err := effective.ActiveOpenAIConfig()
	if err != nil {
		return config.OpenAIConfig{}, err
	}
	if strings.TrimSpace(active.APIKey) == "" {
		return config.OpenAIConfig{}, fmt.Errorf("OpenAI API key is required for live realtime session mode (set AGENT_MODEL__OPENAI__API_KEY, pass --api-key, or configure model.openai.api_key in %s)", config.ConfigFileName)
	}
	if strings.TrimSpace(active.Model) == "" {
		return config.OpenAIConfig{}, fmt.Errorf("OpenAI realtime model is required for live session mode (set AGENT_MODEL__OPENAI__MODEL, pass --model, or configure model.openai.model in %s)", config.ConfigFileName)
	}
	if !isOpenAIRealtimeModel(active.Model) {
		return config.OpenAIConfig{}, fmt.Errorf("OpenAI model %q is not realtime-capable for agent session; use %q or a supported realtime model alias", active.Model, openAIRealtimeModel)
	}
	return *active, nil
}

func isOpenAIRealtimeModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), openAIRealtimeModel)
}

// NewGrokSessionInferencer builds the session-capable Grok realtime inferencer.
func NewGrokSessionInferencer(sessionCfg config.GrokConfig) (messages.SessionInferencer, error) {
	return NewGrokSessionInferencerWithOptions(sessionCfg)
}

// NewGrokSessionInferencerWithOptions builds the session-capable Grok realtime inferencer.
func NewGrokSessionInferencerWithOptions(sessionCfg config.GrokConfig, opts ...grok.Option) (messages.SessionInferencer, error) {
	providerOpts := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey)}
	if strings.TrimSpace(sessionCfg.BaseURL) != "" {
		providerOpts = append(providerOpts, grok.WithBaseURL(sessionCfg.BaseURL))
	}
	providerOpts = append(providerOpts, opts...)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create Grok session gateway: %w", err)
	}
	return inference.NewSessionGatewayInferencer(
		sessionGateway,
		inference.WithSessionModel(sessionCfg.Model),
	), nil
}

// NewOpenAIRealtimeSessionInferencer builds the session-capable OpenAI realtime inferencer.
func NewOpenAIRealtimeSessionInferencer(sessionCfg config.OpenAIConfig) (messages.SessionInferencer, error) {
	return NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg)
}

// NewOpenAIRealtimeSessionInferencerWithOptions builds the OpenAI realtime inferencer.
func NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg config.OpenAIConfig, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
	if !isOpenAIRealtimeModel(sessionCfg.Model) {
		return nil, fmt.Errorf("OpenAI model %q is not realtime-capable for agent session; use %q or a supported realtime model alias", sessionCfg.Model, openAIRealtimeModel)
	}
	providerOpts := []oaiprovider.Option{
		oaiprovider.WithAPIKey(sessionCfg.APIKey),
		oaiprovider.WithModel(sessionCfg.Model),
		oaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(sessionCfg)),
	}
	providerOpts = append(providerOpts, opts...)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI realtime session gateway: %w", err)
	}
	return inference.NewSessionGatewayInferencer(
		sessionGateway,
		inference.WithSessionModel(sessionCfg.Model),
	), nil
}

func openAIRealtimeURL(sessionCfg config.OpenAIConfig) string {
	base := strings.TrimSpace(sessionCfg.BaseURL)
	if base == "" {
		base = openAIRealtimeBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	if query.Get("model") == "" {
		query.Set("model", sessionCfg.Model)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type openAIWebSocketDialerAdapter struct {
	inner grok.WebSocketDialer
}

var _ oaiprovider.WebSocketDialer = (*openAIWebSocketDialerAdapter)(nil)

func newOpenAIWebSocketDialerAdapter(inner grok.WebSocketDialer) *openAIWebSocketDialerAdapter {
	return &openAIWebSocketDialerAdapter{inner: inner}
}

func (d *openAIWebSocketDialerAdapter) Dial(url string, headers map[string]string) (oaiprovider.WebSocketConn, error) {
	conn, err := d.inner.Dial(url, headers)
	if err != nil {
		return nil, err
	}
	return &openAIWebSocketConnAdapter{inner: conn}, nil
}

type openAIWebSocketConnAdapter struct {
	inner grok.WebSocketConn
}

var _ oaiprovider.WebSocketConn = (*openAIWebSocketConnAdapter)(nil)

func (c *openAIWebSocketConnAdapter) ReadMessage() (int, []byte, error) {
	return c.inner.ReadMessage()
}

func (c *openAIWebSocketConnAdapter) WriteMessage(messageType int, data []byte) error {
	return c.inner.WriteMessage(messageType, data)
}

func (c *openAIWebSocketConnAdapter) Close() error {
	return c.inner.Close()
}
