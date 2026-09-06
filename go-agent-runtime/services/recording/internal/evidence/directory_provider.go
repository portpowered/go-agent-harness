package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

func (r *directoryRecorder) ProviderCapturePath() string {
	if r == nil {
		return ""
	}
	if r.options.ProviderCapturePath != "" {
		return r.options.ProviderCapturePath
	}
	return filepath.Join(r.spool, "provider.json")
}

// providerArtifact fingerprints the completed source without allocating a
// second capture-sized buffer. Bundle staging streams it again and checks
// this digest, rejecting source changes or redaction that would invalidate a
// provider capture's own integrity envelope.
func (r *directoryRecorder) providerArtifact() (transcript.RecordingArtifact, bool, error) {
	path := r.ProviderCapturePath()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if r != nil && r.options.ProviderCapturePath == "" && len(r.inputPaths) == 0 && len(r.outputPaths) == 0 {
			// A semantic-only injected session may have no raw wire writer. Once
			// PCM is observed, missing provider evidence is incomplete.
			return transcript.RecordingArtifact{}, false, nil
		}
		return transcript.RecordingArtifact{}, false, recordingWriteError("finalize provider evidence", err)
	}
	if err != nil {
		return transcript.RecordingArtifact{}, false, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		return transcript.RecordingArtifact{}, false, errors.Join(statErr, file.Close())
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return transcript.RecordingArtifact{}, false, errors.Join(errors.New("provider capture must be a non-empty regular file"), file.Close())
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return transcript.RecordingArtifact{}, false, err
	}
	return transcript.RecordingArtifact{Path: "provider.json", SourcePath: path, SHA256: hex.EncodeToString(digest.Sum(nil))}, true, nil
}

var _ recording.ProviderCapture = (*directoryRecorder)(nil)
