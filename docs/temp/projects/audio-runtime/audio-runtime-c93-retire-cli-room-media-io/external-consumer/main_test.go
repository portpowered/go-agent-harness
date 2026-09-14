package main

import "testing"

func TestExternalRoomMediaConsumer(t *testing.T) {
	if err := runExternalConsumer(); err != nil {
		t.Fatal(err)
	}
}
