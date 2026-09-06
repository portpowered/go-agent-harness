package agentruntime

// Tests may customize the factory while production receives a fresh factory
// from the runtime constructor for each plan.
var defaultSessionRuntimeFactory = newDefaultSessionRuntimeFactory()
