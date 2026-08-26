package audio

import (
	"errors"
	"testing"
)

func TestDeviceAdaptersResolveDirectionalDefaultsAndExposeIDs(t *testing.T) {
	registry := adapterTestRegistry(t)

	source, err := NewDeviceSource(registry, "")
	if err != nil {
		t.Fatalf("NewDeviceSource(default) = %v", err)
	}
	if got, want := source.DeviceID(), DeviceID("virtual:input"); got != want {
		t.Fatalf("source DeviceID() = %q, want %q", got, want)
	}

	sink, err := NewDeviceSink(registry, "")
	if err != nil {
		_ = source.Close()
		t.Fatalf("NewDeviceSink(default) = %v", err)
	}
	if got, want := sink.DeviceID(), DeviceID("virtual:output"); got != want {
		_ = source.Close()
		_ = sink.Close()
		t.Fatalf("sink DeviceID() = %q, want %q", got, want)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	var nilSource *DeviceSource
	if got := nilSource.DeviceID(); got != "" {
		t.Fatalf("nil source DeviceID() = %q, want empty", got)
	}
	var nilSink *DeviceSink
	if got := nilSink.DeviceID(); got != "" {
		t.Fatalf("nil sink DeviceID() = %q, want empty", got)
	}
}

func TestDeviceIDResolutionPreservesDirectionalDefaultFailures(t *testing.T) {
	tests := []struct {
		name      string
		registry  DeviceRegistry
		direction Direction
		want      error
	}{
		{
			name:      "no default",
			registry:  &adapterTestRegistryStub{defaultErr: ErrNoDefaultDevice},
			direction: DirectionInput,
			want:      ErrNoDefaultDevice,
		},
		{
			name: "wrong direction",
			registry: &adapterTestRegistryStub{defaultDevice: Device{
				ID:        "virtual:output",
				Direction: DirectionOutput,
			}},
			direction: DirectionInput,
			want:      ErrDeviceDirectionMismatch,
		},
		{
			name: "empty ID",
			registry: &adapterTestRegistryStub{defaultDevice: Device{
				Direction: DirectionInput,
			}},
			direction: DirectionInput,
			want:      ErrInvalidDevice,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := resolveDeviceIDForOpen(testCase.registry, "", testCase.direction)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("resolveDeviceIDForOpen() = %v, want %v", err, testCase.want)
			}
		})
	}
}
