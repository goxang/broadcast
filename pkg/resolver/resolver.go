// Package resolver holds the in-memory state shared between the controller
// (which writes it from informer state) and the proxy data plane (which reads
// it on the request hot path).
//
// The proxy never talks to the Kubernetes API server: it reads this store,
// which the controller keeps current. This is what keeps the hot path
// independent of API calls.
package resolver

import (
	"sync"
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

// Memory is a concurrency-safe in-memory Resolver.
type Memory struct {
	mu   sync.RWMutex
	byID map[types.NamespacedName]State
}

// New returns an empty in-memory Resolver.
func New() *Memory {
	return &Memory{byID: make(map[types.NamespacedName]State)}
}

// State implements Resolver.
func (m *Memory) State(key types.NamespacedName) (State, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.byID[key]
	if !ok {
		return State{}, false
	}
	s.Endpoints = append([]Endpoint(nil), s.Endpoints...)
	return s, true
}

// Set implements Resolver.
func (m *Memory) Set(key types.NamespacedName, state State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state.Endpoints = append([]Endpoint(nil), state.Endpoints...)
	m.byID[key] = state
}

// Delete implements Resolver.
func (m *Memory) Delete(key types.NamespacedName) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, key)
}

// Snapshot implements Resolver.
func (m *Memory) Snapshot() map[types.NamespacedName]State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[types.NamespacedName]State, len(m.byID))
	for k, v := range m.byID {
		v.Endpoints = append([]Endpoint(nil), v.Endpoints...)
		out[k] = v
	}
	return out
}
