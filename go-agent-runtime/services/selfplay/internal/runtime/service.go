package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Service owns self-play policy and lifecycle. It does not know which host
// supplies the two provider sessions or how a host executes one session.
type Service struct {
	deps selfplay.Dependencies
}

func New(deps selfplay.Dependencies) *Service { return &Service{deps: deps} }

func (s *Service) Run(ctx context.Context, out io.Writer, options selfplay.RunOptions) error {
	_, err := s.RunWithResult(ctx, out, options)
	return err
}

func (s *Service) RunWithResult(ctx context.Context, out io.Writer, options selfplay.RunOptions) (selfplay.Result, error) {
	if s == nil {
		return failureResult(), errors.New("self-play service is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}
	normalized, err := normalize(options, s.deps)
	if err != nil {
		return failureResult(), err
	}
	if s.deps.Sessions == nil {
		return failureResult(), errors.New("self-play session factory is required")
	}
	if s.deps.Runner == nil {
		return failureResult(), errors.New("self-play session runner is required")
	}

	base := selfplay.SessionRequest{
		APIKey: normalized.APIKey, Provider: normalized.Provider, Model: normalized.Model,
		BaseURL: normalized.BaseURL, ConfigDir: normalized.ConfigDir, Clock: s.deps.Clock,
	}
	customerRequest := base
	customerRequest.Prompt = selfplay.SelfPlayOpeningSeed
	customerRequest.Persona = selfplay.SelfPlayCustomerPersona
	customer, err := s.deps.Sessions.NewSession(ctx, customerRequest)
	if err != nil {
		return failureResult(), closeAfterConstruction(customer, fmt.Errorf("construct customer live session: %w", err))
	}
	if customer == nil {
		return failureResult(), errors.New("self-play session factory returned a nil customer inferencer")
	}
	assistantRequest := base
	assistantRequest.Persona = selfplay.SelfPlayAssistantPersona
	assistant, err := s.deps.Sessions.NewSession(ctx, assistantRequest)
	if err != nil {
		return failureResult(), closeAfterConstructionPair(customer, assistant, fmt.Errorf("construct assistant live session: %w", err))
	}
	if assistant == nil {
		return failureResult(), closeAfterConstruction(customer, errors.New("self-play session factory returned a nil assistant inferencer"))
	}

	if err := os.MkdirAll(normalized.OutputDir, 0o700); err != nil {
		return failureResult(), closeAfterConstructionPair(customer, assistant, fmt.Errorf("create self-play output directory %q: %w", normalized.OutputDir, err))
	}
	startedAt := now(s.deps.Clock)
	evidence, err := newEvidence(normalized.OutputDir, normalized, startedAt)
	if err != nil {
		return failureResult(), closeAfterConstructionPair(customer, assistant, err)
	}
	result, runErr := s.runConversation(ctx, normalized, customer, assistant, evidence, func() error {
		return errors.Join(closeSession(customer, "customer"), closeSession(assistant, "assistant"))
	})
	if _, writeErr := fmt.Fprintf(out, "self-play stopped: reason=%s customer_turns=%d assistant_turns=%d\n", result.StopReason, result.CustomerTurns, result.AssistantTurns); writeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write self-play result: %w", writeErr))
	}
	return result, runErr
}

func failureResult() selfplay.Result { return selfplay.Result{StopReason: selfplay.StopFailure} }

func normalize(options selfplay.RunOptions, deps selfplay.Dependencies) (selfplay.RunOptions, error) {
	options.Provider = strings.ToLower(strings.TrimSpace(options.Provider))
	if options.Provider == "" {
		options.Provider = selfplay.SelfPlayDefaultProvider
	}
	if options.Provider != selfplay.SelfPlayDefaultProvider {
		return selfplay.RunOptions{}, fmt.Errorf("self-play supports provider %q only; got %q", selfplay.SelfPlayDefaultProvider, options.Provider)
	}
	options.Model = strings.TrimSpace(options.Model)
	if options.Model == "" {
		options.Model = selfplay.SelfPlayDefaultModel
	}
	if deps.ModelAdmission == nil {
		return selfplay.RunOptions{}, errors.New("self-play model admission is required")
	}
	if err := deps.ModelAdmission.ValidateSessionModel(options.Provider, options.Model); err != nil {
		return selfplay.RunOptions{}, fmt.Errorf("self-play model admission: %w", err)
	}
	if options.MaxDuration <= 0 {
		return selfplay.RunOptions{}, fmt.Errorf("self-play max duration must be positive, got %s", options.MaxDuration)
	}
	if options.MaxTurns <= 0 {
		return selfplay.RunOptions{}, fmt.Errorf("self-play max turns must be positive, got %d", options.MaxTurns)
	}
	options.OutputDir = strings.TrimSpace(options.OutputDir)
	if options.OutputDir == "" {
		return selfplay.RunOptions{}, errors.New("self-play output directory is required")
	}
	options.OutputDir = filepath.Clean(options.OutputDir)
	if err := validateOutputTarget(options.OutputDir); err != nil {
		return selfplay.RunOptions{}, err
	}
	if _, err := clock.RequireTimerSource(deps.Clock); err != nil {
		return selfplay.RunOptions{}, fmt.Errorf("self-play clock: %w", err)
	}
	return options, nil
}

func validateOutputTarget(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("prepare self-play output parent %q: %w", destination, err)
	}
	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("self-play output target %q must be a non-symlink directory", destination)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return fmt.Errorf("inspect self-play output directory %q: %w", destination, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("self-play output directory %q is not safe: it must be empty", destination)
		}
		parent = destination
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect self-play output target %q: %w", destination, err)
	}
	probe, err := os.CreateTemp(parent, ".self-play-probe-")
	if err != nil {
		return fmt.Errorf("probe self-play output target %q: %w", destination, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return fmt.Errorf("close self-play output probe %q: %w", destination, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove self-play output probe %q: %w", destination, removeErr)
	}
	return nil
}

func now(source clock.Source) time.Time { return source.Now().UTC() }

func newTimer(source clock.Source, duration time.Duration) (clock.Timer, error) {
	timerSource, err := clock.RequireTimerSource(source)
	if err != nil {
		return nil, err
	}
	timer := timerSource.NewTimer(duration)
	if timer == nil {
		return nil, errors.New("self-play clock returned a nil timer")
	}
	return timer, nil
}

func closeAfterConstruction(session messages.SessionInferencer, err error) error {
	if closer, ok := session.(interface{ Close() error }); ok {
		return errors.Join(err, closer.Close())
	}
	return err
}

func closeAfterConstructionPair(customer, assistant messages.SessionInferencer, err error) error {
	err = closeAfterConstruction(customer, err)
	return closeAfterConstruction(assistant, err)
}

func closeSession(session messages.SessionInferencer, name string) error {
	closer, ok := session.(interface{ Close() error })
	if !ok {
		return nil
	}
	if err := closer.Close(); err != nil {
		return fmt.Errorf("close %s live session: %w", name, err)
	}
	return nil
}
