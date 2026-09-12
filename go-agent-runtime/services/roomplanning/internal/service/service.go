package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type Service struct{}

func New() roomplanning.Service { return &Service{} }

func (s *Service) Await(ctx context.Context, options roomplanning.AwaitOptions) error {
	return roomplanning.Await(ctx, options)
}

func (s *Service) Plan(ctx context.Context, options roomplanning.Options) (roomplanning.PlanResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scope, err := resolveScope(options)
	if err != nil {
		return roomplanning.PlanResult{}, err
	}
	if options.ReplayPlan != nil {
		return s.planReplay(ctx, options)
	}
	if options.LookupCredential == nil {
		options.LookupCredential = func(string) (string, bool) { return "", false }
	}
	known := make(map[string]struct{}, len(options.Manifest.Participants))
	credentials := make(map[string]string, len(options.Manifest.Participants))
	for _, participant := range options.Manifest.Participants {
		known[participant.ID] = struct{}{}
		if normalizeKind(participant.Kind) == rooms.ParticipantKindHuman || participant.APIKeyEnv == "" {
			continue
		}
		if value, ok := options.LookupCredential(participant.APIKeyEnv); ok && value != "" {
			credentials[participant.ID] = value
		}
	}
	for id := range options.SessionInferencers {
		if _, ok := known[id]; !ok {
			return roomplanning.PlanResult{}, fmt.Errorf("room session inferencer provided for unknown participant %q", id)
		}
	}
	needsFactory := false
	for _, participant := range options.Manifest.Participants {
		if normalizeKind(participant.Kind) != rooms.ParticipantKindHuman {
			if _, injected := options.SessionInferencers[participant.ID]; !injected {
				needsFactory = true
				break
			}
		}
	}
	if options.SessionFactory == nil && needsFactory {
		return roomplanning.PlanResult{}, roomplanning.ErrSessionFactory
	}
	plans := make([]*roomplanning.ParticipantPlan, 0, len(options.Manifest.Participants))
	for _, participant := range options.Manifest.Participants {
		if err := ctx.Err(); err != nil {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(err, closeCapabilities(plans))
		}
		kind := normalizeKind(participant.Kind)
		if kind == rooms.ParticipantKindHuman {
			plans = append(plans, &roomplanning.ParticipantPlan{Participant: participant})
			continue
		}
		credential := credentials[participant.ID]
		sessionOptions := roomplanning.SessionOptions{
			Provider: participant.Provider, Model: participant.Model, ModelProvided: true,
			BaseURL: options.BaseURL, ConfigDir: options.ConfigDir,
			WorkDir: scope.PrimaryRoot, AllowPaths: append([]string(nil), scope.AdditionalRoots...), Filesystem: scope,
			Prompt: participant.OpeningPrompt, Voice: participant.Voice,
			WebSocketDialer: options.WebSocketDialer, WaitForClose: true,
		}
		plan := &roomplanning.ParticipantPlan{Participant: participant, Options: sessionOptions}
		markFailure := func(cause error) {
			if cause == nil {
				return
			}
			plan.Options = sessionOptions
			plan.StartupErr = participantError(options, participant.ID, cause)
		}
		static := roomplanning.ToolCapabilities{}
		if len(participant.Tools) > 0 {
			if options.ToolFactory == nil {
				markFailure(roomplanning.ErrParticipantTools)
				plans = append(plans, plan)
				continue
			}
			capabilities, factoryErr := options.ToolFactory(participant)
			if factoryErr != nil {
				markFailure(fmt.Errorf("configure participant tools: %w", factoryErr))
				plans = append(plans, plan)
				continue
			}
			if matchErr := validateTools(participant, capabilities); matchErr != nil {
				if errors.Is(matchErr, roomplanning.ErrParticipantToolMatch) {
					return roomplanning.PlanResult{Plans: plans}, errors.Join(fmt.Errorf("room participant %q capability contract: %w", participant.ID, matchErr), closeCapabilities(plans))
				}
				markFailure(matchErr)
				plans = append(plans, plan)
				continue
			}
			static = capabilities
			sessionOptions.ToolExecutor = static.Executor
			sessionOptions.ToolDefinitions = cloneDefinitions(static.Definitions)
		}
		if options.WebSocketDialerFactory != nil {
			sessionOptions.WebSocketDialer = options.WebSocketDialerFactory(participant)
		}
		if options.ResolveCapturePath != nil {
			if capture, ok := options.ResolveCapturePath(participant.ID); ok {
				// The capture path is carried by the host's session option adapter.
				// It is not inferred from a mutable evidence object in this package.
				sessionOptions.RecordSessionCapturePath = capture
			}
		}
		if participant.BrowserTools != nil {
			if options.BrowserFactory == nil {
				markFailure(roomplanning.ErrParticipantBrowser)
				plans = append(plans, plan)
				continue
			}
			browser, browserErr := options.BrowserFactory(participant, static)
			if browserErr != nil {
				markFailure(fmt.Errorf("configure browser tools: %w", browserErr))
				plans = append(plans, plan)
				continue
			}
			sessionOptions.CapabilityClose = browser.Close
			if capabilityErr := validateBrowser(browser); capabilityErr != nil {
				return roomplanning.PlanResult{Plans: plans}, errors.Join(fmt.Errorf("room participant %q browser capability contract: %w", participant.ID, capabilityErr), closeCapabilities(plans), closeCapability(browser.Close))
			}
			if browser.Initialize != nil {
				if initializeErr := browser.Initialize(ctx); initializeErr != nil {
					markFailure(fmt.Errorf("initialize browser tools: %w", initializeErr))
					plans = append(plans, plan)
					continue
				}
			}
			if browser.RefreshToolDefinitions != nil {
				refreshed, refreshErr := browser.RefreshToolDefinitions(ctx)
				if refreshErr == nil {
					browser.Definitions = cloneDefinitions(refreshed)
				} else if ctx.Err() != nil {
					markFailure(fmt.Errorf("refresh browser tools: %w", refreshErr))
					plans = append(plans, plan)
					continue
				}
			}
			sessionOptions.ToolExecutor = browser.Executor
			sessionOptions.ToolDefinitions = cloneDefinitions(browser.Definitions)
			sessionOptions.ToolDefinitionBase = cloneDefinitions(browser.ToolDefinitionBase)
			sessionOptions.RefreshTools = browser.RefreshToolDefinitions
			sessionOptions.BrowserWatch = browser.BrowserWatch
			sessionOptions.BrowserEventWatch = browser.BrowserEventWatch
			sessionOptions.BrowserTools = true
			sessionOptions.CapabilityClose = browser.Close
		}
		plan.Options = sessionOptions
		if inferencer, injected := options.SessionInferencers[participant.ID]; injected {
			if isNil(inferencer) {
				markFailure(errors.New("injected session inferencer is nil"))
			} else {
				plan.Inferencer = inferencer
			}
		} else {
			inferencer, factoryErr := options.SessionFactory(roomplanning.LiveSessionRequest{Participant: participant, Options: sessionOptions, Credential: credential})
			if factoryErr != nil {
				markFailure(fmt.Errorf("construct live session: %w", factoryErr))
			} else if isNil(inferencer) {
				markFailure(errors.New("session factory returned a nil inferencer"))
			} else {
				plan.Inferencer = inferencer
			}
		}
		if plan.StartupErr == nil && options.ResolveSampleRate != nil && (options.ResolveSampleRateForInjected || options.SessionInferencers[participant.ID] == nil) {
			rate, rateErr := options.ResolveSampleRate(sessionOptions, plan.Inferencer)
			if rateErr != nil {
				markFailure(rateErr)
			} else {
				plan.InputAudioSampleRate = rate
			}
		}
		plans = append(plans, plan)
	}
	return roomplanning.PlanResult{Plans: plans}, nil
}

func (s *Service) planReplay(ctx context.Context, options roomplanning.Options) (roomplanning.PlanResult, error) {
	if options.ReplayPlanner == nil {
		return roomplanning.PlanResult{}, roomplanning.ErrReplayPlanner
	}
	replay := options.ReplayPlan
	manifest := replay.Manifest()
	plans := make([]*roomplanning.ParticipantPlan, 0, len(replay.Participants))
	for index, recorded := range replay.Participants {
		if err := ctx.Err(); err != nil {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(err, closeCapabilities(plans))
		}
		if index >= len(manifest.Participants) {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(participantError(options, recorded.ID, errors.New("replay participant projection is incomplete")), closeCapabilities(plans))
		}
		participant := manifest.Participants[index]
		plan := &roomplanning.ParticipantPlan{Participant: participant, Replay: true}
		if normalizeKind(recorded.Kind) == rooms.ParticipantKindHuman {
			plans = append(plans, plan)
			continue
		}
		if recorded.CapturePath == "" {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(participantError(options, recorded.ID, errors.New("replay provider capture path is empty")), closeCapabilities(plans))
		}
		sessionOptions := roomplanning.SessionOptions{
			Provider: recorded.Provider, Model: recorded.Model, ModelProvided: true,
			ReplayPath: recorded.CapturePath, RoomReplay: true,
			Prompt: recorded.OpeningPrompt, PromptProvided: recorded.OpeningPrompt != "", Voice: recorded.Voice,
			WaitForClose: false,
		}
		replaySession, replayErr := options.ReplayPlanner(ctx, roomplanning.ReplayRequest{Participant: participant, Recorded: recorded, Options: sessionOptions})
		if replayErr != nil {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(participantError(options, recorded.ID, fmt.Errorf("plan replay session: %w", replayErr)), closeCapabilities(plans))
		}
		if isNil(replaySession.Inferencer) {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(participantError(options, recorded.ID, errors.New("replay session planner returned a nil inferencer")), closeCapabilities(plans))
		}
		plan.Options = sessionOptions
		plan.Inferencer = replaySession.Inferencer
		replaySession.MaxDuration = 0
		plan.ReplaySession = replaySession
		plans = append(plans, plan)
	}
	return roomplanning.PlanResult{Plans: plans}, nil
}

func resolveScope(options roomplanning.Options) (roomplanning.FilesystemScope, error) {
	if options.Filesystem != nil {
		if strings.TrimSpace(options.Filesystem.PrimaryRoot) == "" {
			return roomplanning.FilesystemScope{}, roomplanning.ErrFilesystemScope
		}
		return cloneScope(*options.Filesystem), nil
	}
	resolver := options.ResolveFilesystem
	if resolver == nil {
		resolver = defaultFilesystemScope
	}
	scope, err := resolver(options.WorkDir, options.AllowPaths)
	if err != nil {
		return roomplanning.FilesystemScope{}, fmt.Errorf("resolve filesystem scope: %w", err)
	}
	if strings.TrimSpace(scope.PrimaryRoot) == "" {
		return roomplanning.FilesystemScope{}, roomplanning.ErrFilesystemScope
	}
	return cloneScope(scope), nil
}

func defaultFilesystemScope(workDir string, allow []string) (roomplanning.FilesystemScope, error) {
	if strings.TrimSpace(workDir) == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return roomplanning.FilesystemScope{}, err
		}
	}
	primary, err := filepath.Abs(workDir)
	if err != nil {
		return roomplanning.FilesystemScope{}, err
	}
	primary = filepath.Clean(primary)
	info, err := os.Stat(primary)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("root is not a directory")
		}
		return roomplanning.FilesystemScope{}, err
	}
	result := roomplanning.FilesystemScope{PrimaryRoot: primary}
	seen := map[string]struct{}{primary: {}}
	for _, candidate := range allow {
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(primary, candidate)
		}
		resolved, resolveErr := filepath.Abs(candidate)
		if resolveErr != nil {
			return roomplanning.FilesystemScope{}, resolveErr
		}
		resolved = filepath.Clean(resolved)
		if _, exists := seen[resolved]; exists {
			continue
		}
		if _, statErr := os.Stat(resolved); statErr != nil {
			return roomplanning.FilesystemScope{}, statErr
		}
		seen[resolved] = struct{}{}
		result.AdditionalRoots = append(result.AdditionalRoots, resolved)
	}
	return result, nil
}

func participantError(options roomplanning.Options, id string, err error) error {
	if options.ParticipantError != nil {
		return options.ParticipantError(id, err)
	}
	return fmt.Errorf("room participant %q: %w", id, err)
}

func validateTools(participant rooms.Participant, capabilities roomplanning.ToolCapabilities) error {
	if len(participant.Tools) == 0 {
		return nil
	}
	if isNil(capabilities.Executor) {
		return fmt.Errorf("%w: participant %q has no executor", roomplanning.ErrParticipantTools, participant.ID)
	}
	requested := make(map[string]struct{}, len(participant.Tools))
	for _, name := range participant.Tools {
		requested[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(capabilities.Definitions))
	for _, definition := range capabilities.Definitions {
		if _, ok := requested[definition.Name]; !ok {
			return fmt.Errorf("%w: participant %q received unrequested tool %q", roomplanning.ErrParticipantToolMatch, participant.ID, definition.Name)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return fmt.Errorf("%w: participant %q received duplicate tool %q", roomplanning.ErrParticipantToolMatch, participant.ID, definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	if len(seen) != len(requested) {
		for name := range requested {
			if _, ok := seen[name]; !ok {
				return fmt.Errorf("%w: participant %q is missing definition for requested tool %q", roomplanning.ErrParticipantToolMatch, participant.ID, name)
			}
		}
	}
	return nil
}

func validateBrowser(capabilities roomplanning.BrowserCapabilities) error {
	if isNil(capabilities.Executor) {
		return roomplanning.ErrParticipantBrowser
	}
	if err := validateDefinitions(capabilities.Definitions); err != nil {
		return errors.Join(roomplanning.ErrBrowserCapability, err)
	}
	if err := validateDefinitions(capabilities.ToolDefinitionBase); err != nil {
		return errors.Join(roomplanning.ErrBrowserCapability, err)
	}
	return nil
}

func validateDefinitions(definitions []messages.ToolDefinition) error {
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" {
			return errors.New("contains a definition with an empty name")
		}
		if _, exists := seen[definition.Name]; exists {
			return fmt.Errorf("contains duplicate definition %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	return nil
}

func cloneDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func cloneScope(scope roomplanning.FilesystemScope) roomplanning.FilesystemScope {
	scope.AdditionalRoots = append([]string(nil), scope.AdditionalRoots...)
	return scope
}

func closeCapabilities(plans []*roomplanning.ParticipantPlan) error {
	var closeErr error
	for _, plan := range plans {
		if plan != nil && plan.Options.CapabilityClose != nil {
			closeErr = errors.Join(closeErr, plan.Options.CapabilityClose())
		}
	}
	return closeErr
}

func closeCapability(close func() error) error {
	if close == nil {
		return nil
	}
	return close()
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func normalizeKind(kind rooms.ParticipantKind) rooms.ParticipantKind {
	return rooms.ParticipantKindNormalizer{}.Normalize(kind)
}
