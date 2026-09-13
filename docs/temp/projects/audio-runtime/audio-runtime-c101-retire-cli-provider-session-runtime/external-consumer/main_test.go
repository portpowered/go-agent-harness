package main

import "testing"

func TestExternalConsumerUsesIndependentPublicServices(t *testing.T) {
	if err := run(); err != nil {
		t.Fatal(err)
	}
}
