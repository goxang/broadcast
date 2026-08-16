package resolver

import (
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func TestMemorySetAndStateCopy(t *testing.T) {
	m := New()
	key := types.NamespacedName{Namespace: "ns", Name: "b"}

	eps := []Endpoint{{IP: "10.0.0.1", Port: 8080}}
	m.Set(key, State{Endpoints: eps, Timeout: time.Second, Concurrency: 4})

	// Mutating the input slice after Set must not affect the stored state.
	eps[0].IP = "10.0.0.9"
	stored, ok := m.State(key)
	if !ok {
		t.Fatal("state missing")
	}
	if stored.Endpoints[0].IP != "10.0.0.1" {
		t.Fatalf("Set did not copy input: %+v", stored.Endpoints)
	}

	// Mutating the returned State must not affect the store.
	stored.Endpoints[0].IP = "10.0.0.99"
	stored2, _ := m.State(key)
	if stored2.Endpoints[0].IP != "10.0.0.1" {
		t.Fatalf("State did not copy output: %+v", stored2.Endpoints)
	}
}

func TestMemoryUnknownKey(t *testing.T) {
	m := New()
	if _, ok := m.State(types.NamespacedName{Namespace: "ns", Name: "nope"}); ok {
		t.Fatal("unknown key should return ok=false")
	}
}

func TestMemoryDelete(t *testing.T) {
	m := New()
	key := types.NamespacedName{Namespace: "ns", Name: "b"}
	m.Set(key, State{Endpoints: []Endpoint{{IP: "10.0.0.1", Port: 8080}}})
	m.Delete(key)
	if _, ok := m.State(key); ok {
		t.Fatal("key should be deleted")
	}
}

func TestMemorySnapshotIsCopy(t *testing.T) {
	m := New()
	m.Set(types.NamespacedName{Namespace: "ns", Name: "b"}, State{Endpoints: []Endpoint{{IP: "10.0.0.1", Port: 8080}}})

	snap := m.Snapshot()
	snap[types.NamespacedName{Namespace: "ns", Name: "b"}] = State{}

	if _, ok := m.State(types.NamespacedName{Namespace: "ns", Name: "b"}); !ok {
		t.Fatal("snapshot mutation must not affect the store")
	}
}

func TestMemoryConcurrent(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := types.NamespacedName{Namespace: "ns", Name: "b"}
			m.Set(key, State{Endpoints: []Endpoint{{IP: "10.0.0.1", Port: int32(i)}}})
			_, _ = m.State(key)
			_ = m.Snapshot()
		}(i)
	}
	wg.Wait()
}
