package completion

import "errors"

var (
	// ErrInvalidRequest indicates that the protocol-neutral request is invalid.
	ErrInvalidRequest = errors.New("invalid completion request")
	// ErrRequestTimeout indicates that the selected human did not reply in time.
	ErrRequestTimeout = errors.New("completion request timed out")
	// ErrRequestCanceled indicates that the caller canceled the request.
	ErrRequestCanceled = errors.New("completion request canceled")
	// ErrDeliveryFailed indicates that the Channel could not deliver the task.
	ErrDeliveryFailed = errors.New("completion delivery failed")
	// ErrUnknownRequest indicates that a reply no longer has an active waiter.
	ErrUnknownRequest = errors.New("unknown or completed request")
)
