package selfplay

import "testing"

func TestSentinelErrorsExposeStableFailureCategories(t *testing.T) {
	for _, test := range []struct {
		name string
		err  SentinelError
		want string
	}{
		{name: "invalid request", err: ErrInvalidRequest, want: "invalid self-play request"},
		{name: "unsupported provider", err: ErrUnsupportedProvider, want: "unsupported self-play provider"},
		{name: "unsupported model", err: ErrUnsupportedModel, want: "unsupported self-play model"},
		{name: "missing model catalog", err: ErrModelCatalogRequired, want: "self-play model catalog is required"},
		{name: "missing session service", err: ErrSessionServiceRequired, want: "self-play session service is required"},
		{name: "unsafe output target", err: ErrOutputTargetUnsafe, want: "self-play output target is unsafe"},
		{name: "artifact limit", err: ErrArtifactLimit, want: "self-play evidence limit exceeded"},
		{name: "shutdown deadline", err: ErrShutdownTimeout, want: "self-play shutdown deadline exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Error(); got != test.want {
				t.Fatalf("error category = %q, want %q", got, test.want)
			}
		})
	}
}
