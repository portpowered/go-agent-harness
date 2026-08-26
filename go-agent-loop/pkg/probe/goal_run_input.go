package probe

// GoalRunInput is the minimal input a fleet consumer passes to one blind
// acceptance-probe run. It deliberately carries only the stable goal identity
// and the exact plain-English request; capability and artifact expectations
// remain catalog metadata rather than probe hints.
type GoalRunInput struct {
	GoalID   string `json:"goal_id"`
	GoalText string `json:"goal_text"`
}

// RunInput is a concise alias for callers that already operate on per-run
// inputs.
type RunInput = GoalRunInput

// RunInputs projects the catalog into one ordered, detached input per goal.
// The projection preserves the catalog's declared order and does not add any
// metadata to the blind-probe request. Callers should obtain catalogs through
// LoadGoalCatalog or validate custom catalogs before projecting them.
func (c GoalCatalog) RunInputs() []GoalRunInput {
	inputs := make([]GoalRunInput, len(c))
	for index, goal := range c {
		inputs[index] = GoalRunInput{
			GoalID:   goal.ID,
			GoalText: goal.Text,
		}
	}
	return inputs
}

// EnumerateGoalRunInputs is the package-level form of GoalCatalog.RunInputs.
// It is useful to fleet consumers that pass catalog values through generic
// enumeration code.
func EnumerateGoalRunInputs(catalog GoalCatalog) []GoalRunInput {
	return catalog.RunInputs()
}
