package rooms

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	BrowserToolsBackendWebMCP        = "webmcp"
	BrowserAutoSelectOff             = "off"
	BrowserAutoSelectSingle          = "single"
	BrowserAutoSelectPersisted       = "persisted"
	BrowserApprovalAlways            = "always"
	BrowserApprovalWrites            = "writes"
	BrowserApprovalNever             = "never"
	BrowserCancelOnInterruptNever    = "never"
	BrowserCancelOnInterruptReadOnly = "read-only"
	BrowserCancelOnInterruptAlways   = "always"

	defaultBrowserInvocationTimeout = 30 * time.Second
	defaultBrowserInputBytes        = 262144
	defaultBrowserResultBytes       = 262144
)

// UnsupportedBrowserToolsBackendError is a comparable, immutable browser backend error value suitable for errors.Is without package state.
type UnsupportedBrowserToolsBackendError string

func (e UnsupportedBrowserToolsBackendError) Error() string { return string(e) }

// ErrUnsupportedBrowserToolsBackend identifies a backend other than the WebMCP backend frozen by the room contract.
const ErrUnsupportedBrowserToolsBackend UnsupportedBrowserToolsBackendError = "unsupported room browser tools backend"

// InvalidBrowserToolsOptionError is the stable CLI compatibility error value.
type InvalidBrowserToolsOptionError string

func (e InvalidBrowserToolsOptionError) Error() string { return string(e) }

// ErrInvalidBrowserToolsOption identifies an invalid browser option while
// preserving the older CLI-facing sentinel alongside ErrInvalidBrowserOption.
const ErrInvalidBrowserToolsOption InvalidBrowserToolsOptionError = "invalid room browser tools option"

// BrowserToolsConfig is the normalized optional browser capability policy.
// It contains no browser connection or process handle; composition owns those.
type BrowserToolsConfig struct {
	Backend    string                  `json:"backend" yaml:"backend"`
	Connection BrowserConnectionConfig `json:"connection" yaml:"connection"`
	Selection  BrowserSelectionConfig  `json:"selection" yaml:"selection"`
	Policy     BrowserPolicyConfig     `json:"policy" yaml:"policy"`
	Limits     BrowserLimitsConfig     `json:"limits" yaml:"limits"`
	Recording  BrowserRecordingConfig  `json:"recording" yaml:"recording"`
	Replay     BrowserReplayConfig     `json:"replay" yaml:"replay"`
}
type BrowserConnectionConfig struct {
	CDPURL           string `json:"cdp_url" yaml:"cdp_url"`
	WSEndpoint       string `json:"ws_endpoint" yaml:"ws_endpoint"`
	UserDataDir      string `json:"user_data_dir" yaml:"user_data_dir"`
	AllowProcessScan bool   `json:"allow_process_scan" yaml:"allow_process_scan"`
	AllowRemoteCDP   bool   `json:"allow_remote_cdp" yaml:"allow_remote_cdp"`
}
type BrowserSelectionConfig struct {
	Browser     string `json:"browser" yaml:"browser"`
	Tab         string `json:"tab" yaml:"tab"`
	Origin      string `json:"origin" yaml:"origin"`
	AutoSelect  string `json:"auto_select" yaml:"auto_select"`
	ActivateTab bool   `json:"activate_tab" yaml:"activate_tab"`
	Persist     bool   `json:"persist" yaml:"persist"`
}
type BrowserPolicyConfig struct {
	AllowedOrigins    []string `json:"allowed_origins" yaml:"allowed_origins"`
	DeniedOrigins     []string `json:"denied_origins" yaml:"denied_origins"`
	Approval          string   `json:"approval" yaml:"approval"`
	CancelOnInterrupt string   `json:"cancel_on_interrupt" yaml:"cancel_on_interrupt"`
}
type BrowserLimitsConfig struct {
	InvocationTimeout  time.Duration `json:"-" yaml:"-"`
	MaxInputBytes      int           `json:"max_input_bytes" yaml:"max_input_bytes"`
	MaxResultBytes     int           `json:"max_result_bytes" yaml:"max_result_bytes"`
	SerializePerTarget bool          `json:"serialize_per_target" yaml:"serialize_per_target"`
}
type BrowserRecordingConfig struct {
	Enabled           bool `json:"enabled" yaml:"enabled"`
	IncludeArguments  bool `json:"include_arguments" yaml:"include_arguments"`
	IncludeResults    bool `json:"include_results" yaml:"include_results"`
	RedactURLQuery    bool `json:"redact_url_query" yaml:"redact_url_query"`
	RedactURLFragment bool `json:"redact_url_fragment" yaml:"redact_url_fragment"`
}
type BrowserReplayConfig struct {
	Path   string `json:"path" yaml:"path"`
	Strict bool   `json:"strict" yaml:"strict"`
}

// BrowserToolsDefaults provides the complete browser option set used when a
// participant includes an empty browserTools object.
type BrowserToolsDefaults struct{}

// Config returns a fresh default value with initialized option lists.
func (BrowserToolsDefaults) Config() BrowserToolsConfig {
	return BrowserToolsConfig{
		Backend: BrowserToolsBackendWebMCP,
		Selection: BrowserSelectionConfig{
			AutoSelect: BrowserAutoSelectOff,
			Persist:    true,
		},
		Policy: BrowserPolicyConfig{
			AllowedOrigins:    []string{},
			DeniedOrigins:     []string{},
			Approval:          BrowserApprovalWrites,
			CancelOnInterrupt: BrowserCancelOnInterruptReadOnly,
		},
		Limits: BrowserLimitsConfig{
			InvocationTimeout:  defaultBrowserInvocationTimeout,
			MaxInputBytes:      defaultBrowserInputBytes,
			MaxResultBytes:     defaultBrowserResultBytes,
			SerializePerTarget: true,
		},
		Recording: BrowserRecordingConfig{
			IncludeArguments:  true,
			IncludeResults:    true,
			RedactURLQuery:    true,
			RedactURLFragment: true,
		},
		Replay: BrowserReplayConfig{Strict: true},
	}
}

// Validate validates a normalized browser capability at its public root.
func (b BrowserToolsConfig) Validate() error { return b.ValidateAt("browserTools") }

// ValidateAt validates a normalized browser capability while preserving the
// participant-qualified field root used by document admission.
func (b BrowserToolsConfig) ValidateAt(field string) error {
	if b.Backend != BrowserToolsBackendWebMCP {
		return validation(field+".backend", b.Backend, fmt.Sprintf("must be %q", BrowserToolsBackendWebMCP), errors.Join(ErrInvalidBrowserTools, ErrUnsupportedBrowserToolsBackend))
	}
	if err := validateBrowserEndpoint(field+".connection.cdp_url", b.Connection.CDPURL, []string{"http", "https"}, false, b.Connection.AllowRemoteCDP); err != nil {
		return err
	}
	if err := validateBrowserEndpoint(field+".connection.ws_endpoint", b.Connection.WSEndpoint, []string{"ws", "wss"}, true, b.Connection.AllowRemoteCDP); err != nil {
		return err
	}
	if !oneOf(b.Selection.AutoSelect, BrowserAutoSelectOff, BrowserAutoSelectSingle, BrowserAutoSelectPersisted) {
		return browserOptionError(field+".selection.auto_select", "must be one of off, single, persisted")
	}
	if !oneOf(b.Policy.Approval, BrowserApprovalAlways, BrowserApprovalWrites, BrowserApprovalNever) {
		return browserOptionError(field+".policy.approval", "must be one of always, writes, never")
	}
	if !oneOf(b.Policy.CancelOnInterrupt, BrowserCancelOnInterruptNever, BrowserCancelOnInterruptReadOnly, BrowserCancelOnInterruptAlways) {
		return browserOptionError(field+".policy.cancel_on_interrupt", "must be one of never, read-only, always")
	}
	if b.Limits.InvocationTimeout <= 0 {
		return browserOptionError(field+".limits.invocation_timeout", "must be positive")
	}
	if b.Limits.MaxInputBytes < 0 {
		return browserOptionError(field+".limits.max_input_bytes", "must be non-negative")
	}
	if b.Limits.MaxResultBytes < 0 {
		return browserOptionError(field+".limits.max_result_bytes", "must be non-negative")
	}
	return nil
}
func browserOptionError(field, problem string) error {
	return validation(field, "", problem, errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserOption, ErrInvalidBrowserToolsOption))
}
func validateBrowserEndpoint(field, raw string, schemes []string, websocket, allowRemote bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts, ok := browserEndpointParts(raw)
	if !ok || !oneOf(parts.scheme, schemes...) {
		return validation(field, "", "must be a valid browser endpoint", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserEndpoint))
	}
	if parts.hasCredentials {
		return validation(field, "", "must not contain endpoint credentials", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserEndpoint))
	}
	if websocket && (!strings.HasPrefix(parts.path, "/devtools/browser/") || strings.TrimPrefix(parts.path, "/devtools/browser/") == "") {
		return validation(field, "", "must identify a browser websocket under /devtools/browser/", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserEndpoint))
	}
	if !browserHostIsLoopback(parts.host) && !allowRemote {
		return validation(field, "", "must use a loopback host unless connection.allow_remote_cdp is true", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserEndpoint))
	}
	return nil
}
func browserHostIsLoopback(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "localhost.") {
		return true
	}
	if host == "::1" {
		return true
	}
	return browserLoopbackIPv4(host)
}
func browserLoopbackIPv4(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 || parts[0] != "127" {
		return false
	}
	for _, part := range parts[1:] {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 255 {
			return false
		}
	}
	return true
}
func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

type browserToolsJSON struct {
	Backend    string                 `json:"backend"`
	Connection browserConnectionJSON  `json:"connection"`
	Selection  BrowserSelectionConfig `json:"selection"`
	Policy     BrowserPolicyConfig    `json:"policy"`
	Limits     browserLimitsJSON      `json:"limits"`
	Recording  BrowserRecordingConfig `json:"recording"`
	Replay     BrowserReplayConfig    `json:"replay"`
}
type browserConnectionJSON struct {
	CDPURL           string `json:"cdp_url"`
	WSEndpoint       string `json:"ws_endpoint"`
	UserDataDir      string `json:"user_data_dir"`
	AllowProcessScan bool   `json:"allow_process_scan"`
	AllowRemoteCDP   bool   `json:"allow_remote_cdp"`
}
type browserLimitsJSON struct {
	InvocationTimeout  string `json:"invocation_timeout"`
	MaxInputBytes      int    `json:"max_input_bytes"`
	MaxResultBytes     int    `json:"max_result_bytes"`
	SerializePerTarget bool   `json:"serialize_per_target"`
}

func (b BrowserToolsConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(browserToolsJSON{
		Backend: b.Backend,
		Connection: browserConnectionJSON{
			CDPURL:           redactBrowserEndpoint(b.Connection.CDPURL, false),
			WSEndpoint:       redactBrowserEndpoint(b.Connection.WSEndpoint, true),
			UserDataDir:      b.Connection.UserDataDir,
			AllowProcessScan: b.Connection.AllowProcessScan,
			AllowRemoteCDP:   b.Connection.AllowRemoteCDP,
		},
		Selection: b.Selection,
		Policy:    b.Policy,
		Limits: browserLimitsJSON{
			InvocationTimeout:  b.Limits.InvocationTimeout.String(),
			MaxInputBytes:      b.Limits.MaxInputBytes,
			MaxResultBytes:     b.Limits.MaxResultBytes,
			SerializePerTarget: b.Limits.SerializePerTarget,
		},
		Recording: b.Recording,
		Replay:    b.Replay,
	})
}

type browserToolsYAML struct {
	Backend    string                 `yaml:"backend"`
	Connection browserConnectionYAML  `yaml:"connection"`
	Selection  BrowserSelectionConfig `yaml:"selection"`
	Policy     BrowserPolicyConfig    `yaml:"policy"`
	Limits     browserLimitsYAML      `yaml:"limits"`
	Recording  BrowserRecordingConfig `yaml:"recording"`
	Replay     BrowserReplayConfig    `yaml:"replay"`
}
type browserConnectionYAML struct {
	CDPURL           string `yaml:"cdp_url"`
	WSEndpoint       string `yaml:"ws_endpoint"`
	UserDataDir      string `yaml:"user_data_dir"`
	AllowProcessScan bool   `yaml:"allow_process_scan"`
	AllowRemoteCDP   bool   `yaml:"allow_remote_cdp"`
}
type browserLimitsYAML struct {
	InvocationTimeout  string `yaml:"invocation_timeout"`
	MaxInputBytes      int    `yaml:"max_input_bytes"`
	MaxResultBytes     int    `yaml:"max_result_bytes"`
	SerializePerTarget bool   `yaml:"serialize_per_target"`
}

func (b BrowserToolsConfig) MarshalYAML() (any, error) {
	return browserToolsYAML{
		Backend: b.Backend,
		Connection: browserConnectionYAML{
			CDPURL:           redactBrowserEndpoint(b.Connection.CDPURL, false),
			WSEndpoint:       redactBrowserEndpoint(b.Connection.WSEndpoint, true),
			UserDataDir:      b.Connection.UserDataDir,
			AllowProcessScan: b.Connection.AllowProcessScan,
			AllowRemoteCDP:   b.Connection.AllowRemoteCDP,
		},
		Selection: b.Selection,
		Policy:    b.Policy,
		Limits: browserLimitsYAML{
			InvocationTimeout:  b.Limits.InvocationTimeout.String(),
			MaxInputBytes:      b.Limits.MaxInputBytes,
			MaxResultBytes:     b.Limits.MaxResultBytes,
			SerializePerTarget: b.Limits.SerializePerTarget,
		},
		Recording: b.Recording,
		Replay:    b.Replay,
	}, nil
}
func redactBrowserEndpoint(raw string, websocket bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts, ok := browserEndpointParts(raw)
	if !ok || parts.hasCredentials {
		return "<redacted endpoint>"
	}
	if websocket {
		return parts.scheme + "://" + parts.authority + "/%3Credacted%3E"
	}
	return parts.scheme + "://" + parts.authority + parts.path
}

type browserEndpoint struct {
	scheme         string
	authority      string
	host           string
	path           string
	hasCredentials bool
}

func browserEndpointParts(raw string) (browserEndpoint, bool) {
	separator := strings.Index(raw, "://")
	if separator <= 0 || separator+3 >= len(raw) {
		return browserEndpoint{}, false
	}
	rest := raw[separator+3:]
	authorityEnd := len(rest)
	for index, value := range rest {
		if value == '/' || value == '?' || value == '#' {
			authorityEnd = index
			break
		}
	}
	authority := rest[:authorityEnd]
	if authority == "" || strings.ContainsAny(authority, " \t\r\n") {
		return browserEndpoint{}, false
	}
	host, valid := browserEndpointHost(authority)
	if !valid {
		return browserEndpoint{}, false
	}
	path := rest[authorityEnd:]
	if query := strings.IndexAny(path, "?#"); query >= 0 {
		path = path[:query]
	}
	return browserEndpoint{
		scheme:         strings.ToLower(raw[:separator]),
		authority:      authority,
		host:           host,
		path:           path,
		hasCredentials: strings.Contains(authority, "@"),
	}, true
}
func browserEndpointHost(authority string) (string, bool) {
	if strings.Contains(authority, "@") {
		return authority[strings.LastIndex(authority, "@")+1:], true
	}
	host := authority
	if strings.HasPrefix(host, "[") {
		closing := strings.IndexByte(host, ']')
		if closing < 0 || (closing+1 < len(host) && host[closing+1] != ':') {
			return "", false
		}
		return host[1:closing], validBrowserPort(host[closing+1:])
	}
	if strings.Count(host, ":") > 1 {
		return "", false
	}
	if colon := strings.LastIndexByte(host, ':'); colon >= 0 {
		if !validBrowserPort(host[colon+1:]) {
			return "", false
		}
		host = host[:colon]
	}
	return host, host != ""
}
func validBrowserPort(port string) bool {
	if port == "" {
		return true
	}
	for _, value := range port {
		if value < '0' || value > '9' {
			return false
		}
	}
	return true
}
