package main

import "testing"

func TestExternalRoomAdmissionContract(t *testing.T) {
	value, err := run()
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if value.Schema != "audio-runtime.c54.public-admission/v1" || value.ConstructedVia != "rooms/wire.NewManifestProviderFromRegistry" {
		t.Fatalf("contract identity = %+v", value)
	}
	if !value.JSONYAMLEqual || !value.FileAdmission || !value.AdmitAliases || !value.RegistrySnapshotIsolated || !value.Lifecycle.Validated || !value.Lifecycle.Closed {
		t.Fatalf("contract results = %+v", value)
	}
}
