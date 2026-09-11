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

// validateManifestBrowserToolsShape performs a presence-aware preflight
// before yaml.Decoder decodes typed pointers. The decoder remains authoritative
// for strict unknown-field rejection; this preflight only gives browser scalar,
// object, and list mismatches stable participant-qualified errors.
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
	return invalid(field, problem, errors.Join(rooms.ErrInvalidBrowserTools, rooms.ErrInvalidBrowserOption))
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
	configValue := rooms.DefaultBrowserToolsConfig()
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
				return rooms.BrowserToolsConfig{}, invalid(field+".limits.invocation_timeout", "must be a positive Go duration such as 30s", rooms.ErrInvalidBrowserToolsOption)
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
	if err := configValue.ValidateAt(field); err != nil {
		return rooms.BrowserToolsConfig{}, err
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
