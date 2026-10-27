//go:build integration && darwin && cgo

package platform

import (
	"context"
	"errors"
	"testing"
)

func TestAXLatencySchedule(t *testing.T) {
	batch := axLatencySchedule()
	want := [6]int{10, 10, 100, 100, 100, 10}
	if batch.Completed != 0 {
		t.Fatal("pure schedule executed queries")
	}
	for i, sample := range batch.Samples {
		api := 1
		if i == 3 {
			api = 2
		}
		if sample.Sequence != i+1 || sample.API != api || sample.TimeoutMS != want[i] {
			t.Fatalf("unexpected schedule entry %d: %+v", i, sample)
		}
	}
}

func TestAXLatencyCancelledBeforeWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ProbeAXLatency(ctx, 1, 1, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAXLatencyInvalidIdentityBeforeWorker(t *testing.T) {
	_, err := ProbeAXLatency(context.Background(), 0, 0, 0)
	if !errors.Is(err, ErrAXLatencyIdentity) {
		t.Fatal(err)
	}
}

func TestAXLatencyBudgetBoundary(t *testing.T) {
	const budget = 2000000000
	if axLatencyBudgetExpired(budget-1) || !axLatencyBudgetExpired(budget) || !axLatencyBudgetExpired(budget+1) {
		t.Fatal("native budget boundary must stop at two seconds")
	}
}

func TestAXLatencyInFlightGate(t *testing.T) {
	if !axLatencyBusy.CompareAndSwap(false, true) {
		t.Fatal("unexpected worker")
	}
	defer axLatencyBusy.Store(false)
	_, err := ProbeAXLatency(context.Background(), 1, 1, 0)
	if !errors.Is(err, ErrAXLatencyBusy) {
		t.Fatal(err)
	}
}

func TestAXLatencyResultInitialization(t *testing.T) {
	batch := axLatencySchedule()
	if len(batch.Samples) != 6 || batch.Completed != 0 {
		t.Fatal("result bounds changed")
	}
	for _, sample := range batch.Samples {
		if sample.ValuePresent || sample.IsArray || sample.Count != 0 || sample.Elapsed != 0 || sample.SetupCompletedAge != 0 {
			t.Fatal("unexecuted result contains fabricated observations")
		}
	}
}
