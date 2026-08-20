package chatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Xbai-hang/sotapi/internal/completion"
	openaiAuth "github.com/Xbai-hang/sotapi/internal/protocol/openai/auth"
	"github.com/Xbai-hang/sotapi/internal/routing"
)

const (
	// Path is the OpenAI-compatible Chat Completions endpoint.
	Path                     = "/v1/chat/completions"
	defaultKeepAliveInterval = 15 * time.Second
)

// Completer is the subset of the completion service used by this protocol.
type Completer interface {
	// Complete returns one aggregated completion response.
	Complete(ctx context.Context, request completion.Request) (completion.Response, error)
	// Stream emits ordered completion chunks until the request terminates.
	Stream(ctx context.Context, request completion.Request, emit completion.StreamEmitter) error
}

// HandlerConfig contains HTTP-specific Chat Completions settings.
type HandlerConfig struct {
	Authenticator     *openaiAuth.Authenticator
	MaxBodyBytes      int64
	KeepAliveInterval time.Duration
}

// Handler implements the phase-one OpenAI Chat Completions HTTP endpoint.
type Handler struct {
	completer         Completer
	authenticator     *openaiAuth.Authenticator
	maxBodyBytes      int64
	keepAliveInterval time.Duration
}

// NewHandler validates settings and constructs a Chat Completions Handler.
func NewHandler(completer Completer, cfg HandlerConfig) (*Handler, error) {
	if completer == nil {
		return nil, errors.New("chat completions: completer is required")
	}
	if cfg.Authenticator == nil {
		return nil, errors.New("chat completions: authenticator is required")
	}
	if cfg.MaxBodyBytes <= 0 {
		return nil, errors.New("chat completions: max body bytes must be positive")
	}
	if cfg.KeepAliveInterval <= 0 {
		cfg.KeepAliveInterval = defaultKeepAliveInterval
	}
	return &Handler{
		completer:         completer,
		authenticator:     cfg.Authenticator,
		maxBodyBytes:      cfg.MaxBodyBytes,
		keepAliveInterval: cfg.KeepAliveInterval,
	}, nil
}

// ServeHTTP authenticates, decodes and serves one Chat Completions request.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "only POST is supported", nil)
		return
	}
	if !h.authenticator.Require(writer, request) {
		return
	}
	if !isJSONContentType(request.Header.Get("Content-Type")) {
		writeError(writer, http.StatusUnsupportedMediaType, "invalid_request_error", "invalid_content_type", "Content-Type must be application/json", nil)
		return
	}

	input, err := h.decodeRequest(writer, request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request_error", "invalid_json", err.Error(), nil)
		return
	}
	internalRequest, err := convertRequest(input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request_error", "unsupported_request", err.Error(), nil)
		return
	}

	if input.Stream {
		h.serveStream(writer, request, internalRequest)
		return
	}
	result, err := h.completer.Complete(request.Context(), internalRequest)
	if err != nil {
		writeCompletionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, completionResponse{
		ID:      result.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   result.Model,
		Choices: []completionChoice{{
			Index: 0,
			Message: responseMessage{
				Role:             "assistant",
				ReasoningContent: result.Reasoning,
				Content:          result.Content,
			},
			FinishReason: "stop",
		}},
	})
}

func (h *Handler) decodeRequest(writer http.ResponseWriter, request *http.Request) (createRequest, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, h.maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	var input createRequest
	if err := decoder.Decode(&input); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return createRequest{}, fmt.Errorf("request body exceeds %d bytes", h.maxBodyBytes)
		}
		return createRequest{}, fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return createRequest{}, errors.New("request body must contain one JSON object")
	}
	return input, nil
}

func convertRequest(input createRequest) (completion.Request, error) {
	if strings.TrimSpace(input.Model) == "" {
		return completion.Request{}, errors.New("model is required")
	}
	if len(input.Messages) == 0 {
		return completion.Request{}, errors.New("messages must contain at least one item")
	}
	if input.N != nil && *input.N != 1 {
		return completion.Request{}, errors.New("phase one supports only n=1")
	}
	if hasUnsupportedArray(input.Tools) || hasUnsupportedArray(input.Functions) || hasUnsupportedChoice(input.ToolChoice) || hasUnsupportedChoice(input.FunctionCall) {
		return completion.Request{}, errors.New("tool and function calling are not supported in phase one")
	}
	if !isTextResponseFormat(input.ResponseFormat) {
		return completion.Request{}, errors.New("structured output is not supported in phase one")
	}

	messages := make([]completion.Message, 0, len(input.Messages))
	for index, message := range input.Messages {
		if !supportedRole(message.Role) {
			return completion.Request{}, fmt.Errorf("messages[%d].role %q is not supported", index, message.Role)
		}
		if hasUnsupportedArray(message.ToolCalls) || hasUnsupportedChoice(message.FunctionCall) {
			return completion.Request{}, fmt.Errorf("messages[%d] contains unsupported tool or function calls", index)
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err != nil {
			return completion.Request{}, fmt.Errorf("messages[%d].content must be a string", index)
		}
		messages = append(messages, completion.Message{Role: message.Role, Content: content})
	}
	return completion.Request{Model: input.Model, Messages: messages}, nil
}

func supportedRole(role string) bool {
	switch role {
	case "developer", "system", "user", "assistant":
		return true
	default:
		return false
	}
}

func hasUnsupportedArray(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != "[]"
}

func hasUnsupportedChoice(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != `"none"`
}

func isTextResponseFormat(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return true
	}
	var value struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &value) == nil && value.Type == "text"
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func writeCompletionError(writer http.ResponseWriter, err error) {
	status, payload := mapCompletionError(err)
	writeJSON(writer, status, errorResponse{Error: payload})
}

func mapCompletionError(err error) (int, apiError) {
	switch {
	case errors.Is(err, routing.ErrModelNotFound):
		return http.StatusNotFound, newAPIError("invalid_request_error", "model_not_found", "the requested model is not configured", strPtr("model"))
	case errors.Is(err, completion.ErrInvalidRequest):
		return http.StatusBadRequest, newAPIError("invalid_request_error", "invalid_request", err.Error(), nil)
	case errors.Is(err, completion.ErrRequestTimeout):
		return http.StatusGatewayTimeout, newAPIError("server_error", "request_timeout", "the human did not respond before the request timeout", nil)
	case errors.Is(err, completion.ErrRequestCanceled):
		return http.StatusRequestTimeout, newAPIError("server_error", "request_canceled", "the request was canceled", nil)
	case errors.Is(err, completion.ErrDeliveryFailed):
		return http.StatusBadGateway, newAPIError("server_error", "channel_delivery_failed", "the request could not be delivered to the human", nil)
	default:
		return http.StatusInternalServerError, newAPIError("server_error", "internal_error", "an internal error occurred", nil)
	}
}

func writeError(writer http.ResponseWriter, status int, errorType, code, message string, param *string) {
	writeJSON(writer, status, errorResponse{Error: newAPIError(errorType, code, message, param)})
}

func newAPIError(errorType, code, message string, param *string) apiError {
	return apiError{
		Message: message,
		Type:    errorType,
		Param:   param,
		Code:    strPtr(code),
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func strPtr(value string) *string {
	return &value
}
