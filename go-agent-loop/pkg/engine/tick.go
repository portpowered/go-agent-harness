package engine

import (
	"sort"

	"github.com/portpowered/go-agent-loop/pkg/subsystems"
)

// orderedSubsystems sorts helpers by their tick group for deterministic execution.
func orderedSubsystems(hlps []subsystems.Subsystem) []subsystems.Subsystem {
	sorted := make([]subsystems.Subsystem, len(hlps))
	copy(sorted, hlps)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TickGroup() < sorted[j].TickGroup()
	})
	return sorted
}
