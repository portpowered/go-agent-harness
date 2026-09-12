package main

import "testing"

func TestPublicToolPublicationConsumer(t *testing.T) {
	if err := run(); err != nil {
		t.Fatal(err)
	}
}
