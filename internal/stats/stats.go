package stats

import (
	"errors"
	"sync"
	"time"

	"github.com/Xbai-hang/sotapi/internal/completion"
)

// UserStats is an immutable snapshot of one human's request outcomes.
type UserStats struct {
	Responded             uint64
	Unanswered            uint64
	TimedOut              uint64
	Canceled              uint64
	DeliveryFailed        uint64
	ConsecutiveUnanswered uint64
	ThresholdReached      bool
	TotalResponseTime     time.Duration
	AverageResponseTime   time.Duration
}

// Store keeps phase-one operational statistics in memory. It is safe for
// concurrent use. Restarting the process intentionally resets these values.
type Store struct {
	mu        sync.RWMutex
	threshold uint64
	users     map[string]UserStats
}

// NewStore creates an in-memory Store. unansweredThreshold controls when a
// user's consecutive timeout counter is marked as having reached its limit.
func NewStore(unansweredThreshold int) (*Store, error) {
	if unansweredThreshold <= 0 {
		return nil, errors.New("stats: unanswered threshold must be positive")
	}
	return &Store{
		threshold: uint64(unansweredThreshold),
		users:     make(map[string]UserStats),
	}, nil
}

// Record stores one terminal completion observation.
func (s *Store) Record(observation completion.Observation) {
	if observation.UserID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.users[observation.UserID]
	switch observation.Outcome {
	case completion.OutcomeResponded:
		current.Responded++
		current.ConsecutiveUnanswered = 0
		current.TotalResponseTime += observation.Latency
		current.AverageResponseTime = current.TotalResponseTime / time.Duration(current.Responded)
	case completion.OutcomeTimedOut:
		current.Unanswered++
		current.TimedOut++
		current.ConsecutiveUnanswered++
	case completion.OutcomeCanceled:
		current.Canceled++
	case completion.OutcomeDeliveryFailed:
		current.DeliveryFailed++
	default:
		return
	}

	current.ThresholdReached = current.ConsecutiveUnanswered >= s.threshold
	s.users[observation.UserID] = current
}

// All returns a detached snapshot of every observed user.
func (s *Store) All() map[string]UserStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]UserStats, len(s.users))
	for userID, value := range s.users {
		result[userID] = value
	}
	return result
}
