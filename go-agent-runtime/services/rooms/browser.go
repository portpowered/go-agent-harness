package rooms

import (
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
)

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

func (b BrowserToolsConfig) Validate() error { return b.validateAt("browserTools") }

func (b BrowserToolsConfig) validateAt(field string) error {
	if b.Backend != BrowserToolsBackendWebMCP {
		return validation(field+".backend", b.Backend, "must be webmcp", ErrInvalidBrowserTools)
	}
	if !oneOf(b.Selection.AutoSelect, BrowserAutoSelectOff, BrowserAutoSelectSingle, BrowserAutoSelectPersisted) ||
		!oneOf(b.Policy.Approval, BrowserApprovalAlways, BrowserApprovalWrites, BrowserApprovalNever) ||
		!oneOf(b.Policy.CancelOnInterrupt, BrowserCancelOnInterruptNever, BrowserCancelOnInterruptReadOnly, BrowserCancelOnInterruptAlways) {
		return validation(field, "", "contains an unsupported browser option", ErrInvalidBrowserOption)
	}
	if b.Limits.InvocationTimeout <= 0 || b.Limits.MaxInputBytes < 0 || b.Limits.MaxResultBytes < 0 {
		return validation(field+".limits", "", "limits must be positive or non-negative", ErrInvalidBrowserOption)
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
