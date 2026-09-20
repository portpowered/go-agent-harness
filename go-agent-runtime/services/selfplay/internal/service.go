package internal

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const (
	maxPCMBytes        = 32 << 20
	maxDiagnosticBytes = 4 << 20
	maxStreamBytes     = 32 << 20
	shutdownDeadline   = 5 * time.Second
)

var _ selfplay.Service = (*Service)(nil)

// Service owns one run's providers, mutable side state, PCM bridges, stop
// arbitration, and evidence lifecycle. Construction only stores dependencies.
type Service struct {
	sessions providers.SessionService
	catalog  providers.ModelCatalog
	clock    platformclock.Source
	files    fileSystem
}

func NewService(sessions providers.SessionService, catalog providers.ModelCatalog, source platformclock.Source) *Service {
	return &Service{sessions: sessions, catalog: catalog, clock: source, files: osFileSystem{}}
}

func (s *Service) Run(ctx context.Context, request selfplay.Request) (selfplay.Result, error) {
	plan, err := s.prepareRun(ctx, request)
	if err != nil {
		return selfplay.Result{}, err
	}
	return s.executeRun(ctx, plan)
}

type runPlan struct {
	request   selfplay.Request
	source    platformclock.TimerSource
	startedAt time.Time
}

func (s *Service) prepareRun(ctx context.Context, request selfplay.Request) (runPlan, error) {
	if err := validateRunContext(s, ctx); err != nil {
		return runPlan{}, err
	}
	normalized, err := normalizeRequest(request)
	if err != nil {
		return runPlan{}, err
	}
	if err := validateRunModel(s.catalog, normalized); err != nil {
		return runPlan{}, err
	}
	timerSource, err := platformclock.RequireTimerSource(s.clock)
	if err != nil {
		return runPlan{}, fmt.Errorf("self-play clock: %w", err)
	}
	if err := validateOutputTarget(s.files, normalized.OutputDir); err != nil {
		return runPlan{}, err
	}
	return runPlan{request: normalized, source: timerSource, startedAt: timerSource.Now().UTC()}, nil
}

func validateRunContext(service *Service, ctx context.Context) error {
	if service == nil || service.sessions == nil {
		return providersRequiredError()
	}
	if ctx == nil {
		return fmt.Errorf("%w: context is required", selfplay.ErrInvalidRequest)
	}
	return ctx.Err()
}

func validateRunModel(catalog providers.ModelCatalog, request selfplay.Request) error {
	if catalog == nil {
		return selfplay.ErrModelCatalogRequired
	}
	model, ok := catalog.LookupRealtimeModel(request.Provider, request.Model)
	if ok && model.SupportsAudio {
		return nil
	}
	return &selfplay.UnsupportedModelError{Provider: request.Provider, Model: request.Model}
}

func (s *Service) executeRun(ctx context.Context, plan runPlan) (selfplay.Result, error) {
	runCtx, cancelRun, err := platformclock.WithTimeout(ctx, plan.source, plan.request.MaxDuration)
	if err != nil {
		return selfplay.Result{}, fmt.Errorf("self-play duration clock: %w", err)
	}
	defer cancelRun()
	evidence, err := newEvidence(s.files, plan.request, plan.startedAt)
	if err != nil {
		return selfplay.Result{}, err
	}
	result, runErr := s.executeSessions(runCtx, ctx, cancelRun, plan, evidence)
	return finishRun(result, runErr, plan.source, plan.startedAt, plan.request.APIKey, evidence)
}

func (s *Service) executeSessions(runCtx, callerCtx context.Context, cancel context.CancelFunc, plan runPlan, evidence *evidence) (selfplay.Result, error) {
	customer, err := s.buildSession(runCtx, plan.request, selfplay.SelfPlayCustomerPersona)
	if err != nil {
		return failedSessionResult(runCtx, callerCtx, err)
	}
	assistant, err := s.buildSession(runCtx, plan.request, selfplay.SelfPlayAssistantPersona)
	if err != nil {
		return failedSessionResult(runCtx, callerCtx, err)
	}
	return s.runConversation(runCtx, callerCtx, cancel, plan.source, plan.request, customer, assistant, evidence)
}

func failedSessionResult(runCtx, callerCtx context.Context, err error) (selfplay.Result, error) {
	result := selfplay.Result{StopReason: selfplay.StopFailure}
	if errors.Is(err, context.DeadlineExceeded) && runCtx.Err() != nil && callerCtx.Err() == nil {
		result.StopReason = selfplay.StopMaxDuration
		return result, nil
	}
	if callerCtx.Err() != nil {
		err = errors.Join(err, callerCtx.Err())
	}
	return result, err
}

func finishRun(result selfplay.Result, runErr error, source platformclock.TimerSource, startedAt time.Time, secret string, evidence *evidence) (selfplay.Result, error) {
	endedAt := source.Now().UTC()
	result.StartedAt = startedAt
	result.EndedAt = endedAt
	result.Elapsed = endedAt.Sub(startedAt)
	if runErr != nil {
		result = withFailure(result)
	}
	if err := evidence.finalize(&result, runErr, secret); err != nil {
		runErr = errors.Join(runErr, err)
		result = withFailure(result)
	}
	return result, redactReturnedError(runErr, secret)
}

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }

func redactReturnedError(err error, secret string) error {
	if err == nil {
		return nil
	}
	return &redactedError{message: redactError(err.Error(), secret), cause: err}
}

func providersRequiredError() error {
	return selfplay.ErrSessionServiceRequired
}

func normalizeRequest(request selfplay.Request) (selfplay.Request, error) {
	request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
	if request.Provider == "" {
		request.Provider = selfplay.SelfPlayDefaultProvider
	}
	if request.Provider != selfplay.SelfPlayDefaultProvider {
		return selfplay.Request{}, fmt.Errorf("%w: Phase 1 supports %q only; got %q", selfplay.ErrUnsupportedProvider, selfplay.SelfPlayDefaultProvider, request.Provider)
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		request.Model = selfplay.SelfPlayDefaultModel
	}
	request.APIKey = strings.TrimSpace(request.APIKey)
	request.BaseURL = strings.TrimSpace(request.BaseURL)
	request.OutputDir = strings.TrimSpace(request.OutputDir)
	if request.OutputDir == "" {
		return selfplay.Request{}, fmt.Errorf("%w: output directory is required", selfplay.ErrInvalidRequest)
	}
	request.OutputDir = filepath.Clean(request.OutputDir)
	if request.MaxDuration == 0 {
		request.MaxDuration = selfplay.SelfPlayDefaultMaxDuration
	}
	if request.MaxDuration < 0 {
		return selfplay.Request{}, fmt.Errorf("%w: maximum duration must be positive", selfplay.ErrInvalidRequest)
	}
	if request.MaxTurns == 0 {
		request.MaxTurns = selfplay.SelfPlayDefaultTurnTarget
	}
	if request.MaxTurns < 0 {
		return selfplay.Request{}, fmt.Errorf("%w: maximum turns must be positive", selfplay.ErrInvalidRequest)
	}
	return request, nil
}

func (s *Service) buildSession(ctx context.Context, request selfplay.Request, persona string) (messages.SessionInferencer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inferencer, err := s.sessions.BuildSession(ctx, providers.SessionConfig{
		Provider:              request.Provider,
		Model:                 request.Model,
		APIKey:                request.APIKey,
		BaseURL:               request.BaseURL,
		Instructions:          persona,
		InputAudioFormat:      models.AudioFormatPCM16,
		OutputAudioFormat:     models.AudioFormatPCM16,
		InputAudioSampleRate:  models.SampleRate24000,
		OutputAudioSampleRate: models.SampleRate24000,
	})
	if err != nil {
		return nil, fmt.Errorf("construct %s live session: %w", personaName(persona), err)
	}
	if inferencer == nil {
		return nil, fmt.Errorf("construct %s live session: provider returned a nil inferencer", personaName(persona))
	}
	return inferencer, nil
}

func personaName(persona string) string {
	if persona == selfplay.SelfPlayCustomerPersona {
		return "customer"
	}
	return "assistant"
}

func withFailure(result selfplay.Result) selfplay.Result {
	if result.StopReason == "" {
		result.StopReason = selfplay.StopFailure
	}
	return result
}
