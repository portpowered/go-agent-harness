package discovery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxActivePortBytes int64 = 4096

// Endpoint URL schemes accepted for the CDP HTTP discovery endpoint.
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// closeAfterRead releases a read-only resource once its content has been
// consumed. A close failure cannot invalidate bytes that were already read and
// validated, so it is deliberately not surfaced to the caller.
func closeAfterRead(closer io.Closer) {
	if err := closer.Close(); err != nil {
		return
	}
}

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
	defer closeAfterRead(file)

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

type parseURLFailure struct{ reason string }

func (e *parseURLFailure) Error() string {
	if e == nil {
		return "invalid endpoint"
	}
	return e.reason
}

func parseHTTPURL(raw string) (*url.URL, *parseURLFailure) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return nil, &parseURLFailure{reason: "malformed_endpoint"}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS {
		return nil, &parseURLFailure{reason: "unsupported_endpoint_scheme"}
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, &parseURLFailure{reason: "missing_endpoint_host"}
	}
	if parsed.User != nil {
		return nil, &parseURLFailure{reason: "credentials_not_allowed"}
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, &parseURLFailure{reason: "invalid_endpoint_port"}
		}
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

type normalizedWebSocketURL struct {
	url      *url.URL
	loopback bool
}

func parseBrowserWebSocketURL(raw string) (normalizedWebSocketURL, *parseURLFailure) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "malformed_browser_websocket"}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "unsupported_websocket_scheme"}
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "missing_websocket_host"}
	}
	if parsed.User != nil {
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "credentials_not_allowed"}
	}
	if !strings.HasPrefix(parsed.Path, "/devtools/browser/") || strings.TrimPrefix(parsed.Path, "/devtools/browser/") == "" {
		if strings.HasPrefix(parsed.Path, "/devtools/page/") {
			return normalizedWebSocketURL{}, &parseURLFailure{reason: "page_websocket_not_browser_websocket"}
		}
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "browser_websocket_path_required"}
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return normalizedWebSocketURL{}, &parseURLFailure{reason: "invalid_websocket_port"}
		}
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return normalizedWebSocketURL{url: parsed, loopback: isLoopbackHost(parsed.Hostname())}, nil
}

func versionPath(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" || path == "/" {
		return "/json/version"
	}
	if strings.HasSuffix(path, "/json/version") {
		return path
	}
	return path + "/json/version"
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "localhost.") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func addressClass(loopback bool) string {
	if loopback {
		return "loopback"
	}
	return "non_loopback"
}

func addressClassFromEndpointKind(kind EndpointKind) string {
	if kind == EndpointKindCDPHTTP || kind == EndpointKindBrowserWebSocket || kind == EndpointKindActivePort {
		return "loopback"
	}
	return "non_loopback"
}
