package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestReadXVideo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	data := []byte("\x00\x00\x00\x18ftypisomfixture")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, hash, err := readXVideo(path)
	sum := sha256.Sum256(data)
	if err != nil || string(got) != string(data) || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("read = %q %s %v", got, hash, err)
	}
	for _, bad := range []string{filepath.Dir(path), path + ".mov", path + "missing.mp4"} {
		if _, _, err := readXVideo(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if err := os.WriteFile(path, []byte("not a video at all"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readXVideo(path); err == nil {
		t.Fatal("accepted bad MP4 header")
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(xVideoMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, _, err := readXVideo(path); err == nil {
		t.Fatal("accepted oversized MP4")
	}
}

func TestDecodeXVideoReply(t *testing.T) {
	for _, input := range []string{`null`, `{}`, `{"ok":false,"error":{"code":"hash_mismatch","message":"mismatch"}}`, `broken`} {
		if _, err := decodeXVideoReply([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	reply, err := decodeXVideoReply([]byte(`{"ok":true,"data":{"video_processing":true}}`))
	if err != nil || !reply.Data.VideoProcessing {
		t.Fatalf("reply=%+v error=%v", reply, err)
	}
}
