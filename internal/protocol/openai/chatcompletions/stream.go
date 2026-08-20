package chatcompletions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Xbai-hang/sotapi/internal/completion"
)

func (h *Handler) serveStream(writer http.ResponseWriter, request *http.Request, input completion.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, "server_error", "streaming_unsupported", "HTTP streaming is not supported", nil)
		return
	}

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	events := make(chan completion.StreamChunk)
	result := make(chan error, 1)
	go func() {
		result <- h.completer.Stream(ctx, input, func(chunk completion.StreamChunk) error {
			select {
			case events <- chunk:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	ticker := time.NewTicker(h.keepAliveInterval)
	defer ticker.Stop()
	created := time.Now().Unix()
	started := false
	roleSent := false

	for {
		select {
		case chunk := <-events:
			if !started {
				startSSE(writer)
				started = true
			}
			if err := writeStreamChunk(writer, flusher, created, chunk, &roleSent); err != nil {
				cancel()
				return
			}
		case err := <-result:
			if err != nil {
				if !started {
					writeCompletionError(writer, err)
					return
				}
				if writeSSEJSON(writer, errorPayload(err)) == nil {
					_ = writeSSEData(writer, "[DONE]")
					flusher.Flush()
				}
				return
			}
			if !started {
				startSSE(writer)
			}
			_ = writeSSEData(writer, "[DONE]")
			flusher.Flush()
			return
		case <-ticker.C:
			if started {
				if _, err := fmt.Fprint(writer, ": keep-alive\n\n"); err != nil {
					cancel()
					return
				}
				flusher.Flush()
			}
		case <-request.Context().Done():
			return
		}
	}
}

func startSSE(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
}

func writeStreamChunk(writer http.ResponseWriter, flusher http.Flusher, created int64, chunk completion.StreamChunk, roleSent *bool) error {
	choice := streamChoice{Index: 0, FinishReason: nil}
	if !*roleSent {
		choice.Delta.Role = "assistant"
		*roleSent = true
	}
	choice.Delta.ReasoningContent = chunk.ReasoningDelta
	choice.Delta.Content = chunk.ContentDelta
	if chunk.Done {
		finishReason := "stop"
		choice.FinishReason = &finishReason
		choice.Delta = delta{}
	}

	err := writeSSEJSON(writer, streamResponse{
		ID:      chunk.ID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   chunk.Model,
		Choices: []streamChoice{choice},
	})
	if err == nil {
		flusher.Flush()
	}
	return err
}

func writeSSEJSON(writer http.ResponseWriter, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeSSEData(writer, string(data))
}

func writeSSEData(writer http.ResponseWriter, data string) error {
	_, err := fmt.Fprintf(writer, "data: %s\n\n", data)
	return err
}

func errorPayload(err error) errorResponse {
	_, payload := mapCompletionError(err)
	return errorResponse{Error: payload}
}
