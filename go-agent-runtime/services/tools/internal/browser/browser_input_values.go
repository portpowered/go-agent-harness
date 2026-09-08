package browser

import (
	"math/big"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const pageJSONSchemaObjectType = "object"

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
		if pageJSONMatchesType(value, typeName) {
			return true
		}
	}
	return false
}

func pageJSONMatchesType(value *pageJSONValue, typeName string) bool {
	switch typeName {
	case "null":
		return value.kind == pageJSONNull
	case pageJSONSchemaObjectType:
		return value.kind == pageJSONObject
	case "array":
		return value.kind == pageJSONArray
	case "string":
		return value.kind == pageJSONString
	case "number":
		return value.kind == pageJSONNumber
	case "integer":
		return pageJSONIsInteger(value)
	case "boolean":
		return value.kind == pageJSONBoolean
	default:
		return false
	}
}

func pageJSONIsInteger(value *pageJSONValue) bool {
	if value.kind != pageJSONNumber {
		return false
	}
	number, ok := pageJSONNumberRat(value.number)
	return ok && number.IsInt()
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
	return sortedStrings(keys)
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
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
	if left == nil || right == nil {
		return false
	}
	if left.kind != right.kind {
		return pageJSONNumbersEqual(left, right)
	}
	switch left.kind {
	case pageJSONNull:
		return true
	case pageJSONObject:
		return pageJSONObjectEqual(left, right)
	case pageJSONArray:
		return pageJSONArrayEqual(left, right)
	case pageJSONString:
		return left.string == right.string
	case pageJSONNumber:
		return pageJSONNumbersEqual(left, right)
	case pageJSONBoolean:
		return left.boolean == right.boolean
	default:
		return false
	}
}

func pageJSONObjectEqual(left, right *pageJSONValue) bool {
	if len(left.object) != len(right.object) {
		return false
	}
	for key, leftValue := range left.object {
		if !pageJSONEqual(leftValue, right.object[key]) {
			return false
		}
	}
	return true
}

func pageJSONArrayEqual(left, right *pageJSONValue) bool {
	if len(left.array) != len(right.array) {
		return false
	}
	for index := range left.array {
		if !pageJSONEqual(left.array[index], right.array[index]) {
			return false
		}
	}
	return true
}

func pageJSONNumbersEqual(left, right *pageJSONValue) bool {
	if left.kind != pageJSONNumber || right.kind != pageJSONNumber {
		return false
	}
	leftNumber, leftOK := pageJSONNumberRat(left.number)
	rightNumber, rightOK := pageJSONNumberRat(right.number)
	return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
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
		return validPageDate(value)
	case "time":
		return validPageTime(value)
	case "date-time":
		return validPageDateTime(value)
	case "email":
		return validPageEmail(value)
	case "hostname":
		return validHostname(value)
	case "ipv4":
		return validPageIP(value, true)
	case "ipv6":
		return validPageIP(value, false)
	case "uuid":
		return validPageUUID(value)
	case "uri":
		return validPageURI(value)
	case "uri-reference":
		return validPageURIReference(value)
	case "regex":
		return validPageRegex(value)
	case "json-pointer":
		return validPageJSONPointer(value)
	default:
		// Unknown formats are annotations in JSON Schema and are intentionally
		// not treated as executable constraints by the broker.
		return true
	}
}

func validPageDate(value string) bool {
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func validPageTime(value string) bool {
	_, err := time.Parse("15:04:05.999999999Z07:00", value)
	return err == nil
}

func validPageDateTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validPageEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}

func validPageIP(value string, wantV4 bool) bool {
	ip := net.ParseIP(value)
	if ip == nil {
		return false
	}
	if wantV4 {
		return ip.To4() != nil
	}
	return ip.To4() == nil && ip.To16() != nil
}

func validPageUUID(value string) bool {
	matched, err := regexp.MatchString(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`, value)
	return err == nil && matched
}

func validPageURI(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme != ""
}

func validPageURIReference(value string) bool {
	_, err := url.Parse(value)
	return err == nil
}

func validPageRegex(value string) bool {
	_, err := regexp.Compile(value)
	return err == nil
}

func validPageJSONPointer(value string) bool {
	return value == "" || (strings.HasPrefix(value, "/") && validJSONPointer(value))
}

func validHostname(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !validHostnameLabel(label) {
			return false
		}
	}
	return true
}

func validHostnameLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, character := range label {
		if !validHostnameCharacter(character) {
			return false
		}
	}
	return true
}

func validHostnameCharacter(character rune) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9') || character == '-'
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
