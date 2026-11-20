package checker

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeTargetsConcurrentlyReturnsOrderedValues(t *testing.T) {
	targets := []Target{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	values, err := probeTargetsConcurrently(context.Background(), targets, func(ctx context.Context, target Target) (string, error) {
		return "v-" + target.Name, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"v-a", "v-b", "v-c"}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("values[%d] = %q, want %q (full: %v)", i, values[i], want[i], values)
		}
	}
}

func TestProbeTargetsConcurrentlyRunsInParallel(t *testing.T) {
	targets := make([]Target, 6)
	for i := range targets {
		targets[i] = Target{Name: string(rune('a' + i))}
	}
	var peak int64
	var current int64
	_, err := probeTargetsConcurrently(context.Background(), targets, func(ctx context.Context, target Target) (int, error) {
		n := atomic.AddInt64(&current, 1)
		for {
			p := atomic.LoadInt64(&peak)
			if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt64(&current, -1)
		return 0, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&peak) < 2 {
		t.Fatalf("expected concurrent execution, peak parallelism was %d", peak)
	}
}

func TestProbeTargetsConcurrentlyAggregatesEveryFailure(t *testing.T) {
	targets := []Target{{Name: "ok1"}, {Name: "bad-a"}, {Name: "ok2"}, {Name: "bad-b"}}
	_, err := probeTargetsConcurrently(context.Background(), targets, func(ctx context.Context, target Target) (string, error) {
		if strings.HasPrefix(target.Name, "bad") {
			return "", errors.New("boom-" + target.Name)
		}
		return "", nil
	})
	if err == nil {
		t.Fatal("expected aggregated failure")
	}
	for _, want := range []string{"failed for 2 target(s)", "bad-a", "bad-b", "boom-bad-a", "boom-bad-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "ok1") || strings.Contains(err.Error(), "ok2") {
		t.Fatalf("successful targets must not appear in error: %v", err)
	}
}

func TestProbeTargetsConcurrentlyPerTargetTimeoutIsIsolated(t *testing.T) {
	targets := []Target{{Name: "fast"}, {Name: "slow"}}
	start := time.Now()
	_, err := probeTargetsConcurrently(context.Background(), targets, func(ctx context.Context, target Target) (string, error) {
		if target.Name == "slow" {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * probeTargetPhaseTimeout):
				return "done", nil
			}
		}
		return "done", nil
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the slow target to time out")
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Fatalf("expected slow target in error: %v", err)
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("expected timeout wording in error: %v", err)
	}
	if elapsed > probeTargetPhaseTimeout+5*time.Second {
		t.Fatalf("per-target timeout not enforced: elapsed %s", elapsed)
	}
}
