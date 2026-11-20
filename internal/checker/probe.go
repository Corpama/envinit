package checker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// probeTargetPhaseTimeout is the fixed per-target ceiling for the environment
// probe phase (topology, NIC facts, counters, ib devices). Targets are probed
// concurrently, so this is a per-host wall-clock limit rather than a shared
// budget: hosts that finish quickly are unaffected while a single silent host
// aborts the whole run after this deadline. Fixed by design; not configurable.
const probeTargetPhaseTimeout = 30 * time.Second

// probeResult carries one target's probe outcome back from its goroutine.
type probeResult[T any] struct {
	index int
	name  string
	value T
	err   error
	timed bool
}

// probeTargetsConcurrently runs fn for every target in parallel, each bounded by
// probeTargetPhaseTimeout. It returns results ordered by the original target
// index. If any target fails or times out, it returns an aggregated error that
// names every failing target so the operator sees the full picture instead of
// only the first failure. Successful values are still returned for callers that
// want partial context, but callers should treat a non-nil error as fatal.
func probeTargetsConcurrently[T any](ctx context.Context, targets []Target, fn func(ctx context.Context, target Target) (T, error)) ([]T, error) {
	results := make([]probeResult[T], len(targets))
	var wg sync.WaitGroup
	for index, target := range targets {
		wg.Add(1)
		go func(index int, target Target) {
			defer wg.Done()
			targetCtx, cancel := context.WithTimeout(ctx, probeTargetPhaseTimeout)
			defer cancel()
			value, err := fn(targetCtx, target)
			res := probeResult[T]{index: index, name: target.Name, value: value, err: err}
			if err != nil && targetCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				res.timed = true
			}
			results[index] = res
		}(index, target)
	}
	wg.Wait()

	values := make([]T, len(targets))
	var failures []string
	for _, res := range results {
		values[res.index] = res.value
		if res.err == nil {
			continue
		}
		reason := strings.TrimSpace(res.err.Error())
		if res.timed {
			reason = fmt.Sprintf("probe exceeded %s and was aborted: %s", probeTargetPhaseTimeout, reason)
		}
		name := res.name
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("target[%d]", res.index)
		}
		failures = append(failures, fmt.Sprintf("  %s: %s", name, reason))
	}
	if len(failures) == 0 {
		return values, nil
	}
	sort.Strings(failures)
	return values, fmt.Errorf("environment probe failed for %d target(s):\n%s", len(failures), strings.Join(failures, "\n"))
}
