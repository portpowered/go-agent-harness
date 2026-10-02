package gateway

import (
	"reflect"
	"testing"
)

func TestInteractionFixtureReplayerFixtureReturnsAnIndependentCopy(t *testing.T) {
	fixture := completeInteractionFixture()
	replayer, err := NewInteractionFixtureReplayer(fixture)
	if err != nil {
		t.Fatal(err)
	}
	got := replayer.Fixture()
	if !reflect.DeepEqual(got, fixture) {
		t.Fatalf("Fixture() = %#v, want %#v", got, fixture)
	}
	got.Events = nil
	if again := replayer.Fixture(); !reflect.DeepEqual(again, fixture) {
		t.Fatal("mutating a returned fixture changed the replayer's copy")
	}
}
