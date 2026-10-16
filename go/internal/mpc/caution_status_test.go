package mpc

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/state"
)

// A plan that falls back to the point forecast says so on the plan itself,
// not only in a warning string, and the next plan that keeps its caution
// clears it. A style that asks for no caution never reads as caution off.
func TestCautionOffIsPublishedAndClears(t *testing.T) {
	for _, tc := range []struct {
		name       string
		k          float64
		fault      string
		cautionOff bool
	}{
		{"caution kept", .3, "", false},
		{"caution infeasible", .3, "infeasible", true},
		{"caution inputs invalid", .3, "nan uncertainty", true},
		{"caution risk invalid", .3, "nan risk", true},
		{"no caution asked", 0, "infeasible", false},
		{"no caution asked with invalid inputs", 0, "nan uncertainty", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := shadowTestService(t)
			start := time.Now().UTC().Truncate(time.Hour)
			svc.now = func() time.Time { return start.Add(5 * time.Minute) }
			svc.FuseMaxW, svc.PVForecastSafetyK = 3000, tc.k
			fault := tc.fault
			svc.PVUncertaintyW = func() float64 {
				if fault == "nan uncertainty" {
					return math.NaN()
				}
				return 0
			}
			svc.ForecastSnapshot = func(time.Time, []state.ForecastPoint) ForecastInputs {
				return ForecastInputs{Load: func(time.Time) float64 { return 1000 },
					PVUncertaintyW: svc.PVUncertaintyW(),
					Risk: func(_, risk []Slot, _ float64) {
						for i := range risk {
							switch fault {
							case "infeasible":
								// More house load than the fuse and battery can serve.
								risk[i].LoadW = 9000
							case "nan risk":
								risk[i].LoadW = math.NaN()
							default:
								risk[i].LoadW = 1100
							}
						}
					}}
			}
			plan := svc.Replan(context.Background())
			if plan == nil || len(plan.Actions) != 4 {
				t.Fatalf("no full plan: %+v", svc.PlanSnapshot())
			}
			if plan.CautionOff != tc.cautionOff {
				t.Fatalf("caution off = %v, want %v; warnings %q", plan.CautionOff, tc.cautionOff, svc.PlanSnapshot().Warnings)
			}
			if d := svc.Diagnose(); d == nil || d.CautionOff != tc.cautionOff {
				t.Fatal("diagnostic lost the caution state")
			}
			if tc.cautionOff && !strings.Contains(strings.Join(svc.PlanSnapshot().Warnings, " "), "caution") {
				t.Fatalf("no warning says why: %q", svc.PlanSnapshot().Warnings)
			}

			fault = ""
			if plan := svc.Replan(context.Background()); plan == nil || plan.CautionOff {
				t.Fatalf("working caution did not clear the flag: %+v", plan)
			}
		})
	}
}

// A caution failure that repeats on every replan is one record and one write.
// A changed message is new evidence. A hard failure that returns after a
// recovered plan is current again rather than marked recovered.
func TestRepeatedPlanningFailureIsWrittenOnce(t *testing.T) {
	svc := shadowTestService(t)
	start := time.Now().UTC().Truncate(time.Hour)
	svc.now = func() time.Time { return start.Add(5 * time.Minute) }
	svc.PVForecastSafetyK = .3
	uncertainty := math.NaN()
	svc.PVUncertaintyW = func() float64 { return uncertainty }
	if svc.Replan(context.Background()) == nil {
		t.Fatal("no plan")
	}
	first := svc.PlanningFailureSnapshot()
	svc.mu.RLock()
	written := svc.persistedPlanningFailure
	svc.mu.RUnlock()
	if first == nil || first.Stage != "risk_inputs" || written == nil || first.RecoveredDecisionID == "" {
		t.Fatalf("caution failure not recorded and recovered: %+v", first)
	}
	if svc.Replan(context.Background()) == nil {
		t.Fatal("no plan")
	}
	svc.mu.RLock()
	same := svc.persistedPlanningFailure == written && svc.lastPlanningFailure == written
	svc.mu.RUnlock()
	if again := svc.PlanningFailureSnapshot(); !same || again.CapturedAtMs != first.CapturedAtMs {
		t.Fatal("a repeated failure was recorded and written again")
	}

	// NaN and -1 give the same message, so only another field is a change.
	uncertainty = -1
	if svc.Replan(context.Background()) == nil || svc.PlanningFailureSnapshot().Generation != first.Generation {
		t.Fatal("the same message was recorded again")
	}
	uncertainty = 0
	relative := math.NaN()
	svc.PVRelativeUncertainty = func() float64 { return relative }
	if svc.Replan(context.Background()) == nil {
		t.Fatal("no plan")
	}
	if changed := svc.PlanningFailureSnapshot(); changed.Error == first.Error || changed.Generation == first.Generation {
		t.Fatalf("a changed message kept the old evidence: %+v", changed)
	}

	// The same hard failure twice, with a recovered plan in between.
	relative = 0
	if err := svc.Store.SavePrices(nil); err != nil {
		t.Fatal(err)
	}
	svc.Zone = "SE4"
	svc.Replan(context.Background())
	hard := svc.PlanningFailureSnapshot()
	if hard == nil || hard.Stage != "inputs" || hard.RecoveredDecisionID != "" {
		t.Fatalf("hard failure not recorded: %+v", hard)
	}
	svc.Zone = "SE3"
	plan := svc.Replan(context.Background())
	if plan == nil || svc.PlanningFailureSnapshot().RecoveredDecisionID != plan.DecisionID {
		t.Fatal("recovery not marked")
	}
	svc.Zone = "SE4"
	svc.Replan(context.Background())
	if back := svc.PlanningFailureSnapshot(); back.Error != hard.Error || back.RecoveredDecisionID != "" {
		t.Fatalf("returning failure still reads as recovered: %+v", back)
	}
	svc.persistPlanningFailure()
	if !svc.PlanningFailureStorageSnapshot().Durable {
		t.Fatal("current failure was not written")
	}
}
