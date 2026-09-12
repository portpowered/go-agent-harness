package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func parseRoomReplayArtifactInventory(raw json.RawMessage, field string) ([]roomReplayArtifactRef, error) {
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		return parseRoomReplayInventoryArray(raw, field)
	}
	return parseRoomReplayInventoryObject(raw, field)
}

func parseRoomReplayInventoryArray(raw json.RawMessage, field string) ([]roomReplayArtifactRef, error) {
	var values []roomReplayJSONObject
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "artifact inventory array", "invalid", err)
	}
	result := make([]roomReplayArtifactRef, 0, len(values))
	for index, object := range values {
		name, _, _ := firstRoomReplayStringField(object, nil, "name", "id", "key", "role")
		if name == "" {
			name, _, _ = firstRoomReplayStringField(object, nil, "path", "relative_path", "file", "filename")
		}
		if name == "" {
			name = fmt.Sprintf("%d", index)
		}
		ref, err := parseRoomReplayArtifactRef(mustMarshal(object), fmt.Sprintf("%s[%d]", field, index), name)
		if err != nil {
			return nil, err
		}
		if ref.Name == fmt.Sprintf("%d", index) && ref.Path != "" {
			ref.Name = ref.Path
		}
		result = append(result, ref)
	}
	return result, nil
}

func parseRoomReplayInventoryObject(raw json.RawMessage, field string) ([]roomReplayArtifactRef, error) {
	object, err := roomReplayObject(raw)
	if err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "artifact inventory object", "invalid", err)
	}
	if _, present, _ := firstRoomReplayStringField(object, nil, "path", "relative_path", "file", "filename"); present {
		return parseRoomReplaySingleInventoryObject(object, raw, field)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]roomReplayArtifactRef, 0, len(keys))
	for _, key := range keys {
		entries, err := parseRoomReplayInventoryEntry(object, key, field)
		if err != nil {
			return nil, err
		}
		result = append(result, entries...)
	}
	return result, nil
}

func parseRoomReplaySingleInventoryObject(object roomReplayJSONObject, raw json.RawMessage, field string) ([]roomReplayArtifactRef, error) {
	name, _, _ := firstRoomReplayStringField(object, nil, "name", "id", "key", "role")
	if name == "" {
		name, _, _ = firstRoomReplayStringField(object, nil, "path", "relative_path", "file", "filename")
	}
	ref, err := parseRoomReplayArtifactRef(raw, field, name)
	if err != nil {
		return nil, err
	}
	if ref.Name == "" {
		ref.Name = ref.Path
	}
	return []roomReplayArtifactRef{ref}, nil
}

func parseRoomReplayInventoryEntry(object roomReplayJSONObject, key, field string) ([]roomReplayArtifactRef, error) {
	value := object[key]
	if strings.EqualFold(key, "artifacts") || strings.EqualFold(key, "files") {
		return parseRoomReplayArtifactInventory(value, field+"."+key)
	}
	if metadataObject, err := roomReplayObject(value); err == nil {
		if _, present, _ := firstRoomReplayStringField(metadataObject, nil, "path", "relative_path", "file", "filename"); !present {
			metadataObject["path"] = mustMarshal(key)
			value = mustMarshal(metadataObject)
		}
	}
	ref, err := parseRoomReplayArtifactRef(value, field+"."+key, key)
	if err != nil {
		return nil, err
	}
	if ref.Path == "" {
		ref.Path = key
	}
	return []roomReplayArtifactRef{ref}, nil
}

func mergeRoomReplayArtifactMetadata(inventory, refs []roomReplayArtifactRef) (map[string]roomReplayArtifactRef, error) {
	metadata := make(map[string]roomReplayArtifactRef, len(inventory)+len(refs))
	for _, ref := range append(append([]roomReplayArtifactRef(nil), inventory...), refs...) {
		if err := mergeRoomReplayArtifactMetadataEntry(metadata, ref); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func mergeRoomReplayArtifactMetadataEntry(metadata map[string]roomReplayArtifactRef, ref roomReplayArtifactRef) error {
	if ref.Path == "" {
		return nil
	}
	pathKey := roomReplayPathKey(ref.Path)
	if existing, ok := metadata[pathKey]; ok {
		if err := compareRoomReplayArtifactMetadata(existing, ref, pathKey); err != nil {
			return err
		}
		if existing.Size == nil {
			existing.Size = ref.Size
		}
		if existing.SHA256 == "" {
			existing.SHA256 = normalizeRoomReplayDigest(ref.SHA256)
		}
		metadata[pathKey] = existing
		return nil
	}
	ref.SHA256 = normalizeRoomReplayDigest(ref.SHA256)
	metadata[pathKey] = ref
	return nil
}

func compareRoomReplayArtifactMetadata(existing, ref roomReplayArtifactRef, pathKey string) error {
	if existing.Size != nil && ref.Size != nil && *existing.Size != *ref.Size {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, pathKey, fmt.Sprintf("%d", *existing.Size), fmt.Sprintf("%d", *ref.Size), ErrInvalidRoomReplayBundle)
	}
	if existing.SHA256 != "" && ref.SHA256 != "" && existing.SHA256 != normalizeRoomReplayDigest(ref.SHA256) {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, pathKey, existing.SHA256, normalizeRoomReplayDigest(ref.SHA256), ErrInvalidRoomReplayBundle)
	}
	return nil
}
