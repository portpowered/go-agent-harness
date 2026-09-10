package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	roommedia "github.com/portpowered/go-agent-harness/audio-runtime-c36-room-media-epoch-consumer"
)

const maxRequestBytes = 1 << 20

func main() {
	request, err := decodeRequest(os.Stdin)
	if err != nil {
		fail(err)
	}
	if strings.TrimSpace(request.Mode) == "" {
		fail(errors.New("mode is required"))
	}
	if strings.TrimSpace(request.OutputDir) == "" && request.Mode != "cancel-before-start" && request.Mode != "cancel-active" {
		fail(errors.New("output_dir is required"))
	}
	report, err := roommedia.Run(request)
	if err != nil {
		fail(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		fail(fmt.Errorf("encode report: %w", err))
	}
}

func decodeRequest(reader io.Reader) (roommedia.Request, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	if err != nil {
		return roommedia.Request{}, fmt.Errorf("read request: %w", err)
	}
	if len(data) > maxRequestBytes {
		return roommedia.Request{}, fmt.Errorf("request exceeds maximum size of %d bytes", maxRequestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request roommedia.Request
	if err := decoder.Decode(&request); err != nil {
		return roommedia.Request{}, fmt.Errorf("decode request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return roommedia.Request{}, errors.New("request must contain exactly one JSON object")
		}
		return roommedia.Request{}, fmt.Errorf("trailing request data: %w", err)
	}
	return request, nil
}

func fail(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}
