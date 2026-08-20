package completion

import "time"

// Request is the protocol-neutral input for one completion.
type Request struct {
	ID       string
	Model    string
	Messages []Message
	Timeout  time.Duration
}

// Message is one text message in a completion request.
type Message struct {
	Role    string
	Content string
}

// Task is the protocol-neutral work item delivered to a human Channel.
type Task struct {
	RequestID string
	Model     string
	Messages  []Message
}

// Reply is a human answer associated with an in-flight request.
type Reply struct {
	RequestID string
	Content   string
}
