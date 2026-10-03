package transcript

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// textCredentialForms counts the forms of a credential other than base64:
// raw, JSON-escaped and URL-query-escaped.
const textCredentialForms = 3

// CredentialForms lists the forms in which a credential can appear in
// recorded evidence whose payloads are encoded (transcript payloads are
// base64, out of reach of the bundle's own byte redaction), for a recorder
// to redact before it writes them: each credential raw, as a
// JSON string body, URL-query-escaped, and base64 encoded (standard and URL
// alphabets, padded and unpadded). Empty credentials are skipped, each form
// appears once, and longer forms come first so a credential that contains
// another is never left partly visible.
func CredentialForms(credentials []string) []string {
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	perCredential := textCredentialForms + len(encodings)
	seen := make(map[string]struct{}, perCredential*len(credentials))
	forms := make([]string, 0, perCredential*len(credentials))
	add := func(form string) {
		if form == "" {
			return
		}
		if _, ok := seen[form]; ok {
			return
		}
		seen[form] = struct{}{}
		forms = append(forms, form)
	}
	for _, credential := range credentials {
		if strings.TrimSpace(credential) == "" {
			continue
		}
		add(credential)
		if encoded, err := json.Marshal(credential); err == nil {
			add(string(encoded[1 : len(encoded)-1]))
		}
		add(url.QueryEscape(credential))
		for _, encoding := range encodings {
			add(encoding.EncodeToString([]byte(credential)))
		}
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}

// RedactCredentialForms replaces every form in forms (CredentialForms) in
// value with marker.
func RedactCredentialForms(value string, forms []string, marker string) string {
	for _, form := range forms {
		value = strings.ReplaceAll(value, form, marker)
	}
	return value
}
