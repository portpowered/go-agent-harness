package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func (s *providerCaptureSpool) removeSpool() error {
	err := os.Remove(s.spoolPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove provider capture spool: %w", err)
	}
	return nil
}

type providerCaptureSpoolReader struct {
	decoder *json.Decoder
}

func (r *providerCaptureSpoolReader) Next() (gatewaytesting.CapturedSessionEvent, bool, error) {
	var event gatewaytesting.CapturedSessionEvent
	if err := r.decoder.Decode(&event); err != nil {
		if errors.Is(err, io.EOF) {
			return gatewaytesting.CapturedSessionEvent{}, false, nil
		}
		return gatewaytesting.CapturedSessionEvent{}, false, err
	}
	return event, true, nil
}

func writeProviderCaptureLine(file *os.File, encoded []byte) error {
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	_, err := file.Write([]byte{'\n'})
	return err
}

func providerCaptureEventBytes(event gatewaytesting.CapturedSessionEvent) int64 {
	return int64(len(event.Payload) + len(event.Data) + len(event.Type) + len(event.PayloadType) + len(event.Direction) + providerCaptureEventOverhead)
}

func cloneProviderCaptureEvent(event gatewaytesting.CapturedSessionEvent) gatewaytesting.CapturedSessionEvent {
	event.Payload = append([]byte(nil), event.Payload...)
	event.Data = append([]byte(nil), event.Data...)
	return event
}

func sameCapturePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

var _ recording.ProviderCaptureSink = (*providerCaptureSpool)(nil)
var _ gatewaytesting.SessionCaptureRecordReader = (*providerCaptureSpoolReader)(nil)
