// Package models implements OpenAI-compatible model discovery.
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	openaiAuth "github.com/Xbai-hang/sotapi/internal/protocol/openai/auth"
)

const (
	// Path is the OpenAI-compatible model list endpoint.
	Path    = "/v1/models"
	ownedBy = "sotapi"
)

// Handler returns the configured public model catalog.
type Handler struct {
	authenticator *openaiAuth.Authenticator
	models        []model
}

// NewHandler validates model IDs and constructs an immutable models Handler.
func NewHandler(authenticator *openaiAuth.Authenticator, modelIDs []string) (*Handler, error) {
	if authenticator == nil {
		return nil, errors.New("models: authenticator is required")
	}
	if len(modelIDs) == 0 {
		return nil, errors.New("models: at least one model is required")
	}

	created := time.Now().Unix()
	entries := make([]model, 0, len(modelIDs))
	seen := make(map[string]struct{}, len(modelIDs))
	for index, modelID := range modelIDs {
		if strings.TrimSpace(modelID) == "" {
			return nil, fmt.Errorf("models: model ID %d is empty", index)
		}
		if _, exists := seen[modelID]; exists {
			return nil, fmt.Errorf("models: duplicate model ID %q", modelID)
		}
		seen[modelID] = struct{}{}
		entries = append(entries, model{
			ID:      modelID,
			Object:  "model",
			Created: created,
			OwnedBy: ownedBy,
		})
	}
	return &Handler{authenticator: authenticator, models: entries}, nil
}

// ServeHTTP serves GET /v1/models.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}
	if !h.authenticator.Require(writer, request) {
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(listResponse{
		Object: "list",
		Data:   h.models,
	})
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    code,
		},
	})
}

type listResponse struct {
	Object string  `json:"object"`
	Data   []model `json:"data"`
}

type model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}
