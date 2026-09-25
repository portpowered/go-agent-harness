package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimeDevicesWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	captureReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func newTestRoomRunCommand(globalFlags *flags.GlobalFlags, registry devicegw.DeviceRegistry) *RoomRunCommand {
	return NewRoomRunCommand(globalFlags, servicewire.NewRoomServiceWithDevices(
		nil, runtimeDevicesWire.NewService(registry, audioiowire.NewService()), clock.Real{},
		servicewire.NewRoomReplayService(captureReplayWire.NewService()),
		servicewire.NewRoomEvidenceService(), servicewire.NewRoomLatencyService(),
	), registry)
}

// closeForTest closes a test-owned resource whose close must succeed.
func closeForTest(t testing.TB, closeFn func() error) {
	t.Helper()
	if err := closeFn(); err != nil {
		t.Errorf("close test resource: %v", err)
	}
}

// releaseForTest runs teardown whose error cannot change the test verdict:
// the resource may already be closed by the code under test, or its peer
// may have hung up first.
func releaseForTest(release func() error) {
	if err := release(); err != nil {
		return
	}
}

// mustJSONMarshal encodes a test fixture value.
func mustJSONMarshal(t testing.TB, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	return encoded
}

// jsonSlice asserts that a decoded JSON value is an array.
func jsonSlice(t testing.TB, value any) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("decoded JSON value %v (%T) is not an array", value, value)
	}
	return items
}

// jsonObject asserts that a decoded JSON value is an object.
func jsonObject(t testing.TB, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("decoded JSON value %v (%T) is not an object", value, value)
	}
	return object
}

// jsonString asserts that a decoded JSON value is a string.
func jsonString(t testing.TB, value any) string {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("decoded JSON value %v (%T) is not a string", value, value)
	}
	return text
}

// jsonText returns a decoded JSON string, or "" when the field is absent or
// not a string.
func jsonText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// requireFixtureStep fails the test when a fixture step fails.
func requireFixtureStep(t testing.TB, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

// scriptedBrowserHandle asserts that a scripted runtime opened its own handle type.
func scriptedBrowserHandle(t testing.TB, value any) *testkit.ScriptedBrowserHandle {
	t.Helper()
	handle, ok := value.(*testkit.ScriptedBrowserHandle)
	if !ok {
		t.Fatalf("browser handle = %T, want *testkit.ScriptedBrowserHandle", value)
	}
	return handle
}

// fixtureJSON encodes a static fixture value from a context without a test
// handle. Static fixture values always encode, so a failure is a fixture
// defect and panics.
func fixtureJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode fixture %T: %v", value, err))
	}
	return encoded
}

// encodeFixtureJSON writes a fixture HTTP response body. A failed write
// surfaces to the client under test as a transport or decode failure, so the
// fixture handler has nothing further to report.
func encodeFixtureJSON(writer io.Writer, value any) {
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return
	}
}

// writeFixtureText writes a formatted fixture response; see encodeFixtureJSON
// for why a write failure is left to the client under test.
func writeFixtureText(writer io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(writer, format, args...); err != nil {
		return
	}
}

func assertAskFlagError(t *testing.T, err error, wantMessage string, wantIs error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("error = %v, want message containing %q", err, wantMessage)
	}
	if wantIs != nil && !errors.Is(err, wantIs) {
		t.Fatalf("error = %v, want wrapped identity %v", err, wantIs)
	}
}
