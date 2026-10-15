package mpc

import (
	"context"
	"sync/atomic"
	"testing"
)

// A phone shows the plan the box holds, so the box says each time that moves:
// a published plan, and a newest replan that ends without one. A request
// that a newer one replaced says nothing; the newer one will.
func TestOnPlanChangeHearsEveryChangeAReaderSees(t *testing.T) {
	svc := newCancellationTestService(t, nil)
	var calls atomic.Int32
	svc.OnPlanChange(func() { calls.Add(1) })

	if svc.Replan(context.Background()) == nil || calls.Load() != 1 {
		t.Fatalf("published plan: %d calls, want 1", calls.Load())
	}

	svc.Zone = "no-prices"
	svc.RequestReplan("saved-goal")
	waitForRequestedReplans(t, svc)
	if got := calls.Load(); got != 2 || !svc.PlanSnapshot().Outdated {
		t.Fatalf("failed replan: %d calls, want 2", got)
	}

	older := svc.beginReplan(context.Background(), "older")
	newer := svc.beginReplan(context.Background(), "newer")
	svc.finishReplan(older)
	if got := calls.Load(); got != 2 {
		t.Fatalf("a replaced request spoke: %d calls", got)
	}
	svc.finishReplan(newer)
	if got := calls.Load(); got != 3 {
		t.Fatalf("the newest request ending unpublished: %d calls, want 3", got)
	}
}
