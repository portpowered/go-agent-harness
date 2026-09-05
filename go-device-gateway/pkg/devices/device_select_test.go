package devices_test

import (
	"testing"

	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/stretchr/testify/require"
)

func TestResolveSelectionRows(t *testing.T) {
	rows := []struct {
		name, input, output   string
		wantInput, wantOutput devicegw.DeviceID
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
			got, err := devicegw.ResolveDeviceSelection(r, devicegw.DeviceSelectionRequest{InputSelector: row.input, OutputSelector: row.output, AudioInConfigured: row.wantInput == ""})
			require.NoError(t, err)
			require.Equal(t, row.wantInput != "", got.InputSelected)
			require.True(t, got.OutputSelected)
			require.Equal(t, row.wantInput, got.Input.ID)
			require.Equal(t, row.wantOutput, got.Output.ID)
			require.Equal(t, devicegw.DeviceLossPolicyFail, got.LossPolicy)
			o := r.observations()
			require.Equal(t, row.lists, o.ListCalls)
			require.Equal(t, row.defaults, o.DefaultCalls)
		})
	}
}
func TestSelectionFailures(t *testing.T) {
	t.Run("unknown exact ID", func(t *testing.T) {
		r := newSelectionRegistry(t)
		_, err := devicegw.ResolveDeviceSelection(r, devicegw.DeviceSelectionRequest{InputSelector: "virtual:gone"})
		var typed *devicegw.DeviceNotFoundError
		require.ErrorAs(t, err, &typed)
		require.ErrorIs(t, err, devicegw.ErrDeviceNotFound)
		require.Equal(t, devicegw.DeviceID("virtual:gone"), typed.ID)
		require.Contains(t, err.Error(), `"virtual:gone"`)
		require.Zero(t, r.observations().OpenCount)
	})
	t.Run("ambiguous name lists sorted candidates", func(t *testing.T) {
		r := newSelectionRegistry(t)
		_, err := devicegw.ResolveDeviceSelection(r, devicegw.DeviceSelectionRequest{InputSelector: "shared"})
		var typed *devicegw.AmbiguousDeviceNameError
		require.ErrorAs(t, err, &typed)
		require.ErrorIs(t, err, devicegw.ErrAmbiguousDeviceName)
		require.Equal(t, []devicegw.DeviceID{"virtual:input-amb-a", "virtual:input-amb-b"}, []devicegw.DeviceID{typed.Candidates[0].ID, typed.Candidates[1].ID})
		require.Contains(t, err.Error(), "Shared Microphone A")
		require.Contains(t, err.Error(), "Shared Microphone B")
		require.Zero(t, r.observations().OpenCount)
	})
	for _, direction := range []devicegw.Direction{devicegw.DirectionInput, devicegw.DirectionOutput} {
		t.Run("missing "+direction.String()+" default", func(t *testing.T) {
			r := newSelectionRegistry(t)
			delete(r.defaults, direction)
			req := devicegw.DeviceSelectionRequest{InputSelector: "virtual:input-choice"}
			if direction == devicegw.DirectionInput {
				req.InputSelector = ""
			}
			_, err := devicegw.ResolveDeviceSelection(r, req)
			var typed *devicegw.NoDefaultDeviceError
			require.ErrorAs(t, err, &typed)
			require.ErrorIs(t, err, devicegw.ErrNoDefaultDevice)
			require.Equal(t, direction, typed.Direction)
			require.Contains(t, err.Error(), "agent devices list")
			require.Zero(t, r.observations().OpenCount)
		})
	}
}
func TestSelectionValidationAndAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  devicegw.DeviceSelectionRequest
		want error
	}{
		{"invalid policy", devicegw.DeviceSelectionRequest{OnDeviceLoss: "retry"}, devicegw.ErrInvalidDeviceLossPolicy},
		{"file/device conflict", devicegw.DeviceSelectionRequest{AudioInFile: "fixture.wav", InputSelector: "virtual:input-choice"}, devicegw.ErrDeviceSelectionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSelectionRegistry(t)
			_, err := devicegw.ResolveDeviceSelection(r, tc.req)
			require.ErrorIs(t, err, tc.want)
			o := r.observations()
			require.Equal(t, 0, o.ListCalls+o.DefaultCalls+o.OpenCount)
			if tc.want == devicegw.ErrDeviceSelectionConflict {
				require.Contains(t, err.Error(), "--audio-in")
				require.Contains(t, err.Error(), "--audio-in-device")
			}
		})
	}
	t.Run("open closes idempotently and cleans partial acquisition", func(t *testing.T) {
		r := newSelectionRegistry(t)
		got, err := devicegw.OpenDeviceSelection(r, devicegw.DeviceSelectionRequest{InputSelector: "virtual:input-choice", OutputSelector: "virtual:output-choice"})
		require.NoError(t, err)
		require.NoError(t, got.Close())
		require.NoError(t, got.Close())
		require.Equal(t, devicegw.DeviceRegistryObservations{ListCalls: 2, OpenCount: 2, ReleaseCount: 2}, r.observations())

		r = newSelectionRegistry(t)
		r.inUse["virtual:output-choice"] = true
		_, err = devicegw.OpenDeviceSelection(r, devicegw.DeviceSelectionRequest{InputSelector: "virtual:input-choice", OutputSelector: "virtual:output-choice"})
		require.ErrorIs(t, err, devicegw.ErrDeviceInUse)
		require.Equal(t, devicegw.DeviceRegistryObservations{ListCalls: 2, OpenCount: 1, ReleaseCount: 1}, r.observations())
	})
}
func TestHandleDeviceLossPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, replacement  string
		policy             devicegw.DeviceLossPolicy
		unavailable, stale bool
		wantOutcome        devicegw.DeviceLossOutcome
		wantErr            error
	}{
		{"fail by default", "", "", false, false, devicegw.DeviceLossOutcomeFailed, devicegw.ErrDeviceLost},
		{"fail explicitly", "", devicegw.DeviceLossPolicyFail, false, false, devicegw.DeviceLossOutcomeFailed, devicegw.ErrDeviceLost},
		{"default opens current replacement", "virtual:input-replacement", devicegw.DeviceLossPolicyDefault, false, false, devicegw.DeviceLossOutcomeDefaulted, nil},
		{"stop has no fallback acquisition", "", devicegw.DeviceLossPolicyStop, false, false, devicegw.DeviceLossOutcomeStopped, nil},
		{"default disappears", "", devicegw.DeviceLossPolicyDefault, false, false, devicegw.DeviceLossOutcomeFailed, devicegw.ErrNoDefaultDevice},
		{"default is lost device", "virtual:input-default", devicegw.DeviceLossPolicyDefault, false, true, devicegw.DeviceLossOutcomeFailed, devicegw.ErrDeviceLost},
		{"replacement is unavailable", "virtual:input-replacement", devicegw.DeviceLossPolicyDefault, true, true, devicegw.DeviceLossOutcomeFailed, devicegw.ErrDeviceNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSelectionRegistry(t)
			selection, err := devicegw.OpenDeviceSelection(r, devicegw.DeviceSelectionRequest{})
			require.NoError(t, err)
			lost := selection.Input
			var stale devicegw.Device
			if tc.replacement != "" {
				stale = r.devices[tc.replacement]
			}
			r.remove(lost.ID)
			var registry devicegw.DeviceRegistry = r
			if tc.replacement != "" {
				r.defaults[devicegw.DirectionInput] = tc.replacement
				if tc.unavailable {
					r.remove(tc.replacement)
				}
				if tc.stale {
					registry = &staleDefaultRegistry{fixtureRegistry: r, device: stale}
				}
			}
			before := r.observations()
			result, err := devicegw.HandleDeviceLoss(registry, lost, tc.policy)
			require.Equal(t, lost, result.Lost)
			require.Equal(t, tc.wantOutcome, result.Outcome)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				if tc.wantErr == devicegw.ErrDeviceLost {
					var typed *devicegw.DeviceLostError
					require.ErrorAs(t, err, &typed)
					require.Equal(t, lost.ID, typed.ID)
					require.Equal(t, devicegw.DirectionInput, typed.Direction)
				}
				require.Nil(t, result.Handle)
			} else {
				require.NoError(t, err)
				if tc.replacement != "" {
					require.Equal(t, devicegw.DeviceID(tc.replacement), result.Device.ID)
					require.NotEmpty(t, result.Device.ID)
					require.NotNil(t, result.Handle)
				} else {
					require.Nil(t, result.Handle)
				}
			}
			after := r.observations()
			if tc.policy == devicegw.DeviceLossPolicyDefault {
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
	for _, d := range []devicegw.Device{
		mustFixtureDevice("input-choice", "Desk virtual:input-exact microphone", devicegw.DirectionInput),
		mustFixtureDevice("input-exact", "virtual:input-exact", devicegw.DirectionInput),
		mustFixtureDevice("input-replacement", "Replacement Microphone", devicegw.DirectionInput),
		mustFixtureDevice("input-amb-a", "Shared Microphone A", devicegw.DirectionInput),
		mustFixtureDevice("input-amb-b", "Shared Microphone B", devicegw.DirectionInput),
		mustFixtureDevice("output-choice", "Monitor Speaker", devicegw.DirectionOutput),
	} {
		r.devices[d.ID] = d
	}
	return r
}

type staleDefaultRegistry struct {
	*fixtureRegistry
	device devicegw.Device
}

func (r *staleDefaultRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	if r.device.Direction == direction {
		r.mu.Lock()
		r.defaultCalls++
		r.mu.Unlock()
		return r.device, nil
	}
	return r.fixtureRegistry.Default(direction)
}
