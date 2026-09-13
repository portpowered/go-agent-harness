package main

import "testing"

func TestExternalAudioOutputContract(t *testing.T) {
	if err := run(); err != nil {
		t.Fatal(err)
	}
}
