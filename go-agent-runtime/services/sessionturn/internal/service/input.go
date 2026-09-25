package service

import (
	"context"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/images"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/instructions"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/sessionwrap"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const errNoSeededRun sessionturn.Error = "seeded session run is not configured"

// RunSeeded places the seed wrapper inside a bounded run's admission
// boundary, or on the plan inferencer before an unbounded run.
func (s *Service) RunSeeded(request sessionturn.SeededRunRequest) error {
	if request.Run == nil {
		return errNoSeededRun
	}
	if !request.Seed.Present {
		return request.Run(request.Output, nil)
	}
	wirePrompt := request.WirePrompt
	if wirePrompt == "" {
		wirePrompt = s.NextWirePrompt()
	}
	if request.SetPrompt != nil {
		request.SetPrompt(wirePrompt)
	}
	output := sessionwrap.NewOutput(request.Output)
	wrap := func(inner messages.SessionInferencer) messages.SessionInferencer {
		return sessionwrap.NewSeedInferencer(inner, wirePrompt, request.Seed.Value)
	}
	if request.Bounded {
		return errors.Join(request.Run(output, wrap), output.Err())
	}
	if request.Inferencer != nil && request.SetInferencer != nil {
		request.SetInferencer(wrap(request.Inferencer))
	}
	return errors.Join(request.Run(output, nil), output.Err())
}

// NextWirePrompt allocates a unique text-seed sentinel.
func (s *Service) NextWirePrompt() string { return s.wirePrompts.Next() }

// NewTextSeedInferencer substitutes the sentinel with the explicit seed.
func (s *Service) NewTextSeedInferencer(inner messages.SessionInferencer, wirePrompt, value string) messages.SessionInferencer {
	return sessionwrap.NewSeedInferencer(inner, wirePrompt, value)
}

// NewOutput wraps writer so its first failure can be joined to the run error.
func (s *Service) NewOutput(writer io.Writer) sessionturn.Output {
	return sessionwrap.NewOutput(writer)
}

// ResolveInstructions selects and resolves the session instruction value.
func (s *Service) ResolveInstructions(ctx context.Context, request sessionturn.InstructionsRequest) (string, error) {
	return instructions.Resolve(ctx, s.instructions, request)
}

// ComposeInstructions adds the model-facing capability policy.
func (s *Service) ComposeInstructions(composition session.InstructionComposition) string {
	if s.instructions == nil {
		return composition.Instructions
	}
	return s.instructions.Compose(composition)
}

// NewInstructionsInferencer configures injected sessions after they open.
func (s *Service) NewInstructionsInferencer(inner messages.SessionInferencer, value string, definitions []messages.ToolDefinition) messages.SessionInferencer {
	return sessionwrap.NewInstructionsInferencer(inner, value, definitions)
}

// ResolveImageCapabilities decides whether the session model accepts images.
func (s *Service) ResolveImageCapabilities(request sessionturn.ImageCapabilityRequest) (sessionturn.ImageCapabilities, error) {
	return images.ResolveCapabilities(request)
}

// PrepareImageParts validates and loads session images in order.
func (s *Service) PrepareImageParts(request sessionturn.ImagePartsRequest) ([]messages.ImagePart, error) {
	return images.PrepareParts(request)
}

// AttachImages binds image parts to the first user turn and selects the loop
// prompt: a text seed receives a new sentinel, and an image without text uses
// the image-only trigger.
func (s *Service) AttachImages(request sessionturn.ImageAttachRequest) (sessionturn.ImageAttachment, error) {
	if request.Inferencer == nil {
		return sessionturn.ImageAttachment{}, sessionturn.ErrMissingInferencer
	}
	inferencer := sessionwrap.NewImageInferencer(request.Inferencer, request.Parts, request.DeferResponse)
	attachment := sessionturn.ImageAttachment{Inferencer: inferencer, FirstTurn: inferencer.FirstTurn()}
	switch {
	case request.Seed.Present:
		attachment.WirePrompt = s.NextWirePrompt()
		attachment.Prompt = attachment.WirePrompt
	case request.Prompt == "":
		attachment.Prompt = sessionturn.ImageOnlyPrompt
	}
	return attachment, nil
}

// SendImageTurn sends one reusable text and image user turn.
func (s *Service) SendImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart) error {
	if sessionwrap.SendImageTurn(ctx, session, text, parts, true) {
		return nil
	}
	return sessionwrap.ImageSendError()
}

// BindImageTools binds read_image to one capability snapshot.
func (s *Service) BindImageTools(binding sessionturn.ImageToolBinding) messages.ToolExecutor {
	return images.BindTools(binding)
}

// StageImageTools stages the initial images for later read_image calls.
func (s *Service) StageImageTools(ctx context.Context, request sessionturn.ImageStagingRequest) (tools.ImageStagingResult, error) {
	return images.Stage(ctx, s.staging, request)
}

// CompleteMessageSupport reports the optional complete-message paths.
func (s *Service) CompleteMessageSupport(session messages.Session) (complete, withoutResponse bool) {
	return sessionwrap.CompleteMessageSupport(session)
}
