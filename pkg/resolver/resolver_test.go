package resolver

import (
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func TestMemorySetCopiesInput(t *testing.T) {
	m := New()
	key := types.NamespacedName{Namespace: "ns", Name: "b"}

	eps := []Endpoint{{IP: "10.0.0.1", Port: 8080}}
	m.Set(key, State{Endpoints: eps, Timeout: time.Second, Concurrency: 4})

	// Mutating the caller's slice after Set must not affect the live snapshot.
	eps[0].IP = "10.0.0.9"
	stored, ok := m.State(key)
	if !ok {
		t.Fatal("state missing")
	}
	if stored.Endpoints[0].IP != "10.0.0.1" {
		t.Fatalf("Set did not copy input: %+v", stored.Endpoints)
	}
}

func TestMemoryStateIsStable(t *testing.T) {
	m := New()
	key := types.NamespacedName{Namespace: "ns", Name: "b"}
	m.Set(key, State{Endpoints: []Endpoint{{IP: "10.0.0.1", Port: 8080}}, Timeout: time.Second, Concurrency: 4})

	// Repeated reads return the same immutable snapshot (no mutation, no copy).
	first, _ := m.State(key)
	second, _ := m.State(key)
	if len(first.Endpoints) != 1 || len(second.Endpoints) != 1 {
		t.Fatalf("unexpected endpoints: %+v / %+v", first.Endpoints, second.Endpoints)
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
	key := types.NamespacedName{Namespace: "ns", Name: "b"}
	m.Set(key, State{Endpoints: []Endpoint{{IP: "10.0.0.1", Port: 8080}}})

	snap := m.Snapshot()
	snap[key] = State{}

	if _, ok := m.State(key); !ok {
		t.Fatal("snapshot mutation must not affect the store")
	}
}

// TestMemoryIndependentInstances demonstrates the horizontal-scalability
// property: each replica (pod) holds its own Memory, and instances share no
// state. This is why scaling the Deployment replicas requires no coordination.
func TestMemoryIndependentInstances(t *testing.T) {
	a := New()
	b := New()
	key := types.NamespacedName{Namespace: "ns", Name: "b"}

	a.Set(key, State{Endpoints: []Endpoint{{IP: "10.0.0.1", Port: 8080}}})
	b.Set(key, State{Endpoints: []Endpoint{{IP: "10.0.0.2", Port: 8080}}})

	sa, _ := a.State(key)
	sb, _ := b.State(key)
	if sa.Endpoints[0].IP != "10.0.0.1" || sb.Endpoints[0].IP != "10.0.0.2" {
		t.Fatalf("instances must be isolated: a=%s b=%s", sa.Endpoints[0].IP, sb.Endpoints[0].IP)
	}

	// Updating one instance must not affect the other.
	a.Set(key, State{Endpoints: []Endpoint{{IP: "10.0.0.3", Port: 8080}}})
	if sb2, _ := b.State(key); sb2.Endpoints[0].IP != "10.0.0.2" {
		t.Fatalf("updating a must not affect b: %s", sb2.Endpoints[0].IP)
	}
}

// TestMemoryConsistentReadsDuringWrites verifies that concurrent readers always
// observe a complete snapshot (never a torn view) while a writer alternates the
// endpoint set between 1 and 3 entries. Also exercises data-race freedom under
// `go test -race`.
func TestMemoryConsistentReadsDuringWrites(t *testing.T) {
	m := New()
	key := types.NamespacedName{Namespace: "ns", Name: "b"}
	one := State{Endpoints: []Endpoint{{IP: "10.0.0.1", Port: 1}}, Timeout: time.Second, Concurrency: 4}
	three := State{Endpoints: []Endpoint{
		{IP: "10.0.0.1", Port: 1}, {IP: "10.0.0.2", Port: 1}, {IP: "10.0.0.3", Port: 1},
	}, Timeout: time.Second, Concurrency: 4}
	m.Set(key, one)

	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			if i%2 == 0 {
				m.Set(key, one)
			} else {
				m.Set(key, three)
			}
		}
		close(done)
	}()

	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				s, ok := m.State(key)
				if ok {
					if n := len(s.Endpoints); n != 1 && n != 3 {
						t.Errorf("torn read: saw %d endpoints", n)
						return
					}
				}
				select {
				case <-done:
					return
				default:
				}
			}
		}()
	}

	wg.Wait()
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

func BenchmarkMemoryState(b *testing.B) {
	m := New()
	key := types.NamespacedName{Namespace: "ns", Name: "b"}
	eps := make([]Endpoint, 100)
	for i := range eps {
		eps[i] = Endpoint{IP: "10.0.0.1", Port: int32(i + 1)}
	}
	m.Set(key, State{Endpoints: eps, Timeout: time.Second, Concurrency: 16})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = m.State(key)
		}
	})
}
