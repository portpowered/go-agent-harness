package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

var literalPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54,
	0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0, 0x1f, 0x00,
	0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d,
	0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44,
	0xae, 0x42, 0x60, 0x82,
}

func main() {
	mode := "positive"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	var result any
	var err error
	switch mode {
	case "positive":
		result, err = runPositive(false)
	case "negative":
		result, err = runPositive(true)
	case "cleanup-negative":
		result, err = runCleanupNegative()
	default:
		err = fmt.Errorf("unknown mode %q (want positive, negative, or cleanup-negative)", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}

type publicSurface struct {
	root        string
	stagingRoot string
	stager      runtimeTools.ImageStaging
	capability  runtimeTools.Capability
	definitions []messages.ToolDefinition
	refresh     func(context.Context) ([]messages.ToolDefinition, error)
	cleanup     func() error
	executor    messages.ToolExecutor
	path        string
}

func newPublicSurface() (*publicSurface, func(), error) {
	root, err := os.MkdirTemp("", "audio-runtime-c43-image-consumer-")
	if err != nil {
		return nil, nil, fmt.Errorf("create consumer root: %w", err)
	}
	removeRoot := func() { _ = os.RemoveAll(root) }
	stagingRoot := filepath.Join(root, "config")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		removeRoot()
		return nil, nil, fmt.Errorf("create staging root: %w", err)
	}

	service := runtimeToolsWire.NewService()
	capability, err := service.Resolve(context.Background(), runtimeTools.Request{
		WorkDir:        stagingRoot,
		UseDefaultTool: true,
	})
	if err != nil {
		removeRoot()
		return nil, nil, fmt.Errorf("resolve public tools: %w", err)
	}
	definitions := append([]messages.ToolDefinition(nil), capability.Definitions...)
	refresh := func(context.Context) ([]messages.ToolDefinition, error) {
		return append([]messages.ToolDefinition(nil), definitions...), nil
	}
	pathHint := filepath.Join(root, "original.png")
	staged, err := runtimeToolsWire.NewImageStaging().Stage(context.Background(), runtimeTools.ImageStagingRequest{
		StagingRoot:            stagingRoot,
		SourcePaths:            []string{pathHint},
		ImageParts:             []messages.ImagePart{{Bytes: append([]byte(nil), literalPNG...), MediaType: "image/png"}},
		ToolDefinitions:        definitions,
		RefreshToolDefinitions: refresh,
	})
	if err != nil {
		removeRoot()
		return nil, nil, fmt.Errorf("stage through public Wire contract: %w", err)
	}
	if len(staged.StagedPaths) != 1 {
		_ = staged.Cleanup()
		removeRoot()
		return nil, nil, fmt.Errorf("staged paths = %d, want 1", len(staged.StagedPaths))
	}

	binder, ok := capability.Executor.(runtimeTools.SessionImagePreparerBinder)
	if !ok {
		_ = staged.Cleanup()
		removeRoot()
		return nil, nil, fmt.Errorf("public default executor %T does not expose image-preparer binding", capability.Executor)
	}
	stagedPath := staged.StagedPaths[0]
	executor := binder.WithSessionImagePreparer(func(paths []string) ([]messages.ImagePart, error) {
		if len(paths) != 1 || filepath.Clean(paths[0]) != filepath.Clean(stagedPath) {
			return nil, fmt.Errorf("preparer received unexpected path %q", paths)
		}
		data, err := os.ReadFile(paths[0])
		if err != nil {
			return nil, err
		}
		return []messages.ImagePart{{Bytes: data, MediaType: "image/png"}}, nil
	})
	if executor == nil {
		_ = staged.Cleanup()
		removeRoot()
		return nil, nil, errors.New("public image-preparer binding returned nil executor")
	}

	surface := &publicSurface{
		root:        root,
		stagingRoot: stagingRoot,
		stager:      runtimeToolsWire.NewImageStaging(),
		capability:  capability,
		definitions: staged.ToolDefinitions,
		refresh:     staged.RefreshToolDefinitions,
		cleanup:     staged.Cleanup,
		executor:    executor,
		path:        stagedPath,
	}
	return surface, removeRoot, nil
}

func runPositive(wrongOracle bool) (map[string]any, error) {
	surface, removeRoot, err := newPublicSurface()
	if err != nil {
		return nil, err
	}
	defer removeRoot()

	result, imageBytes, err := executeReadImage(surface)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(literalPNG)
	expectedDigest := hex.EncodeToString(digest[:])
	if result.SHA256 != expectedDigest {
		return nil, fmt.Errorf("public read_image SHA256 = %q, want literal PNG digest %q", result.SHA256, expectedDigest)
	}
	wantBytes := literalPNG
	if wrongOracle {
		wantBytes = append(append([]byte(nil), literalPNG...), 0x00)
	}
	if !equalBytes(imageBytes, wantBytes) {
		return nil, fmt.Errorf("wrong PNG oracle: public read_image returned %d bytes, oracle expected %d", len(imageBytes), len(wantBytes))
	}

	refreshed, err := surface.refresh(context.Background())
	if err != nil {
		return nil, fmt.Errorf("refresh staged definitions: %w", err)
	}
	if !definitionsMentionPath(refreshed, surface.path) {
		return nil, fmt.Errorf("refreshed definitions do not mention staged path %q", surface.path)
	}
	if !definitionsMentionPath(surface.definitions, surface.path) {
		return nil, fmt.Errorf("initial definitions do not mention staged path %q", surface.path)
	}

	if err := surface.cleanup(); err != nil {
		return nil, fmt.Errorf("cleanup staged image: %w", err)
	}
	if err := surface.cleanup(); err != nil {
		return nil, fmt.Errorf("idempotent cleanup staged image: %w", err)
	}
	if _, err := os.Stat(surface.path); !os.IsNotExist(err) {
		return nil, fmt.Errorf("staged path %q still exists after cleanup: %v", surface.path, err)
	}

	return map[string]any{
		"mode":                     "positive",
		"read_image_status":        result.Status,
		"read_image_sha256":        result.SHA256,
		"literal_png_sha256":       expectedDigest,
		"read_image_bytes":         len(imageBytes),
		"advertised_path_is_exact": definitionsMentionPath(surface.definitions, surface.path),
		"refreshed_path_is_exact":  definitionsMentionPath(refreshed, surface.path),
		"cleanup_idempotent":       true,
		"staged_path_removed":      true,
	}, nil
}

func runCleanupNegative() (map[string]any, error) {
	surface, removeRoot, err := newPublicSurface()
	if err != nil {
		return nil, err
	}
	defer removeRoot()

	if _, _, err := executeReadImage(surface); err != nil {
		return nil, err
	}
	if err := surface.cleanup(); err != nil {
		return nil, fmt.Errorf("cleanup staged image: %w", err)
	}
	if err := surface.cleanup(); err != nil {
		return nil, fmt.Errorf("idempotent cleanup staged image: %w", err)
	}
	response, err := surface.executor.Execute(context.Background(), messages.ToolCall{
		ID:        "cleanup-negative",
		Name:      runtimeTools.ReadImageToolID,
		Arguments: mustJSON(map[string]string{"path": surface.path}),
	})
	if err != nil {
		return nil, fmt.Errorf("read_image after cleanup returned Go error: %w", err)
	}
	var result runtimeTools.ReadImageResult
	if err := json.Unmarshal([]byte(response.Content), &result); err != nil {
		return nil, fmt.Errorf("decode post-cleanup read_image result: %w", err)
	}
	if result.Status != runtimeTools.ReadImageResultStatusError {
		return nil, fmt.Errorf("post-cleanup read_image status = %q, want %q", result.Status, runtimeTools.ReadImageResultStatusError)
	}
	if result.Error == "" {
		return nil, errors.New("post-cleanup read_image error envelope omitted its error")
	}
	return map[string]any{
		"mode":                  "cleanup-negative",
		"read_image_status":     result.Status,
		"post_cleanup_rejected": true,
	}, nil
}

func executeReadImage(surface *publicSurface) (runtimeTools.ReadImageResult, []byte, error) {
	response, err := surface.executor.Execute(context.Background(), messages.ToolCall{
		ID:        "public-image",
		Name:      runtimeTools.ReadImageToolID,
		Arguments: mustJSON(map[string]string{"path": surface.path}),
	})
	if err != nil {
		return runtimeTools.ReadImageResult{}, nil, fmt.Errorf("public read_image execution: %w", err)
	}
	var result runtimeTools.ReadImageResult
	if err := json.Unmarshal([]byte(response.Content), &result); err != nil {
		return runtimeTools.ReadImageResult{}, nil, fmt.Errorf("decode public read_image envelope: %w", err)
	}
	if result.Status != runtimeTools.ReadImageResultStatusSuccess {
		return result, nil, fmt.Errorf("public read_image status = %q, error = %q", result.Status, result.Error)
	}
	for _, part := range response.ContentParts {
		image, ok := part.(messages.ImagePart)
		if ok {
			return result, image.Bytes, nil
		}
	}
	return result, nil, errors.New("public read_image response omitted its typed image part")
}

func definitionsMentionPath(definitions []messages.ToolDefinition, path string) bool {
	for _, definition := range definitions {
		if definition.Name != runtimeTools.ReadImageToolID {
			continue
		}
		for _, parameter := range definition.Parameters {
			if parameter.Name == "path" && strings.Contains(parameter.Description, path) {
				return true
			}
		}
	}
	return false
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
