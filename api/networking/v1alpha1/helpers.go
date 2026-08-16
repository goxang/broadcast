package v1alpha1

import "time"

// TimeoutDuration returns the effective fan-out timeout, applying the default
// when unset or non-positive.
func (s *BroadcastSpec) TimeoutDuration() time.Duration {
	if s.Timeout != nil && s.Timeout.Duration > 0 {
		return s.Timeout.Duration
	}
	return DefaultTimeout.Duration
}

// ConcurrencyValue returns the effective fan-out concurrency, applying the
// default when unset or non-positive.
func (s *BroadcastSpec) ConcurrencyValue() int32 {
	if s.Concurrency != nil && *s.Concurrency > 0 {
		return *s.Concurrency
	}
	return DefaultConcurrency
}
