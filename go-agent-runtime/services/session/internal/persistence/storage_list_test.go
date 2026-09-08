package session

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStorage_ListWithOptionsFiltersAndBoundsMetadata(t *testing.T) {
	st := newTestStorage(t)
	base := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)

	for i, id := range []string{"Alpha-older", "beta-match", "ALPHA-newer", "unrelated"} {
		if err := st.Save(id, nil); err != nil {
			t.Fatalf("Save %q: %v", id, err)
		}
		setSessionModTime(t, filepath.Join(st.sessionsDir, "session-"+id+".json"), base.Add(time.Duration(i)*time.Minute))
	}
	// Query metadata even when the session body is not parseable. Listing must
	// not load message bodies just to determine the result set.
	writeRawSession(t, st, "ALPHA-corrupt", "not-json")
	setSessionModTime(t, filepath.Join(st.sessionsDir, "session-ALPHA-corrupt.json"), base.Add(4*time.Minute))

	since := base.Add(1 * time.Minute)
	infos, err := st.ListWithOptions(SessionListOptions{Limit: 2, Since: &since, Filter: "alpha"})
	if err != nil {
		t.Fatalf("ListWithOptions: %v", err)
	}
	if got := sessionInfoIDs(infos); !reflect.DeepEqual(got, []string{"ALPHA-corrupt", "ALPHA-newer"}) {
		t.Fatalf("filtered IDs: got %#v, want %#v", got, []string{"ALPHA-corrupt", "ALPHA-newer"})
	}

	infos, err = st.ListWithOptions(SessionListOptions{})
	if err != nil {
		t.Fatalf("ListWithOptions default: %v", err)
	}
	if len(infos) != 5 {
		t.Fatalf("default query count: got %d, want 5", len(infos))
	}
}

func TestStorage_ListWithOptionsRejectsInvalidLimit(t *testing.T) {
	st := newTestStorage(t)
	for _, limit := range []int{-1, MaxSessionListLimit + 1} {
		if _, err := st.ListWithOptions(SessionListOptions{Limit: limit}); err == nil {
			t.Fatalf("ListWithOptions limit %d: got nil error", limit)
		}
	}
}
