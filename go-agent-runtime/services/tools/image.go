package tools

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// ReadImageToolID is the stable name of the local image attachment tool.
// Hosts use the value when staging provider-aware image inputs without
// importing the runtime's filesystem implementation.
const ReadImageToolID = "read_image"

// ImagePartPreparer is the session-owned image preparation seam. A host
// supplies provider/model-aware validation while the tools service owns the
// local read and result envelope.
type ImagePartPreparer func([]string) ([]messages.ImagePart, error)

// SessionImagePreparerBinder creates a session-isolated executor with an
// image preparer. Binding returns a new executor and leaves other capability
// snapshots unchanged.
type SessionImagePreparerBinder interface {
	WithSessionImagePreparer(ImagePartPreparer) messages.ToolExecutor
}
