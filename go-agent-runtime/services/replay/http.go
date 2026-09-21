package replay

import "net/http"

// HTTPReplayService is the replay-owned extension used by provider builders
// for ordinary HTTP capture files. The provider service does not inspect or
// match captured request/response data itself.
type HTTPReplayService interface {
	OpenHTTPReplay(string) (http.RoundTripper, error)
}
