package services

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// roomEvidenceArtifactIntegrity is one entry of roomEvidenceManifest's
// artifact_integrity map: the declared size and sha256 digest of one
// artifact file, computed from the file actually written to disk.
type roomEvidenceArtifactIntegrity struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// roomEvidenceHashArtifact stats and hashes one bundle-relative artifact path
// and reports whether it could be read. A missing or unreadable file (for
// example one behind a degraded recording sink) is reported as absent rather
// than as an integrity entry with a size of zero: an absent entry correctly
// makes replay admission reject that artifact as incomplete, instead of
// admitting a zero-byte stand-in as if it were the real, complete artifact.
func roomEvidenceHashArtifact(destination, relativePath string) (roomEvidenceArtifactIntegrity, bool) {
	if strings.TrimSpace(relativePath) == "" {
		return roomEvidenceArtifactIntegrity{}, false
	}
	data, err := os.ReadFile(filepath.Join(destination, relativePath))
	if err != nil {
		return roomEvidenceArtifactIntegrity{}, false
	}
	digest := sha256.Sum256(data)
	return roomEvidenceArtifactIntegrity{Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}, true
}

// hashArtifactInto is writeManifest's artifact_integrity population helper:
// it hashes one bundle-relative artifact path and, if the file could be
// read, records its size/sha256 into integrity keyed by that same path (the
// key the replay reader's artifact-metadata merge looks entries up by, not
// the artifact's role name). A relativePath that is empty (an artifact role
// a participant does not have, such as "capture" for a human participant) is
// silently skipped.
func (e *roomEvidence) hashArtifactInto(integrity map[string]roomEvidenceArtifactIntegrity, relativePath string) {
	if relativePath == "" {
		return
	}
	if entry, ok := roomEvidenceHashArtifact(e.destination, relativePath); ok {
		integrity[relativePath] = entry
	}
}
