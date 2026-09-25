package session

// LiveImageStageRequest names the opening images of one live invocation.
// SourcePaths are the host paths whose bytes already populate the request's
// OpeningContentParts, in the same order. Directory is the parent under which
// a private, invocation-scoped staging directory is created.
type LiveImageStageRequest struct {
	Directory   string
	SourcePaths []string
}

// LiveImageStager gives the read_image tool session-owned copies of a live
// request's opening images. When the request's capabilities advertise
// read_image, Stage writes the copies, rewrites the capability snapshot (and
// every later refresh) so the tool advertises the exact staged paths, and
// returns a cleanup that removes them. Otherwise it leaves the request
// unchanged and returns a no-op cleanup.
type LiveImageStager interface {
	StageOpeningImages(LiveImageStageRequest, *LiveRequest) (cleanup func() error, err error)
}
