package tools

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// ReadImageToolID is the stable name of the local image attachment tool.
// Hosts use the value when staging provider-aware image inputs without
// importing the runtime's filesystem implementation.
const ReadImageToolID = "read_image"

// ImageSource is one local image read_image has read: the requested path,
// for messages, and the bytes the tools service read from it. With a
// filesystem policy the read is checked at open time, so a preparer must
// use Bytes and never read Path again.
type ImageSource struct {
	Path  string
	Bytes []byte
}

// ImagePartPreparer is the session-owned image preparation seam. A host
// supplies provider/model-aware validation of the bytes while the tools
// service owns the local read and result envelope.
type ImagePartPreparer func([]ImageSource) ([]messages.ImagePart, error)

// SessionImagePreparerBinder creates a session-isolated executor with an
// image preparer. Binding returns a new executor and leaves other capability
// snapshots unchanged.
type SessionImagePreparerBinder interface {
	WithSessionImagePreparer(ImagePartPreparer) messages.ToolExecutor
}
