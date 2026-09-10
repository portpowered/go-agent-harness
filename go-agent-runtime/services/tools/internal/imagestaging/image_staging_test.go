package imagestaging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestService_StagesExactBytesExtensionsPermissionsAndIdempotentCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	parts := []messages.ImagePart{
		{Bytes: []byte("png-bytes"), MediaType: "image/png; charset=utf-8"},
		{Bytes: []byte("jpeg-bytes"), MediaType: "text/plain"},
		{Bytes: []byte("avif-bytes"), MediaType: "image/avif"},
	}
	definitions := []messages.ToolDefinition{
		{Name: "unrelated", Description: "keep me"},
		{Name: public.ReadImageToolID, Description: "read", Parameters: []messages.ToolParameter{{Name: "path", Type: "string", Description: "Path to image", Required: true}}},
	}
	result, err := New().Stage(context.Background(), public.ImageStagingRequest{
		StagingRoot:     root,
		SourcePaths:     []string{"first.any", "photo.JPEG", "third.avif"},
		ImageParts:      parts,
		ToolDefinitions: definitions,
	})
	if err != nil {
		t.Fatalf("Stage returned error: %v", err)
	}
	if len(result.StagedPaths) != len(parts) {
		t.Fatalf("staged paths = %v, want %d paths", result.StagedPaths, len(parts))
	}
	assertStagedImageFiles(t, result.StagedPaths, parts)
	assertStagedDefinitions(t, result.ToolDefinitions, result.StagedPaths)
	assertStagedCleanup(t, result)
}

func assertStagedImageFiles(t *testing.T, paths []string, parts []messages.ImagePart) {
	t.Helper()
	wantSuffixes := []string{"image-000.png", "image-001.jpeg", "image-002.avif"}
	for index, path := range paths {
		if !filepath.IsAbs(path) || filepath.Base(path) != wantSuffixes[index] {
			t.Fatalf("staged path %q, want absolute path ending in %q", path, wantSuffixes[index])
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read staged image %q: %v", path, err)
		}
		if !bytes.Equal(got, parts[index].Bytes) {
			t.Fatalf("staged bytes[%d] = %q, want %q", index, got, parts[index].Bytes)
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat staged image %q: %v", path, err)
		}
		if gotMode := fileInfo.Mode().Perm(); gotMode != 0o600 {
			t.Fatalf("staged file mode = %o, want 600", gotMode)
		}
		stageInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("stat staging directory: %v", err)
		}
		if gotMode := stageInfo.Mode().Perm(); gotMode != 0o700 {
			t.Fatalf("staging directory mode = %o, want 700", gotMode)
		}
	}
}

func assertStagedDefinitions(t *testing.T, definitions []messages.ToolDefinition, paths []string) {
	t.Helper()
	readImage := findDefinition(definitions, public.ReadImageToolID)
	if readImage == nil || len(readImage.Parameters) != 1 || !strings.Contains(readImage.Parameters[0].Description, paths[0]) || !strings.Contains(readImage.Parameters[0].Description, paths[2]) {
		t.Fatalf("read_image definition = %#v, want all staged paths", readImage)
	}
	if findDefinition(definitions, "unrelated") == nil {
		t.Fatal("staging removed an unrelated tool definition")
	}
}

func assertStagedCleanup(t *testing.T, result public.ImageStagingResult) {
	t.Helper()
	if err := result.Cleanup(); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if err := result.Cleanup(); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(result.StagedPaths[0])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory stat error after cleanup = %v, want not-exist", err)
	}
}

func TestService_NoReadImageIsNoOpBeforeRootOrMismatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-be-created")
	definitions := []messages.ToolDefinition{{Name: "unrelated"}}
	result, err := New().Stage(context.Background(), public.ImageStagingRequest{
		StagingRoot:     root,
		SourcePaths:     []string{"one.png"},
		ToolDefinitions: definitions,
	})
	if err != nil {
		t.Fatalf("no-read_image Stage returned error: %v", err)
	}
	if len(result.StagedPaths) != 0 || result.RefreshToolDefinitions != nil {
		t.Fatalf("no-op result = %#v, want no staged paths or refresh wrapper", result)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op root stat error = %v, want not-exist", err)
	}
	if err := result.Cleanup(); err != nil {
		t.Fatalf("no-op cleanup: %v", err)
	}
}

func TestService_PartialWriteFailureCleansAlreadyWrittenFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	var (
		mu              sync.Mutex
		writeCount      int
		removedDir      string
		removeCallCount int
	)
	service := &Service{files: fileOps{
		mkdirAll:  os.MkdirAll,
		mkdirTemp: os.MkdirTemp,
		writeFile: func(path string, data []byte, mode os.FileMode) error {
			mu.Lock()
			writeCount++
			count := writeCount
			mu.Unlock()
			if count == 2 {
				return errors.New("forced partial write")
			}
			return os.WriteFile(path, data, mode)
		},
		removeAll: func(path string) error {
			mu.Lock()
			removedDir = path
			removeCallCount++
			mu.Unlock()
			return os.RemoveAll(path)
		},
	}}
	_, err := service.Stage(context.Background(), public.ImageStagingRequest{
		StagingRoot: root,
		SourcePaths: []string{"first.png", "second.png"},
		ImageParts: []messages.ImagePart{
			{Bytes: []byte("first"), MediaType: "image/png"},
			{Bytes: []byte("second"), MediaType: "image/png"},
		},
		ToolDefinitions: []messages.ToolDefinition{{Name: public.ReadImageToolID}},
	})
	if err == nil || !strings.Contains(err.Error(), "forced partial write") {
		t.Fatalf("partial Stage error = %v, want forced write failure", err)
	}
	mu.Lock()
	gotDir, gotRemoveCalls := removedDir, removeCallCount
	mu.Unlock()
	if gotDir == "" || gotRemoveCalls != 1 {
		t.Fatalf("partial cleanup path/calls = %q/%d, want one cleanup call", gotDir, gotRemoveCalls)
	}
	if _, statErr := os.Stat(gotDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial staging directory stat error = %v, want not-exist", statErr)
	}
}

func TestService_PartialWriteFailureJoinsCleanupError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	writeErr := errors.New("forced partial write")
	cleanupErr := errors.New("forced cleanup failure")
	service := &Service{files: fileOps{
		mkdirAll:  os.MkdirAll,
		mkdirTemp: os.MkdirTemp,
		writeFile: func(string, []byte, os.FileMode) error { return writeErr },
		removeAll: func(string) error { return cleanupErr },
	}}
	_, err := service.Stage(context.Background(), public.ImageStagingRequest{
		StagingRoot:     root,
		SourcePaths:     []string{"image.png"},
		ImageParts:      []messages.ImagePart{{Bytes: []byte("image"), MediaType: "image/png"}},
		ToolDefinitions: []messages.ToolDefinition{{Name: public.ReadImageToolID}},
	})
	if !errors.Is(err, writeErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("partial Stage error = %v, want write and cleanup causes", err)
	}
}

func TestService_RefreshPreservesPathsAndCallbackErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	refreshErr := errors.New("refresh failed")
	refreshCalls := 0
	result, err := New().Stage(context.Background(), public.ImageStagingRequest{
		StagingRoot: root,
		SourcePaths: []string{"image.bin"},
		ImageParts:  []messages.ImagePart{{Bytes: []byte("image"), MediaType: "image/png"}},
		ToolDefinitions: []messages.ToolDefinition{{
			Name:       public.ReadImageToolID,
			Parameters: []messages.ToolParameter{{Name: "path", Type: "string", Required: true}},
		}},
		RefreshToolDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
			refreshCalls++
			if refreshCalls == 2 {
				return nil, refreshErr
			}
			return []messages.ToolDefinition{{Name: public.ReadImageToolID}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Stage returned error: %v", err)
	}
	refreshed, err := result.RefreshToolDefinitions(context.Background())
	if err != nil {
		t.Fatalf("refresh returned error: %v", err)
	}
	readImage := findDefinition(refreshed, public.ReadImageToolID)
	if readImage == nil || len(readImage.Parameters) != 1 || !strings.Contains(readImage.Parameters[0].Description, result.StagedPaths[0]) {
		t.Fatalf("refreshed read_image definition = %#v, want staged path", readImage)
	}
	refreshed[0].Parameters[0].Description = "caller mutation"
	refreshedAgain, err := result.RefreshToolDefinitions(context.Background())
	if !errors.Is(err, refreshErr) || refreshedAgain != nil {
		t.Fatalf("second refresh = %#v/%v, want original callback error", refreshedAgain, err)
	}
	if refreshCalls != 2 {
		t.Fatalf("refresh calls = %d, want 2", refreshCalls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := result.RefreshToolDefinitions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh error = %v, want context.Canceled", err)
	}
	if err := result.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestService_IsolatesConcurrentSessions(t *testing.T) {
	service := New()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for index, content := range [][]byte{[]byte("one"), []byte("two")} {
		index, content := index, content
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Stage(context.Background(), public.ImageStagingRequest{
				StagingRoot:     filepath.Join(t.TempDir(), "config"),
				SourcePaths:     []string{filepath.Join("session", string(rune('a'+index))+".png")},
				ImageParts:      []messages.ImagePart{{Bytes: content, MediaType: "image/png"}},
				ToolDefinitions: []messages.ToolDefinition{{Name: public.ReadImageToolID}},
			})
			if err != nil {
				errs <- err
				return
			}
			got, err := os.ReadFile(result.StagedPaths[0])
			if err != nil || !bytes.Equal(got, content) {
				errs <- errors.Join(err, fmt.Errorf("bytes = %q, want %q", got, content))
				return
			}
			errs <- result.Cleanup()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func findDefinition(definitions []messages.ToolDefinition, name string) *messages.ToolDefinition {
	for index := range definitions {
		if definitions[index].Name == name {
			return &definitions[index]
		}
	}
	return nil
}
