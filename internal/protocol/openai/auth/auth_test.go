package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticatorAcceptsAnyConfiguredAPIKey(t *testing.T) {
	authenticator, err := New(ModeAPIKey, []string{"first", "second"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, key := range []string{"first", "second"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		request.Header.Set("Authorization", "Bearer "+key)
		if !authenticator.Require(httptest.NewRecorder(), request) {
			t.Fatalf("Require() rejected configured key %q", key)
		}
	}
}

func TestAuthenticatorRejectsInvalidCredentials(t *testing.T) {
	authenticator, err := New(ModeAPIKey, []string{"secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, header := range []string{"", "Basic secret", "Bearer wrong"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		request.Header.Set("Authorization", header)
		response := httptest.NewRecorder()
		if authenticator.Require(response, request) {
			t.Fatalf("Require(%q) succeeded", header)
		}
		if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("response = status %d, content-type %q", response.Code, response.Header().Get("Content-Type"))
		}
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Error.Code != "invalid_api_key" {
			t.Fatalf("error response = %s, decode error = %v", response.Body.String(), err)
		}
	}
}

func TestAuthenticatorNoneModeAllowsAnonymousRequests(t *testing.T) {
	authenticator, err := New(ModeNone, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if !authenticator.Require(httptest.NewRecorder(), request) {
		t.Fatal("Require() rejected anonymous request in none mode")
	}
}

func TestNewRejectsInvalidAuthenticationConfiguration(t *testing.T) {
	tests := []struct {
		name string
		mode string
		keys []string
	}{
		{name: "mode", mode: "unknown"},
		{name: "missing keys", mode: ModeAPIKey},
		{name: "empty key", mode: ModeAPIKey, keys: []string{" "}},
		{name: "duplicate key", mode: ModeAPIKey, keys: []string{"same", "same"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.mode, test.keys); err == nil {
				t.Fatal("New() succeeded")
			}
		})
	}
}

func TestNilAuthenticatorRejectsRequest(t *testing.T) {
	var authenticator *Authenticator
	response := httptest.NewRecorder()
	if authenticator.Require(response, httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("nil Authenticator accepted request")
	}
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}
