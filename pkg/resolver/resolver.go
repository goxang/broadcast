// Package resolver holds the in-memory state shared between the controller
// (which writes it from informer state) and the proxy data plane (which reads
// it on the request hot path).
//
// The proxy never talks to the Kubernetes API server: it reads this store,
// which the controller keeps current. This is what keeps the hot path
// independent of API calls.
//
// The store is copy-on-write: writes build a fresh immutable snapshot and
// publish it with a single atomic pointer swap, so the read path is lock-free
// and allocation-free. Each replica (pod) runs its own independent resolver,
// which is what makes the data plane horizontally scalable with no shared
// state or coordination layer.
package resolver

import (
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// Endpoint is a single eligible target for a broadcast. It is derived from a
// ready (and non-terminating) EndpointSlice endpoint.
type Endpoint struct {
	// IP is the endpoint address (IPv4 or IPv6).
	IP string
	// Port is the target port the endpoint listens on.
	Port int32
}

// State is the fully-resolved runtime state of a single Broadcast, ready to be
// consumed by the proxy without any further Kubernetes access.
//
// The Endpoints slice returned by Resolver.State is immutable and shared with
// the live snapshot; callers must treat it as read-only.
type State struct {
	// Endpoints is the current set of ready endpoints.
	Endpoints []Endpoint
	// Timeout bounds the fan-out operation.
	Timeout time.Duration
	// Concurrency bounds concurrent target requests per broadcast.
	Concurrency int32
}

// Resolver is the interface the proxy uses to resolve the current state for a
// Broadcast. It is implemented by an in-memory store fed by the controller.
type Resolver interface {
	// State returns the current state for key. The boolean is false when the
	// key has never been set (unknown Broadcast).
	State(key types.NamespacedName) (State, bool)
	// Set replaces the state for key.
	Set(key types.NamespacedName, state State)
	// Delete removes key (used when a Broadcast is deleted).
	Delete(key types.NamespacedName)
	// Snapshot returns a copy of all known keys and their state. Used only for
	// metrics/observability, never on the hot path.
	Snapshot() map[types.NamespacedName]State
}

// snapshot is an immutable map of namespaced-name to State. A new snapshot is
// allocated on every write and published atomically, so readers always observe
// a complete, self-consistent view and never lock.
type snapshot map[types.NamespacedName]State

// Memory is a copy-on-write, concurrency-safe Resolver. Reads (the proxy hot
// path) are a single atomic pointer load with no lock and no allocation;
// writes (the controller) build a new snapshot and publish it atomically.
type Memory struct {
	snap atomic.Pointer[snapshot]
}

// New returns an empty in-memory Resolver.
func New() *Memory {
	m := &Memory{}
	empty := snapshot{}
	m.snap.Store(&empty)
	return m
}

// State implements Resolver. It returns the immutable state for key without
// copying; the caller must not mutate the returned Endpoints slice.
func (m *Memory) State(key types.NamespacedName) (State, bool) {
	s, ok := (*m.snap.Load())[key]
	return s, ok
}

// Set implements Resolver. It copies the caller's Endpoints slice before
// publishing so the live snapshot never aliases caller-owned memory.
func (m *Memory) Set(key types.NamespacedName, state State) {
	state.Endpoints = append([]Endpoint(nil), state.Endpoints...)
	m.publish(key, state, false)
}

// Delete implements Resolver.
func (m *Memory) Delete(key types.NamespacedName) {
	m.publish(key, State{}, true)
}

// Snapshot implements Resolver. It returns a fresh map (so callers may mutate
// it freely) whose Endpoints slices are shared with the immutable snapshot.
func (m *Memory) Snapshot() map[types.NamespacedName]State {
	cur := *m.snap.Load()
	out := make(map[types.NamespacedName]State, len(cur))
	for k, v := range cur {
		out[k] = v
	}
	return out
}

// publish builds a new immutable snapshot derived from the current one and
// atomically swaps it in. Writes are rare (controller reconcile), so the
// copy-on-write cost is negligible; the CAS retry loop handles concurrent
// writers.
func (m *Memory) publish(key types.NamespacedName, state State, remove bool) {
	for {
		old := m.snap.Load()
		next := make(snapshot, len(*old)+1)
		for k, v := range *old {
			next[k] = v
		}
		if remove {
			delete(next, key)
		} else {
			next[key] = state
		}
		if m.snap.CompareAndSwap(old, &next) {
			return
		}
	}
}
