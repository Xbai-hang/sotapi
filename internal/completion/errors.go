package completion

import "errors"

var (
	// ErrInvalidRequest indicates that the protocol-neutral request is invalid.
	ErrInvalidRequest = errors.New("invalid completion request")
	// ErrRequestTimeout indicates that the selected human did not reply in time.
	ErrRequestTimeout = errors.New("completion request timed out")
	// ErrRequestCanceled indicates that the caller canceled the request.
	ErrRequestCanceled = errors.New("completion request canceled")
	// ErrServiceReloading indicates that the runtime canceled the request to
	// apply a new configuration.
	ErrServiceReloading = errors.New("completion service is reloading")
	// ErrDeliveryFailed indicates that the Channel could not deliver the task.
	ErrDeliveryFailed = errors.New("completion delivery failed")
	// ErrFallbackFailed indicates that the configured fallback could not
	// generate a response.
	ErrFallbackFailed = errors.New("completion fallback failed")
	// ErrUnknownRequest indicates that a reply no longer has an active waiter.
	ErrUnknownRequest = errors.New("unknown or completed request")
)
