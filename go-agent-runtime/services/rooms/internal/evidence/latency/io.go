package latency

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReadBundle loads one immutable room timing ledger. It does not inspect live
// state or infer missing boundaries from other evidence artifacts.
func ReadBundle(path string) (RoomLatencyBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RoomLatencyBundle{}, fmt.Errorf("read room latency artifact: %w", err)
	}
	var bundle RoomLatencyBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return RoomLatencyBundle{}, fmt.Errorf("decode room latency artifact: %w", err)
	}
	if bundle.SchemaVersion != RoomLatencyBundleSchemaVersion {
		return RoomLatencyBundle{}, fmt.Errorf("unsupported room latency schema version %d", bundle.SchemaVersion)
	}
	return bundle, nil
}

// AnalyzeFile derives a report from a caller-selected ledger path.
func AnalyzeFile(path string) (RoomLatencyReport, error) {
	bundle, err := ReadBundle(path)
	if err != nil {
		return RoomLatencyReport{}, err
	}
	return Analyze(bundle)
}

func (r *Recorder) Write(path string) (writeErr error) {
	if r == nil {
		return nil
	}
	data, err := json.MarshalIndent(r.Bundle(), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal room latency artifact: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".room-latency-*.tmp")
	if err != nil {
		return fmt.Errorf("create room latency artifact temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if !removeTemporary {
			return
		}
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			writeErr = errors.Join(writeErr, fmt.Errorf("remove room latency temporary file: %w", err))
		}
	}()
	if err := writeLatencyTemporary(temporary, data); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace room latency artifact: %w", err)
	}
	removeTemporary = false
	return nil
}

func writeLatencyTemporary(temporary *os.File, data []byte) (writeErr error) {
	defer func() {
		if err := temporary.Close(); err != nil {
			writeErr = errors.Join(writeErr, fmt.Errorf("close room latency artifact temporary file: %w", err))
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write room latency artifact temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync room latency artifact temporary file: %w", err)
	}
	return nil
}
