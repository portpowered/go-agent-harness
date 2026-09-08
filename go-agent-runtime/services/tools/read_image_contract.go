package tools

// ReadImageResultVersion is the compact result envelope version for the
// read_image capability. Image bytes travel in a correlated ImagePart; the
// textual envelope carries metadata and the stable projection marker.
const (
	ReadImageResultVersion                   = 2
	ReadImageResultStatusSuccess             = "success"
	ReadImageResultStatusError               = "error"
	ReadImageResultTypedProjectionInputImage = "input_image"
	// FilesystemScopeStartupNotice is the user-facing explanation for the
	// normalized filesystem boundary carried by a tool request.
	FilesystemScopeStartupNotice = "Filesystem tools are confined to the effective workdir and additional allowed roots; protected system and credential reads remain denied even when --allow-path includes them. Shell-command deny-pattern policy is separate, and this is not an operating-system sandbox."
)

// ReadImageResult is the provider-neutral textual projection emitted by the
// read_image tool. Refusal is intentionally raw JSON so this public contract
// does not expose the filesystem implementation's private error types.
type ReadImageResult struct {
	Version         int    `json:"version"`
	Status          string `json:"status"`
	MIMEType        string `json:"mime_type,omitempty"`
	ByteLength      int    `json:"byte_length,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	TypedProjection string `json:"typed_projection,omitempty"`
	Error           string `json:"error,omitempty"`
	Refusal         any    `json:"refusal,omitempty"`
}
