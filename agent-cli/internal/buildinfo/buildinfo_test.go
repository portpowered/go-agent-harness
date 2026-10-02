package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestVersionPrefersTheLinkedThenTheModuleVersion(t *testing.T) {
	for _, tc := range []struct {
		linked string
		info   *debug.BuildInfo
		want   string
	}{
		{linked: " v1.2.3 ", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}}, want: "v1.2.3"},
		{info: &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}}, want: "v0.9.0"},
		{info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, want: "dev"},
		{want: "dev"},
	} {
		if got := resolve(tc.linked, tc.info); got != tc.want {
			t.Errorf("resolve(%q, %+v) = %q, want %q", tc.linked, tc.info, got, tc.want)
		}
	}
	if Version() == "" {
		t.Fatal("Version is empty")
	}
}
