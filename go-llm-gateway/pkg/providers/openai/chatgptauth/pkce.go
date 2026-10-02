package chatgptauth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

const (
	pkceVerifierBytes = 64
	stateBytes        = 32
)

// PKCE is an RFC 7636 verifier and its S256 challenge.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE draws a 64-byte verifier from random and derives its S256
// challenge. Both are base64url without padding, as Codex generates them.
func NewPKCE(random io.Reader) (PKCE, error) {
	verifier, err := randomToken(random, pkceVerifierBytes)
	if err != nil {
		return PKCE{}, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	return PKCE{Verifier: verifier, Challenge: challengeS256(verifier)}, nil
}

// NewState draws an unguessable OAuth state value from random.
func NewState(random io.Reader) (string, error) {
	state, err := randomToken(random, stateBytes)
	if err != nil {
		return "", fmt.Errorf("generate OAuth state: %w", err)
	}
	return state, nil
}

func challengeS256(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func randomToken(random io.Reader, size int) (string, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
