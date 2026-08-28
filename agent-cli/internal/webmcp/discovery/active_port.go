package discovery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxActivePortBytes int64 = 4096

// FileActivePortReader reads the standard DevToolsActivePort file without
// assuming that the harness owns the browser or profile.
type FileActivePortReader struct{}

// Read implements ActivePortReader.
func (FileActivePortReader) Read(ctx context.Context, userDataDir string) (ActivePortRecord, error) {
	if err := ctx.Err(); err != nil {
		return ActivePortRecord{}, err
	}
	path := filepath.Join(userDataDir, "DevToolsActivePort")
	file, err := os.Open(path)
	if err != nil {
		return ActivePortRecord{}, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxActivePortBytes+1))
	if err != nil {
		return ActivePortRecord{}, err
	}
	if int64(len(data)) > maxActivePortBytes {
		return ActivePortRecord{}, errors.New("active-port record is too large")
	}
	if err := ctx.Err(); err != nil {
		return ActivePortRecord{}, err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lines := make([]string, 0, 2)
	for scanner.Scan() {
		lines = append(lines, strings.TrimSpace(scanner.Text()))
		if len(lines) > 2 {
			return ActivePortRecord{}, errors.New("too many active-port lines")
		}
	}
	if err := scanner.Err(); err != nil {
		return ActivePortRecord{}, err
	}
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return ActivePortRecord{}, errors.New("active-port record is incomplete")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1 || port > 65535 {
		return ActivePortRecord{}, errors.New("active-port port is invalid")
	}
	if strings.ContainsAny(lines[1], "\r\n") {
		return ActivePortRecord{}, errors.New("active-port websocket path is invalid")
	}
	return ActivePortRecord{Port: port, BrowserWebSocketPath: lines[1]}, nil
}
func endpointFromActivePort(record ActivePortRecord) (Endpoint, error) {
	if record.Port < 1 || record.Port > 65535 {
		return Endpoint{}, fmt.Errorf("invalid active-port port")
	}
	path := strings.TrimSpace(record.BrowserWebSocketPath)
	if path == "" {
		return Endpoint{}, errors.New("active-port websocket path is empty")
	}
	if parsed, err := url.Parse(path); err == nil && parsed.Scheme != "" {
		return Endpoint{CDPURL: fmt.Sprintf("http://%s/json/version", parsed.Host), BrowserWSEndpoint: path}, nil
	}
	if !strings.HasPrefix(path, "/") {
		return Endpoint{}, errors.New("active-port websocket path is invalid")
	}
	return Endpoint{
		CDPURL:            fmt.Sprintf("http://127.0.0.1:%d/json/version", record.Port),
		BrowserWSEndpoint: fmt.Sprintf("ws://127.0.0.1:%d%s", record.Port, path),
	}, nil
}
