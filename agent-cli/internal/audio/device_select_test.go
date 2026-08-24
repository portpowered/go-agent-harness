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
		{"file input skips input device", "", "", "", "virtual:output-default", 0, 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := newSelectionRegistry(t)
			got, err := audio.ResolveDeviceSelection(r, audio.DeviceSelectionRequest{InputSelector: row.input, OutputSelector: row.output, AudioInConfigured: row.wantInput == ""})
			require.NoError(t, err)
			require.Equal(t, row.wantInput != "", got.InputSelected)
			require.True(t, got.OutputSelected)
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
			o := r.observations()
			require.Equal(t, 0, o.ListCalls+o.DefaultCalls+o.OpenCount)
			if tc.want == audio.ErrDeviceSelectionConflict {
				require.Contains(t, err.Error(), "--audio-in")
				require.Contains(t, err.Error(), "--audio-in-device")
			}
		})
	}
	t.Run("open closes idempotently and cleans partial acquisition", func(t *testing.T) {
		r := newSelectionRegistry(t)
		got, err := audio.OpenDeviceSelection(r, audio.DeviceSelectionRequest{InputSelector: "virtual:input-choice", OutputSelector: "virtual:output-choice"})
		require.NoError(t, err)
		require.NoError(t, got.Close())
		require.NoError(t, got.Close())
		require.Equal(t, audio.DeviceRegistryObservations{ListCalls: 2, OpenCount: 2, ReleaseCount: 2}, r.observations())

		r = newSelectionRegistry(t)
		r.inUse["virtual:output-choice"] = true
		_, err = audio.OpenDeviceSelection(r, audio.DeviceSelectionRequest{InputSelector: "virtual:input-choice", OutputSelector: "virtual:output-choice"})
		require.ErrorIs(t, err, audio.ErrDeviceInUse)
		require.Equal(t, audio.DeviceRegistryObservations{ListCalls: 2, OpenCount: 1, ReleaseCount: 1}, r.observations())
	})
}
func TestHandleDeviceLossPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, replacement  string
		policy             audio.DeviceLossPolicy
		unavailable, stale bool
		wantOutcome        audio.DeviceLossOutcome
		wantErr            error
	}{
		{"fail by default", "", "", false, false, audio.DeviceLossOutcomeFailed, audio.ErrDeviceLost},
		{"fail explicitly", "", audio.DeviceLossPolicyFail, false, false, audio.DeviceLossOutcomeFailed, audio.ErrDeviceLost},
		{"default opens current replacement", "virtual:input-replacement", audio.DeviceLossPolicyDefault, false, false, audio.DeviceLossOutcomeDefaulted, nil},
		{"stop has no fallback acquisition", "", audio.DeviceLossPolicyStop, false, false, audio.DeviceLossOutcomeStopped, nil},
		{"default disappears", "", audio.DeviceLossPolicyDefault, false, false, audio.DeviceLossOutcomeFailed, audio.ErrNoDefaultDevice},
		{"default is lost device", "virtual:input-default", audio.DeviceLossPolicyDefault, false, true, audio.DeviceLossOutcomeFailed, audio.ErrDeviceLost},
		{"replacement is unavailable", "virtual:input-replacement", audio.DeviceLossPolicyDefault, true, true, audio.DeviceLossOutcomeFailed, audio.ErrDeviceNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSelectionRegistry(t)
			selection, err := audio.OpenDeviceSelection(r, audio.DeviceSelectionRequest{})
			require.NoError(t, err)
			lost := selection.Input
			var stale audio.Device
			if tc.replacement != "" {
				stale = r.devices[tc.replacement]
			}
			r.remove(lost.ID)
			var registry audio.DeviceRegistry = r
			if tc.replacement != "" {
				r.defaults[audio.DirectionInput] = tc.replacement
				if tc.unavailable {
					r.remove(tc.replacement)
				}
				if tc.stale {
					registry = &staleDefaultRegistry{fixtureRegistry: r, device: stale}
				}
			}
			before := r.observations()
			result, err := audio.HandleDeviceLoss(registry, lost, tc.policy)
			require.Equal(t, lost, result.Lost)
			require.Equal(t, tc.wantOutcome, result.Outcome)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				if tc.wantErr == audio.ErrDeviceLost {
					var typed *audio.DeviceLostError
					require.ErrorAs(t, err, &typed)
					require.Equal(t, lost.ID, typed.ID)
					require.Equal(t, audio.DirectionInput, typed.Direction)
				}
				require.Nil(t, result.Handle)
			} else {
				require.NoError(t, err)
				if tc.replacement != "" {
					require.Equal(t, audio.DeviceID(tc.replacement), result.Device.ID)
					require.NotEmpty(t, result.Device.ID)
					require.NotNil(t, result.Handle)
				} else {
					require.Nil(t, result.Handle)
				}
			}
			after := r.observations()
			if tc.policy == audio.DeviceLossPolicyDefault {
				require.Equal(t, before.DefaultCalls+1, after.DefaultCalls)
				if tc.wantErr == nil {
					require.Equal(t, before.OpenCount+1, after.OpenCount)
				} else {
					require.Equal(t, before.OpenCount, after.OpenCount)
				}
			} else {
				require.Equal(t, before, after)
			}
			require.NoError(t, result.Close())
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

type staleDefaultRegistry struct {
	*fixtureRegistry
	device audio.Device
}

func (r *staleDefaultRegistry) Default(direction audio.Direction) (audio.Device, error) {
	if r.device.Direction == direction {
		r.mu.Lock()
		r.defaultCalls++
		r.mu.Unlock()
		return r.device, nil
	}
	return r.fixtureRegistry.Default(direction)
}
