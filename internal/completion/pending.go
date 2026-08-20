package completion

import (
	"errors"
	"sync"
)

var errDuplicateRequest = errors.New("duplicate pending request")

// pendingBroker owns the short-lived mapping between request IDs and the
// goroutines waiting for human replies. Entries are consumed at most once.
type pendingBroker struct {
	mu      sync.Mutex
	entries map[string]chan Reply
}

func newPendingBroker() *pendingBroker {
	return &pendingBroker{entries: make(map[string]chan Reply)}
}

func (b *pendingBroker) register(requestID string) (<-chan Reply, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.entries[requestID]; exists {
		return nil, nil, errDuplicateRequest
	}

	replies := make(chan Reply, 1)
	b.entries[requestID] = replies
	return replies, func() { b.remove(requestID) }, nil
}

func (b *pendingBroker) resolve(reply Reply) bool {
	b.mu.Lock()
	replies, exists := b.entries[reply.RequestID]
	if exists {
		delete(b.entries, reply.RequestID)
	}
	b.mu.Unlock()

	if !exists {
		return false
	}
	replies <- reply
	return true
}

func (b *pendingBroker) remove(requestID string) {
	b.mu.Lock()
	delete(b.entries, requestID)
	b.mu.Unlock()
}
