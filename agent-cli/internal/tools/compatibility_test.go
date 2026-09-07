package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type filesystemPolicyFixture struct {
	policy              *FilesystemPolicy
	primary             string
	additional          string
	canonicalPrimary    string
	canonicalAdditional string
}

func newFilesystemPolicyFixture(t *testing.T) filesystemPolicyFixture {
	t.Helper()
	primary := t.TempDir()
	additional := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(additional, "extra.txt"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := ResolveFilesystemPolicy(primary, additional, additional)
	if err != nil {
		t.Fatalf("ResolveFilesystemPolicy: %v", err)
	}
	canonicalPrimary, err := filepath.EvalSymlinks(primary)
	if err != nil {
		t.Fatal(err)
	}
	canonicalAdditional, err := filepath.EvalSymlinks(additional)
	if err != nil {
		t.Fatal(err)
	}
	return filesystemPolicyFixture{policy: policy, primary: primary, additional: additional, canonicalPrimary: canonicalPrimary, canonicalAdditional: canonicalAdditional}
}

func TestFilesystemPolicyCanonicalizesRootsAndScope(t *testing.T) {
	fixture := newFilesystemPolicyFixture(t)
	if fixture.policy.PrimaryRoot() != fixture.canonicalPrimary {
		t.Fatalf("primary root = %q, want %q", fixture.policy.PrimaryRoot(), fixture.canonicalPrimary)
	}
	if got := fixture.policy.AdditionalRoots(); len(got) != 1 || got[0] != fixture.canonicalAdditional {
		t.Fatalf("additional roots = %v, want one canonical root", got)
	}
	if got := fixture.policy.ScopeDescription(); !strings.Contains(got, "workdir="+fixture.canonicalPrimary) || !strings.Contains(got, "additional_allowed_roots="+fixture.canonicalAdditional) {
		t.Fatalf("scope description = %q", got)
	}
	additionalRoots := fixture.policy.AdditionalRoots()
	additionalRoots[0] = fixture.primary
	if fixture.policy.AdditionalRoots()[0] != fixture.canonicalAdditional {
		t.Fatal("AdditionalRoots exposed mutable policy storage")
	}
	writableRoots := fixture.policy.WritableRoots()
	writableRoots[0] = fixture.additional
	if fixture.policy.WritableRoots()[0] != fixture.canonicalPrimary {
		t.Fatal("WritableRoots exposed mutable policy storage")
	}
}

func TestFilesystemPolicyAuthorizesRootsAndRejectsEscapes(t *testing.T) {
	fixture := newFilesystemPolicyFixture(t)
	for _, path := range []string{"inside.txt", filepath.Join(fixture.primary, "missing", "future.txt"), filepath.Join(fixture.additional, "extra.txt")} {
		if err := fixture.policy.AuthorizeRead(path); err != nil {
			t.Errorf("AuthorizeRead(%q) = %v, want allowed", path, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.policy.AuthorizeRead(outside); !errors.Is(err, ErrFilesystemAccessDenied) {
		t.Fatalf("AuthorizeRead(outside) = %v, want access denial", err)
	}
	link := filepath.Join(fixture.primary, "outside-link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Logf("symlink setup unavailable: %v", err)
	} else if err := fixture.policy.AuthorizeRead(link); !errors.Is(err, ErrFilesystemAccessDenied) {
		t.Fatalf("AuthorizeRead(symlink outside) = %v, want access denial", err)
	}
}

func TestFilesystemPolicyConstructorsAndNilPolicy(t *testing.T) {
	fixture := newFilesystemPolicyFixture(t)
	fromRoots, err := NewFilesystemPolicyFromRoots(fixture.primary, []string{fixture.additional})
	if err != nil || fromRoots.PrimaryRoot() != fixture.canonicalPrimary {
		t.Fatalf("NewFilesystemPolicyFromRoots = %v, %v", fromRoots, err)
	}
	if err := (*FilesystemPolicy)(nil).AuthorizeRead("anything"); err != nil {
		t.Fatalf("nil policy AuthorizeRead = %v", err)
	}
	if got := (*FilesystemPolicy)(nil).ScopeDescription(); got != "filesystem scope unavailable" {
		t.Fatalf("nil scope description = %q", got)
	}
	if got := (*FilesystemPolicy)(nil).PrimaryRoot(); got != "" {
		t.Fatalf("nil primary root = %q", got)
	}
	if got := (*FilesystemPolicy)(nil).AdditionalRoots(); got != nil {
		t.Fatalf("nil additional roots = %v", got)
	}
	if got := (*FilesystemPolicy)(nil).WritableRoots(); got != nil {
		t.Fatalf("nil writable roots = %v", got)
	}
	if got := (*FilesystemPolicy)(nil).ProtectedReadRoots(); got != nil {
		t.Fatalf("nil protected roots = %v", got)
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, badRoot := range []string{"", fileRoot, filepath.Join(t.TempDir(), "missing")} {
		if _, err := NewFilesystemPolicy(badRoot); !errors.Is(err, ErrInvalidFilesystemRoot) {
			t.Errorf("NewFilesystemPolicy(%q) = %v, want invalid-root error", badRoot, err)
		}
	}
}

func TestFilesystemRefusalRoundTrip(t *testing.T) {
	refusal := FilesystemRefusal{
		Type:        FilesystemRefusalType,
		Version:     FilesystemRefusalVersion,
		Status:      FilesystemRefusalStatus,
		Operation:   "read_image",
		Path:        "image.png",
		WorkDir:     "/workspace",
		Reason:      FilesystemRefusalOutsidePermittedRoots,
		Message:     "path is outside the permitted roots",
		Remediation: "choose a path inside the workspace",
	}
	if err := refusal.Validate(); err != nil {
		t.Fatalf("valid refusal rejected: %v", err)
	}
	encoded, err := MarshalFilesystemRefusal(refusal)
	if err != nil {
		t.Fatalf("MarshalFilesystemRefusal: %v", err)
	}
	decoded, err := DecodeFilesystemRefusal(encoded)
	if err != nil || decoded != refusal {
		t.Fatalf("DecodeFilesystemRefusal = %+v, %v", decoded, err)
	}
	if direct, ok := FilesystemRefusalFromContent(string(encoded)); !ok || direct != refusal {
		t.Fatalf("direct refusal content = %+v, %t", direct, ok)
	}
	nested, err := json.Marshal(map[string]any{"refusal": refusal})
	if err != nil {
		t.Fatal(err)
	}
	if wrapped, ok := FilesystemRefusalFromContent(string(nested)); !ok || wrapped != refusal {
		t.Fatalf("nested refusal content = %+v, %t", wrapped, ok)
	}
	for _, content := range []string{"", "{}", `{"refusal":{"type":"wrong"}}`, "not json"} {
		decoded, ok := FilesystemRefusalFromContent(content)
		if ok || decoded != (FilesystemRefusal{}) {
			t.Errorf("invalid refusal content %q was accepted", content)
		}
	}

}

func validFilesystemRefusal() FilesystemRefusal {
	return FilesystemRefusal{
		Type:        FilesystemRefusalType,
		Version:     FilesystemRefusalVersion,
		Status:      FilesystemRefusalStatus,
		Operation:   "read_image",
		Path:        "image.png",
		WorkDir:     "/workspace",
		Reason:      FilesystemRefusalOutsidePermittedRoots,
		Message:     "path is outside the permitted roots",
		Remediation: "choose a path inside the workspace",
	}
}

func TestFilesystemRefusalValidation(t *testing.T) {
	const invalidValue = "wrong"
	refusal := validFilesystemRefusal()
	invalid := []FilesystemRefusal{
		func() FilesystemRefusal { copy := refusal; copy.Type = ""; return copy }(),
		func() FilesystemRefusal { copy := refusal; copy.Type = invalidValue; return copy }(),
		func() FilesystemRefusal { copy := refusal; copy.Version = invalidValue; return copy }(),
		func() FilesystemRefusal { copy := refusal; copy.OK = true; return copy }(),
		func() FilesystemRefusal { copy := refusal; copy.Status = "ok"; return copy }(),
		func() FilesystemRefusal { copy := refusal; copy.Operation = " "; return copy }(),
		func() FilesystemRefusal { copy := refusal; copy.Reason = invalidValue; return copy }(),
		func() FilesystemRefusal { copy := refusal; copy.Message = ""; return copy }(),
		func() FilesystemRefusal { copy := refusal; copy.Remediation = ""; return copy }(),
	}
	for index, candidate := range invalid {
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid refusal %d was accepted", index)
		}
		if _, err := MarshalFilesystemRefusal(candidate); err == nil {
			t.Errorf("invalid refusal %d was marshaled", index)
		}
	}

	errEnvelope := &FilesystemRefusalError{Refusal: refusal}
	if !errors.Is(errEnvelope, ErrFilesystemRefused) || !strings.Contains(errEnvelope.Error(), "read_image") {
		t.Fatalf("FilesystemRefusalError = %v", errEnvelope)
	}
	var nilEnvelope *FilesystemRefusalError
	if nilEnvelope.Error() != ErrFilesystemRefused.Error() {
		t.Fatalf("nil FilesystemRefusalError = %q", nilEnvelope.Error())
	}
	if (FilesystemRefusal{}).Error() != ErrFilesystemRefused.Error() {
		t.Fatal("empty refusal did not use stable sentinel")
	}
}

func TestDisplayCapabilityContract(t *testing.T) {
	if !IsPhysicalDisplayToolName(ScreenToolID) || !IsPhysicalDisplayToolName(HostDisplayToolID) || IsPhysicalDisplayToolName("page_sight") {
		t.Fatal("physical display name classification changed")
	}
	if got := UsableDisplayCapability(-1); got.DisplayCount != 0 || got.Available || got.Usable() {
		t.Fatalf("negative usable capability = %+v", got)
	}
	if got := UsableDisplayCapability(2); !got.Usable() || got.DisplayCount != 2 {
		t.Fatalf("positive usable capability = %+v", got)
	}
	if got := UnavailableDisplayCapability("no display"); got.Usable() || got.Reason != "no display" {
		t.Fatalf("unavailable capability = %+v", got)
	}
}

func TestDisplayCaptureErrorIdentity(t *testing.T) {
	cause := errors.New("permission denied by host")
	for _, state := range []ScreenCaptureState{ScreenCaptureGranted, ScreenCaptureDenied, ScreenCaptureUnavailable, ScreenCaptureCanceled, ScreenCaptureTimedOut, ScreenCaptureFailed} {
		err := &ScreenCaptureError{State: state, Operation: "show", Reason: "boundary reason", Cause: cause}
		if err.Error() == "" || !errors.Is(err, ErrScreenCapture) || !errors.Is(err, cause) {
			t.Errorf("screen error state %q = %v", state, err)
		}
	}
	if !errors.Is((&ScreenCaptureError{State: ScreenCaptureDenied}), ErrScreenRecordingPermissionDenied) {
		t.Fatal("denied screen error lost permission identity")
	}
	permissionErr := &ScreenRecordingPermissionError{Detail: "TCC denied", Cause: cause}
	if !errors.Is(permissionErr, ErrScreenRecordingPermissionDenied) || !errors.Is(permissionErr, cause) {
		t.Fatalf("permission error = %v", permissionErr)
	}
	var nilPermissionErr *ScreenRecordingPermissionError
	if nilPermissionErr.Error() != ErrScreenRecordingPermissionDenied.Error() || !errors.Is(nilPermissionErr, ErrScreenRecordingPermissionDenied) {
		t.Fatal("nil permission error did not retain stable identity")
	}
}

func TestDisplayPermissionGuidance(t *testing.T) {
	cause := errors.New("permission denied by host")
	permissionErr := &ScreenRecordingPermissionError{Detail: "TCC denied", Cause: cause}
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	if got := screenRecordingHostName(); got != "iTerm2" {
		t.Fatalf("iTerm host name = %q", got)
	}
	for _, marker := range []string{"screen recording", "not authorized", "not authorised", "operation not permitted", "permission denied", "tcc", "could not create image from display"} {
		if !screenRecordingPermissionText(marker) {
			t.Errorf("permission marker %q not classified", marker)
		}
	}
	if screenRecordingPermissionText("ordinary capture failure") {
		t.Fatal("ordinary capture failure was classified as permission denial")
	}
	operatorJSON := ScreenToolErrorResult(permissionErr)
	if !strings.Contains(operatorJSON, "Screen-recording permission") || !strings.Contains(operatorJSON, "iTerm2") {
		t.Fatalf("operator error omitted actionable host guidance: %s", operatorJSON)
	}
	var operator struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal([]byte(operatorJSON), &operator); err != nil || operator.ErrorCode != ScreenRecordingPermissionDeniedErrorCode {
		t.Fatalf("operator error envelope = %s, %v", operatorJSON, err)
	}
	sessionJSON := ScreenToolSessionErrorResult(permissionErr)
	if strings.Contains(sessionJSON, "iTerm2") || !strings.Contains(sessionJSON, "Screen sight is unavailable.") {
		t.Fatalf("session error leaked operator guidance: %s", sessionJSON)
	}
	if got := ScreenToolErrorCode(fmt.Errorf("host: %w", ErrScreenRecordingPermissionDenied)); got != ScreenRecordingPermissionDeniedErrorCode {
		t.Fatalf("wrapped permission error code = %q", got)
	}
}

func TestDisplaySeams(t *testing.T) {
	ctx := context.Background()
	var probeCalled bool
	probe := DisplayCapabilityProbeFunc(func(context.Context) (DisplayCapability, error) {
		probeCalled = true
		return UsableDisplayCapability(1), nil
	})
	if capability, err := probe.Probe(ctx); err != nil || !capability.Usable() || !probeCalled {
		t.Fatalf("probe seam = %+v, %v, called=%t", capability, err, probeCalled)
	}
	var nilProbe DisplayCapabilityProbeFunc
	if capability, err := nilProbe.Probe(ctx); err != nil || capability.Reason == "" {
		t.Fatalf("nil probe = %+v, %v", capability, err)
	}

	permissionCalls := 0
	permission := DisplayPermissionCheckerFunc(func(context.Context) (DisplayPermission, error) {
		permissionCalls++
		return DisplayPermission{State: DisplayPermissionGranted}, nil
	})
	if got, err := permission.Check(ctx); err != nil || got.State != DisplayPermissionGranted {
		t.Fatalf("permission seam = %+v, %v", got, err)
	}
	var nilPermission DisplayPermissionCheckerFunc
	if got, err := nilPermission.Check(ctx); err != nil || got.State != DisplayPermissionGranted {
		t.Fatalf("nil permission seam = %+v, %v", got, err)
	}
}

func TestDisplayProcessAndCapturerSeams(t *testing.T) {
	ctx := context.Background()
	process := testDisplayProcess()
	if output, err := process.Run(ctx, "xrandr"); err != nil || string(output) != "Monitors: 1\n" {
		t.Fatalf("configured display process runner = %q, %v", output, err)
	}
	if _, err := (DisplayProcessAdapter{}).Run(ctx, "missing"); err == nil {
		t.Fatal("unconfigured display process runner did not fail")
	}
	if got, err := (DisplayProcessAdapter{}).LookPath("scrot"); err != nil || got != "scrot" {
		t.Fatalf("default display process lookup = %q, %v", got, err)
	}
	captured := false
	capturer := DisplayCapturerFunc(func(_ context.Context, display int, bounds image.Rectangle) (*image.RGBA, error) {
		captured = display == 2 && bounds == image.Rect(1, 2, 4, 6)
		return image.NewRGBA(bounds), nil
	})
	if imageValue, err := capturer.Capture(ctx, 2, image.Rect(1, 2, 4, 6)); err != nil || imageValue == nil || !captured {
		t.Fatalf("capturer seam = %v, %v, captured=%t", imageValue, err, captured)
	}
	var nilCapturer DisplayCapturerFunc
	if _, err := nilCapturer.Capture(ctx, 0, image.Rectangle{}); err == nil {
		t.Fatal("nil capturer did not fail")
	}
}

func TestDisplayHostSurfaceCaptureAndDiscovery(t *testing.T) {
	ctx := context.Background()
	process := testDisplayProcess()
	permissionCalls := 0
	permission := DisplayPermissionCheckerFunc(func(context.Context) (DisplayPermission, error) {
		permissionCalls++
		return DisplayPermission{State: DisplayPermissionGranted}, nil
	})
	if got, err := permission.Check(ctx); err != nil || got.State != DisplayPermissionGranted {
		t.Fatalf("surface permission checker = %+v, %v", got, err)
	}
	surface := NewHostDisplaySurfaceWithOptions(HostDisplaySurfaceOptions{Process: process, PermissionChecker: permission, Capturer: testDisplayCapturer()})
	if surface == nil {
		t.Fatal("NewHostDisplaySurfaceWithOptions returned nil")
	}
	if imageValue, err := surface.Capture(ctx, image.Rect(0, 0, 2, 2)); err != nil || imageValue == nil {
		t.Fatalf("surface Capture = %v, %v", imageValue, err)
	}
	concrete, ok := surface.(*hostDisplaySurface)
	if !ok {
		t.Fatalf("surface concrete type = %T, want hostDisplaySurface", surface)
	}
	if imageValue, err := concrete.CaptureDisplay(ctx, 2, image.Rect(1, 2, 4, 6)); err != nil || imageValue == nil {
		t.Fatalf("surface CaptureDisplay = %v, %v", imageValue, err)
	}
	assertDisplayPermissionRecheck(t, concrete, ctx, permissionCalls)
	assertDisplayDiscovery(t, surface, ctx)
}

func assertDisplayPermissionRecheck(t *testing.T, surface *hostDisplaySurface, ctx context.Context, permissionCalls int) {
	t.Helper()
	got, err := surface.RecheckScreenRecordingPermission(ctx)
	if surface.ScreenRecordingPermissionRecheckSupported() {
		if err != nil || got.State != DisplayPermissionGranted {
			t.Fatalf("surface permission recheck = %+v, %v", got, err)
		}
		if permissionCalls == 0 {
			t.Fatal("surface permission seam was not exercised")
		}
		return
	}
	if err != nil || got.State != DisplayPermissionUnavailable {
		t.Fatalf("unsupported surface permission recheck = %+v, %v", got, err)
	}
}

func assertDisplayDiscovery(t *testing.T, surface DisplaySurface, ctx context.Context) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	capability, err := surface.Probe(ctx)
	if err != nil || !capability.Usable() || capability.DisplayCount != 1 {
		t.Fatalf("surface Probe = %+v, %v", capability, err)
	}
	if count, err := surface.DisplayCount(ctx); err != nil || count != 1 {
		t.Fatalf("surface DisplayCount = %d, %v", count, err)
	}
	bounds, err := surface.Bounds(ctx, 0)
	if err != nil || bounds.Empty() {
		t.Fatalf("surface Bounds = %v, %v", bounds, err)
	}
}

func TestDisplaySurfaceCancellationAndDefaults(t *testing.T) {
	ctx := context.Background()
	process := testDisplayProcess()
	surface := NewHostDisplaySurfaceWithOptions(HostDisplaySurfaceOptions{
		Process: process,
		PermissionChecker: DisplayPermissionCheckerFunc(func(context.Context) (DisplayPermission, error) {
			return DisplayPermission{State: DisplayPermissionGranted}, nil
		}),
		Capturer: testDisplayCapturer(),
	})
	deniedSurface := NewHostDisplaySurfaceWithOptions(HostDisplaySurfaceOptions{
		Process: process,
		PermissionChecker: DisplayPermissionCheckerFunc(func(context.Context) (DisplayPermission, error) {
			return DisplayPermission{State: DisplayPermissionDenied, Reason: "denied for test"}, nil
		}),
	})
	if capability, err := deniedSurface.Probe(ctx); err == nil || capability.State != ScreenCaptureDenied {
		t.Fatalf("denied surface Probe = %+v, %v", capability, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if capability, err := surface.Probe(canceled); err == nil || capability.State != ScreenCaptureCanceled {
		t.Fatalf("canceled surface Probe = %+v, %v", capability, err)
	}

	readerCtx, cancelReader := context.WithCancel(ctx)
	reader := contextReader{ctx: readerCtx, r: cancelingReader{cancel: cancelReader}}
	buffer := make([]byte, 8)
	if n, err := reader.Read(buffer); n != 7 || !errors.Is(err, context.Canceled) {
		t.Fatalf("context reader = (%d, %v), want read plus cancellation", n, err)
	}
	if _, err := (contextReader{ctx: canceled, r: strings.NewReader("ignored")}).Read(buffer); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled context reader = %v", err)
	}
	if _, err := io.Copy(io.Discard, strings.NewReader("ordinary")); err != nil {
		t.Fatal(err)
	}

	if got := normalizeDisplayProcess(nil); got == nil {
		t.Fatal("normalizeDisplayProcess(nil) returned nil")
	}
	if got := NewHostDisplaySurface(); got == nil {
		t.Fatal("NewHostDisplaySurface returned nil")
	}
}

func testDisplayProcess() DisplayProcessAdapter {
	return DisplayProcessAdapter{
		RunFunc: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			switch name {
			case "xrandr":
				return []byte("Monitors: 1\n"), nil
			case "xdotool":
				return []byte("1920 1080\n"), nil
			case "system_profiler":
				return []byte("Resolution: 1920 x 1080\n"), nil
			default:
				return nil, fmt.Errorf("unexpected display command %q", name)
			}
		},
		LookPathFunc: func(file string) (string, error) { return "/usr/bin/" + file, nil },
	}
}

func testDisplayCapturer() DisplayCapturerFunc {
	return func(_ context.Context, _ int, bounds image.Rectangle) (*image.RGBA, error) {
		return image.NewRGBA(bounds), nil
	}
}

func TestDisplaySurfaceReportsUnavailableDiscovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows discovery boundary is a host API, not a process seam")
	}
	process := DisplayProcessAdapter{
		RunFunc: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "xrandr" || name == "system_profiler" {
				return nil, errors.New("discovery failed")
			}
			return nil, nil
		},
		LookPathFunc: func(string) (string, error) { return "", errors.New("missing capture command") },
	}
	surface := NewHostDisplaySurfaceWithOptions(HostDisplaySurfaceOptions{
		Process: process,
		PermissionChecker: DisplayPermissionCheckerFunc(func(context.Context) (DisplayPermission, error) {
			return DisplayPermission{State: DisplayPermissionGranted}, nil
		}),
	})
	capability, err := surface.Probe(context.Background())
	if err == nil || capability.State != ScreenCaptureUnavailable {
		t.Fatalf("failed discovery Probe = %+v, %v", capability, err)
	}
}

type cancelingReader struct {
	cancel context.CancelFunc
	done   bool
}

func (r cancelingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	copy(p, "capture")
	r.cancel()
	return len("capture"), nil
}

var _ runtimeTools.DisplayCapabilityProbe = DisplayCapabilityProbeFunc(nil)
