package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput"
	audiooutputwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// RunSessionWithAudioOut runs a session and writes assistant PCM to path; an empty path preserves normal output and "-" writes raw PCM16.
func RunSessionWithAudioOut(ctx context.Context, out io.Writer, opts SessionRunOptions, path string) (runErr error) {
	return RunSessionWithAudioOutAndTextSeed(ctx, out, opts, path, SessionTextSeed{})
}

// RunSessionWithAudioOutAndTextSeed combines text-seed behavior with assistant audio output.
func RunSessionWithAudioOutAndTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, seed SessionTextSeed) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if path == "" {
		if seed.Present {
			return RunSessionWithTextSeed(ctx, out, opts, seed)
		}
		return RunSession(ctx, out, opts)
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	opts.AudioOutputRequested = true

	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}

	audioOut, err := newSessionAudioOutputForPlan(&plan, path, out, audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: VoiceLoudnessGainDB(opts.Voice)}))
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, closeErr))
		}
	}()

	if plan.inferencer != nil {
		wirePrompt := ""
		if seed.Present {
			wirePrompt = nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
		}
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
		plan.inferencer = wrapped

		sessionOut := out
		if path == "-" {
			sessionOut = io.Discard
		}
		runErr = plan.run(ctx, sessionOut)
		wrapped.wait()
		if outputErr := wrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
		}
		return runErr
	}

	sessionOut := out
	if path == "-" {
		sessionOut = io.Discard
	}
	return plan.run(ctx, sessionOut)
}

// RunSessionWithAudioOutAndTextSeedAndMaxDuration combines assistant audio output with duration control.
func RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if path == "" {
		return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed)
	}
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if seed.Present {
		opts.Prompt = seed.Value
	}
	opts.AudioOutputRequested = true
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}

	audioOut, err := newSessionAudioOutputForPlan(&plan, path, out, audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: VoiceLoudnessGainDB(opts.Voice)}))
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, closeErr))
		}
	}()

	if plan.inferencer != nil {
		wirePrompt := ""
		if seed.Present {
			wirePrompt = nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
		}
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
		plan.inferencer = wrapped

		sessionOut := out
		if path == "-" {
			sessionOut = io.Discard
		}
		if maxDuration == 0 {
			runErr = plan.run(ctx, sessionOut)
		} else {
			durationCtx, durationErr := prepareSessionDurationArtifacts(ctx)
			if durationErr != nil {
				return durationErr
			}
			runErr = runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
		}
		wrapped.wait()
		if outputErr := wrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
		}
		return runErr
	}

	sessionOut := out
	if path == "-" {
		sessionOut = io.Discard
	}
	if maxDuration == 0 {
		return plan.run(ctx, sessionOut)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
}

type sessionAudioOutput struct {
	output audiooutput.Output
}

func newSessionAudioOutputForPlan(plan *sessionRuntimePlan, path string, out io.Writer, loudness *audio.LoudnessNormalizer) (*sessionAudioOutput, error) {
	if plan == nil {
		return nil, fmt.Errorf("%w: session runtime plan is nil", audiooutput.ErrInvalidConfig)
	}
	var observe func([]byte, messages.StreamMessage)
	if plan.runtime != nil {
		observe = plan.runtime.audioOutputMessage
	}
	deviceBound := plan.rtcDeviceRequest.outputSelected()
	output, err := audiooutputwire.NewService().Open(audiooutput.Config{
		Path:               path,
		Writer:             out,
		SampleRate:         plan.outputAudioSampleRate,
		DeviceBound:        deviceBound,
		Loudness:           loudness,
		ObserveAudioOutput: observe,
	})
	if err != nil {
		return nil, err
	}
	result := &sessionAudioOutput{output: output}
	if deviceBound {
		prior := plan.rtcDeviceRequest.PlaybackSamplesObserver
		plan.rtcDeviceRequest.PlaybackSamplesObserver = func(ctx context.Context, rate int, samples []int16) error {
			var priorErr error
			if prior != nil {
				priorErr = prior(ctx, rate, samples)
			}
			return errors.Join(priorErr, result.writeDeviceSamples(ctx, rate, samples))
		}
	}
	return result, nil
}

func (o *sessionAudioOutput) writeDeviceSamples(ctx context.Context, sampleRate int, samples []int16) error {
	return o.output.ObserveDeviceSamples(ctx, sampleRate, samples)
}

func (o *sessionAudioOutput) close() error {
	if o == nil || o.output == nil {
		return nil
	}
	return o.output.Close()
}

type sessionAudioOutputInferencer struct {
	audiooutput.SessionInferencer
}

func newSessionAudioOutputInferencer(inner messages.SessionInferencer, output *sessionAudioOutput, wirePrompt string, seedValue string) *sessionAudioOutputInferencer {
	return &sessionAudioOutputInferencer{
		SessionInferencer: audiooutputwire.NewService().Wrap(inner, output.output, audiooutput.SessionOptions{
			WirePrompt:   wirePrompt,
			SeedValue:    seedValue,
			AdaptSession: adaptSessionAudioOutputCapabilities,
		}),
	}
}

func (i *sessionAudioOutputInferencer) wait() {
	i.SessionInferencer.Wait()
}

func (i *sessionAudioOutputInferencer) err() error {
	return i.SessionInferencer.Err()
}

// adaptSessionAudioOutputCapabilities bridges the CLI's private provider-media
// seam to the host-neutral service contract. All other optional session
// capabilities are forwarded explicitly so wrapping does not narrow behavior.
func adaptSessionAudioOutputCapabilities(session messages.Session) messages.Session {
	media, ok := rtcMediaFromSession(session)
	if !ok {
		return session
	}
	return &sessionAudioOutputMediaSession{Session: session, media: media}
}

type sessionAudioOutputMediaSession struct {
	messages.Session
	media RTCMediaEndpoints
}

func (s *sessionAudioOutputMediaSession) RTCMedia() RTCMediaEndpoints { return s.media }

func (s *sessionAudioOutputMediaSession) TerminalError() error {
	return terminalSessionError(s.Session)
}

func (s *sessionAudioOutputMediaSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *sessionAudioOutputMediaSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *sessionAudioOutputMediaSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *sessionAudioOutputMediaSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionAudioOutputMediaSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *sessionAudioOutputMediaSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}
