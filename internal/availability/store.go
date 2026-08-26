// Package availability maintains channel-independent in-memory availability
// state for configured human responders.
package availability

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Xbai-hang/sotapi/internal/routing"
)

type Config struct {
	Enabled            bool
	AfterMissedReplies int
}

type Status struct {
	Online        bool
	MissedReplies int
}

type Transition struct {
	User          routing.User
	MissedReplies int
	BecameOffline bool
}

type userState struct {
	user          routing.User
	online        bool
	missedReplies int
}

// Store keeps availability state for one process lifetime. All users start
// online, and the state is safe for concurrent completion requests.
type Store struct {
	mu         sync.RWMutex
	config     Config
	users      map[string]userState
	byEndpoint map[string]string
}

func NewStore(users []routing.User, config Config) (*Store, error) {
	if config.AfterMissedReplies <= 0 {
		return nil, errors.New("availability: after missed replies must be positive")
	}
	if len(users) == 0 {
		return nil, errors.New("availability: at least one user is required")
	}
	store := &Store{
		config:     config,
		users:      make(map[string]userState, len(users)),
		byEndpoint: make(map[string]string, len(users)),
	}
	for _, user := range users {
		if strings.TrimSpace(user.ID) == "" || strings.TrimSpace(user.Channel) == "" || strings.TrimSpace(user.Recipient) == "" {
			return nil, errors.New("availability: user ID, channel and recipient are required")
		}
		if _, exists := store.users[user.ID]; exists {
			return nil, fmt.Errorf("availability: duplicate user %q", user.ID)
		}
		endpoint := endpointKey(user.Channel, user.Recipient)
		if existing, exists := store.byEndpoint[endpoint]; exists {
			return nil, fmt.Errorf("availability: users %q and %q share one channel endpoint", existing, user.ID)
		}
		store.users[user.ID] = userState{user: user, online: true}
		store.byEndpoint[endpoint] = user.ID
	}
	return store, nil
}

func (s *Store) IsOnline(userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, exists := s.users[userID]
	return exists && state.online
}

func (s *Store) RecordMissedReply(userID string) (Transition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, exists := s.users[userID]
	if !exists {
		return Transition{}, fmt.Errorf("availability: unknown user %q", userID)
	}
	transition := Transition{User: state.user, MissedReplies: state.missedReplies}
	if !s.config.Enabled || !state.online {
		return transition, nil
	}
	state.missedReplies++
	transition.MissedReplies = state.missedReplies
	if state.missedReplies >= s.config.AfterMissedReplies {
		state.online = false
		transition.BecameOffline = true
	}
	s.users[userID] = state
	return transition, nil
}

func (s *Store) RecordReply(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, exists := s.users[userID]
	if !exists {
		return fmt.Errorf("availability: unknown user %q", userID)
	}
	state.missedReplies = 0
	s.users[userID] = state
	return nil
}

func (s *Store) SetOnline(channel, recipient string) (routing.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	userID, exists := s.byEndpoint[endpointKey(channel, recipient)]
	if !exists {
		return routing.User{}, fmt.Errorf("availability: unknown %s recipient %q", channel, recipient)
	}
	state := s.users[userID]
	state.online = true
	state.missedReplies = 0
	s.users[userID] = state
	return state.user, nil
}

func (s *Store) Status(userID string) (Status, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, exists := s.users[userID]
	if !exists {
		return Status{}, false
	}
	return Status{Online: state.online, MissedReplies: state.missedReplies}, true
}

func endpointKey(channel, recipient string) string {
	return channel + "\x00" + recipient
}
