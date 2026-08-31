package cli

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type sessionDisplaySurfaceFake struct {
	capability tools.DisplayCapability
	probeErr   error
	probes     int
	captures   int
}

func (s *sessionDisplaySurfaceFake) Probe(context.Context) (tools.DisplayCapability, error) {
	s.probes++
	return s.capability, s.probeErr
}

func (*sessionDisplaySurfaceFake) DisplayCount(context.Context) (int, error) { return 1, nil }

func (*sessionDisplaySurfaceFake) Bounds(context.Context, int) (image.Rectangle, error) {
	return image.Rect(0, 0, 1, 1), nil
}

func (s *sessionDisplaySurfaceFake) Capture(context.Context, image.Rectangle) (*image.RGBA, error) {
	s.captures++
	return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
}

func displayAdmissionConfig() *config.Config {
	browser := config.DefaultBrowserConfig()
	return &config.Config{
		Browser: browser,
		Tools: config.ToolsConfig{List: []config.ToolEntry{
			{ID: "show", Enabled: true},
			{ID: "mouse", Enabled: true},
			{ID: "read_file", Enabled: true},
		}},
	}
}

// TestSessionToolCapabilitiesFactoryOmitsDisplayToolsOnHeadlessProbe is #297's
// original de-advertisement regression, preserved unchanged: a capability
// that cannot prove a display exists at all (headless Linux CI, no desktop
// session) must still omit show/mouse. Only the permission-denied-but
// -grantable case (see the Advertises... test above) was fixed to keep
// advertising.
func TestSessionToolCapabilitiesFactoryOmitsDisplayToolsOnHeadlessProbe(t *testing.T) {
	surface := &sessionDisplaySurfaceFake{capability: tools.UnavailableDisplayCapability("no desktop session")}
	factory := NewSessionToolCapabilitiesFactoryWithDisplaySurface(nil, nil, surface)

	capabilities, err := factory(displayAdmissionConfig())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if capabilities.DisplayCapability.Usable() || capabilities.DisplayCapability.State != tools.DisplayCapabilityUnavailable {
		t.Fatalf("display capability = %+v, want explicit unavailable state", capabilities.DisplayCapability)
	}
	for _, definition := range capabilities.Definitions {
		if definition.Name == "show" || definition.Name == "mouse" {
			t.Fatalf("headless definition retained display tool %q", definition.Name)
		}
	}
	if _, err := capabilities.Executor.Execute(context.Background(), messages.ToolCall{ID: "headless-show", Name: "show"}); err == nil || !errors.Is(err, tools.ErrToolNotFound) {
		t.Fatalf("headless show route result = %v, want absent route", err)
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "read_file"); !ok {
		t.Fatal("headless admission dropped unrelated read_file")
	}
	if surface.probes != 1 || surface.captures != 0 {
		t.Fatalf("admission surface calls = probes:%d captures:%d, want probe only", surface.probes, surface.captures)
	}
}

func TestSessionToolCapabilitiesFactoryRetainsShowOnUsableProbe(t *testing.T) {
	surface := &sessionDisplaySurfaceFake{capability: tools.UsableDisplayCapability(1)}
	factory := NewSessionToolCapabilitiesFactoryWithDisplaySurface(nil, nil, surface)

	capabilities, err := factory(displayAdmissionConfig())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !capabilities.DisplayCapability.Usable() {
		t.Fatalf("display capability = %+v, want usable", capabilities.DisplayCapability)
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "show"); !ok {
		t.Fatal("usable admission omitted show")
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "mouse"); !ok {
		t.Fatal("usable admission omitted mouse")
	}
	if surface.probes != 1 || surface.captures != 0 {
		t.Fatalf("admission surface calls = probes:%d captures:%d, want probe only", surface.probes, surface.captures)
	}
}

// TestSessionToolCapabilitiesFactoryAdvertisesShowWhenPermissionDeniedWithDisplay
// guards the regression this branch fixes: PR #297's "gate display tools by
// capability" de-advertised show/mouse whenever the display was not
// immediately Usable, which included the permission-denied-but-grantable
// case on a real macOS host with a display but no Screen Recording grant.
// That left the model with no tool to invoke and no way to relay PR #301's
// invocation-time grant-instructions envelope. This mirrors the real
// hostDisplaySurface.Probe contract: on permission denial it returns both a
// non-nil error AND a capability whose State is Denied (not Unavailable),
// with Available/DisplayCount left at their zero values.
func TestSessionToolCapabilitiesFactoryAdvertisesShowWhenPermissionDeniedWithDisplay(t *testing.T) {
	surface := &sessionDisplaySurfaceFake{
		capability: tools.DisplayCapability{
			State:  tools.ScreenCaptureDenied,
			Reason: "CGPreflightScreenCaptureAccess reported that Screen Recording access is denied",
		},
		probeErr: &tools.ScreenCaptureError{
			State:     tools.ScreenCaptureDenied,
			Operation: "screen recording permission check",
			Reason:    "CGPreflightScreenCaptureAccess reported that Screen Recording access is denied",
		},
	}
	factory := NewSessionToolCapabilitiesFactoryWithDisplaySurface(nil, nil, surface)

	capabilities, err := factory(displayAdmissionConfig())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if capabilities.DisplayCapability.Usable() {
		t.Fatalf("display capability = %+v, want not usable (permission denied)", capabilities.DisplayCapability)
	}
	if capabilities.DisplayCapability.State != tools.ScreenCaptureDenied {
		t.Fatalf("display capability state = %q, want denied preserved (not flattened to unavailable)", capabilities.DisplayCapability.State)
	}
	if !capabilities.DisplayCapability.Advertisable() {
		t.Fatalf("display capability = %+v, want advertisable despite denied permission", capabilities.DisplayCapability)
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "show"); !ok {
		t.Fatal("permission-denied-with-display admission omitted show; the model can never reach the invocation-time permission envelope")
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "mouse"); !ok {
		t.Fatal("permission-denied-with-display admission omitted mouse")
	}

	// Invoking the advertised show tool must return #301's actionable
	// permission-denied envelope, not a bare "tool not found" route.
	_, execErr := capabilities.Executor.Execute(context.Background(), messages.ToolCall{ID: "show-denied", Name: "show", Arguments: `{"action":"screenshot"}`})
	if execErr == nil {
		t.Fatal("show invocation with denied permission succeeded, want a typed denial")
	}
	if errors.Is(execErr, tools.ErrToolNotFound) {
		t.Fatal("show was not routable even though it was advertised")
	}
	envelope, decodeErr := sight.Decode([]byte(tools.ScreenToolErrorResult(execErr)))
	if decodeErr != nil {
		t.Fatalf("decode show denial envelope: %v", decodeErr)
	}
	if envelope.Status != sight.StatusError || envelope.Source != sight.SourceScreen || envelope.ErrorCode != tools.ScreenRecordingPermissionDeniedErrorCode {
		t.Fatalf("show denial envelope = %+v, want a screen_recording_permission_denied error", envelope)
	}
	for _, want := range []string{
		"System Settings",
		"Privacy & Security",
		"Screen & System Audio Recording",
		"restart",
		"Tell the customer",
	} {
		if !strings.Contains(envelope.Error, want) {
			t.Errorf("show denial envelope error %q does not contain %q", envelope.Error, want)
		}
	}
}

// TestSessionToolCapabilitiesFactoryCapturesNormallyWhenPermissionGranted
// covers the unaffected happy path: when the display is present and Screen
// Recording permission is granted, show remains advertised (as before this
// change) and a real invocation still produces a normal, successful capture
// -- proving the gating fix did not loosen anything on the granted path.
func TestSessionToolCapabilitiesFactoryCapturesNormallyWhenPermissionGranted(t *testing.T) {
	surface := &sessionDisplaySurfaceFake{capability: tools.UsableDisplayCapability(1)}
	factory := NewSessionToolCapabilitiesFactoryWithDisplaySurface(nil, nil, surface)

	capabilities, err := factory(displayAdmissionConfig())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !capabilities.DisplayCapability.Usable() {
		t.Fatalf("display capability = %+v, want usable", capabilities.DisplayCapability)
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "show"); !ok {
		t.Fatal("usable admission omitted show")
	}

	response, execErr := capabilities.Executor.Execute(context.Background(), messages.ToolCall{ID: "show-granted", Name: "show", Arguments: `{"action":"screenshot"}`})
	if execErr != nil {
		t.Fatalf("show invocation with granted permission failed: %v", execErr)
	}
	if len(response.ContentParts) != 2 {
		t.Fatalf("granted show response = %#v, want correlated metadata plus image", response)
	}
	result, decodeErr := sight.Decode([]byte(response.Content))
	if decodeErr != nil {
		t.Fatalf("decode show success envelope: %v", decodeErr)
	}
	if result.Status != sight.StatusSuccess || result.Source != sight.SourceScreen {
		t.Fatalf("granted show result = %+v, want success", result)
	}
	if surface.captures != 1 {
		t.Fatalf("granted show surface captures = %d, want 1", surface.captures)
	}
}

func TestSessionDisplayAdmissionProbeIsBoundedAndFailsClosed(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	probe := tools.DisplayCapabilityProbeFunc(func(context.Context) (tools.DisplayCapability, error) {
		close(started)
		<-release
		return tools.UnavailableDisplayCapability("released after timeout"), nil
	})
	t.Cleanup(func() { close(release) })
	factory := NewSessionToolCapabilitiesFactoryWithDisplayProbe(nil, nil, probe)
	startedAt := time.Now()
	capabilities, err := factory(displayAdmissionConfig())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 2*sessionDisplayCapabilityProbeTimeout {
		t.Fatalf("bounded probe took %s", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("display probe was not started")
	}
	if capabilities.DisplayCapability.Usable() {
		t.Fatalf("timed-out probe admitted display tools: %+v", capabilities.DisplayCapability)
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "show"); ok {
		t.Fatal("timed-out probe retained show")
	}
}

func TestSessionToolDiagnosticSinkWritesTypedOperatorDetail(t *testing.T) {
	var stderr strings.Builder
	sessionToolDiagnosticSink(&stderr).RecordSessionToolDiagnostic(services.SessionToolDiagnostic{
		ToolCallID: "show-screen-call",
		ToolName:   tools.HostDisplayToolID,
		Source:     sight.SourceScreen,
		ErrorCode:  tools.ScreenRecordingPermissionDeniedErrorCode,
		Error: &tools.ScreenCaptureError{
			State:     tools.ScreenCaptureDenied,
			Operation: "show",
			Reason:    "screen recording permission denied",
		},
	})
	got := stderr.String()
	for _, want := range []string{
		`tool="show_screen"`,
		`call_id="show-screen-call"`,
		`source="screen"`,
		`error_code="screen_recording_permission_denied"`,
		"System Settings → Privacy & Security → Screen & System Audio Recording",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr diagnostic %q does not contain %q", got, want)
		}
	}
}

func findSessionDefinition(definitions []messages.ToolDefinition, name string) (messages.ToolDefinition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return messages.ToolDefinition{}, false
}

var _ tools.DisplaySurface = (*sessionDisplaySurfaceFake)(nil)
