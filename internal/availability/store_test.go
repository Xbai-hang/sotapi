package availability

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Xbai-hang/sotapi/internal/routing"
)

func TestStoreConcurrentMissesProduceOneOfflineTransition(t *testing.T) {
	store := newTestStore(t, Config{Enabled: true, AfterMissedReplies: 3})
	var transitions atomic.Int32
	var wait sync.WaitGroup
	for range 10 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			transition, err := store.RecordMissedReply("alice")
			if err != nil {
				t.Errorf("RecordMissedReply() error = %v", err)
				return
			}
			if transition.BecameOffline {
				transitions.Add(1)
			}
		}()
	}
	wait.Wait()
	status, _ := store.Status("alice")
	if transitions.Load() != 1 || status.Online || status.MissedReplies != 3 {
		t.Fatalf("transitions = %d, status = %#v", transitions.Load(), status)
	}
}

func TestStoreDefaultsUsersOnlineAndTransitionsAtConfiguredLimit(t *testing.T) {
	store := newTestStore(t, Config{Enabled: true, AfterMissedReplies: 3})

	if !store.IsOnline("alice") {
		t.Fatal("alice should default to online")
	}
	for missed := 1; missed <= 3; missed++ {
		transition, err := store.RecordMissedReply("alice")
		if err != nil {
			t.Fatalf("RecordMissedReply() error = %v", err)
		}
		wantOffline := missed == 3
		if transition.BecameOffline != wantOffline || transition.MissedReplies != missed {
			t.Fatalf("miss %d transition = %#v", missed, transition)
		}
	}
	if store.IsOnline("alice") {
		t.Fatal("alice should be offline after the third missed reply")
	}

	transition, err := store.RecordMissedReply("alice")
	if err != nil || transition.BecameOffline {
		t.Fatalf("offline RecordMissedReply() = %#v, %v", transition, err)
	}
}

func TestStoreReplyResetsConsecutiveMisses(t *testing.T) {
	store := newTestStore(t, Config{Enabled: true, AfterMissedReplies: 2})
	if _, err := store.RecordMissedReply("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordReply("alice"); err != nil {
		t.Fatal(err)
	}

	status, exists := store.Status("alice")
	if !exists || !status.Online || status.MissedReplies != 0 {
		t.Fatalf("Status() = %#v, %v", status, exists)
	}
	transition, err := store.RecordMissedReply("alice")
	if err != nil || transition.BecameOffline || transition.MissedReplies != 1 {
		t.Fatalf("RecordMissedReply() after reset = %#v, %v", transition, err)
	}
}

func TestStoreDisabledPolicyNeverTakesUserOffline(t *testing.T) {
	store := newTestStore(t, Config{Enabled: false, AfterMissedReplies: 1})
	for range 3 {
		transition, err := store.RecordMissedReply("alice")
		if err != nil || transition.BecameOffline {
			t.Fatalf("RecordMissedReply() = %#v, %v", transition, err)
		}
	}
	status, _ := store.Status("alice")
	if !status.Online || status.MissedReplies != 0 {
		t.Fatalf("disabled policy status = %#v", status)
	}
}

func TestStoreSetOnlineByChannelEndpoint(t *testing.T) {
	store := newTestStore(t, Config{Enabled: true, AfterMissedReplies: 1})
	if _, err := store.RecordMissedReply("alice"); err != nil {
		t.Fatal(err)
	}

	user, err := store.SetOnline("telegram", "123")
	if err != nil {
		t.Fatalf("SetOnline() error = %v", err)
	}
	if user.ID != "alice" || !store.IsOnline("alice") {
		t.Fatalf("SetOnline() user = %#v", user)
	}
	status, _ := store.Status("alice")
	if status.MissedReplies != 0 {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestStoreRejectsInvalidConfigurationAndUnknownUsers(t *testing.T) {
	users := []routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}}
	if _, err := NewStore(users, Config{}); err == nil {
		t.Fatal("NewStore() with zero missed reply limit succeeded")
	}
	if _, err := NewStore(nil, Config{AfterMissedReplies: 3}); err == nil {
		t.Fatal("NewStore() without users succeeded")
	}
	if _, err := NewStore([]routing.User{{ID: "alice"}}, Config{AfterMissedReplies: 3}); err == nil {
		t.Fatal("NewStore() with incomplete user succeeded")
	}
	if _, err := NewStore(append(users, users[0]), Config{AfterMissedReplies: 3}); err == nil {
		t.Fatal("NewStore() with duplicate user succeeded")
	}
	if _, err := NewStore(append(users, routing.User{ID: "alias", Channel: "telegram", Recipient: "123"}), Config{AfterMissedReplies: 3}); err == nil {
		t.Fatal("NewStore() with duplicate endpoint succeeded")
	}

	store := newTestStore(t, Config{Enabled: true, AfterMissedReplies: 3})
	if store.IsOnline("missing") {
		t.Fatal("missing user is online")
	}
	if _, err := store.RecordMissedReply("missing"); err == nil {
		t.Fatal("RecordMissedReply(missing) succeeded")
	}
	if err := store.RecordReply("missing"); err == nil {
		t.Fatal("RecordReply(missing) succeeded")
	}
	if _, err := store.SetOnline("telegram", "999"); err == nil {
		t.Fatal("SetOnline(missing) succeeded")
	}
	if _, exists := store.Status("missing"); exists {
		t.Fatal("Status(missing) exists")
	}
}

func newTestStore(t *testing.T, config Config) *Store {
	t.Helper()
	store, err := NewStore([]routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}}, config)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}
