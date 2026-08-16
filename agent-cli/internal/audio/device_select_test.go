package audio_test

import (
	"testing"

	audio "github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/stretchr/testify/require"
)

func TestResolveSelectionRows(t *testing.T) {
	rows := []struct {
		name, input, output   string
		wantInput, wantOutput audio.DeviceID
		lists, defaults       int
	}{
		{"defaults", "", "", "virtual:input-default", "virtual:output-default", 0, 2},
		{"input only", "virtual:input-choice", "", "virtual:input-choice", "virtual:output-default", 1, 1},
		{"output only", "", "virtual:output-choice", "virtual:input-default", "virtual:output-choice", 1, 1},
		{"both names", "desk", "MONITOR", "virtual:input-choice", "virtual:output-choice", 2, 0},
		{"exact ID", "virtual:input-exact", "", "virtual:input-exact", "virtual:output-default", 1, 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := newSelectionRegistry(t)
			got, err := audio.ResolveDeviceSelection(r, audio.DeviceSelectionRequest{InputSelector: row.input, OutputSelector: row.output})
			require.NoError(t, err)
			require.True(t, got.InputSelected && got.OutputSelected)
			require.Equal(t, row.wantInput, got.Input.ID)
			require.Equal(t, row.wantOutput, got.Output.ID)
			require.Equal(t, audio.DeviceLossPolicyFail, got.LossPolicy)
			o := r.observations()
			require.Equal(t, row.lists, o.ListCalls)
			require.Equal(t, row.defaults, o.DefaultCalls)
		})
	}
}

func TestSelectionFailures(t *testing.T) {
	t.Run("unknown exact ID", func(t *testing.T) {
		r := newSelectionRegistry(t)
		_, err := audio.ResolveDeviceSelection(r, audio.DeviceSelectionRequest{InputSelector: "virtual:gone"})
		var typed *audio.DeviceNotFoundError
		require.ErrorAs(t, err, &typed)
		require.ErrorIs(t, err, audio.ErrDeviceNotFound)
		require.Equal(t, audio.DeviceID("virtual:gone"), typed.ID)
		require.Contains(t, err.Error(), `"virtual:gone"`)
		require.Zero(t, r.observations().OpenCount)
	})
	t.Run("ambiguous name lists sorted candidates", func(t *testing.T) {
		r := newSelectionRegistry(t)
		_, err := audio.ResolveDeviceSelection(r, audio.DeviceSelectionRequest{InputSelector: "shared"})
		var typed *audio.AmbiguousDeviceNameError
		require.ErrorAs(t, err, &typed)
		require.ErrorIs(t, err, audio.ErrAmbiguousDeviceName)
		require.Equal(t, []audio.DeviceID{"virtual:input-amb-a", "virtual:input-amb-b"}, []audio.DeviceID{typed.Candidates[0].ID, typed.Candidates[1].ID})
		require.Contains(t, err.Error(), "Shared Microphone A")
		require.Contains(t, err.Error(), "Shared Microphone B")
		require.Zero(t, r.observations().OpenCount)
	})
	for _, direction := range []audio.Direction{audio.DirectionInput, audio.DirectionOutput} {
		t.Run("missing "+direction.String()+" default", func(t *testing.T) {
			r := newSelectionRegistry(t)
			delete(r.defaults, direction)
			req := audio.DeviceSelectionRequest{InputSelector: "virtual:input-choice"}
			if direction == audio.DirectionInput {
				req.InputSelector = ""
			}
			_, err := audio.ResolveDeviceSelection(r, req)
			var typed *audio.NoDefaultDeviceError
			require.ErrorAs(t, err, &typed)
			require.ErrorIs(t, err, audio.ErrNoDefaultDevice)
			require.Equal(t, direction, typed.Direction)
			require.Contains(t, err.Error(), "agent devices list")
			require.Zero(t, r.observations().OpenCount)
		})
	}
}

func TestSelectionValidationAndAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  audio.DeviceSelectionRequest
		want error
	}{
		{"invalid policy", audio.DeviceSelectionRequest{OnDeviceLoss: "retry"}, audio.ErrInvalidDeviceLossPolicy},
		{"file/device conflict", audio.DeviceSelectionRequest{AudioInFile: "fixture.wav", InputSelector: "virtual:input-choice"}, audio.ErrDeviceSelectionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSelectionRegistry(t)
			_, err := audio.ResolveDeviceSelection(r, tc.req)
			require.ErrorIs(t, err, tc.want)
			require.Zero(t, r.observations().ListCalls+r.observations().DefaultCalls+r.observations().OpenCount)
			if tc.name == "file/device conflict" {
				require.Contains(t, err.Error(), "--audio-in")
				require.Contains(t, err.Error(), "--audio-in-device")
			}
		})
	}
	t.Run("file input skips input device", func(t *testing.T) {
		r := newSelectionRegistry(t)
		got, err := audio.ResolveDeviceSelection(r, audio.DeviceSelectionRequest{AudioInConfigured: true})
		require.NoError(t, err)
		require.False(t, got.InputSelected)
		require.Empty(t, got.Input.ID)
		require.Equal(t, audio.DeviceID("virtual:output-default"), got.Output.ID)
		require.Equal(t, 1, r.observations().DefaultCalls)
	})
	t.Run("open closes success and later failure", func(t *testing.T) {
		r := newSelectionRegistry(t)
		got, err := audio.OpenDeviceSelection(r, audio.DeviceSelectionRequest{InputSelector: "virtual:input-choice", OutputSelector: "virtual:output-choice"})
		require.NoError(t, err)
		require.NotNil(t, got.InputHandle)
		require.NotNil(t, got.OutputHandle)
		require.Equal(t, 2, r.observations().OpenCount)
		require.NoError(t, got.Close())
		require.NoError(t, got.Close())
		require.Equal(t, 2, r.observations().ReleaseCount)
		r = newSelectionRegistry(t)
		failing := &failOpenRegistry{fixtureRegistry: r, id: "virtual:output-choice"}
		_, err = audio.OpenDeviceSelection(failing, audio.DeviceSelectionRequest{InputSelector: "virtual:input-choice", OutputSelector: "virtual:output-choice"})
		require.ErrorIs(t, err, audio.ErrDeviceNotFound)
		require.Equal(t, 1, r.observations().OpenCount)
		require.Equal(t, 1, r.observations().ReleaseCount)
	})
}

func TestHandleDeviceLossPolicies(t *testing.T) {
	for _, policy := range []audio.DeviceLossPolicy{"", audio.DeviceLossPolicyFail} {
		t.Run("fail "+string(policy), func(t *testing.T) {
			r := newSelectionRegistry(t)
			selection, err := audio.OpenDeviceSelection(r, audio.DeviceSelectionRequest{})
			require.NoError(t, err)
			r.remove(selection.Input.ID)
			before := r.observations()
			result, err := audio.HandleDeviceLoss(r, selection.Input, policy)
			var typed *audio.DeviceLostError
			require.ErrorAs(t, err, &typed)
			require.ErrorIs(t, err, audio.ErrDeviceLost)
			require.Equal(t, selection.Input.ID, typed.ID)
			require.Equal(t, audio.DirectionInput, typed.Direction)
			require.Equal(t, audio.DeviceLossOutcomeFailed, result.Outcome)
			require.Nil(t, result.Handle)
			require.Equal(t, before, r.observations())
			require.NoError(t, selection.Close())
		})
	}
	t.Run("default opens current replacement", func(t *testing.T) {
		r := newSelectionRegistry(t)
		selection, err := audio.OpenDeviceSelection(r, audio.DeviceSelectionRequest{})
		require.NoError(t, err)
		lost := selection.Input
		r.remove(lost.ID)
		r.defaults[audio.DirectionInput] = "virtual:input-replacement"
		result, err := audio.HandleDeviceLoss(r, lost, audio.DeviceLossPolicyDefault)
		require.NoError(t, err)
		require.Equal(t, audio.DeviceLossOutcomeDefaulted, result.Outcome)
		require.Equal(t, audio.DeviceID("virtual:input-replacement"), result.Device.ID)
		require.NotNil(t, result.Handle)
		require.Equal(t, 3, r.observations().OpenCount)
		require.Equal(t, 3, r.observations().DefaultCalls)
		require.NoError(t, result.Close())
		require.NoError(t, selection.Close())
	})
	t.Run("stop has no fallback acquisition", func(t *testing.T) {
		r := newSelectionRegistry(t)
		selection, err := audio.OpenDeviceSelection(r, audio.DeviceSelectionRequest{})
		require.NoError(t, err)
		r.remove(selection.Input.ID)
		before := r.observations()
		result, err := audio.HandleDeviceLoss(r, selection.Input, audio.DeviceLossPolicyStop)
		require.NoError(t, err)
		require.Equal(t, audio.DeviceLossOutcomeStopped, result.Outcome)
		require.Nil(t, result.Handle)
		require.Equal(t, before, r.observations())
		require.NoError(t, selection.Close())
	})
}

func TestDefaultLossFailures(t *testing.T) {
	for _, tc := range []struct {
		name, defaultID string
		stale           bool
		want            error
	}{
		{"default disappears", "", false, audio.ErrNoDefaultDevice},
		{"default is lost device", "virtual:input-default", true, audio.ErrDeviceLost},
		{"replacement is unavailable", "virtual:input-replacement", true, audio.ErrDeviceNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSelectionRegistry(t)
			selection, err := audio.OpenDeviceSelection(r, audio.DeviceSelectionRequest{})
			require.NoError(t, err)
			lost := selection.Input
			stale := r.devices[tc.defaultID]
			r.remove(lost.ID)
			if tc.defaultID != "" {
				r.defaults[audio.DirectionInput] = tc.defaultID
				if tc.want == audio.ErrDeviceNotFound {
					r.remove(tc.defaultID)
				}
			}
			var registry audio.DeviceRegistry = r
			if tc.stale {
				registry = &staleDefaultRegistry{fixtureRegistry: r, device: stale}
			}
			before := r.observations().OpenCount
			result, err := audio.HandleDeviceLoss(registry, lost, audio.DeviceLossPolicyDefault)
			require.ErrorIs(t, err, tc.want)
			require.Equal(t, audio.DeviceLossOutcomeFailed, result.Outcome)
			require.Nil(t, result.Handle)
			require.Equal(t, before, r.observations().OpenCount)
			require.NoError(t, selection.Close())
		})
	}
}

func newSelectionRegistry(t *testing.T) *fixtureRegistry {
	t.Helper()
	r := newFixture().Registry.(*fixtureRegistry)
	for _, d := range []audio.Device{
		mustFixtureDevice("input-choice", "Desk virtual:input-exact microphone", audio.DirectionInput),
		mustFixtureDevice("input-exact", "virtual:input-exact", audio.DirectionInput),
		mustFixtureDevice("input-replacement", "Replacement Microphone", audio.DirectionInput),
		mustFixtureDevice("input-amb-a", "Shared Microphone A", audio.DirectionInput),
		mustFixtureDevice("input-amb-b", "Shared Microphone B", audio.DirectionInput),
		mustFixtureDevice("output-choice", "Monitor Speaker", audio.DirectionOutput),
	} {
		r.devices[d.ID] = d
	}
	return r
}

type failOpenRegistry struct {
	*fixtureRegistry
	id audio.DeviceID
}

func (r *failOpenRegistry) Open(id audio.DeviceID) (audio.OpenedDevice, error) {
	if id == r.id {
		return nil, audio.NewDeviceNotFoundError(id)
	}
	return r.fixtureRegistry.Open(id)
}

type staleDefaultRegistry struct {
	*fixtureRegistry
	device audio.Device
}

func (r *staleDefaultRegistry) Default(direction audio.Direction) (audio.Device, error) {
	if r.device.Direction == direction {
		return r.device, nil
	}
	return r.fixtureRegistry.Default(direction)
}
