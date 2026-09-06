package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"unicode/utf8"
)

const maxInputValidationIssues = 64

type pageIssueCollector struct {
	issues []ToolResultIssue
}

func (c *pageIssueCollector) add(path, code string) {
	if len(c.issues) >= maxInputValidationIssues {
		return
	}
	if path == "" {
		path = "/"
	}
	c.issues = append(c.issues, ToolResultIssue{Path: path, Code: code})
}

func (c *pageIssueCollector) sorted() []ToolResultIssue {
	issues := append([]ToolResultIssue(nil), c.issues...)
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path != issues[j].Path {
			return issues[i].Path < issues[j].Path
		}
		return issues[i].Code < issues[j].Code
	})
	return issues
}

// ValidatePageToolInput validates one page-tool input against its JSON Schema.
// It intentionally returns only JSON Pointer paths and issue codes, so invalid
// user values are never echoed through a model-facing error. The validator is
// bounded by maxBytes and by a fixed issue count to keep broker failures safe.
func validatePageToolInput(input, schema json.RawMessage, maxBytes int) []ToolResultIssue {
	if input == nil {
		// The direct broker seam permits an omitted input. The stable flat tool
		// adapter still requires input_json, so this is only a convenience for
		// callers using the neutral API directly.
		input = json.RawMessage(`{}`)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxInputBytes
	}
	collector := &pageIssueCollector{}
	if len(input) > maxBytes {
		collector.add("/", "input_too_large")
		return collector.sorted()
	}
	if !utf8.Valid(input) {
		collector.add("/", "invalid_utf8")
		return collector.sorted()
	}

	value, duplicatePaths, err := parsePageJSONDocument(input)
	if err != nil {
		var multipleValues multiplePageJSONValuesError
		if errors.As(err, &multipleValues) {
			collector.add("/", "multiple_json_values")
		} else {
			collector.add("/", "invalid_json")
		}
		return collector.sorted()
	}
	for _, path := range duplicatePaths {
		collector.add(path, "duplicate_property")
	}
	if value.kind != pageJSONObject {
		collector.add("/", "object_required")
		return collector.sorted()
	}

	schemaBytes := schema
	if len(bytes.TrimSpace(schemaBytes)) == 0 {
		schemaBytes = json.RawMessage(`{}`)
	}
	if !utf8.Valid(schemaBytes) {
		collector.add("/", "invalid_schema")
		return collector.sorted()
	}
	schemaValue, _, err := parsePageJSONDocument(schemaBytes)
	if err != nil || schemaValue == nil || schemaValue.kind != pageJSONObject {
		collector.add("/", "invalid_schema")
		return collector.sorted()
	}

	validator := pageSchemaValidator{root: schemaValue}
	validator.validate(value, schemaValue, "", collector)
	return collector.sorted()
}

func (v pageSchemaValidator) validateObjectBounds(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	if minimum, ok := schemaInteger(schema.object["minProperties"]); ok && int64(len(value.object)) < minimum {
		collector.add(path, "min_properties")
	}
	if maximum, ok := schemaInteger(schema.object["maxProperties"]); ok && int64(len(value.object)) > maximum {
		collector.add(path, "max_properties")
	}
}

func (v pageSchemaValidator) validateObjectProperties(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	properties := schemaObject(schema.object["properties"])
	patternProperties := schemaObject(schema.object["patternProperties"])
	patterns := sortedSchemaKeys(patternProperties)
	propertyNames := schema.object["propertyNames"]
	for _, key := range sortedSchemaKeys(value.object) {
		v.validateObjectProperty(value, schema, properties, patternProperties, patterns, propertyNames, key, path, collector, refDepth)
	}
}

func (v pageSchemaValidator) validateObjectProperty(value *pageJSONValue, schema *pageJSONValue, properties, patternProperties map[string]*pageJSONValue, patterns []string, propertyNames *pageJSONValue, key, path string, collector *pageIssueCollector, refDepth int) {
	childPath := pageJSONChildPath(path, key)
	if propertySchema := properties[key]; propertySchema != nil {
		v.validateAt(value.object[key], propertySchema, childPath, collector, refDepth)
	}
	matchedPattern := v.validatePatternProperties(value.object[key], patternProperties, patterns, key, childPath, collector, refDepth)
	if propertyNames != nil {
		v.validateAt(&pageJSONValue{kind: pageJSONString, string: key}, propertyNames, childPath, collector, refDepth)
	}
	if properties[key] == nil && !matchedPattern {
		v.validateAdditionalProperty(value.object[key], schema.object["additionalProperties"], childPath, collector, refDepth)
	}
}

func (v pageSchemaValidator) validatePatternProperties(value *pageJSONValue, properties map[string]*pageJSONValue, patterns []string, key, path string, collector *pageIssueCollector, refDepth int) bool {
	matched := false
	for _, pattern := range patterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			collector.add(path, "invalid_schema")
			continue
		}
		if compiled.MatchString(key) {
			matched = true
			v.validateAt(value, properties[pattern], path, collector, refDepth)
		}
	}
	return matched
}

func (v pageSchemaValidator) validateAdditionalProperty(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	switch {
	case schema == nil:
		// JSON Schema defaults additionalProperties to true.
	case schema.kind == pageJSONBoolean && !schema.boolean:
		collector.add(path, "unknown_property")
	default:
		v.validateAt(value, schema, path, collector, refDepth)
	}
}

func (v pageSchemaValidator) validateObjectRequired(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	for _, key := range sortedStrings(schemaStringList(schema.object["required"])) {
		if _, present := value.object[key]; !present {
			collector.add(pageJSONChildPath(path, key), "required")
		}
	}
}

func (v pageSchemaValidator) validateObjectDependencies(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	dependentRequired := schemaObject(schema.object["dependentRequired"])
	for _, dependency := range sortedSchemaKeys(dependentRequired) {
		if _, present := value.object[dependency]; !present {
			continue
		}
		v.validateDependency(value, dependentRequired[dependency], path, collector)
	}
}

func (v pageSchemaValidator) validateDependency(value *pageJSONValue, required *pageJSONValue, path string, collector *pageIssueCollector) {
	for _, requiredKey := range schemaStringList(required) {
		if _, present := value.object[requiredKey]; !present {
			collector.add(pageJSONChildPath(path, requiredKey), "dependent_required")
		}
	}
}

func (v pageSchemaValidator) validateArrayBounds(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	if minimum, ok := schemaInteger(schema.object["minItems"]); ok && int64(len(value.array)) < minimum {
		collector.add(path, "min_items")
	}
	if maximum, ok := schemaInteger(schema.object["maxItems"]); ok && int64(len(value.array)) > maximum {
		collector.add(path, "max_items")
	}
	if unique, ok := schemaBool(schema.object["uniqueItems"]); ok && unique {
		v.validateArrayUniqueness(value, path, collector)
	}
}

func (v pageSchemaValidator) validateArrayUniqueness(value *pageJSONValue, path string, collector *pageIssueCollector) {
	for i := 0; i < len(value.array); i++ {
		for j := i + 1; j < len(value.array); j++ {
			if pageJSONEqual(value.array[i], value.array[j]) {
				collector.add(path, "unique_items")
				return
			}
		}
	}
}

func (v pageSchemaValidator) validateArrayItems(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	v.validatePrefixItems(value, schemaArray(schema.object["prefixItems"]), path, collector, refDepth)
	items := schema.object["items"]
	if items == nil {
		return
	}
	if tuple := schemaArray(items); tuple != nil {
		v.validateTupleItems(value, tuple, path, collector, refDepth)
		v.validateAdditionalItems(value, schema.object["additionalItems"], len(tuple), path, collector, refDepth)
		return
	}
	for index, item := range value.array {
		v.validateAt(item, items, pageJSONArrayChildPath(path, index), collector, refDepth)
	}
}

func (v pageSchemaValidator) validatePrefixItems(value *pageJSONValue, prefixItems []*pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	for index, itemSchema := range prefixItems {
		if index >= len(value.array) {
			break
		}
		v.validateAt(value.array[index], itemSchema, pageJSONArrayChildPath(path, index), collector, refDepth)
	}
}

func (v pageSchemaValidator) validateTupleItems(value *pageJSONValue, tuple []*pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	for index, itemSchema := range tuple {
		if index >= len(value.array) {
			break
		}
		v.validateAt(value.array[index], itemSchema, pageJSONArrayChildPath(path, index), collector, refDepth)
	}
}

func (v pageSchemaValidator) validateAdditionalItems(value, schema *pageJSONValue, start int, path string, collector *pageIssueCollector, refDepth int) {
	additional, isBoolean := schemaBool(schema)
	if isBoolean {
		if !additional && len(value.array) > start {
			collector.add(path, "additional_items")
		}
		return
	}
	if schema == nil {
		return
	}
	for index := start; index < len(value.array); index++ {
		v.validateAt(value.array[index], schema, pageJSONArrayChildPath(path, index), collector, refDepth)
	}
}

func (v pageSchemaValidator) validateArrayContains(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	contains := schema.object["contains"]
	if contains == nil {
		return
	}
	matches := v.countMatchingItems(value.array, contains, refDepth)
	minimum := int64(1)
	if configured, ok := schemaInteger(schema.object["minContains"]); ok {
		minimum = configured
	}
	if int64(matches) < minimum {
		collector.add(path, "contains")
	}
	if maximum, ok := schemaInteger(schema.object["maxContains"]); ok && int64(matches) > maximum {
		collector.add(path, "max_contains")
	}
}

func (v pageSchemaValidator) countMatchingItems(items []*pageJSONValue, schema *pageJSONValue, refDepth int) int {
	matches := 0
	for _, item := range items {
		if v.matches(item, schema, refDepth) {
			matches++
		}
	}
	return matches
}
