package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

type multiplePageJSONValuesError struct{}

func (multiplePageJSONValuesError) Error() string {
	return "webmcp: input contains multiple JSON values"
}

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

func parsePageJSONDocument(data []byte) (*pageJSONValue, []string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, duplicatePaths, err := parsePageJSONValue(decoder, "")
	if err != nil {
		return nil, nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, nil, multiplePageJSONValuesError{}
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
			return parsePageJSONObject(decoder, path)
		case '[':
			return parsePageJSONArray(decoder, path)
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

func parsePageJSONObject(decoder *json.Decoder, path string) (*pageJSONValue, []string, error) {
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
}

func parsePageJSONArray(decoder *json.Decoder, path string) (*pageJSONValue, []string, error) {
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
