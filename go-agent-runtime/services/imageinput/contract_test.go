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
	textPart, ok := part.(messages.TextPart)
	if err != nil || !ok || textPart.Text != "input" {
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
	if wrapped.Path != "input" || !errors.Is(wrapped.Err, cause) {
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

func TestContractErrorFormattingAndUnwrapBranches(t *testing.T) {
	if got := ErrCapability.Error(); got != string(ErrCapability) {
		t.Fatalf("error kind string = %q, want %q", got, ErrCapability)
	}

	var nilCapability *CapabilityError
	if got := nilCapability.Error(); got != ErrCapability.Error() {
		t.Fatalf("nil capability error = %q, want %q", got, ErrCapability)
	}

	if got := (&InputError{Message: "custom image error"}).Error(); got != "custom image error" {
		t.Fatalf("custom input error = %q", got)
	}
	var nilInput *InputError
	if got := nilInput.Error(); got != "image input error" {
		t.Fatalf("nil input error = %q", got)
	}
	if got := (&InputError{Kind: ErrInvalidContent}).Unwrap(); len(got) != 1 || !errors.Is(errors.Join(got...), ErrInvalidContent) {
		t.Fatalf("kind-only input unwrap = %#v", got)
	}
	legacyCause := errors.New("legacy cause")
	if got := (&InputError{Kind: ErrUnreadableFile, Err: legacyCause}).Unwrap(); len(got) != 2 || !errors.Is(errors.Join(got...), legacyCause) {
		t.Fatalf("legacy input unwrap = %#v", got)
	}
	if got := nilInput.Unwrap(); got != nil {
		t.Fatalf("nil input unwrap = %#v, want nil", got)
	}

	wrapperCases := []struct {
		name string
		err  error
		kind ErrorKind
	}{
		{name: "missing", err: &MissingFileError{InputError: InputError{Path: "missing", Kind: ErrMissingFile}}, kind: ErrMissingFile},
		{name: "unreadable", err: &UnreadableFileError{InputError: InputError{Path: "unreadable", Kind: ErrUnreadableFile}}, kind: ErrUnreadableFile},
		{name: "unsupported", err: &UnsupportedMIMEError{InputError: InputError{Path: "unsupported", Kind: ErrUnsupportedMIME}}, kind: ErrUnsupportedMIME},
		{name: "invalid", err: &InvalidContentError{InputError: InputError{Path: "invalid", Kind: ErrInvalidContent}}, kind: ErrInvalidContent},
	}
	for _, testCase := range wrapperCases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.err.Error() == "" || !errors.Is(testCase.err, testCase.kind) {
				t.Fatalf("wrapper = %v, want non-empty message and %v identity", testCase.err, testCase.kind)
			}
		})
	}

	var nilEmpty *EmptyFileError
	if got := nilEmpty.Error(); got != ErrEmptyFile.Error() {
		t.Fatalf("nil empty error = %q, want %q", got, ErrEmptyFile)
	}

	var nilSend *SendError
	if got := nilSend.Error(); got != ErrSend.Error() {
		t.Fatalf("nil send error = %q, want %q", got, ErrSend)
	}
	if got := (&SendError{Mode: "response"}).Error(); got == "" {
		t.Fatal("send error without cause had no message")
	}
	if got := (&SendError{Mode: "response", Cause: legacyCause}).Error(); got == "" {
		t.Fatal("send error with cause had no message")
	}
	if got := nilSend.Unwrap(); len(got) != 1 || !errors.Is(errors.Join(got...), ErrSend) {
		t.Fatalf("nil send unwrap = %#v, want send identity", got)
	}
}
