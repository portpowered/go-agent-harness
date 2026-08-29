package messages

import "sort"

// CanonicalToolDefinitions returns an independently owned tool-definition
// snapshot in deterministic order. Both the tool list and each tool's
// parameter list are sorted by name. The input slices are never modified, so
// callers can retain their own configuration while sharing the returned
// snapshot across prompt and provider composition.
func CanonicalToolDefinitions(definitions []ToolDefinition) []ToolDefinition {
	if definitions == nil {
		return nil
	}

	canonical := make([]ToolDefinition, len(definitions))
	for index, definition := range definitions {
		canonical[index] = definition
		if definition.Parameters != nil {
			canonical[index].Parameters = make([]ToolParameter, len(definition.Parameters))
			copy(canonical[index].Parameters, definition.Parameters)
		}
	}

	sort.SliceStable(canonical, func(i, j int) bool {
		return canonical[i].Name < canonical[j].Name
	})
	for index := range canonical {
		sort.SliceStable(canonical[index].Parameters, func(i, j int) bool {
			return canonical[index].Parameters[i].Name < canonical[index].Parameters[j].Name
		})
	}
	return canonical
}
