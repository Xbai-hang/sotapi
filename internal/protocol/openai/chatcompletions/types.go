package chatcompletions

import "encoding/json"

type createRequest struct {
	Model          string           `json:"model"`
	Messages       []requestMessage `json:"messages"`
	Stream         bool             `json:"stream"`
	N              *int             `json:"n,omitempty"`
	Tools          json.RawMessage  `json:"tools,omitempty"`
	ToolChoice     json.RawMessage  `json:"tool_choice,omitempty"`
	Functions      json.RawMessage  `json:"functions,omitempty"`
	FunctionCall   json.RawMessage  `json:"function_call,omitempty"`
	ResponseFormat json.RawMessage  `json:"response_format,omitempty"`
}

type requestMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	ToolCalls    json.RawMessage `json:"tool_calls,omitempty"`
	FunctionCall json.RawMessage `json:"function_call,omitempty"`
}

type completionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
}

type completionChoice struct {
	Index        int             `json:"index"`
	Message      responseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type responseMessage struct {
	Role             string `json:"role"`
	ReasoningContent string `json:"reasoning_content"`
	Content          string `json:"content"`
}

type streamResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
}

type streamChoice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role             string `json:"role,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Content          string `json:"content,omitempty"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}
