package api

import (
	"context"
	"fmt"
	"math"
	"testing"
	"testing/synctest"
	"time"

	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

// Field report, 22 Oct 2026: a three-phase plan order an Easee served on L1
// at 3.13 kW all night. The loadpoint controller sees it from the site
// meter, and the verdict names the phase and its current instead of a
// charger that does not follow.
func TestThreePhaseOrderDrawnOnOnePhaseIsLimited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tel := telemetry.NewStore()
		tel.DriverHealthMut("easee")
		var steps []float64
		for a := 0.0; a <= 16; a++ {
			if a == 0 || a >= 6 {
				steps = append(steps, a*690)
			}
		}
		mgr := loadpoint.NewManager()
		mgr.Load([]loadpoint.Config{{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11040, AllowedStepsW: steps}})
		slot := time.Now()
		plan := func(time.Time) (loadpoint.Directive, bool) {
			return loadpoint.Directive{SlotStart: slot, SlotEnd: slot.Add(time.Hour), LoadpointEnergyWh: map[string]float64{"garage": 7590}}, true
		}
		sample := func(string) (loadpoint.EVSample, bool) {
			return loadpoint.EVSample{Connected: true, RequestActive: true, PowerW: 3130, PowerAt: time.Now()}, true
		}
		send := func(_ context.Context, driver string, payload []byte) error {
			c := tel.BeginCommand(driver, payload, time.Now())
			tel.CompleteCommand(c, "accepted")
			return nil
		}
		ctrl := loadpoint.NewController(mgr, plan, sample, send)
		ctrl.SetSiteFuse(loadpoint.SiteFuse{MaxAmps: 25, Voltage: 230, PhaseCnt: 3})
		ctrl.SetSitePhases(func() (loadpoint.SitePhases, bool) {
			return loadpoint.SitePhases{A: [3]float64{14.4, .75, .75}, At: time.Now()}, true
		})
		srv := New(&Deps{Tel: tel, Loadpoints: mgr})
		changedAt := time.Now()
		var f ControlFeedback
		for range 6 * 60 / 5 {
			ctrl.Tick(context.Background(), time.Now())
			tel.Update("easee", telemetry.DerEV, 3130, nil, []byte(fmt.Sprintf(
				`{"connected":true,"control_power_observed_at":%q,"control_power_confirmed":true,"power_max_age_s":180}`,
				changedAt.Format(time.RFC3339Nano))))
			time.Sleep(5 * time.Second)
			f = srv.controlFeedback(time.Now())[0]
		}
		if f.Status != "limited" || f.Reason != "one_phase" || f.Severity != "warning" || f.ChargingPhase != "L1" ||
			f.ChargingPhaseA == nil || math.Abs(*f.ChargingPhaseA-13.6) > .1 {
			a := math.NaN()
			if f.ChargingPhaseA != nil {
				a = *f.ChargingPhaseA
			}
			t.Fatalf("3.13 kW on L1 under a %.0f W order reads %s/%s/%s, phase %q at %.1f A", *f.SentW, f.Status, f.Reason, f.Severity, f.ChargingPhase, a)
		}
	})
}

// One phase never hides a charger that draws more than it is told.
func TestOnePhaseDoesNotExplainADrawAboveTheOrder(t *testing.T) {
	sent, actual := 0.0, 3130.0
	f := ControlFeedback{Kind: "ev", SentW: &sent, ActualW: &actual, ChargingPhase: "L1", Response: "device_reported"}
	cmd := telemetry.CommandEvidence{Result: "accepted"}
	connected := true
	classifyControlFeedback(&f, cmd, true, true, "fuse_limit", &connected, time.Now())
	if f.Reason == "one_phase" {
		t.Fatalf("a draw against a stop read as one phase: %+v", f.Reason)
	}
}
