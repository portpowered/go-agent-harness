package webmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxInputValidationIssues = 64

var errMultiplePageJSONValues = errors.New("webmcp: input contains multiple JSON values")

type pageJSONKind uint8

const (
	pageJSONNull pageJSONKind = iota
	pageJSONObject
	pageJSONArray
	pageJSONString
	pageJSONNumber
	pageJSONBoolean
)

// pageJSONValue is deliberately separate from interface{}: numbers retain
// their original decimal token and are compared with exact rational arithmetic
// when a schema constraint requires a numeric comparison.
type pageJSONValue struct {
	kind    pageJSONKind
	object  map[string]*pageJSONValue
	array   []*pageJSONValue
	string  string
	number  string
	boolean bool
}

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
		if errors.Is(err, errMultiplePageJSONValues) {
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

func invalidPageInputError(ref ToolRef, descriptor ToolDescriptor, issues []ToolResultIssue) error {
	schema := cloneJSON(descriptor.InputSchema)
	if len(bytes.TrimSpace(schema)) == 0 {
		schema = json.RawMessage(`{}`)
	}
	boundedIssues := append([]ToolResultIssue(nil), issues...)
	if len(boundedIssues) > maxInputValidationIssues {
		boundedIssues = boundedIssues[:maxInputValidationIssues]
	}
	return classified(ErrorInvalidToolInput, "Input does not match the selected tool schema.", map[string]any{
		"tool_ref":     string(ref),
		"input_schema": schema,
		"issues":       boundedIssues,
	}, ErrInvalidToolInput)
}

func parsePageJSONDocument(data []byte) (*pageJSONValue, []string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, duplicatePaths, err := parsePageJSONValue(decoder, "")
	if err != nil {
		return nil, nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, nil, errMultiplePageJSONValues
		}
		return nil, nil, err
	}
	return value, duplicatePaths, nil
}

func parsePageJSONValue(decoder *json.Decoder, path string) (*pageJSONValue, []string, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := &pageJSONValue{kind: pageJSONObject, object: make(map[string]*pageJSONValue)}
			var duplicatePaths []string
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, nil, errors.New("webmcp: object key is not a string")
				}
				childPath := pageJSONChildPath(path, key)
				child, nestedDuplicates, err := parsePageJSONValue(decoder, childPath)
				if err != nil {
					return nil, nil, err
				}
				duplicatePaths = append(duplicatePaths, nestedDuplicates...)
				if _, exists := object.object[key]; exists {
					duplicatePaths = append(duplicatePaths, childPath)
					continue
				}
				object.object[key] = child
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				if err != nil {
					return nil, nil, err
				}
				return nil, nil, errors.New("webmcp: object did not close")
			}
			return object, duplicatePaths, nil
		case '[':
			array := &pageJSONValue{kind: pageJSONArray}
			var duplicatePaths []string
			for index := 0; decoder.More(); index++ {
				childPath := pageJSONArrayChildPath(path, index)
				child, nestedDuplicates, err := parsePageJSONValue(decoder, childPath)
				if err != nil {
					return nil, nil, err
				}
				array.array = append(array.array, child)
				duplicatePaths = append(duplicatePaths, nestedDuplicates...)
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				if err != nil {
					return nil, nil, err
				}
				return nil, nil, errors.New("webmcp: array did not close")
			}
			return array, duplicatePaths, nil
		default:
			return nil, nil, errors.New("webmcp: unexpected JSON delimiter")
		}
	case string:
		return &pageJSONValue{kind: pageJSONString, string: value}, nil, nil
	case json.Number:
		return &pageJSONValue{kind: pageJSONNumber, number: string(value)}, nil, nil
	case bool:
		return &pageJSONValue{kind: pageJSONBoolean, boolean: value}, nil, nil
	case nil:
		return &pageJSONValue{kind: pageJSONNull}, nil, nil
	default:
		return nil, nil, errors.New("webmcp: unsupported JSON token")
	}
}

func pageJSONChildPath(parent, property string) string {
	return parent + "/" + escapeJSONPointer(property)
}

func pageJSONArrayChildPath(parent string, index int) string {
	return parent + "/" + strconv.Itoa(index)
}

func escapeJSONPointer(value string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(value)
}

type pageSchemaValidator struct {
	root *pageJSONValue
}

func (v pageSchemaValidator) validate(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	v.validateAt(value, schema, path, collector, 0)
}

func (v pageSchemaValidator) validateAt(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	if schema == nil {
		return
	}
	if schema.kind == pageJSONBoolean {
		if !schema.boolean {
			collector.add(path, "schema_false")
		}
		return
	}
	if schema.kind != pageJSONObject {
		collector.add(path, "invalid_schema")
		return
	}
	if ref, ok := schemaString(schema, "$ref"); ok {
		if refDepth >= 32 {
			collector.add(path, "schema_ref_cycle")
		} else if target := v.resolveRef(ref); target == nil {
			collector.add(path, "unsupported_schema")
		} else {
			v.validateAt(value, target, path, collector, refDepth+1)
		}
	}

	if typeSchema := schema.object["type"]; typeSchema != nil {
		types := schemaTypes(typeSchema)
		if len(types) > 0 && !pageJSONMatchesAnyType(value, types) {
			collector.add(path, "invalid_type")
			return
		}
	}
	if enum := schema.object["enum"]; enum != nil && enum.kind == pageJSONArray {
		matched := false
		for _, candidate := range enum.array {
			if pageJSONEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			collector.add(path, "enum")
		}
	}
	if constant := schema.object["const"]; constant != nil && !pageJSONEqual(value, constant) {
		collector.add(path, "const")
	}

	v.validateCombinators(value, schema, path, collector, refDepth)
	switch value.kind {
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
	if allOf := schema.object["allOf"]; allOf != nil && allOf.kind == pageJSONArray {
		for _, branch := range allOf.array {
			v.validateAt(value, branch, path, collector, refDepth)
		}
	}
	if anyOf := schema.object["anyOf"]; anyOf != nil && anyOf.kind == pageJSONArray {
		matches := 0
		for _, branch := range anyOf.array {
			if v.matches(value, branch, refDepth) {
				matches++
			}
		}
		if matches == 0 {
			collector.add(path, "any_of")
		}
	}
	if oneOf := schema.object["oneOf"]; oneOf != nil && oneOf.kind == pageJSONArray {
		matches := 0
		for _, branch := range oneOf.array {
			if v.matches(value, branch, refDepth) {
				matches++
			}
		}
		if matches != 1 {
			collector.add(path, "one_of")
		}
	}
	if notSchema := schema.object["not"]; notSchema != nil && v.matches(value, notSchema, refDepth) {
		collector.add(path, "not")
	}
	if ifSchema := schema.object["if"]; ifSchema != nil {
		if v.matches(value, ifSchema, refDepth) {
			if thenSchema := schema.object["then"]; thenSchema != nil {
				v.validateAt(value, thenSchema, path, collector, refDepth)
			}
		} else if elseSchema := schema.object["else"]; elseSchema != nil {
			v.validateAt(value, elseSchema, path, collector, refDepth)
		}
	}
}

func (v pageSchemaValidator) matches(value, schema *pageJSONValue, refDepth int) bool {
	collector := &pageIssueCollector{}
	v.validateAt(value, schema, "", collector, refDepth+1)
	return len(collector.issues) == 0
}

func (v pageSchemaValidator) validateObject(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	if minimum, ok := schemaInteger(schema.object["minProperties"]); ok && int64(len(value.object)) < minimum {
		collector.add(path, "min_properties")
	}
	if maximum, ok := schemaInteger(schema.object["maxProperties"]); ok && int64(len(value.object)) > maximum {
		collector.add(path, "max_properties")
	}

	properties := schemaObject(schema.object["properties"])
	patternProperties := schemaObject(schema.object["patternProperties"])
	patterns := make([]string, 0, len(patternProperties))
	for pattern := range patternProperties {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)

	propertyNames := schema.object["propertyNames"]
	propertyKeys := make([]string, 0, len(value.object))
	for key := range value.object {
		propertyKeys = append(propertyKeys, key)
	}
	sort.Strings(propertyKeys)
	for _, key := range propertyKeys {
		childPath := pageJSONChildPath(path, key)
		if propertySchema := properties[key]; propertySchema != nil {
			v.validateAt(value.object[key], propertySchema, childPath, collector, refDepth)
		}
		matchedPattern := false
		for _, pattern := range patterns {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				collector.add(childPath, "invalid_schema")
				continue
			}
			if compiled.MatchString(key) {
				matchedPattern = true
				v.validateAt(value.object[key], patternProperties[pattern], childPath, collector, refDepth)
			}
		}
		if propertyNames != nil {
			v.validateAt(&pageJSONValue{kind: pageJSONString, string: key}, propertyNames, childPath, collector, refDepth)
		}
		if properties[key] == nil && !matchedPattern {
			switch additional := schema.object["additionalProperties"]; {
			case additional == nil:
				// JSON Schema defaults additionalProperties to true.
			case additional.kind == pageJSONBoolean && !additional.boolean:
				collector.add(childPath, "unknown_property")
			default:
				v.validateAt(value.object[key], additional, childPath, collector, refDepth)
			}
		}
	}

	required := schemaStringList(schema.object["required"])
	sort.Strings(required)
	for _, key := range required {
		if _, present := value.object[key]; !present {
			collector.add(pageJSONChildPath(path, key), "required")
		}
	}

	if dependentRequired := schemaObject(schema.object["dependentRequired"]); dependentRequired != nil {
		for _, dependency := range sortedSchemaKeys(dependentRequired) {
			if _, present := value.object[dependency]; !present {
				continue
			}
			for _, requiredKey := range schemaStringList(dependentRequired[dependency]) {
				if _, present := value.object[requiredKey]; !present {
					collector.add(pageJSONChildPath(path, requiredKey), "dependent_required")
				}
			}
		}
	}
}

func (v pageSchemaValidator) validateArray(value, schema *pageJSONValue, path string, collector *pageIssueCollector, refDepth int) {
	if minimum, ok := schemaInteger(schema.object["minItems"]); ok && int64(len(value.array)) < minimum {
		collector.add(path, "min_items")
	}
	if maximum, ok := schemaInteger(schema.object["maxItems"]); ok && int64(len(value.array)) > maximum {
		collector.add(path, "max_items")
	}
	if unique, ok := schemaBool(schema.object["uniqueItems"]); ok && unique {
		for i := 0; i < len(value.array); i++ {
			for j := i + 1; j < len(value.array); j++ {
				if pageJSONEqual(value.array[i], value.array[j]) {
					collector.add(path, "unique_items")
					i = len(value.array)
					break
				}
			}
		}
	}

	if prefixItems := schemaArray(schema.object["prefixItems"]); prefixItems != nil {
		for index, itemSchema := range prefixItems {
			if index >= len(value.array) {
				break
			}
			v.validateAt(value.array[index], itemSchema, pageJSONArrayChildPath(path, index), collector, refDepth)
		}
	}
	if items := schema.object["items"]; items != nil {
		if tuple := schemaArray(items); tuple != nil {
			for index, itemSchema := range tuple {
				if index >= len(value.array) {
					break
				}
				v.validateAt(value.array[index], itemSchema, pageJSONArrayChildPath(path, index), collector, refDepth)
			}
			if additional, ok := schemaBool(schema.object["additionalItems"]); ok && !additional && len(value.array) > len(tuple) {
				collector.add(path, "additional_items")
			} else if additionalSchema := schema.object["additionalItems"]; additionalSchema != nil && !ok {
				for index := len(tuple); index < len(value.array); index++ {
					v.validateAt(value.array[index], additionalSchema, pageJSONArrayChildPath(path, index), collector, refDepth)
				}
			}
		} else {
			for index, item := range value.array {
				v.validateAt(item, items, pageJSONArrayChildPath(path, index), collector, refDepth)
			}
		}
	}

	if contains := schema.object["contains"]; contains != nil {
		matches := 0
		for _, item := range value.array {
			if v.matches(item, contains, refDepth) {
				matches++
			}
		}
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
}

func (v pageSchemaValidator) validateString(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	length := int64(utf8.RuneCountInString(value.string))
	if minimum, ok := schemaInteger(schema.object["minLength"]); ok && length < minimum {
		collector.add(path, "min_length")
	}
	if maximum, ok := schemaInteger(schema.object["maxLength"]); ok && length > maximum {
		collector.add(path, "max_length")
	}
	if pattern, ok := schemaString(schema, "pattern"); ok {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			collector.add(path, "invalid_schema")
		} else if !compiled.MatchString(value.string) {
			collector.add(path, "pattern")
		}
	}
	if format, ok := schemaString(schema, "format"); ok && !validPageFormat(format, value.string) {
		collector.add(path, "format")
	}
}

func (v pageSchemaValidator) validateNumber(value, schema *pageJSONValue, path string, collector *pageIssueCollector) {
	number, ok := pageJSONNumberRat(value.number)
	if !ok {
		collector.add(path, "invalid_json")
		return
	}
	if minimum, ok := schemaNumber(schema.object["minimum"]); ok && number.Cmp(minimum) < 0 {
		collector.add(path, "minimum")
	}
	if maximum, ok := schemaNumber(schema.object["maximum"]); ok && number.Cmp(maximum) > 0 {
		collector.add(path, "maximum")
	}
	if exclusiveMinimum, ok := schemaNumber(schema.object["exclusiveMinimum"]); ok && number.Cmp(exclusiveMinimum) <= 0 {
		collector.add(path, "exclusive_minimum")
	} else if exclusive, ok := schemaBool(schema.object["exclusiveMinimum"]); ok && exclusive {
		if minimum, ok := schemaNumber(schema.object["minimum"]); ok && number.Cmp(minimum) <= 0 {
			collector.add(path, "exclusive_minimum")
		}
	}
	if exclusiveMaximum, ok := schemaNumber(schema.object["exclusiveMaximum"]); ok && number.Cmp(exclusiveMaximum) >= 0 {
		collector.add(path, "exclusive_maximum")
	} else if exclusive, ok := schemaBool(schema.object["exclusiveMaximum"]); ok && exclusive {
		if maximum, ok := schemaNumber(schema.object["maximum"]); ok && number.Cmp(maximum) >= 0 {
			collector.add(path, "exclusive_maximum")
		}
	}
	if multiple, ok := schemaNumber(schema.object["multipleOf"]); ok && multiple.Sign() > 0 {
		quotient := new(big.Rat).Quo(number, multiple)
		if !quotient.IsInt() {
			collector.add(path, "multiple_of")
		}
	}
}

func schemaTypes(schema *pageJSONValue) []string {
	if schema == nil {
		return nil
	}
	if schema.kind == pageJSONString {
		return []string{schema.string}
	}
	if schema.kind != pageJSONArray {
		return nil
	}
	result := make([]string, 0, len(schema.array))
	for _, item := range schema.array {
		if item.kind == pageJSONString {
			result = append(result, item.string)
		}
	}
	return result
}

func pageJSONMatchesAnyType(value *pageJSONValue, types []string) bool {
	for _, typeName := range types {
		switch typeName {
		case "null":
			if value.kind == pageJSONNull {
				return true
			}
		case "object":
			if value.kind == pageJSONObject {
				return true
			}
		case "array":
			if value.kind == pageJSONArray {
				return true
			}
		case "string":
			if value.kind == pageJSONString {
				return true
			}
		case "number":
			if value.kind == pageJSONNumber {
				return true
			}
		case "integer":
			if value.kind == pageJSONNumber {
				if number, ok := pageJSONNumberRat(value.number); ok && number.IsInt() {
					return true
				}
			}
		case "boolean":
			if value.kind == pageJSONBoolean {
				return true
			}
		}
	}
	return false
}

func schemaObject(value *pageJSONValue) map[string]*pageJSONValue {
	if value == nil || value.kind != pageJSONObject {
		return nil
	}
	return value.object
}

func schemaArray(value *pageJSONValue) []*pageJSONValue {
	if value == nil || value.kind != pageJSONArray {
		return nil
	}
	return value.array
}

func schemaString(schema *pageJSONValue, key string) (string, bool) {
	if schema == nil || schema.kind != pageJSONObject {
		return "", false
	}
	value := schema.object[key]
	if value == nil || value.kind != pageJSONString {
		return "", false
	}
	return value.string, true
}

func schemaBool(value *pageJSONValue) (bool, bool) {
	if value == nil || value.kind != pageJSONBoolean {
		return false, false
	}
	return value.boolean, true
}

func schemaNumber(value *pageJSONValue) (*big.Rat, bool) {
	if value == nil || value.kind != pageJSONNumber {
		return nil, false
	}
	return pageJSONNumberRat(value.number)
}

func schemaInteger(value *pageJSONValue) (int64, bool) {
	number, ok := schemaNumber(value)
	if !ok || !number.IsInt() || !number.Num().IsInt64() {
		return 0, false
	}
	return number.Num().Int64(), true
}

func schemaStringList(value *pageJSONValue) []string {
	if value == nil || value.kind != pageJSONArray {
		return nil
	}
	result := make([]string, 0, len(value.array))
	for _, item := range value.array {
		if item.kind == pageJSONString {
			result = append(result, item.string)
		}
	}
	return result
}

func sortedSchemaKeys(values map[string]*pageJSONValue) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (v pageSchemaValidator) resolveRef(ref string) *pageJSONValue {
	if ref == "#" {
		return v.root
	}
	if !strings.HasPrefix(ref, "#/") || v.root == nil {
		return nil
	}
	current := v.root
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		segment = strings.NewReplacer("~1", "/", "~0", "~").Replace(segment)
		if current == nil || current.kind != pageJSONObject {
			return nil
		}
		current = current.object[segment]
	}
	return current
}

func pageJSONEqual(left, right *pageJSONValue) bool {
	if left == nil || right == nil || left.kind != right.kind {
		if left != nil && right != nil && left.kind == pageJSONNumber && right.kind == pageJSONNumber {
			leftNumber, leftOK := pageJSONNumberRat(left.number)
			rightNumber, rightOK := pageJSONNumberRat(right.number)
			return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
		}
		return false
	}
	switch left.kind {
	case pageJSONNull:
		return true
	case pageJSONObject:
		if len(left.object) != len(right.object) {
			return false
		}
		for key, leftValue := range left.object {
			if !pageJSONEqual(leftValue, right.object[key]) {
				return false
			}
		}
		return true
	case pageJSONArray:
		if len(left.array) != len(right.array) {
			return false
		}
		for index := range left.array {
			if !pageJSONEqual(left.array[index], right.array[index]) {
				return false
			}
		}
		return true
	case pageJSONString:
		return left.string == right.string
	case pageJSONNumber:
		leftNumber, leftOK := pageJSONNumberRat(left.number)
		rightNumber, rightOK := pageJSONNumberRat(right.number)
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	case pageJSONBoolean:
		return left.boolean == right.boolean
	default:
		return false
	}
}

func pageJSONNumberRat(value string) (*big.Rat, bool) {
	result := new(big.Rat)
	if _, ok := result.SetString(value); !ok {
		return nil, false
	}
	return result, true
}

func validPageFormat(format, value string) bool {
	switch format {
	case "date":
		_, err := time.Parse("2006-01-02", value)
		return err == nil
	case "time":
		_, err := time.Parse("15:04:05.999999999Z07:00", value)
		return err == nil
	case "date-time":
		_, err := time.Parse(time.RFC3339Nano, value)
		return err == nil
	case "email":
		address, err := mail.ParseAddress(value)
		return err == nil && address.Address == value
	case "hostname":
		return validHostname(value)
	case "ipv4":
		ip := net.ParseIP(value)
		return ip != nil && ip.To4() != nil
	case "ipv6":
		ip := net.ParseIP(value)
		return ip != nil && ip.To4() == nil && ip.To16() != nil
	case "uuid":
		matched, _ := regexp.MatchString(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`, value)
		return matched
	case "uri":
		parsed, err := url.ParseRequestURI(value)
		return err == nil && parsed.Scheme != ""
	case "uri-reference":
		_, err := url.Parse(value)
		return err == nil
	case "regex":
		_, err := regexp.Compile(value)
		return err == nil
	case "json-pointer":
		return value == "" || (strings.HasPrefix(value, "/") && validJSONPointer(value))
	default:
		// Unknown formats are annotations in JSON Schema and are intentionally
		// not treated as executable constraints by the broker.
		return true
	}
}

func validHostname(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func validJSONPointer(value string) bool {
	for index := 1; index < len(value); index++ {
		if value[index] != '~' {
			continue
		}
		if index+1 >= len(value) || (value[index+1] != '0' && value[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}
