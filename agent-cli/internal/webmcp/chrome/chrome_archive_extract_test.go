package chrome

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeDeflatedArchive writes a highly compressible archive whose entries
// together expand to entries*entrySize bytes.
func writeDeflatedArchive(t *testing.T, entries, entrySize int) string {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for i := range entries {
		header := &zip.FileHeader{Name: filepath.ToSlash(filepath.Join("chrome", string(rune('a'+i)))), Method: zip.Deflate}
		header.SetMode(0o600)
		file, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		if _, err := file.Write(bytes.Repeat([]byte{0}, entrySize)); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	path := filepath.Join(t.TempDir(), "chrome.zip")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return path
}

func TestExtractManagedChromeArchiveBoundsDecompressedSize(t *testing.T) {
	const limit = 64 << 10
	path := writeDeflatedArchive(t, 2, limit/2+1)
	err := extractManagedChromeArchiveWithin(path, t.TempDir(), limit)
	if !errors.Is(err, errChromeArchiveTooLarge) {
		t.Fatalf("extract over-budget archive = %v, want %v", err, errChromeArchiveTooLarge)
	}

	within := writeDeflatedArchive(t, 2, limit/2)
	destination := t.TempDir()
	if err := extractManagedChromeArchiveWithin(within, destination, limit); err != nil {
		t.Fatalf("extract archive at budget: %v", err)
	}
	info, err := os.Stat(filepath.Join(destination, "chrome", "b"))
	if err != nil || info.Size() != limit/2 {
		t.Fatalf("extracted entry = %v, %v; want %d bytes", info, err, limit/2)
	}
}
