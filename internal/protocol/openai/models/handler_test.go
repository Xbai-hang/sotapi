package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openaiAuth "github.com/Xbai-hang/sotapi/internal/protocol/openai/auth"
)

func TestHandlerListsConfiguredModels(t *testing.T) {
	authenticator := mustAuth(t, openaiAuth.ModeAPIKey, []string{"first", "second"})
	modelIDs := []string{"human-general", "human-reviewer"}
	handler, err := NewHandler(authenticator, modelIDs)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	modelIDs[0] = "changed"

	request := httptest.NewRequest(http.MethodGet, Path, nil)
	request.Header.Set("Authorization", "Bearer second")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d, content-type %q, body %s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var payload listResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Object != "list" || len(payload.Data) != 2 {
		t.Fatalf("response = %#v", payload)
	}
	for index, expectedID := range []string{"human-general", "human-reviewer"} {
		actual := payload.Data[index]
		if actual.ID != expectedID || actual.Object != "model" || actual.Created <= 0 || actual.OwnedBy != ownedBy {
			t.Fatalf("model %d = %#v", index, actual)
		}
	}
}

func TestHandlerRequiresAuthentication(t *testing.T) {
	handler, err := NewHandler(mustAuth(t, openaiAuth.ModeAPIKey, []string{"secret"}), []string{"human"})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path, nil))
	if response.Code != http.StatusUnauthorized || !json.Valid(response.Body.Bytes()) {
		t.Fatalf("response = status %d, body %s", response.Code, response.Body.String())
	}
}

func TestHandlerAllowsAnonymousAccessInNoneMode(t *testing.T) {
	handler, err := NewHandler(mustAuth(t, openaiAuth.ModeNone, nil), []string{"human"})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsUnsupportedMethod(t *testing.T) {
	handler, err := NewHandler(mustAuth(t, openaiAuth.ModeAPIKey, []string{"secret"}), []string{"human"})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, Path, nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet || !json.Valid(response.Body.Bytes()) {
		t.Fatalf("response = status %d, allow %q, body %s", response.Code, response.Header().Get("Allow"), response.Body.String())
	}
}

func TestNewHandlerValidation(t *testing.T) {
	authenticator := mustAuth(t, openaiAuth.ModeAPIKey, []string{"secret"})
	tests := []struct {
		name          string
		authenticator *openaiAuth.Authenticator
		modelIDs      []string
	}{
		{name: "authenticator", modelIDs: []string{"human"}},
		{name: "models", authenticator: authenticator},
		{name: "empty model", authenticator: authenticator, modelIDs: []string{" "}},
		{name: "duplicate model", authenticator: authenticator, modelIDs: []string{"human", "human"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewHandler(test.authenticator, test.modelIDs); err == nil {
				t.Fatal("NewHandler() succeeded")
			}
		})
	}
}

func mustAuth(t *testing.T, mode string, keys []string) *openaiAuth.Authenticator {
	t.Helper()
	authenticator, err := openaiAuth.New(mode, keys)
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	return authenticator
}
