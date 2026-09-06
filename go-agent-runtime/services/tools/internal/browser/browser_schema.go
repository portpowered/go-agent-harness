package browser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// A schema that cannot be expressed as an object at all - for example a bare
// scalar or array at the top level, which OpenAI's function-calling contract
// has never accepted regardless of this bug - cannot be normalized. Callers
// must skip that tool rather than send a shape the provider will reject and
// lose every other tool along with it.
//
// Other JSON Schema constructs that can trip the same class of strict
// validator, surveyed but deliberately NOT handled here because no connected
// page has been observed to emit them: "$ref" siblings (a $ref alongside
// other keywords), "if"/"then"/"else" conditionals, a "not" schema, and a
// "type" expressed as a union array (e.g. ["string","null"]). anyOf/oneOf/
// allOf are the ones known to reproduce the outage.
func NormalizeBrowserParameterSchema(schema json.RawMessage) (json.RawMessage, string, bool) {
	trimmed := bytes.TrimSpace(schema)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), "", true
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var raw map[string]interface{}
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		// Not a JSON object at all (e.g. `true`, `[...]`, a bare scalar).
		// Pass it through unchanged: existing non-object-schema behavior
		// (ultimately falling back to the flat Parameters contract) is
		// preserved rather than newly rejecting something this function did
		// not previously touch.
		return append(json.RawMessage(nil), schema...), "", true
	}

	combinatorKey, branches, hasCombinator := topLevelCombinator(raw)
	if !hasCombinator {
		if declaredType, ok := raw["type"].(string); ok && declaredType != pageJSONSchemaObjectType {
			return nil, fmt.Sprintf("top-level schema type %q cannot be represented as function-call parameters (must be an object)", declaredType), false
		}
		return append(json.RawMessage(nil), schema...), "", true
	}

	flattened, reason, ok := flattenTopLevelCombinator(raw, combinatorKey, branches)
	if !ok {
		return nil, reason, false
	}
	encoded, err := json.Marshal(flattened)
	if err != nil {
		return nil, fmt.Sprintf("failed to encode normalized schema: %v", err), false
	}
	return json.RawMessage(encoded), "", true
}

// topLevelCombinator reports the first top-level JSON Schema combinator key
// present on raw, in anyOf/oneOf/allOf precedence order (multiple combinators
// on one schema are not observed in practice; picking one deterministically
// keeps normalization simple and predictable).
func topLevelCombinator(raw map[string]interface{}) (string, []interface{}, bool) {
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if value, ok := raw[key]; ok {
			branches, ok := value.([]interface{})
			if !ok {
				branches = nil
			}
			return key, branches, true
		}
	}
	return "", nil, false
}

// flattenTopLevelCombinator merges a top-level anyOf/oneOf/allOf into one
// object schema. Every branch must itself describe an object (explicit
// "type":"object", "properties", or no type at all); a branch that names a
// non-object type cannot be merged without fabricating meaning, so the whole
// tool is reported as unnormalizable.
func flattenTopLevelCombinator(raw map[string]interface{}, key string, branches []interface{}) (map[string]interface{}, string, bool) {
	if len(branches) == 0 {
		return nil, fmt.Sprintf("%s must be a non-empty array of schemas", key), false
	}

	merged := withoutCombinator(raw)
	properties := schemaProperties(merged)
	requiredSets := schemaRequiredSets(merged)
	for index, branchValue := range branches {
		branch, reason, ok := objectBranch(key, index, branchValue)
		if !ok {
			return nil, reason, false
		}
		mergeSchemaProperties(properties, branch)
		requiredSets = append(requiredSets, branchRequired(branch))
	}

	merged["type"] = pageJSONSchemaObjectType
	merged["properties"] = properties
	merged["required"] = combinedRequired(key, requiredSets)
	return merged, "", true
}

func withoutCombinator(raw map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(raw))
	for key, value := range raw {
		if key != "anyOf" && key != "oneOf" && key != "allOf" {
			merged[key] = value
		}
	}
	return merged
}

func schemaProperties(schema map[string]interface{}) map[string]interface{} {
	properties := map[string]interface{}{}
	existing, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return properties
	}
	for name, property := range existing {
		properties[name] = property
	}
	return properties
}

func schemaRequiredSets(schema map[string]interface{}) [][]string {
	required, ok := stringSliceValue(schema["required"])
	if !ok {
		return nil
	}
	return [][]string{required}
}

func objectBranch(key string, index int, value interface{}) (map[string]interface{}, string, bool) {
	branch, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Sprintf("%s[%d] is not an object schema", key, index), false
	}
	if branchType, ok := branch["type"].(string); ok && branchType != pageJSONSchemaObjectType {
		return nil, fmt.Sprintf("%s[%d] has type %q; only object branches can be merged into function-call parameters", key, index, branchType), false
	}
	return branch, "", true
}

func mergeSchemaProperties(properties, branch map[string]interface{}) {
	branchProperties, ok := branch["properties"].(map[string]interface{})
	if !ok {
		return
	}
	for name, property := range branchProperties {
		if _, exists := properties[name]; !exists {
			properties[name] = property
		}
	}
}

func branchRequired(branch map[string]interface{}) []string {
	required, _ := stringSliceValue(branch["required"])
	return required
}

func combinedRequired(key string, sets [][]string) []string {
	if key == "allOf" {
		return unionStrings(sets)
	}
	return intersectStrings(sets)
}

// stringSliceValue reads a JSON array-of-strings value decoded through
// interface{} (e.g. a schema's "required" list). ok is false when value is
// not a JSON array at all, distinguishing "absent" from "present but empty".
func stringSliceValue(value interface{}) ([]string, bool) {
	list, ok := value.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if name, ok := item.(string); ok {
			out = append(out, name)
		}
	}
	return out, true
}

// intersectStrings returns the names common to every set (an anyOf/oneOf
// branch that omits "required" contributes an empty set, correctly zeroing
// out anything not required by every alternative). No sets at all yields an
// empty, not nil, result so the caller can always marshal a `[]`.
func intersectStrings(sets [][]string) []string {
	out := []string{}
	if len(sets) == 0 {
		return out
	}
	counts := map[string]int{}
	for _, set := range sets {
		seen := map[string]bool{}
		for _, name := range set {
			if seen[name] {
				continue
			}
			seen[name] = true
			counts[name]++
		}
	}
	for name, count := range counts {
		if count == len(sets) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// unionStrings returns the deduplicated names across every set, used for
// allOf where every branch's requirement applies simultaneously.
func unionStrings(sets [][]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, set := range sets {
		for _, name := range set {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}
