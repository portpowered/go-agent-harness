package manifest

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	yamlv3 "gopkg.in/yaml.v3"
)

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

func manifestBrowserToolsFields() []browserNodeField {
	return []browserNodeField{
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
}

// validateManifestBrowserToolsShape performs a presence-aware preflight
// before yaml.Decoder decodes typed pointers. The decoder remains authoritative
// for strict unknown-field rejection; this preflight only gives browser scalar,
// object, and list mismatches stable participant-qualified errors.
func validateManifestBrowserToolsShape(data []byte) error {
	var document yamlv3.Node
	if yamlv3.Unmarshal(data, &document) != nil || len(document.Content) == 0 {
		return ignoreBrowserShapeParseError()
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
		if err := validateBrowserYAMLNodeFields(browserTools, field, manifestBrowserToolsFields()); err != nil {
			return err
		}
	}
	return nil
}
func ignoreBrowserShapeParseError() error { return nil }
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
	if kind == browserNodeStringList {
		return validateBrowserStringList(node, field)
	}
	if browserNodeValueMatches(node, kind) {
		return nil
	}
	return browserToolsShapeError(field, browserNodeValueProblem(kind))
}

func browserNodeValueMatches(node *yamlv3.Node, kind browserNodeValueKind) bool {
	switch kind {
	case browserNodeString:
		return node.Kind == yamlv3.ScalarNode && node.Tag == "!!str"
	case browserNodeBool:
		return node.Kind == yamlv3.ScalarNode && node.Tag == "!!bool" && (node.Value == "true" || node.Value == "false")
	case browserNodeInteger:
		return node.Kind == yamlv3.ScalarNode && node.Tag == "!!int"
	case browserNodeObject:
		return node.Kind == yamlv3.MappingNode
	case browserNodeStringList:
		return false
	}
	return false
}

func validateBrowserStringList(node *yamlv3.Node, field string) error {
	if node.Kind != yamlv3.SequenceNode {
		return browserToolsShapeError(field, browserNodeValueProblem(browserNodeStringList))
	}
	for index, item := range node.Content {
		if item.Kind != yamlv3.ScalarNode || item.Tag != "!!str" {
			return browserToolsShapeError(fmt.Sprintf("%s[%d]", field, index), "must be a string")
		}
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
	return invalid(field, problem, errors.Join(rooms.ErrInvalidBrowserTools, rooms.ErrInvalidBrowserOption, rooms.ErrInvalidBrowserToolsOption))
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

func normalizeBrowser(raw *manifestBrowserTools, field string) (rooms.BrowserToolsConfig, error) {
	configValue := rooms.BrowserToolsDefaults{}.Config()
	if raw == nil {
		return configValue, nil
	}
	if raw.Backend != nil {
		configValue.Backend = normalizeString(raw.Backend)
	}
	applyBrowserConnection(&configValue, raw.Connection)
	applyBrowserSelection(&configValue, raw.Selection)
	applyBrowserPolicy(&configValue, raw.Policy)
	if err := applyBrowserLimits(&configValue, raw.Limits, field); err != nil {
		return rooms.BrowserToolsConfig{}, err
	}
	applyBrowserRecording(&configValue, raw.Recording)
	applyBrowserReplay(&configValue, raw.Replay)
	if err := configValue.ValidateAt(field); err != nil {
		return rooms.BrowserToolsConfig{}, err
	}
	return configValue, nil
}

func applyBrowserConnection(config *rooms.BrowserToolsConfig, raw *manifestBrowserConnection) {
	if raw == nil {
		return
	}
	if raw.CDPURL != nil {
		config.Connection.CDPURL = normalizeString(raw.CDPURL)
	}
	if raw.WSEndpoint != nil {
		config.Connection.WSEndpoint = normalizeString(raw.WSEndpoint)
	}
	if raw.UserDataDir != nil {
		config.Connection.UserDataDir = normalizeString(raw.UserDataDir)
	}
	if raw.AllowProcessScan != nil {
		config.Connection.AllowProcessScan = *raw.AllowProcessScan
	}
	if raw.AllowRemoteCDP != nil {
		config.Connection.AllowRemoteCDP = *raw.AllowRemoteCDP
	}
}

func applyBrowserSelection(config *rooms.BrowserToolsConfig, raw *manifestBrowserSelection) {
	if raw == nil {
		return
	}
	if raw.Browser != nil {
		config.Selection.Browser = normalizeString(raw.Browser)
	}
	if raw.Tab != nil {
		config.Selection.Tab = normalizeString(raw.Tab)
	}
	if raw.Origin != nil {
		config.Selection.Origin = normalizeString(raw.Origin)
	}
	if raw.AutoSelect != nil {
		config.Selection.AutoSelect = normalizeString(raw.AutoSelect)
	}
	if raw.ActivateTab != nil {
		config.Selection.ActivateTab = *raw.ActivateTab
	}
	if raw.Persist != nil {
		config.Selection.Persist = *raw.Persist
	}
}

func applyBrowserPolicy(config *rooms.BrowserToolsConfig, raw *manifestBrowserPolicy) {
	if raw == nil {
		return
	}
	if raw.AllowedOrigins != nil {
		config.Policy.AllowedOrigins = normalizeBrowserToolsStrings(*raw.AllowedOrigins)
	}
	if raw.DeniedOrigins != nil {
		config.Policy.DeniedOrigins = normalizeBrowserToolsStrings(*raw.DeniedOrigins)
	}
	if raw.Approval != nil {
		config.Policy.Approval = normalizeString(raw.Approval)
	}
	if raw.CancelOnInterrupt != nil {
		config.Policy.CancelOnInterrupt = normalizeString(raw.CancelOnInterrupt)
	}
}

func applyBrowserLimits(config *rooms.BrowserToolsConfig, raw *manifestBrowserLimits, field string) error {
	if raw == nil {
		return nil
	}
	if raw.InvocationTimeout != nil {
		duration, err := time.ParseDuration(strings.TrimSpace(*raw.InvocationTimeout))
		if err != nil || duration <= 0 {
			return invalid(field+".limits.invocation_timeout", "must be a positive Go duration such as 30s", rooms.ErrInvalidBrowserToolsOption)
		}
		config.Limits.InvocationTimeout = duration
	}
	if raw.MaxInputBytes != nil {
		config.Limits.MaxInputBytes = *raw.MaxInputBytes
	}
	if raw.MaxResultBytes != nil {
		config.Limits.MaxResultBytes = *raw.MaxResultBytes
	}
	if raw.SerializePerTarget != nil {
		config.Limits.SerializePerTarget = *raw.SerializePerTarget
	}
	return nil
}

func applyBrowserRecording(config *rooms.BrowserToolsConfig, raw *manifestBrowserRecording) {
	if raw == nil {
		return
	}
	if raw.Enabled != nil {
		config.Recording.Enabled = *raw.Enabled
	}
	if raw.IncludeArguments != nil {
		config.Recording.IncludeArguments = *raw.IncludeArguments
	}
	if raw.IncludeResults != nil {
		config.Recording.IncludeResults = *raw.IncludeResults
	}
	if raw.RedactURLQuery != nil {
		config.Recording.RedactURLQuery = *raw.RedactURLQuery
	}
	if raw.RedactURLFragment != nil {
		config.Recording.RedactURLFragment = *raw.RedactURLFragment
	}
}

func applyBrowserReplay(config *rooms.BrowserToolsConfig, raw *manifestBrowserReplay) {
	if raw == nil {
		return
	}
	if raw.Path != nil {
		config.Replay.Path = normalizeString(raw.Path)
	}
	if raw.Strict != nil {
		config.Replay.Strict = *raw.Strict
	}
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
