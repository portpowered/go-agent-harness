package imageinput

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestContractLoaderAndErrorIdentities(t *testing.T) {
	loader := ContentLoaderFunc(func(_ context.Context, path string) (messages.ContentPart, error) {
		return messages.TextPart{Text: path}, nil
	})
	part, err := loader.Load(context.Background(), "input")
	if err != nil || part.(messages.TextPart).Text != "input" {
		t.Fatalf("loader result = %#v, %v", part, err)
	}
	if _, err := ContentLoaderFunc(nil).Load(context.Background(), "input"); err == nil {
		t.Fatal("nil loader unexpectedly succeeded")
	}

	cause := errors.New("read failed")
	inputErr := InputError{Path: "input", Kind: ErrUnreadableFile, Cause: cause, Err: cause}
	wrapped := &UnreadableFileError{InputError: inputErr}
	if !errors.Is(wrapped, ErrUnreadableFile) || !errors.Is(wrapped, cause) {
		t.Fatalf("wrapped error = %v, want kind and cause", wrapped)
	}
	if wrapped.Path != "input" || wrapped.Err != cause {
		t.Fatalf("wrapped fields = %#v, want compatibility payload", wrapped)
	}
	if (&CapabilityError{Model: "text", Capability: "image input"}).Error() == "" {
		t.Fatal("capability error had no message")
	}
	if !errors.Is(&CapabilityError{}, ErrCapability) {
		t.Fatal("capability error lost sentinel")
	}
	if !errors.Is(&EmptyFileError{Path: "empty"}, ErrEmptyFile) {
		t.Fatal("empty error lost sentinel")
	}
	if !errors.Is(&SendError{Mode: "response", Cause: cause}, cause) || !errors.Is(&SendError{}, ErrSend) {
		t.Fatal("send error lost identities")
	}
}
