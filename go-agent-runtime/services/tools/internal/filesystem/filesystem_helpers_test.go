package filesystem

import "encoding/json"

// FilesystemRefusalFromContent recognizes both the direct refusal shape and
// the nested refusal carried by the read_image result envelope.
func FilesystemRefusalFromContent(content string) (FilesystemRefusal, bool) {
	if refusal, err := DecodeFilesystemRefusal([]byte(content)); err == nil {
		return refusal, true
	}

	var wrapped struct {
		Refusal *FilesystemRefusal `json:"refusal"`
	}
	if err := json.Unmarshal([]byte(content), &wrapped); err != nil || wrapped.Refusal == nil {
		return FilesystemRefusal{}, false
	}
	if err := wrapped.Refusal.Validate(); err != nil {
		return FilesystemRefusal{}, false
	}
	return *wrapped.Refusal, true
}

// NewFilesystemPolicyFromRoots is the slice-taking form of
// NewFilesystemPolicy for callers that already collect repeatable roots.
func NewFilesystemPolicyFromRoots(primaryRoot string, additionalRoots []string) (*FilesystemPolicy, error) {
	return NewFilesystemPolicy(primaryRoot, additionalRoots...)
}
