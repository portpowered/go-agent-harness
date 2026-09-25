package room

import (
	"fmt"
	"io"
)

// fixtureAs converts a decoded manifest fixture value to its expected shape.
// Fixture documents come from validManifestData, so a mismatch is a test bug
// and fails the test loudly.
func fixtureAs[T any](value any) T {
	typed, ok := value.(T)
	if !ok {
		panic(fmt.Sprintf("manifest fixture value %T is not %T", value, typed))
	}
	return typed
}

// manifestParticipant returns one participant object of a fixture document.
func manifestParticipant(document map[string]any, index int) map[string]any {
	return fixtureAs[map[string]any](fixtureAs[[]any](document["participants"])[index])
}

// manifestRoom returns the room object of a fixture document.
func manifestRoom(document map[string]any) map[string]any {
	return fixtureAs[map[string]any](document["room"])
}

// releaseTestResource closes a mesh or mixer during teardown after the test's
// assertions ran. Tests that care about the close result assert it directly;
// several tests here inject pair close failures on purpose.
func releaseTestResource(resource io.Closer) {
	if err := resource.Close(); err != nil {
		return
	}
}
