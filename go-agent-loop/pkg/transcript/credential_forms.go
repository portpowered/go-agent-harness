package transcript

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// MinRedactableCredentialLength is the shortest credential, and the shortest
// form of one, a recorder redacts. A dummy key such as "x", "none" or
// "ollama" is not a secret, and redacting it, or its two-character base64
// form, would rewrite ordinary text and base64 audio across the evidence.
const MinRedactableCredentialLength = 8

// textCredentialForms counts the forms of a credential other than base64:
// raw, JSON-escaped and URL-query-escaped.
const textCredentialForms = 3

// base64Alignments is how many byte offsets within a 3-byte base64 group a
// credential can start at.
const base64Alignments = 3

// RedactableCredentials returns the credentials worth redacting: those at
// least MinRedactableCredentialLength bytes long once trimmed.
func RedactableCredentials(credentials []string) []string {
	out := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		if len(strings.TrimSpace(credential)) >= MinRedactableCredentialLength {
			out = append(out, credential)
		}
	}
	return out
}

// CredentialForms lists the forms in which a credential can appear in
// recorded evidence whose payloads are encoded (transcript payloads are
// base64, out of reach of the bundle's own byte redaction), for a recorder
// to redact before it writes them: each credential raw, as a JSON string
// body, URL-query-escaped, and base64 encoded in the standard and URL
// alphabets. Base64 is listed standalone (padded and unpadded) and as the
// characters a credential embedded in a longer base64 stream always encodes
// to at each of the three byte alignments (HTTP Basic credentials, for
// example, follow a user name): the characters that also encode a
// neighbouring byte are trimmed, so a match leaves at most those few
// characters of the credential. A credential shorter than
// MinRedactableCredentialLength is skipped and so is any form shorter than
// it; each form appears once, and longer forms come first so a credential
// that contains another is never left partly visible.
func CredentialForms(credentials []string) []string {
	alphabets := []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding}
	perCredential := textCredentialForms + len(alphabets)*(base64Alignments+2)
	seen := make(map[string]struct{}, perCredential*len(credentials))
	forms := make([]string, 0, perCredential*len(credentials))
	add := func(form string) {
		if len(form) < MinRedactableCredentialLength {
			return
		}
		if _, ok := seen[form]; ok {
			return
		}
		seen[form] = struct{}{}
		forms = append(forms, form)
	}
	for _, credential := range RedactableCredentials(credentials) {
		add(credential)
		if encoded, err := json.Marshal(credential); err == nil {
			add(string(encoded[1 : len(encoded)-1]))
		}
		add(url.QueryEscape(credential))
		for _, padded := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			add(padded.EncodeToString([]byte(credential)))
		}
		for _, alphabet := range alphabets {
			add(alphabet.EncodeToString([]byte(credential)))
			for offset := range base64Alignments {
				add(embeddedBase64(alphabet, credential, offset))
			}
		}
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}

// embeddedBase64 is the base64 of credential starting offset bytes into a
// 3-byte group, without the leading and trailing characters that also
// encode the neighbouring bytes.
func embeddedBase64(alphabet *base64.Encoding, credential string, offset int) string {
	encoded := alphabet.EncodeToString(append(make([]byte, offset), credential...))
	const bitsPerByte, bitsPerChar = 8, 6
	lead := (offset*bitsPerByte + bitsPerChar - 1) / bitsPerChar
	end := len(encoded)
	if (offset+len(credential))*bitsPerByte%bitsPerChar != 0 {
		end-- // the last character also encodes the byte that follows
	}
	if lead >= end {
		return ""
	}
	return encoded[lead:end]
}

// RedactCredentialForms replaces every form in forms (CredentialForms) in
// value with marker.
func RedactCredentialForms(value string, forms []string, marker string) string {
	for _, form := range forms {
		value = strings.ReplaceAll(value, form, marker)
	}
	return value
}
