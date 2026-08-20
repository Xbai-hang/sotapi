package routing

import (
	"errors"
	"strings"
	"testing"
)

func TestRouterResolve(t *testing.T) {
	models := []Model{
		{ID: "human-general", PoolID: "friends"},
		{ID: "human-reviewer", PoolID: "friends"},
	}
	pools := []Pool{{ID: "friends", UserIDs: []string{"alice"}}}
	users := []User{{ID: "alice", Channel: "telegram", Recipient: "123"}}

	router, err := NewRouter(models, pools, users)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	// The router must detach its routing graph from caller-owned slices.
	pools[0].UserIDs[0] = "changed"

	for _, modelID := range []string{"human-general", "human-reviewer"} {
		target, err := router.Resolve(modelID)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", modelID, err)
		}
		if target.User.ID != "alice" {
			t.Fatalf("Resolve(%q) = %#v", modelID, target)
		}
	}
}

func TestRouterResolveUnknownModel(t *testing.T) {
	router := mustRouter(t)
	_, err := router.Resolve("missing")
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrModelNotFound", err)
	}
}

func TestNewRouterRejectsInvalidGraph(t *testing.T) {
	validModels := []Model{{ID: "human", PoolID: "pool"}}
	validPools := []Pool{{ID: "pool", UserIDs: []string{"alice"}}}
	validUsers := []User{{ID: "alice", Channel: "telegram", Recipient: "123"}}

	tests := []struct {
		name    string
		models  []Model
		pools   []Pool
		users   []User
		wantErr string
	}{
		{name: "no models", pools: validPools, users: validUsers, wantErr: "at least one model"},
		{name: "no pools", models: validModels, users: validUsers, wantErr: "at least one pool"},
		{name: "no users", models: validModels, pools: validPools, wantErr: "at least one user"},
		{name: "empty user ID", models: validModels, pools: validPools, users: []User{{Channel: "telegram", Recipient: "123"}}, wantErr: "user ID"},
		{name: "missing channel", models: validModels, pools: validPools, users: []User{{ID: "alice", Recipient: "123"}}, wantErr: "no channel"},
		{name: "missing recipient", models: validModels, pools: validPools, users: []User{{ID: "alice", Channel: "telegram"}}, wantErr: "no recipient"},
		{name: "duplicate user", models: validModels, pools: validPools, users: append(validUsers, validUsers[0]), wantErr: "duplicate user"},
		{name: "empty pool ID", models: validModels, pools: []Pool{{UserIDs: []string{"alice"}}}, users: validUsers, wantErr: "pool ID"},
		{name: "duplicate pool", models: validModels, pools: append(validPools, validPools[0]), users: validUsers, wantErr: "duplicate pool"},
		{name: "empty pool", models: validModels, pools: []Pool{{ID: "pool"}}, users: validUsers, wantErr: "exactly one user"},
		{name: "multi-user phase one pool", models: validModels, pools: []Pool{{ID: "pool", UserIDs: []string{"alice", "bob"}}}, users: append(validUsers, User{ID: "bob", Channel: "telegram", Recipient: "456"}), wantErr: "exactly one user"},
		{name: "unknown pool user", models: validModels, pools: []Pool{{ID: "pool", UserIDs: []string{"bob"}}}, users: validUsers, wantErr: "unknown user"},
		{name: "empty model ID", models: []Model{{PoolID: "pool"}}, pools: validPools, users: validUsers, wantErr: "model ID"},
		{name: "missing model pool", models: []Model{{ID: "human"}}, pools: validPools, users: validUsers, wantErr: "has no pool"},
		{name: "duplicate model", models: append(validModels, validModels[0]), pools: validPools, users: validUsers, wantErr: "duplicate model"},
		{name: "unknown model pool", models: []Model{{ID: "human", PoolID: "missing"}}, pools: validPools, users: validUsers, wantErr: "unknown pool"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRouter(test.models, test.pools, test.users)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("NewRouter() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func mustRouter(t *testing.T) *Router {
	t.Helper()
	router, err := NewRouter(
		[]Model{{ID: "human", PoolID: "pool"}},
		[]Pool{{ID: "pool", UserIDs: []string{"alice"}}},
		[]User{{ID: "alice", Channel: "telegram", Recipient: "123"}},
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	return router
}
