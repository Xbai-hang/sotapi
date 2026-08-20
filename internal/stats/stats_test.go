package stats

import (
	"testing"
	"time"

	"github.com/Xbai-hang/sotapi/internal/completion"
)

func TestStoreTracksResponsesTimeoutsAndThreshold(t *testing.T) {
	store, err := NewStore(2)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	store.Record(completion.Observation{UserID: "alice", Outcome: completion.OutcomeTimedOut, Latency: time.Second})
	first, exists := store.All()["alice"]
	if !exists || first.Unanswered != 1 || first.ConsecutiveUnanswered != 1 || first.ThresholdReached {
		t.Fatalf("first timeout stats = %#v, exists = %v", first, exists)
	}
	store.Record(completion.Observation{UserID: "alice", Outcome: completion.OutcomeTimedOut, Latency: time.Second})
	second := store.All()["alice"]
	if second.TimedOut != 2 || second.ConsecutiveUnanswered != 2 || !second.ThresholdReached {
		t.Fatalf("second timeout stats = %#v", second)
	}

	store.Record(completion.Observation{UserID: "alice", Outcome: completion.OutcomeResponded, Latency: 2 * time.Second})
	store.Record(completion.Observation{UserID: "alice", Outcome: completion.OutcomeResponded, Latency: 4 * time.Second})
	responded := store.All()["alice"]
	if responded.Responded != 2 || responded.ConsecutiveUnanswered != 0 || responded.ThresholdReached {
		t.Fatalf("response stats = %#v", responded)
	}
	if responded.TotalResponseTime != 6*time.Second || responded.AverageResponseTime != 3*time.Second {
		t.Fatalf("response times = total %v average %v", responded.TotalResponseTime, responded.AverageResponseTime)
	}
}

func TestStoreSeparatesCancellationAndDeliveryFailure(t *testing.T) {
	store, err := NewStore(1)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	store.Record(completion.Observation{UserID: "alice", Outcome: completion.OutcomeCanceled})
	store.Record(completion.Observation{UserID: "alice", Outcome: completion.OutcomeDeliveryFailed})

	snapshot := store.All()["alice"]
	if snapshot.Canceled != 1 || snapshot.DeliveryFailed != 1 || snapshot.Unanswered != 0 || snapshot.ThresholdReached {
		t.Fatalf("stats = %#v", snapshot)
	}
}

func TestStoreAllReturnsDetachedSnapshot(t *testing.T) {
	store, err := NewStore(1)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	store.Record(completion.Observation{UserID: "alice", Outcome: completion.OutcomeResponded, Latency: time.Second})

	all := store.All()
	all["alice"] = UserStats{}
	actual, exists := store.All()["alice"]
	if !exists || actual.Responded != 1 {
		t.Fatalf("stored stats changed through All(): %#v", actual)
	}
	if _, exists := store.All()["missing"]; exists {
		t.Fatal("Snapshot(missing) exists")
	}
}

func TestStoreIgnoresMalformedObservations(t *testing.T) {
	store, err := NewStore(1)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	store.Record(completion.Observation{Outcome: completion.OutcomeResponded})
	store.Record(completion.Observation{UserID: "alice", Outcome: completion.Outcome("unknown")})
	if all := store.All(); len(all) != 0 {
		t.Fatalf("All() = %#v, want no malformed observations", all)
	}
}

func TestNewStoreRejectsNonPositiveThreshold(t *testing.T) {
	for _, threshold := range []int{0, -1} {
		if _, err := NewStore(threshold); err == nil {
			t.Fatalf("NewStore(%d) succeeded", threshold)
		}
	}
}
