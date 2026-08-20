package completion

import "time"

// Response is the protocol-neutral result of one completion.
type Response struct {
	ID        string
	Model     string
	Reasoning string
	Content   string
}

// StreamChunk is an incremental completion result.
type StreamChunk struct {
	ID             string
	Model          string
	ReasoningDelta string
	ContentDelta   string
	Done           bool
}

// Outcome describes how an accepted completion request ended.
type Outcome string

const (
	// OutcomeResponded means the selected human supplied an answer.
	OutcomeResponded Outcome = "responded"
	// OutcomeTimedOut means no answer arrived before the request deadline.
	OutcomeTimedOut Outcome = "timed_out"
	// OutcomeCanceled means the caller disconnected or canceled the request.
	OutcomeCanceled Outcome = "canceled"
	// OutcomeDeliveryFailed means the Channel could not deliver the task.
	OutcomeDeliveryFailed Outcome = "delivery_failed"
)

// Observation is one request outcome recorded for operational statistics.
type Observation struct {
	RequestID string
	UserID    string
	Outcome   Outcome
	Latency   time.Duration
}
