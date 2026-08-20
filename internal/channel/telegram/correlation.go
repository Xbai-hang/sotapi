package telegram

import (
	"fmt"
	"sync"
)

type correlationStore struct {
	mu       sync.Mutex
	requests map[string]string
}

func newCorrelationStore() *correlationStore {
	return &correlationStore{requests: make(map[string]string)}
}

func (s *correlationStore) register(chatID int64, messageID int, requestID string) (string, error) {
	key := correlationKey(chatID, messageID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.requests[key]; exists {
		return "", fmt.Errorf("telegram: duplicate correlation %s", key)
	}
	s.requests[key] = requestID
	return key, nil
}

func (s *correlationStore) consume(chatID int64, messageID int) (string, bool) {
	key := correlationKey(chatID, messageID)
	s.mu.Lock()
	defer s.mu.Unlock()
	requestID, exists := s.requests[key]
	if exists {
		delete(s.requests, key)
	}
	return requestID, exists
}

func (s *correlationStore) forget(key string) {
	s.mu.Lock()
	delete(s.requests, key)
	s.mu.Unlock()
}

func correlationKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}
