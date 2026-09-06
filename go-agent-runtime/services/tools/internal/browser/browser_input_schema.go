package browser

import (
	"math/big"
	"regexp"
	"unicode/utf8"
)

const maxPageSchemaRefDepth = 32

type pageSchemaValidator struct {
	root *pageJSONValue
}

func (v pageSchemaValidator) validate(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	v.validateAt(value, schema, path, collector, 0)
}

func (v pageSchemaValidator) validateAt(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	switch {
	case schema == nil:
		return
	case schema.kind == pageJSONBoolean:
		if !schema.boolean {
			collector.add(path, "schema_false")
		}
	case schema.kind != pageJSONObject:
		collector.add(path, "invalid_schema")
	default:
		v.validateObjectSchema(value, schema, path, collector, refDepth)
	}
}

func (v pageSchemaValidator) validateObjectSchema(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	v.validateSchemaReference(value, schema, path, collector, refDepth)
	if typeSchema := schema.object["type"]; typeSchema != nil && !v.validateSchemaType(value, typeSchema, path, collector) {
		return
	}
	v.validateSchemaEnum(value, schema, path, collector)
	if constant := schema.object["const"]; constant != nil && !pageJSONEqual(value, constant) {
		collector.add(path, "const")
	}
	v.validateCombinators(value, schema, path, collector, refDepth)
	v.validateValue(value, schema, path, collector, refDepth)
}

func (v pageSchemaValidator) validateSchemaReference(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	ref, ok := schemaString(schema, "$ref")
	if !ok {
		return
	}
	if refDepth >= maxPageSchemaRefDepth {
		collector.add(path, "schema_ref_cycle")
		return
	}
	target := v.resolveRef(ref)
	if target == nil {
		collector.add(path, "unsupported_schema")
		return
	}
	v.validateAt(value, target, path, collector, refDepth+1)
}

func (v pageSchemaValidator) validateSchemaType(value, schema *pageJSONValue, path string, collector *pageIssueCollector) bool {
	types := schemaTypes(schema)
	if len(types) == 0 || pageJSONMatchesAnyType(value, types) {
		return true
	}
	collector.add(path, "invalid_type")
	return false
}

func (v pageSchemaValidator) validateSchemaEnum(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	enum := schema.object["enum"]
	if enum == nil || enum.kind != pageJSONArray {
		return
	}
	for _, candidate := range enum.array {
		if pageJSONEqual(value, candidate) {
			return
		}
	}
	collector.add(path, "enum")
}

func (v pageSchemaValidator) validateValue(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	switch value.kind {
	case pageJSONNull, pageJSONBoolean:
		return
	case pageJSONObject:
		v.validateObject(value, schema, path, collector, refDepth)
	case pageJSONArray:
		v.validateArray(value, schema, path, collector, refDepth)
	case pageJSONString:
		v.validateString(value, schema, path, collector)
	case pageJSONNumber:
		v.validateNumber(value, schema, path, collector)
	}
}

func (v pageSchemaValidator) validateCombinators(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	v.validateAllOf(value, schema, path, collector, refDepth)
	v.validateAnyOf(value, schema, path, collector, refDepth)
	v.validateOneOf(value, schema, path, collector, refDepth)
	v.validateNot(value, schema, path, collector, refDepth)
	v.validateConditional(value, schema, path, collector, refDepth)
}

func (v pageSchemaValidator) validateAllOf(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	allOf := schemaArray(schema.object["allOf"])
	for _, branch := range allOf {
		v.validateAt(value, branch, path, collector, refDepth)
	}
}

func (v pageSchemaValidator) validateAnyOf(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	anyOf := schemaArray(schema.object["anyOf"])
	if len(anyOf) > 0 && v.countMatches(value, anyOf, refDepth) == 0 {
		collector.add(path, "any_of")
	}
}

func (v pageSchemaValidator) validateOneOf(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	oneOf := schemaArray(schema.object["oneOf"])
	if len(oneOf) > 0 && v.countMatches(value, oneOf, refDepth) != 1 {
		collector.add(path, "one_of")
	}
}

func (v pageSchemaValidator) countMatches(value *pageJSONValue, schemas []*pageJSONValue, refDepth int) int {
	matches := 0
	for _, schema := range schemas {
		if v.matches(value, schema, refDepth) {
			matches++
		}
	}
	return matches
}

func (v pageSchemaValidator) validateNot(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	if notSchema := schema.object["not"]; notSchema != nil && v.matches(value, notSchema, refDepth) {
		collector.add(path, "not")
	}
}

func (v pageSchemaValidator) validateConditional(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	ifSchema := schema.object["if"]
	if ifSchema == nil {
		return
	}
	if v.matches(value, ifSchema, refDepth) {
		if thenSchema := schema.object["then"]; thenSchema != nil {
			v.validateAt(value, thenSchema, path, collector, refDepth)
		}
		return
	}
	if elseSchema := schema.object["else"]; elseSchema != nil {
		v.validateAt(value, elseSchema, path, collector, refDepth)
	}
}

func (v pageSchemaValidator) matches(value, schema *pageJSONValue, refDepth int) bool {
	collector := &pageIssueCollector{}
	v.validateAt(value, schema, "", collector, refDepth+1)
	return len(collector.issues) == 0
}

func (v pageSchemaValidator) validateObject(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	v.validateObjectBounds(value, schema, path, collector)
	v.validateObjectProperties(value, schema, path, collector, refDepth)
	v.validateObjectRequired(value, schema, path, collector)
	v.validateObjectDependencies(value, schema, path, collector)
}

func (v pageSchemaValidator) validateArray(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	v.validateArrayBounds(value, schema, path, collector)
	v.validateArrayItems(value, schema, path, collector, refDepth)
	v.validateArrayContains(value, schema, path, collector, refDepth)
}

func (v pageSchemaValidator) validateString(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	v.validateStringLength(value, schema, path, collector)
	v.validateStringPattern(value, schema, path, collector)
	v.validateStringFormat(value, schema, path, collector)
}

func (v pageSchemaValidator) validateNumber(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	number, ok := pageJSONNumberRat(value.number)
	if !ok {
		collector.add(path, "invalid_json")
		return
	}
	v.validateNumberBounds(number, schema, path, collector)
	v.validateNumberMultiple(number, schema, path, collector)
}

func (v pageSchemaValidator) validateStringLength(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	length := int64(utf8.RuneCountInString(value.string))
	if minimum, ok := schemaInteger(schema.object["minLength"]); ok && length < minimum {
		collector.add(path, "min_length")
	}
	if maximum, ok := schemaInteger(schema.object["maxLength"]); ok && length > maximum {
		collector.add(path, "max_length")
	}
}

func (v pageSchemaValidator) validateStringPattern(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	pattern, ok := schemaString(schema, "pattern")
	if !ok {
		return
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		collector.add(path, "invalid_schema")
		return
	}
	if !compiled.MatchString(value.string) {
		collector.add(path, "pattern")
	}
}

func (v pageSchemaValidator) validateStringFormat(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	format, ok := schemaString(schema, "format")
	if ok && !validPageFormat(format, value.string) {
		collector.add(path, "format")
	}
}

func (v pageSchemaValidator) validateNumberBounds(number *big.Rat, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	v.validateNumberMinimum(number, schema, path, collector)
	v.validateNumberMaximum(number, schema, path, collector)
}

func (v pageSchemaValidator) validateNumberMinimum(number *big.Rat, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	if minimum, ok := schemaNumber(schema.object["minimum"]); ok && number.Cmp(minimum) < 0 {
		collector.add(path, "minimum")
	}
	if exclusiveMinimum, ok := schemaNumber(schema.object["exclusiveMinimum"]); ok && number.Cmp(exclusiveMinimum) <= 0 {
		collector.add(path, "exclusive_minimum")
		return
	}
	if exclusive, ok := schemaBool(schema.object["exclusiveMinimum"]); ok && exclusive {
		if minimum, ok := schemaNumber(schema.object["minimum"]); ok && number.Cmp(minimum) <= 0 {
			collector.add(path, "exclusive_minimum")
		}
	}
}

func (v pageSchemaValidator) validateNumberMaximum(number *big.Rat, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	if maximum, ok := schemaNumber(schema.object["maximum"]); ok && number.Cmp(maximum) > 0 {
		collector.add(path, "maximum")
	}
	if exclusiveMaximum, ok := schemaNumber(schema.object["exclusiveMaximum"]); ok && number.Cmp(exclusiveMaximum) >= 0 {
		collector.add(path, "exclusive_maximum")
		return
	}
	if exclusive, ok := schemaBool(schema.object["exclusiveMaximum"]); ok && exclusive {
		if maximum, ok := schemaNumber(schema.object["maximum"]); ok && number.Cmp(maximum) >= 0 {
			collector.add(path, "exclusive_maximum")
		}
	}
}

func (v pageSchemaValidator) validateNumberMultiple(number *big.Rat, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	multiple, ok := schemaNumber(schema.object["multipleOf"])
	if !ok || multiple.Sign() <= 0 {
		return
	}
	quotient := new(big.Rat).Quo(number, multiple)
	if !quotient.IsInt() {
		collector.add(path, "multiple_of")
	}
}
