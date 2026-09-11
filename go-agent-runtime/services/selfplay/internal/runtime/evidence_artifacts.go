package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

const maxManifestBytes int64 = 256 << 10

func redactEvidenceError(value, secret string) string {
	if value == "" {
		return ""
	}
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	for _, marker := range evidenceCredentialMarkers() {
		value = redactEvidenceMarker(value, marker)
	}
	return value
}

func evidenceCredentialMarkers() []string {
	return []string{"authorization: bearer ", "authorization=bearer ", "authorization: ", "authorization=", "x-api-key: ", "x-api-key=", "api-key: ", "api-key=", "api_key: ", "api_key=", "bearer "}
}

func redactEvidenceMarker(value, marker string) string {
	for {
		lower := strings.ToLower(value)
		start := strings.Index(lower, marker)
		if start < 0 {
			return value
		}
		markerEnd := start + len(marker)
		if strings.HasPrefix(value[markerEnd:], "[REDACTED]") {
			return value
		}
		end := evidenceCredentialTokenEnd(value, markerEnd)
		value = value[:markerEnd] + "[REDACTED]" + value[end:]
	}
}

func evidenceCredentialTokenEnd(value string, start int) int {
	for end := start; end < len(value); end++ {
		switch value[end] {
		case ' ', '\t', '\r', '\n', ',', ';', ')', ']', '}':
			return end
		}
	}
	return len(value)
}

func writeAtomicEvidenceJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON artifact: %w", err)
	}
	data = append(data, '\n')
	if int64(len(data)) > maxManifestBytes {
		return fmt.Errorf("%w: %s", selfplay.ErrEvidenceQuota, path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".self-play-artifact-*.tmp")
	if err != nil {
		return fmt.Errorf("create JSON artifact temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := writeEvidenceAll(tmp, data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write JSON artifact temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync JSON artifact temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close JSON artifact temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace JSON artifact: %w", err)
	}
	remove = false
	return nil
}
