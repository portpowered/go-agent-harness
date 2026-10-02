package tools

// JSON Schema type names used when normalizing and validating tool schemas.
const (
	schemaTypeString  = "string"
	schemaTypeBoolean = "boolean"
	schemaTypeObject  = "object"
)

// noPageSelectedMessage is the model-facing text for calls made before a page
// is selected.
const noPageSelectedMessage = "no page is selected"

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
