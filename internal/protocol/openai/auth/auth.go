// Package auth implements the authentication modes shared by SotAPI's
// OpenAI-compatible HTTP endpoints.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	// ModeAPIKey requires a configured Bearer token.
	ModeAPIKey = "api_key"
	// ModeNone explicitly disables API authentication.
	ModeNone = "none"
)

// Authenticator validates HTTP requests against one explicit authentication
// mode. API keys are retained only as SHA-256 hashes.
type Authenticator struct {
	disabled  bool
	keyHashes [][sha256.Size]byte
}

// New validates an authentication mode and constructs an Authenticator.
func New(mode string, apiKeys []string) (*Authenticator, error) {
	switch mode {
	case ModeNone:
		return &Authenticator{disabled: true}, nil
	case ModeAPIKey:
		if len(apiKeys) == 0 {
			return nil, errors.New("openai auth: at least one API key is required")
		}
	default:
		return nil, fmt.Errorf("openai auth: unsupported mode %q", mode)
	}

	keyHashes := make([][sha256.Size]byte, 0, len(apiKeys))
	seen := make(map[[sha256.Size]byte]struct{}, len(apiKeys))
	for index, key := range apiKeys {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("openai auth: API key %d is empty", index)
		}
		hash := sha256.Sum256([]byte(key))
		if _, exists := seen[hash]; exists {
			return nil, fmt.Errorf("openai auth: API key %d is duplicated", index)
		}
		seen[hash] = struct{}{}
		keyHashes = append(keyHashes, hash)
	}
	return &Authenticator{keyHashes: keyHashes}, nil
}

// Require authorizes request or writes an OpenAI-compatible 401 response.
func (a *Authenticator) Require(writer http.ResponseWriter, request *http.Request) bool {
	if a != nil && a.authorized(request.Header.Get("Authorization")) {
		return true
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]any{
			"message": "invalid API key",
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    "invalid_api_key",
		},
	})
	return false
}

func (a *Authenticator) authorized(header string) bool {
	if a.disabled {
		return true
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return false
	}

	provided := sha256.Sum256([]byte(token))
	matched := 0
	for _, configured := range a.keyHashes {
		matched |= subtle.ConstantTimeCompare(provided[:], configured[:])
	}
	return matched == 1
}
