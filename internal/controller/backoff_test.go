package controller

import (
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// TestBackoffDelaySequence pins the documented backoff progression
// (30s, 60s, 120s, 240s, capped at 5m) so the retry behavior surfaced in
// status messages stays accurate.
func TestBackoffDelaySequence(t *testing.T) {
	r := &GatewaySyncReconciler{}
	key := types.NamespacedName{Name: "cr", Namespace: "ns"}

	if got := r.backoffDelay(key); got != 30*time.Second {
		t.Errorf("delay before any failure = %v, want 30s", got)
	}

	want := []time.Duration{
		30 * time.Second,
		60 * time.Second,
		120 * time.Second,
		240 * time.Second,
		5 * time.Minute,
		5 * time.Minute,
	}
	for i, w := range want {
		r.recordFailure(key)
		if got := r.backoffDelay(key); got != w {
			t.Errorf("delay after %d failure(s) = %v, want %v", i+1, got, w)
		}
	}

	r.resetBackoff(key)
	if got := r.backoffDelay(key); got != 30*time.Second {
		t.Errorf("delay after reset = %v, want 30s", got)
	}
}

// TestReconcilerSharedStateConcurrency exercises the backoff and token-cache
// maps from concurrent goroutines, mirroring MaxConcurrentReconciles > 1 where
// reconciles for different CRs touch this shared state simultaneously. Run with
// -race this fails if the mutex guarding the maps is removed.
func TestReconcilerSharedStateConcurrency(t *testing.T) {
	r := &GatewaySyncReconciler{}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := types.NamespacedName{Name: "cr", Namespace: "ns"}
			cacheKey := "123:456"
			if n%2 == 0 {
				key.Name = "other-cr"
				cacheKey = "789:101"
			}
			for range 100 {
				r.recordFailure(key)
				_ = r.backoffDelay(key)
				r.storeGitHubToken(cacheKey, cachedToken{token: "t", expiry: time.Now()})
				_, _ = r.cachedGitHubToken(cacheKey)
				r.resetBackoff(key)
			}
		}(i)
	}
	wg.Wait()
}
