package routing

import (
	"errors"
	"fmt"
	"strings"
)

// ErrModelNotFound indicates that no configured route exists for a model.
var ErrModelNotFound = errors.New("model not found")

// Router resolves externally visible model names to human destinations.
// It is immutable after construction and therefore safe for concurrent use.
type Router struct {
	models map[string]Model
	pools  map[string]Pool
	users  map[string]User
}

// NewRouter validates the phase-one routing graph and returns an immutable
// Router. A phase-one pool must contain exactly one configured human.
func NewRouter(models []Model, pools []Pool, users []User) (*Router, error) {
	if len(models) == 0 {
		return nil, errors.New("routing: at least one model is required")
	}
	if len(pools) == 0 {
		return nil, errors.New("routing: at least one pool is required")
	}
	if len(users) == 0 {
		return nil, errors.New("routing: at least one user is required")
	}

	router := &Router{
		models: make(map[string]Model, len(models)),
		pools:  make(map[string]Pool, len(pools)),
		users:  make(map[string]User, len(users)),
	}

	for _, user := range users {
		if err := validateUser(user); err != nil {
			return nil, err
		}
		if _, exists := router.users[user.ID]; exists {
			return nil, fmt.Errorf("routing: duplicate user %q", user.ID)
		}
		router.users[user.ID] = user
	}

	for _, pool := range pools {
		if strings.TrimSpace(pool.ID) == "" {
			return nil, errors.New("routing: pool ID is required")
		}
		if _, exists := router.pools[pool.ID]; exists {
			return nil, fmt.Errorf("routing: duplicate pool %q", pool.ID)
		}
		if len(pool.UserIDs) != 1 {
			return nil, fmt.Errorf("routing: pool %q must contain exactly one user in phase one", pool.ID)
		}
		if _, exists := router.users[pool.UserIDs[0]]; !exists {
			return nil, fmt.Errorf("routing: pool %q references unknown user %q", pool.ID, pool.UserIDs[0])
		}
		router.pools[pool.ID] = Pool{ID: pool.ID, UserIDs: append([]string(nil), pool.UserIDs...)}
	}

	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			return nil, errors.New("routing: model ID is required")
		}
		if strings.TrimSpace(model.PoolID) == "" {
			return nil, fmt.Errorf("routing: model %q has no pool", model.ID)
		}
		if _, exists := router.models[model.ID]; exists {
			return nil, fmt.Errorf("routing: duplicate model %q", model.ID)
		}
		if _, exists := router.pools[model.PoolID]; !exists {
			return nil, fmt.Errorf("routing: model %q references unknown pool %q", model.ID, model.PoolID)
		}
		router.models[model.ID] = model
	}

	return router, nil
}

// Resolve returns the phase-one human target configured for modelID.
func (r *Router) Resolve(modelID string) (Target, error) {
	model, exists := r.models[modelID]
	if !exists {
		return Target{}, fmt.Errorf("%w: %s", ErrModelNotFound, modelID)
	}
	pool := r.pools[model.PoolID]
	return Target{
		User: r.users[pool.UserIDs[0]],
	}, nil
}

func validateUser(user User) error {
	if strings.TrimSpace(user.ID) == "" {
		return errors.New("routing: user ID is required")
	}
	if strings.TrimSpace(user.Channel) == "" {
		return fmt.Errorf("routing: user %q has no channel", user.ID)
	}
	if strings.TrimSpace(user.Recipient) == "" {
		return fmt.Errorf("routing: user %q has no recipient", user.ID)
	}
	return nil
}
