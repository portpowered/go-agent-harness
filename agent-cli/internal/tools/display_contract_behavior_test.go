package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestScreenCaptureErrorDescribesAndClassifiesEveryState(t *testing.T) {
	cause := errors.New("boundary closed")
	for _, test := range []struct {
		state ScreenCaptureState
		want  error
	}{
		{runtimeTools.ScreenCaptureUnavailable, ErrDisplayUnavailable},
		{runtimeTools.ScreenCaptureCanceled, ErrScreenCaptureCanceled},
		{runtimeTools.ScreenCaptureTimedOut, ErrScreenCaptureTimedOut},
		{runtimeTools.ScreenCaptureFailed, ErrScreenCaptureFailed},
		{ScreenCaptureDenied, ErrScreenRecordingPermissionDenied},
	} {
		err := &ScreenCaptureError{State: test.state, Cause: cause}
		if !errors.Is(err, test.want) || !errors.Is(err, ErrScreenCapture) || !errors.Is(err, cause) {
			t.Errorf("%s: error %v does not classify as %v, the capture sentinel and its cause", test.state, err, test.want)
		}
		if !strings.HasPrefix(err.Error(), "display capture ("+string(test.state)+"): boundary closed") {
			t.Errorf("%s: message = %q", test.state, err.Error())
		}
		if got := ScreenToolErrorCode(err); test.state != ScreenCaptureDenied && got != string(test.state) {
			t.Errorf("%s: error code = %q", test.state, got)
		}
	}
	var nilErr *ScreenCaptureError
	if nilErr.Error() != ErrScreenCapture.Error() || !errors.Is(nilErr.Unwrap(), ErrScreenCapture) {
		t.Fatal("nil capture error must report the capture sentinel")
	}
	bare := &ScreenCaptureError{State: runtimeTools.ScreenCaptureFailed, Operation: "screenshot"}
	if !strings.Contains(bare.Error(), "screenshot (") || !strings.Contains(bare.Error(), "did not produce an image") {
		t.Fatalf("bare capture error = %q", bare.Error())
	}
}

func TestScreenRecordingPermissionErrorCarriesDetailAndCause(t *testing.T) {
	cause := errors.New("tcc")
	err := &ScreenRecordingPermissionError{Detail: " denied by TCC ", Cause: cause}
	if !strings.HasSuffix(err.Error(), ": denied by TCC") || !errors.Is(err, ErrScreenRecordingPermissionDenied) || !errors.Is(err, cause) {
		t.Fatalf("permission error = %v", err)
	}
	var nilErr *ScreenRecordingPermissionError
	if nilErr.Error() != ErrScreenRecordingPermissionDenied.Error() || !errors.Is(nilErr.Unwrap(), ErrScreenRecordingPermissionDenied) {
		t.Fatal("nil permission error must report the permission sentinel")
	}
	if got := ScreenToolErrorCode(err); got != ScreenRecordingPermissionDeniedErrorCode {
		t.Fatalf("permission error code = %q", got)
	}
}

func TestScreenRecordingGuidanceNamesTheHostTerminal(t *testing.T) {
	for program, want := range map[string]string{
		"Apple_Terminal": "Terminal", "iTerm.app": "iTerm2", "vscode": "VS Code",
		"GoLand": "the JetBrains IDE terminal host", "WezTerm": "WezTerm",
	} {
		t.Setenv("TERM_PROGRAM", program)
		if got := screenRecordingHostName(); got != want {
			t.Errorf("TERM_PROGRAM=%q host = %q, want %q", program, got, want)
		}
	}
	t.Setenv("TERM_PROGRAM", "")
	if got := screenRecordingHostName(); got == "" {
		t.Fatal("an unset TERM_PROGRAM must fall back to the executable name")
	}
}

func TestResolveFilesystemPolicyRejectsMissingWorkdirAndJoinsRelativeRoots(t *testing.T) {
	if _, err := ResolveFilesystemPolicy(FilesystemHost{WorkDir: "  "}); !errors.Is(err, ErrInvalidFilesystemRoot) {
		t.Fatalf("blank workdir error = %v", err)
	}
	if _, err := ResolveFilesystemPolicy(FilesystemHost{WorkDir: filepath.Join(t.TempDir(), "missing")}); !errors.Is(err, ErrInvalidFilesystemRoot) {
		t.Fatalf("missing workdir error = %v", err)
	}
	primary := t.TempDir()
	if err := os.Mkdir(filepath.Join(primary, "extra"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := ResolveFilesystemPolicy(FilesystemHost{WorkDir: primary}, "extra")
	if err != nil {
		t.Fatalf("ResolveFilesystemPolicy: %v", err)
	}
	if roots := policy.AdditionalRoots(); len(roots) != 1 || filepath.Base(roots[0]) != "extra" {
		t.Fatalf("additional roots = %v, want the relative root joined to the workdir", roots)
	}
}

func TestFilesystemRefusalErrorMessages(t *testing.T) {
	var nilErr *FilesystemRefusalError
	if nilErr.Error() != ErrFilesystemRefused.Error() || (&FilesystemRefusalError{}).Error() != ErrFilesystemRefused.Error() {
		t.Fatal("a refusal without an operation must report the refusal sentinel")
	}
}

func TestOSDisplayProcessHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := defaultDisplayProcess().Run(ctx, "true"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Run error = %v", err)
	}
	if _, err := defaultDisplayProcess().LookPath("definitely-not-a-display-command"); err == nil {
		t.Fatal("LookPath of a missing command must fail")
	}
}
