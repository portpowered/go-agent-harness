package webmcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// StableToolRef derives the wire-shaped opaque reference from the complete
// page-tool identity protected by a generation-bound binding. It intentionally
// excludes Ref and AddedSequence, which are broker bookkeeping fields.
func StableToolRef(descriptor ToolDescriptor) (ToolRef, error) {
	identity := struct {
		BrowserID    BrowserID
		TargetID     TargetID
		FrameID      FrameID
		Generation   uint64
		ToolName     string
		SchemaDigest string
		Origin       string
		Description  string
		InputSchema  string
		ReadOnly     *bool
		Untrusted    *bool
		AutoSubmit   *bool
		Annotations  string
	}{
		BrowserID:    descriptor.BrowserID,
		TargetID:     descriptor.TargetID,
		FrameID:      descriptor.FrameID,
		Generation:   descriptor.Generation,
		ToolName:     descriptor.Name,
		SchemaDigest: descriptor.SchemaDigest,
		Origin:       descriptor.Origin,
		Description:  descriptor.Description,
		InputSchema:  string(descriptor.InputSchema),
		ReadOnly:     descriptor.Annotations.ReadOnly,
		Untrusted:    descriptor.Annotations.UntrustedContent,
		AutoSubmit:   descriptor.Annotations.AutoSubmit,
		Annotations:  string(descriptor.Annotations.Raw),
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return ToolRef(ToolRefPrefix + base64.RawURLEncoding.EncodeToString(digest[:16])), nil
}

func refCurrentLocked(selected *brokerSession, record refRecord) bool {
	if record.binding.BrowserID != selected.context.Key.BrowserID ||
		record.binding.TargetID != selected.context.Key.TargetID ||
		record.binding.Generation != selected.context.Generation {
		return false
	}
	current, ok := selected.catalog[record.key]
	if !ok {
		return false
	}
	return descriptorEqual(current, record.descriptor) && bindingFor(current) == record.binding
}

func normalizeToolDescriptor(input ToolDescriptor, contextValue PageContext) (ToolDescriptor, error) {
	descriptor := cloneToolDescriptor(input)
	if descriptor.Name == "" || descriptor.FrameID == "" {
		return ToolDescriptor{}, errors.New("webmcp: catalog descriptor requires name and frame")
	}
	if descriptor.BrowserID == "" {
		descriptor.BrowserID = contextValue.Key.BrowserID
	}
	if descriptor.TargetID == "" {
		descriptor.TargetID = contextValue.Key.TargetID
	}
	if descriptor.BrowserID != contextValue.Key.BrowserID || descriptor.TargetID != contextValue.Key.TargetID {
		return ToolDescriptor{}, errors.New("webmcp: catalog descriptor target does not match selected page")
	}
	if descriptor.Generation == 0 {
		descriptor.Generation = contextValue.Generation
	}
	if descriptor.Origin == "" {
		descriptor.Origin = contextValue.Origin
	}
	canonical, digest, err := canonicalSchema(descriptor.InputSchema)
	if err != nil {
		return ToolDescriptor{}, err
	}
	descriptor.InputSchema = canonical
	descriptor.SchemaDigest = digest
	descriptor.Ref = ""
	descriptor.AddedSequence = 0
	return descriptor, nil
}

func canonicalSchema(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "", fmt.Errorf("webmcp: invalid page input schema: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, "", errors.New("webmcp: page input schema contains multiple JSON values")
		}
		return nil, "", err
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, "", errors.New("webmcp: page input schema must be an object")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(canonical)
	return json.RawMessage(canonical), fmt.Sprintf("%x", digest[:]), nil
}

func descriptorEqual(left, right ToolDescriptor) bool {
	return left.Name == right.Name && left.Description == right.Description &&
		bytesEqual(left.InputSchema, right.InputSchema) && annotationsEqual(left.Annotations, right.Annotations) &&
		left.BrowserID == right.BrowserID && left.TargetID == right.TargetID && left.FrameID == right.FrameID &&
		left.Origin == right.Origin && left.Generation == right.Generation && left.SchemaDigest == right.SchemaDigest
}

func bindingFor(descriptor ToolDescriptor) ToolRefBinding {
	return ToolRefBinding{
		BrowserID:    descriptor.BrowserID,
		TargetID:     descriptor.TargetID,
		FrameID:      descriptor.FrameID,
		Generation:   descriptor.Generation,
		ToolName:     descriptor.Name,
		SchemaDigest: descriptor.SchemaDigest,
	}
}

func cloneToolDescriptor(descriptor ToolDescriptor) ToolDescriptor {
	descriptor.InputSchema = cloneJSON(descriptor.InputSchema)
	descriptor.Annotations.Raw = cloneJSON(descriptor.Annotations.Raw)
	if descriptor.Annotations.ReadOnly != nil {
		value := *descriptor.Annotations.ReadOnly
		descriptor.Annotations.ReadOnly = &value
	}
	if descriptor.Annotations.UntrustedContent != nil {
		value := *descriptor.Annotations.UntrustedContent
		descriptor.Annotations.UntrustedContent = &value
	}
	if descriptor.Annotations.AutoSubmit != nil {
		value := *descriptor.Annotations.AutoSubmit
		descriptor.Annotations.AutoSubmit = &value
	}
	return descriptor
}

func normalizePageContext(page PageContext, browserID BrowserID, target Target) PageContext {
	if page.Key.BrowserID == "" {
		page.Key.BrowserID = browserID
	}
	if page.Key.TargetID == "" {
		page.Key.TargetID = target.ID
	}
	if page.Title == "" {
		page.Title = target.Title
	}
	if page.URL == "" {
		page.URL = target.URL
	}
	if page.Origin == "" {
		page.Origin = target.Origin
	}
	if page.Generation == 0 {
		page.Generation = target.Generation
	}
	if page.Generation == 0 {
		page.Generation = 1
	}
	if page.DocumentReadyState == "" {
		page.DocumentReadyState = target.DocumentReadyState
	}
	if !page.DocumentLoadingKnown && target.DocumentLoadingKnown {
		page.DocumentLoading = target.DocumentLoading
		page.DocumentLoadingKnown = true
	}
	page.Connected = true
	return page
}

func clonePageContext(page PageContext) PageContext { return page }

func validateToolRefSyntax(ref ToolRef) error {
	value := string(ref)
	if !strings.HasPrefix(value, ToolRefPrefix) || len(value) != len(ToolRefPrefix)+22 {
		return errors.New("tool reference must use the webmcp.tool-ref.v1 format")
	}
	for _, character := range value[len(ToolRefPrefix):] {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return errors.New("tool reference contains invalid characters")
		}
	}
	return nil
}

// ValidateToolRef reports whether ref has the exact C0 wire grammar. It does
// not assert that the reference is current in any broker session.
func ValidateToolRef(ref ToolRef) error { return validateToolRefSyntax(ref) }

// IsValidToolRef is the boolean form of ValidateToolRef.
func IsValidToolRef(ref ToolRef) bool { return validateToolRefSyntax(ref) == nil }

func invalidToolRefError(ref ToolRef, cause error) error {
	return classified(ErrorInvalidToolInput, "the tool reference is invalid", map[string]any{
		"tool_ref": string(ref),
		"issues":   []ToolResultIssue{{Path: "/tool_ref", Code: "invalid_tool_ref"}},
	}, errors.Join(ErrInvalidToolInput, cause))
}

func staleToolRefError(ref ToolRef, generation uint64) error {
	return classified(ErrorStaleToolRef, "the page tool reference is no longer current", map[string]any{
		"tool_ref":           string(ref),
		"current_generation": generation,
		"refresh_required":   true,
	}, ErrStaleToolRef)
}
