// Package imageinput defines the provider-neutral image attachment contract
// used by session hosts and embedders.
package imageinput

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ErrorKind is a comparable error identity for image-input failures. String
// constants keep the public contract immutable while still supporting
// errors.Is without package-level mutable error variables.
type ErrorKind string

func (kind ErrorKind) Error() string { return string(kind) }

const (
	ErrMissingFile     ErrorKind = "image input file is missing"
	ErrUnreadableFile  ErrorKind = "image input file is unreadable"
	ErrUnsupportedMIME ErrorKind = "image input MIME type is unsupported"
	ErrInvalidContent  ErrorKind = "image input content is invalid"
	ErrEmptyFile       ErrorKind = "image input file is empty"
	ErrCapability      ErrorKind = "image input capability is unsupported"
	ErrSend            ErrorKind = "image input turn could not be sent"

	// ImageOnlyPrompt is an internal transport value used by a host when it
	// needs the first session prompt to publish an image-only user turn.
	ImageOnlyPrompt = "\x00agent-session-image-turn\x00"

	// DeferredInstruction gives an image-only turn useful context when the
	// response is intentionally deferred until a separately captured spoken
	// question arrives.
	DeferredInstruction = "Use the attached image to answer the user's next spoken question."
)

// Capabilities is the host-resolved provider capability snapshot. The service
// copies the supported MIME slice and never mutates the caller's value.
type Capabilities struct {
	Model                   string
	SupportsImageInput      bool
	SupportedInputMIMETypes []string
}

// ContentLoader is the injected boundary for host-owned content loading. A
// reusable image service never discovers filesystem policy or reads paths on
// its own.
type ContentLoader interface {
	Load(context.Context, string) (messages.ContentPart, error)
}

// ContentLoaderFunc adapts a function to ContentLoader for embedders and
// focused consumer probes.
type ContentLoaderFunc func(context.Context, string) (messages.ContentPart, error)

func (loader ContentLoaderFunc) Load(ctx context.Context, path string) (messages.ContentPart, error) {
	if loader == nil {
		return nil, fmt.Errorf("image input content loader is nil")
	}
	return loader(ctx, path)
}

// TurnOptions selects whether publication should request a provider response
// immediately or queue the complete message for a later audio boundary.
type TurnOptions struct {
	DeferResponse bool
}

// PublicationOptions is a descriptive alias for callers that name the
// operation rather than the session turn.
type PublicationOptions = TurnOptions

// Attachment is the session decoration and the one-shot first-turn outcome
// used by a host's session loop. FirstTurn receives nil after the image user
// message is admitted, or a typed error when publication fails.
type Attachment struct {
	Inferencer messages.SessionInferencer
	FirstTurn  <-chan error
}

// MessageSender is the optional complete-message provider capability.
type MessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

// MessageSenderWithoutResponse queues a complete message without requesting a
// provider response. Audio-enabled image turns use this mode.
type MessageSenderWithoutResponse interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

// MessageSenderWithError is an optional stronger send seam. Providers that
// expose it let the image service preserve their exact send error identity.
type MessageSenderWithError interface {
	SendMessageWithError(context.Context, messages.Message) error
}

// MessageSenderWithoutResponseWithError is the error-reporting counterpart of
// MessageSenderWithoutResponse.
type MessageSenderWithoutResponseWithError interface {
	SendMessageWithoutResponseWithError(context.Context, messages.Message) error
}

// CompleteMessageCapabilities prevents a wrapper from advertising a complete
// message method that its underlying provider cannot actually deliver.
type CompleteMessageCapabilities interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

// TerminalErrorSource is forwarded when a provider exposes a terminal error
// outside the generic messages.Session contract.
type TerminalErrorSource interface {
	TerminalError() error
}

// UnderlyingSessionSource lets a host preserve an unrelated transport
// capability without taking ownership of image publication or capability
// forwarding.
type UnderlyingSessionSource interface {
	UnderlyingSession() messages.Session
}

// ForwardingSession is the optional capability surface implemented by the
// service's decorated session. It is useful to a host that must add one
// transport-specific adapter while retaining the service's complete-message
// and response forwarding behavior.
type ForwardingSession interface {
	messages.Session
	MessageSender
	MessageSenderWithoutResponse
	CompleteMessageCapabilities
	messages.SessionResponseRequester
	messages.SessionResponseCapability
	TerminalErrorSource
	UnderlyingSessionSource
}

// CapabilityError identifies a provider that cannot accept image input.
type CapabilityError struct {
	Model      string
	Capability string
}

func (err *CapabilityError) Error() string {
	if err == nil {
		return ErrCapability.Error()
	}
	return fmt.Sprintf("model %q does not support %s capability", err.Model, err.Capability)
}

func (*CapabilityError) Unwrap() error { return ErrCapability }

// InputError carries the original path, detected MIME type, stable supported
// MIME snapshot, and the underlying loader or decoder cause.
type InputError struct {
	Path          string
	DetectedMIME  string
	SupportedMIME []string
	Cause         error
	// Err is the legacy spelling retained for callers that inspected the
	// original CLI error payload. It mirrors Cause.
	Err     error
	Kind    ErrorKind
	Message string
}

func (err *InputError) Error() string {
	if err == nil {
		return "image input error"
	}
	if err.Message != "" {
		return err.Message
	}
	return fmt.Sprintf("image input %q failed: %s", err.Path, err.Kind)
}

func (err *InputError) Unwrap() []error {
	if err == nil {
		return nil
	}
	cause := err.Cause
	if cause == nil {
		cause = err.Err
	}
	if cause == nil {
		return []error{err.Kind}
	}
	return []error{err.Kind, cause}
}

// MissingFileError identifies a path that does not exist.
type MissingFileError struct{ InputError }

func (err *MissingFileError) Error() string   { return err.InputError.Error() }
func (err *MissingFileError) Unwrap() []error { return err.InputError.Unwrap() }

// UnreadableFileError identifies a path that exists but could not be loaded.
type UnreadableFileError struct{ InputError }

func (err *UnreadableFileError) Error() string   { return err.InputError.Error() }
func (err *UnreadableFileError) Unwrap() []error { return err.InputError.Unwrap() }

// UnsupportedMIMEError identifies a content part whose MIME type is outside
// the stable provider-supported allowlist.
type UnsupportedMIMEError struct{ InputError }

func (err *UnsupportedMIMEError) Error() string   { return err.InputError.Error() }
func (err *UnsupportedMIMEError) Unwrap() []error { return err.InputError.Unwrap() }

// InvalidContentError identifies bytes that cannot be decoded as their image
// format, or whose decoded format does not match the declared MIME type.
type InvalidContentError struct{ InputError }

func (err *InvalidContentError) Error() string   { return err.InputError.Error() }
func (err *InvalidContentError) Unwrap() []error { return err.InputError.Unwrap() }

// EmptyFileError identifies a content part with no inline image bytes.
type EmptyFileError struct{ Path string }

func (err *EmptyFileError) Error() string {
	if err == nil {
		return ErrEmptyFile.Error()
	}
	return fmt.Sprintf("image input %q is empty", err.Path)
}

func (*EmptyFileError) Unwrap() error { return ErrEmptyFile }

// SendError preserves the image-send sentinel and, when available, the
// provider, context, or shutdown cause.
type SendError struct {
	Mode  string
	Cause error
}

func (err *SendError) Error() string {
	if err == nil {
		return ErrSend.Error()
	}
	if err.Cause == nil {
		return fmt.Sprintf("%s (%s)", ErrSend, err.Mode)
	}
	return fmt.Sprintf("%s (%s): %v", ErrSend, err.Mode, err.Cause)
}

func (err *SendError) Unwrap() []error {
	if err == nil || err.Cause == nil {
		return []error{ErrSend}
	}
	return []error{ErrSend, err.Cause}
}

// Service owns image preparation and exactly-once publication of the first
// image-backed user turn. Implementations are constructed by services/imageinput/wire.
type Service interface {
	Prepare(context.Context, []string, Capabilities) ([]messages.ImagePart, error)
	Attach(messages.SessionInferencer, []messages.ImagePart, TurnOptions) (Attachment, error)
	Send(context.Context, messages.Session, string, []messages.ImagePart, TurnOptions) error
}

// Loader is retained as a concise role alias for embedders.
type Loader = ContentLoader
