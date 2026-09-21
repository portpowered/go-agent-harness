package replay

// HTTPReplayService is the replay-owned extension used by provider builders
// for ordinary HTTP capture files. The provider service does not inspect or
// match captured request/response data itself.
type HTTPReplayService interface {
	// OpenHTTPReplay returns an opaque host transport. The provider adapter
	// validates the concrete net/http transport before installing it.
	OpenHTTPReplay(string) (any, error)
}
