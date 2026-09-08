package tools

// SkillRoot identifies one directory that directly contains skill
// subdirectories. Roots are evaluated in order; earlier roots win when names
// collide. Hosts resolve workspace, configuration, and other policy paths
// before passing them to the reusable tools service.
type SkillRoot struct {
	Directory string
}

// SkillSummaryRequest asks the tools service for prompt-sized skill metadata.
// The service reads only the explicitly supplied roots and never discovers
// paths from process state.
type SkillSummaryRequest struct {
	SkillRoots []SkillRoot
}
