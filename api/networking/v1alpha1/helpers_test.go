package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTimeoutDuration(t *testing.T) {
	if got := (&BroadcastSpec{}).TimeoutDuration(); got != DefaultTimeout.Duration {
		t.Fatalf("default timeout = %v, want %v", got, DefaultTimeout.Duration)
	}

	d := metav1.Duration{Duration: 50 * time.Millisecond}
	if got := (&BroadcastSpec{Timeout: &d}).TimeoutDuration(); got != 50*time.Millisecond {
		t.Fatalf("explicit timeout = %v, want 50ms", got)
	}

	zero := metav1.Duration{}
	if got := (&BroadcastSpec{Timeout: &zero}).TimeoutDuration(); got != DefaultTimeout.Duration {
		t.Fatalf("zero timeout should fall back to default, got %v", got)
	}
}

func TestConcurrencyValue(t *testing.T) {
	if got := (&BroadcastSpec{}).ConcurrencyValue(); got != DefaultConcurrency {
		t.Fatalf("default concurrency = %d, want %d", got, DefaultConcurrency)
	}

	c := int32(5)
	if got := (&BroadcastSpec{Concurrency: &c}).ConcurrencyValue(); got != 5 {
		t.Fatalf("explicit concurrency = %d, want 5", got)
	}

	zero := int32(0)
	if got := (&BroadcastSpec{Concurrency: &zero}).ConcurrencyValue(); got != DefaultConcurrency {
		t.Fatalf("zero concurrency should fall back to default, got %d", got)
	}
}
