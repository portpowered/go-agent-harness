package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	yamlv3 "gopkg.in/yaml.v3"
)

var (
	// ErrInvalidBrowserTools identifies a malformed participant browser
	// capability object. It is deliberately separate from the ordinary
	// participant errors so callers can report browser admission distinctly
	// without parsing a rendered validation message.
	ErrInvalidBrowserTools = errors.New("invalid room participant browser tools")
	// ErrUnsupportedBrowserToolsBackend identifies a backend other than the
	// WebMCP backend frozen by the room contract.
	ErrUnsupportedBrowserToolsBackend = errors.New("unsupported room participant browser tools backend")
	// ErrInvalidBrowserToolsOption identifies a browser option that is not
	// compatible with the session browser configuration contract.
	ErrInvalidBrowserToolsOption = errors.New("invalid room participant browser tools option")
	// ErrInvalidBrowserEndpoint identifies an endpoint that cannot be admitted
	// safely without opening a browser connection.
	ErrInvalidBrowserEndpoint = errors.New("invalid room participant browser endpoint")
)

// BrowserToolsConfig is the normalized, credential-free browser capability
// configuration for one room participant. The presence of this object on a
// Participant is the activation switch; Backend is intentionally flattened
// here so the room manifest reads browserTools.backend rather than carrying
// the session config's disabled/enabled wrapper.
//
// Endpoint strings are retained internally for runtime planning, but their
// query, fragment, and browser websocket path data is removed by the custom
// marshalers below before a manifest or evidence bundle records this value.
type BrowserToolsConfig struct {
	Backend    string                  `json:"backend" yaml:"backend"`
	Connection BrowserConnectionConfig `json:"connection" yaml:"connection"`
	Selection  BrowserSelectionConfig  `json:"selection" yaml:"selection"`
	Policy     BrowserPolicyConfig     `json:"policy" yaml:"policy"`
	Limits     BrowserLimitsConfig     `json:"limits" yaml:"limits"`
	Recording  BrowserRecordingConfig  `json:"recording" yaml:"recording"`
	Replay     BrowserReplayConfig     `json:"replay" yaml:"replay"`
}

// BrowserConnectionConfig mirrors config.BrowserConnectionConfig without
// coupling the room manifest's public shape to the session's tools wrapper.
type BrowserConnectionConfig struct {
	CDPURL           string `json:"cdp_url" yaml:"cdp_url"`
	WSEndpoint       string `json:"ws_endpoint" yaml:"ws_endpoint"`
	UserDataDir      string `json:"user_data_dir" yaml:"user_data_dir"`
	AllowProcessScan bool   `json:"allow_process_scan" yaml:"allow_process_scan"`
	AllowRemoteCDP   bool   `json:"allow_remote_cdp" yaml:"allow_remote_cdp"`
}

// BrowserSelectionConfig mirrors the session browser target selection
// options. Persistence is later overridden to an in-memory store by the room
// capability composition story; the manifest still accepts the session
// equivalent option so it can be normalized and recorded consistently.
type BrowserSelectionConfig struct {
	Browser     string `json:"browser" yaml:"browser"`
	Tab         string `json:"tab" yaml:"tab"`
	Origin      string `json:"origin" yaml:"origin"`
	AutoSelect  string `json:"auto_select" yaml:"auto_select"`
	ActivateTab bool   `json:"activate_tab" yaml:"activate_tab"`
	Persist     bool   `json:"persist" yaml:"persist"`
}

// BrowserPolicyConfig mirrors the session browser origin and invocation
// policy options.
type BrowserPolicyConfig struct {
	AllowedOrigins    []string `json:"allowed_origins" yaml:"allowed_origins"`
	DeniedOrigins     []string `json:"denied_origins" yaml:"denied_origins"`
	Approval          string   `json:"approval" yaml:"approval"`
	CancelOnInterrupt string   `json:"cancel_on_interrupt" yaml:"cancel_on_interrupt"`
}

// BrowserLimitsConfig mirrors the session browser invocation limits.
type BrowserLimitsConfig struct {
	InvocationTimeout  time.Duration `json:"-" yaml:"-"`
	MaxInputBytes      int           `json:"max_input_bytes" yaml:"max_input_bytes"`
	MaxResultBytes     int           `json:"max_result_bytes" yaml:"max_result_bytes"`
	SerializePerTarget bool          `json:"serialize_per_target" yaml:"serialize_per_target"`
}

// BrowserRecordingConfig mirrors the session semantic browser recording
// options.
type BrowserRecordingConfig struct {
	Enabled           bool `json:"enabled" yaml:"enabled"`
	IncludeArguments  bool `json:"include_arguments" yaml:"include_arguments"`
	IncludeResults    bool `json:"include_results" yaml:"include_results"`
	RedactURLQuery    bool `json:"redact_url_query" yaml:"redact_url_query"`
	RedactURLFragment bool `json:"redact_url_fragment" yaml:"redact_url_fragment"`
}

// BrowserReplayConfig mirrors the session semantic browser replay options.
type BrowserReplayConfig struct {
	Path   string `json:"path" yaml:"path"`
	Strict bool   `json:"strict" yaml:"strict"`
}

// DefaultBrowserToolsConfig returns the complete participant browser option
// set with the same defaults as config.DefaultBrowserConfig. The returned
// value represents an enabled participant because it is only installed when
// the optional browserTools object is present.
func DefaultBrowserToolsConfig() BrowserToolsConfig {
	defaults := config.DefaultBrowserConfig()
	return BrowserToolsConfig{
		Backend: defaults.Tools.Backend,
		Connection: BrowserConnectionConfig{
			CDPURL:           defaults.Connection.CDPURL,
			WSEndpoint:       defaults.Connection.WSEndpoint,
			UserDataDir:      defaults.Connection.UserDataDir,
			AllowProcessScan: defaults.Connection.AllowProcessScan,
			AllowRemoteCDP:   defaults.Connection.AllowRemoteCDP,
		},
		Selection: BrowserSelectionConfig{
			Browser:     defaults.Selection.Browser,
			Tab:         defaults.Selection.Tab,
			Origin:      defaults.Selection.Origin,
			AutoSelect:  defaults.Selection.AutoSelect,
			ActivateTab: defaults.Selection.ActivateTab,
			Persist:     defaults.Selection.Persist,
		},
		Policy: BrowserPolicyConfig{
			AllowedOrigins:    cloneBrowserToolsStrings(defaults.Policy.AllowedOrigins),
			DeniedOrigins:     cloneBrowserToolsStrings(defaults.Policy.DeniedOrigins),
			Approval:          defaults.Policy.Approval,
			CancelOnInterrupt: defaults.Policy.CancelOnInterrupt,
		},
		Limits: BrowserLimitsConfig{
			InvocationTimeout:  defaults.Limits.InvocationTimeout,
			MaxInputBytes:      defaults.Limits.MaxInputBytes,
			MaxResultBytes:     defaults.Limits.MaxResultBytes,
			SerializePerTarget: defaults.Limits.SerializePerTarget,
		},
		Recording: BrowserRecordingConfig{
			Enabled:           defaults.Recording.Enabled,
			IncludeArguments:  defaults.Recording.IncludeArguments,
			IncludeResults:    defaults.Recording.IncludeResults,
			RedactURLQuery:    defaults.Recording.RedactURLQuery,
			RedactURLFragment: defaults.Recording.RedactURLFragment,
		},
		Replay: BrowserReplayConfig{
			Path:   defaults.Replay.Path,
			Strict: defaults.Replay.Strict,
		},
	}
}

// AsBrowserConfig converts a normalized room participant capability into the
// existing session browser configuration. Presence of BrowserTools is the
// activation decision, so the returned Tools.Enabled is always true.
func (b BrowserToolsConfig) AsBrowserConfig() config.BrowserConfig {
	defaults := config.DefaultBrowserConfig()
	defaults.Tools.Enabled = true
	defaults.Tools.Backend = b.Backend
	defaults.Connection.CDPURL = b.Connection.CDPURL
	defaults.Connection.WSEndpoint = b.Connection.WSEndpoint
	defaults.Connection.UserDataDir = b.Connection.UserDataDir
	defaults.Connection.AllowProcessScan = b.Connection.AllowProcessScan
	defaults.Connection.AllowRemoteCDP = b.Connection.AllowRemoteCDP
	defaults.Selection.Browser = b.Selection.Browser
	defaults.Selection.Tab = b.Selection.Tab
	defaults.Selection.Origin = b.Selection.Origin
	defaults.Selection.AutoSelect = b.Selection.AutoSelect
	defaults.Selection.ActivateTab = b.Selection.ActivateTab
	defaults.Selection.Persist = b.Selection.Persist
	defaults.Policy.AllowedOrigins = cloneBrowserToolsStrings(b.Policy.AllowedOrigins)
	defaults.Policy.DeniedOrigins = cloneBrowserToolsStrings(b.Policy.DeniedOrigins)
	defaults.Policy.Approval = b.Policy.Approval
	defaults.Policy.CancelOnInterrupt = b.Policy.CancelOnInterrupt
	defaults.Limits.InvocationTimeout = b.Limits.InvocationTimeout
	defaults.Limits.MaxInputBytes = b.Limits.MaxInputBytes
	defaults.Limits.MaxResultBytes = b.Limits.MaxResultBytes
	defaults.Limits.SerializePerTarget = b.Limits.SerializePerTarget
	defaults.Recording.Enabled = b.Recording.Enabled
	defaults.Recording.IncludeArguments = b.Recording.IncludeArguments
	defaults.Recording.IncludeResults = b.Recording.IncludeResults
	defaults.Recording.RedactURLQuery = b.Recording.RedactURLQuery
	defaults.Recording.RedactURLFragment = b.Recording.RedactURLFragment
	defaults.Replay.Path = b.Replay.Path
	defaults.Replay.Strict = b.Replay.Strict
	return defaults
}

// Validate validates a normalized participant browser configuration using a
// stable browser-tools field root. Manifest.Validate uses the participant
// qualified variant below.
func (b BrowserToolsConfig) Validate() error {
	return b.validateAt("browserTools")
}

func (b BrowserToolsConfig) validateAt(field string) error {
	if b.Backend != config.BrowserToolsBackendWebMCP {
		return validation(field+".backend", b.Backend, fmt.Sprintf("must be %q", config.BrowserToolsBackendWebMCP), errors.Join(ErrInvalidBrowserTools, ErrUnsupportedBrowserToolsBackend))
	}
	if err := validateRoomBrowserEndpoint(field+".connection.cdp_url", b.Connection.CDPURL, []string{"http", "https"}, false, b.Connection.AllowRemoteCDP); err != nil {
		return err
	}
	if err := validateRoomBrowserEndpoint(field+".connection.ws_endpoint", b.Connection.WSEndpoint, []string{"ws", "wss"}, true, b.Connection.AllowRemoteCDP); err != nil {
		return err
	}
	if !containsRoomBrowserString([]string{config.BrowserAutoSelectOff, config.BrowserAutoSelectSingle, config.BrowserAutoSelectPersisted}, b.Selection.AutoSelect) {
		return browserToolsOptionError(field+".selection.auto_select", fmt.Sprintf("must be one of %s", strings.Join([]string{config.BrowserAutoSelectOff, config.BrowserAutoSelectSingle, config.BrowserAutoSelectPersisted}, ", ")))
	}
	if !containsRoomBrowserString([]string{config.BrowserApprovalAlways, config.BrowserApprovalWrites, config.BrowserApprovalNever}, b.Policy.Approval) {
		return browserToolsOptionError(field+".policy.approval", fmt.Sprintf("must be one of %s", strings.Join([]string{config.BrowserApprovalAlways, config.BrowserApprovalWrites, config.BrowserApprovalNever}, ", ")))
	}
	if !containsRoomBrowserString([]string{config.BrowserCancelOnInterruptNever, config.BrowserCancelOnInterruptReadOnly, config.BrowserCancelOnInterruptAlways}, b.Policy.CancelOnInterrupt) {
		return browserToolsOptionError(field+".policy.cancel_on_interrupt", fmt.Sprintf("must be one of %s", strings.Join([]string{config.BrowserCancelOnInterruptNever, config.BrowserCancelOnInterruptReadOnly, config.BrowserCancelOnInterruptAlways}, ", ")))
	}
	if b.Limits.InvocationTimeout <= 0 {
		return browserToolsOptionError(field+".limits.invocation_timeout", "must be positive")
	}
	if b.Limits.MaxInputBytes < 0 {
		return browserToolsOptionError(field+".limits.max_input_bytes", "must be non-negative")
	}
	if b.Limits.MaxResultBytes < 0 {
		return browserToolsOptionError(field+".limits.max_result_bytes", "must be non-negative")
	}
	return nil
}

func browserToolsOptionError(field, problem string) error {
	return validation(field, "", problem, errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserToolsOption))
}

func validateRoomBrowserEndpoint(field, raw string, schemes []string, browserWebSocket, allowRemote bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || !containsRoomBrowserString(schemes, strings.ToLower(parsed.Scheme)) {
		return validation(field, "", "must be a valid browser endpoint", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserEndpoint))
	}
	if parsed.User != nil {
		return validation(field, "", "must not contain endpoint credentials", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserEndpoint))
	}
	if browserWebSocket && (!strings.HasPrefix(parsed.Path, "/devtools/browser/") || strings.TrimPrefix(parsed.Path, "/devtools/browser/") == "") {
		return validation(field, "", "must identify a browser websocket under /devtools/browser/", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserEndpoint))
	}
	if !roomBrowserHostIsLoopback(parsed.Hostname()) && !allowRemote {
		return validation(field, "", "must use a loopback host unless connection.allow_remote_cdp is true", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserEndpoint))
	}
	return nil
}

func roomBrowserHostIsLoopback(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "localhost.") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func containsRoomBrowserString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneBrowserToolsStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
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
			CDPURL:           redactRoomBrowserEndpoint(b.Connection.CDPURL, false),
			WSEndpoint:       redactRoomBrowserEndpoint(b.Connection.WSEndpoint, true),
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
			CDPURL:           redactRoomBrowserEndpoint(b.Connection.CDPURL, false),
			WSEndpoint:       redactRoomBrowserEndpoint(b.Connection.WSEndpoint, true),
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

func redactRoomBrowserEndpoint(raw string, websocket bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "<redacted endpoint>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	if websocket {
		parsed.Path = "/<redacted>"
		parsed.RawPath = ""
	}
	return parsed.String()
}

type manifestBrowserTools struct {
	Backend    *string                    `json:"backend" yaml:"backend"`
	Connection *manifestBrowserConnection `json:"connection" yaml:"connection"`
	Selection  *manifestBrowserSelection  `json:"selection" yaml:"selection"`
	Policy     *manifestBrowserPolicy     `json:"policy" yaml:"policy"`
	Limits     *manifestBrowserLimits     `json:"limits" yaml:"limits"`
	Recording  *manifestBrowserRecording  `json:"recording" yaml:"recording"`
	Replay     *manifestBrowserReplay     `json:"replay" yaml:"replay"`
}

type manifestBrowserConnection struct {
	CDPURL           *string `json:"cdp_url" yaml:"cdp_url"`
	WSEndpoint       *string `json:"ws_endpoint" yaml:"ws_endpoint"`
	UserDataDir      *string `json:"user_data_dir" yaml:"user_data_dir"`
	AllowProcessScan *bool   `json:"allow_process_scan" yaml:"allow_process_scan"`
	AllowRemoteCDP   *bool   `json:"allow_remote_cdp" yaml:"allow_remote_cdp"`
}

type manifestBrowserSelection struct {
	Browser     *string `json:"browser" yaml:"browser"`
	Tab         *string `json:"tab" yaml:"tab"`
	Origin      *string `json:"origin" yaml:"origin"`
	AutoSelect  *string `json:"auto_select" yaml:"auto_select"`
	ActivateTab *bool   `json:"activate_tab" yaml:"activate_tab"`
	Persist     *bool   `json:"persist" yaml:"persist"`
}

type manifestBrowserPolicy struct {
	AllowedOrigins    *[]string `json:"allowed_origins" yaml:"allowed_origins"`
	DeniedOrigins     *[]string `json:"denied_origins" yaml:"denied_origins"`
	Approval          *string   `json:"approval" yaml:"approval"`
	CancelOnInterrupt *string   `json:"cancel_on_interrupt" yaml:"cancel_on_interrupt"`
}

type manifestBrowserLimits struct {
	InvocationTimeout  *string `json:"invocation_timeout" yaml:"invocation_timeout"`
	MaxInputBytes      *int    `json:"max_input_bytes" yaml:"max_input_bytes"`
	MaxResultBytes     *int    `json:"max_result_bytes" yaml:"max_result_bytes"`
	SerializePerTarget *bool   `json:"serialize_per_target" yaml:"serialize_per_target"`
}

type manifestBrowserRecording struct {
	Enabled           *bool `json:"enabled" yaml:"enabled"`
	IncludeArguments  *bool `json:"include_arguments" yaml:"include_arguments"`
	IncludeResults    *bool `json:"include_results" yaml:"include_results"`
	RedactURLQuery    *bool `json:"redact_url_query" yaml:"redact_url_query"`
	RedactURLFragment *bool `json:"redact_url_fragment" yaml:"redact_url_fragment"`
}

type manifestBrowserReplay struct {
	Path   *string `json:"path" yaml:"path"`
	Strict *bool   `json:"strict" yaml:"strict"`
}

type browserNodeValueKind uint8

const (
	browserNodeString browserNodeValueKind = iota
	browserNodeBool
	browserNodeInteger
	browserNodeStringList
	browserNodeObject
)

type browserNodeField struct {
	name     string
	kind     browserNodeValueKind
	children []browserNodeField
}

var manifestBrowserToolsFields = []browserNodeField{
	{name: "backend", kind: browserNodeString},
	{name: "connection", kind: browserNodeObject, children: []browserNodeField{
		{name: "cdp_url", kind: browserNodeString},
		{name: "ws_endpoint", kind: browserNodeString},
		{name: "user_data_dir", kind: browserNodeString},
		{name: "allow_process_scan", kind: browserNodeBool},
		{name: "allow_remote_cdp", kind: browserNodeBool},
	}},
	{name: "selection", kind: browserNodeObject, children: []browserNodeField{
		{name: "browser", kind: browserNodeString},
		{name: "tab", kind: browserNodeString},
		{name: "origin", kind: browserNodeString},
		{name: "auto_select", kind: browserNodeString},
		{name: "activate_tab", kind: browserNodeBool},
		{name: "persist", kind: browserNodeBool},
	}},
	{name: "policy", kind: browserNodeObject, children: []browserNodeField{
		{name: "allowed_origins", kind: browserNodeStringList},
		{name: "denied_origins", kind: browserNodeStringList},
		{name: "approval", kind: browserNodeString},
		{name: "cancel_on_interrupt", kind: browserNodeString},
	}},
	{name: "limits", kind: browserNodeObject, children: []browserNodeField{
		{name: "invocation_timeout", kind: browserNodeString},
		{name: "max_input_bytes", kind: browserNodeInteger},
		{name: "max_result_bytes", kind: browserNodeInteger},
		{name: "serialize_per_target", kind: browserNodeBool},
	}},
	{name: "recording", kind: browserNodeObject, children: []browserNodeField{
		{name: "enabled", kind: browserNodeBool},
		{name: "include_arguments", kind: browserNodeBool},
		{name: "include_results", kind: browserNodeBool},
		{name: "redact_url_query", kind: browserNodeBool},
		{name: "redact_url_fragment", kind: browserNodeBool},
	}},
	{name: "replay", kind: browserNodeObject, children: []browserNodeField{
		{name: "path", kind: browserNodeString},
		{name: "strict", kind: browserNodeBool},
	}},
}

// validateManifestBrowserToolsShape performs a small, presence-aware
// preflight before yaml.Decoder decodes typed pointers. The normal decoder is
// still authoritative for strict unknown-field rejection; this preflight only
// turns browser-specific scalar/object/list mismatches into stable,
// participant-qualified validation errors.
func validateManifestBrowserToolsShape(data []byte) error {
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(data, &document); err != nil || len(document.Content) == 0 {
		return nil
	}
	root := document.Content[0]
	if root.Kind == yamlv3.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	participants, ok := browserYAMLMappingValue(root, "participants")
	if !ok || participants.Kind != yamlv3.SequenceNode {
		return nil
	}
	for index, participant := range participants.Content {
		browserTools, present := browserYAMLMappingValue(participant, "browserTools")
		if !present {
			continue
		}
		field := fmt.Sprintf("participants[%d].browserTools", index)
		if browserTools.Kind != yamlv3.MappingNode {
			return browserToolsShapeError(field, "must be an object")
		}
		if err := validateBrowserYAMLNodeFields(browserTools, field, manifestBrowserToolsFields); err != nil {
			return err
		}
	}
	return nil
}

func validateBrowserYAMLNodeFields(node *yamlv3.Node, field string, fields []browserNodeField) error {
	for _, spec := range fields {
		value, present := browserYAMLMappingValue(node, spec.name)
		if !present {
			continue
		}
		childField := field + "." + spec.name
		if err := validateBrowserYAMLNodeValue(value, childField, spec.kind); err != nil {
			return err
		}
		if len(spec.children) > 0 {
			if err := validateBrowserYAMLNodeFields(value, childField, spec.children); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBrowserYAMLNodeValue(node *yamlv3.Node, field string, kind browserNodeValueKind) error {
	valid := false
	switch kind {
	case browserNodeString:
		valid = node.Kind == yamlv3.ScalarNode && node.Tag == "!!str"
	case browserNodeBool:
		valid = node.Kind == yamlv3.ScalarNode && node.Tag == "!!bool" && (node.Value == "true" || node.Value == "false")
	case browserNodeInteger:
		valid = node.Kind == yamlv3.ScalarNode && node.Tag == "!!int"
	case browserNodeStringList:
		if node.Kind == yamlv3.SequenceNode {
			valid = true
			for index, item := range node.Content {
				if item.Kind != yamlv3.ScalarNode || item.Tag != "!!str" {
					return browserToolsShapeError(fmt.Sprintf("%s[%d]", field, index), "must be a string")
				}
			}
		}
	case browserNodeObject:
		valid = node.Kind == yamlv3.MappingNode
	}
	if !valid {
		return browserToolsShapeError(field, browserNodeValueProblem(kind))
	}
	return nil
}

func browserYAMLMappingValue(node *yamlv3.Node, key string) (*yamlv3.Node, bool) {
	if node == nil || node.Kind != yamlv3.MappingNode {
		return nil, false
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1], true
		}
	}
	return nil, false
}

func browserToolsShapeError(field, problem string) error {
	return validation(field, "", problem, errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserToolsOption))
}

func browserNodeValueProblem(kind browserNodeValueKind) string {
	switch kind {
	case browserNodeString:
		return "must be a string"
	case browserNodeBool:
		return "must be true or false"
	case browserNodeInteger:
		return "must be a non-negative decimal integer"
	case browserNodeStringList:
		return "must be a list of strings"
	case browserNodeObject:
		return "must be an object"
	default:
		return "has an invalid value"
	}
}

func normalizeManifestBrowserTools(raw *manifestBrowserTools, field string) (BrowserToolsConfig, error) {
	configValue := DefaultBrowserToolsConfig()
	if raw == nil {
		return configValue, nil
	}
	if raw.Backend != nil {
		configValue.Backend = normalizeString(raw.Backend)
	}
	if raw.Connection != nil {
		if raw.Connection.CDPURL != nil {
			configValue.Connection.CDPURL = normalizeString(raw.Connection.CDPURL)
		}
		if raw.Connection.WSEndpoint != nil {
			configValue.Connection.WSEndpoint = normalizeString(raw.Connection.WSEndpoint)
		}
		if raw.Connection.UserDataDir != nil {
			configValue.Connection.UserDataDir = normalizeString(raw.Connection.UserDataDir)
		}
		if raw.Connection.AllowProcessScan != nil {
			configValue.Connection.AllowProcessScan = *raw.Connection.AllowProcessScan
		}
		if raw.Connection.AllowRemoteCDP != nil {
			configValue.Connection.AllowRemoteCDP = *raw.Connection.AllowRemoteCDP
		}
	}
	if raw.Selection != nil {
		if raw.Selection.Browser != nil {
			configValue.Selection.Browser = normalizeString(raw.Selection.Browser)
		}
		if raw.Selection.Tab != nil {
			configValue.Selection.Tab = normalizeString(raw.Selection.Tab)
		}
		if raw.Selection.Origin != nil {
			configValue.Selection.Origin = normalizeString(raw.Selection.Origin)
		}
		if raw.Selection.AutoSelect != nil {
			configValue.Selection.AutoSelect = normalizeString(raw.Selection.AutoSelect)
		}
		if raw.Selection.ActivateTab != nil {
			configValue.Selection.ActivateTab = *raw.Selection.ActivateTab
		}
		if raw.Selection.Persist != nil {
			configValue.Selection.Persist = *raw.Selection.Persist
		}
	}
	if raw.Policy != nil {
		if raw.Policy.AllowedOrigins != nil {
			configValue.Policy.AllowedOrigins = normalizeBrowserToolsStrings(*raw.Policy.AllowedOrigins)
		}
		if raw.Policy.DeniedOrigins != nil {
			configValue.Policy.DeniedOrigins = normalizeBrowserToolsStrings(*raw.Policy.DeniedOrigins)
		}
		if raw.Policy.Approval != nil {
			configValue.Policy.Approval = normalizeString(raw.Policy.Approval)
		}
		if raw.Policy.CancelOnInterrupt != nil {
			configValue.Policy.CancelOnInterrupt = normalizeString(raw.Policy.CancelOnInterrupt)
		}
	}
	if raw.Limits != nil {
		if raw.Limits.InvocationTimeout != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*raw.Limits.InvocationTimeout))
			if err != nil || duration <= 0 {
				return BrowserToolsConfig{}, validation(field+".limits.invocation_timeout", "", "must be a positive Go duration such as 30s", errors.Join(ErrInvalidBrowserTools, ErrInvalidBrowserToolsOption))
			}
			configValue.Limits.InvocationTimeout = duration
		}
		if raw.Limits.MaxInputBytes != nil {
			configValue.Limits.MaxInputBytes = *raw.Limits.MaxInputBytes
		}
		if raw.Limits.MaxResultBytes != nil {
			configValue.Limits.MaxResultBytes = *raw.Limits.MaxResultBytes
		}
		if raw.Limits.SerializePerTarget != nil {
			configValue.Limits.SerializePerTarget = *raw.Limits.SerializePerTarget
		}
	}
	if raw.Recording != nil {
		if raw.Recording.Enabled != nil {
			configValue.Recording.Enabled = *raw.Recording.Enabled
		}
		if raw.Recording.IncludeArguments != nil {
			configValue.Recording.IncludeArguments = *raw.Recording.IncludeArguments
		}
		if raw.Recording.IncludeResults != nil {
			configValue.Recording.IncludeResults = *raw.Recording.IncludeResults
		}
		if raw.Recording.RedactURLQuery != nil {
			configValue.Recording.RedactURLQuery = *raw.Recording.RedactURLQuery
		}
		if raw.Recording.RedactURLFragment != nil {
			configValue.Recording.RedactURLFragment = *raw.Recording.RedactURLFragment
		}
	}
	if raw.Replay != nil {
		if raw.Replay.Path != nil {
			configValue.Replay.Path = normalizeString(raw.Replay.Path)
		}
		if raw.Replay.Strict != nil {
			configValue.Replay.Strict = *raw.Replay.Strict
		}
	}
	if err := configValue.validateAt(field); err != nil {
		return BrowserToolsConfig{}, err
	}
	return configValue, nil
}

func normalizeBrowserToolsStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	return result
}
