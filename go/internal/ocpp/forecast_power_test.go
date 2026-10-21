package ocpp

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lorenzodonini/ocpp-go/ocpp1.6/core"
	types16 "github.com/lorenzodonini/ocpp-go/ocpp1.6/types"
	"github.com/lorenzodonini/ocpp-go/ocpp2.0.1/availability"
	"github.com/lorenzodonini/ocpp-go/ocpp2.0.1/meter"
	"github.com/lorenzodonini/ocpp-go/ocpp2.0.1/transactions"
	types201 "github.com/lorenzodonini/ocpp-go/ocpp2.0.1/types"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

type forecastOCPPFixture struct {
	h         *Handler
	tel       *telemetry.Store
	power     func(float64, time.Time)
	energy    func()
	status    func()
	available func()
	// unavailable and faulted leave power unknown; heartbeat is the charger's.
	unavailable func()
	faulted     func()
	heartbeat   func()
	stop        func()
	phases      func(l1, l2, l3 float64, at time.Time)
}

func newForecastOCPPFixture(t *testing.T, version string) forecastOCPPFixture {
	t.Helper()
	tel := telemetry.NewStore()
	h := NewHandler(tel, 60)
	h.SetApprovedIDs([]string{"charger"})
	h.OnConnect("charger")
	f := forecastOCPPFixture{h: h, tel: tel}
	if version == "1.6" {
		f.power = func(w float64, at time.Time) {
			h.OnMeterValues("charger", &core.MeterValuesRequest{ConnectorId: 1, MeterValue: []types16.MeterValue{{Timestamp: types16.NewDateTime(at), SampledValue: []types16.SampledValue{{Value: fmt.Sprint(w), Measurand: types16.MeasurandPowerActiveImport}}}}})
		}
		f.energy = func() {
			h.OnMeterValues("charger", &core.MeterValuesRequest{ConnectorId: 1, MeterValue: []types16.MeterValue{{Timestamp: types16.NewDateTime(time.Now()), SampledValue: []types16.SampledValue{{Value: "1234", Measurand: types16.MeasurandEnergyActiveImportRegister}}}}})
		}
		f.status = func() {
			h.OnStatusNotification("charger", &core.StatusNotificationRequest{ConnectorId: 1, Status: core.ChargePointStatusCharging})
		}
		f.available = func() {
			h.OnStatusNotification("charger", &core.StatusNotificationRequest{ConnectorId: 1, Status: core.ChargePointStatusAvailable})
		}
		f.unavailable = func() {
			h.OnStatusNotification("charger", &core.StatusNotificationRequest{ConnectorId: 1, Status: core.ChargePointStatusUnavailable})
		}
		f.faulted = func() {
			h.OnStatusNotification("charger", &core.StatusNotificationRequest{ConnectorId: 1, Status: core.ChargePointStatusFaulted, ErrorCode: core.GroundFailure})
		}
		f.heartbeat = func() { h.OnHeartbeat("charger", &core.HeartbeatRequest{}) }
		f.stop = func() { h.OnStopTransaction("charger", &core.StopTransactionRequest{}) }
		f.phases = func(l1, l2, l3 float64, at time.Time) {
			h.OnMeterValues("charger", &core.MeterValuesRequest{ConnectorId: 1, MeterValue: []types16.MeterValue{{Timestamp: types16.NewDateTime(at), SampledValue: []types16.SampledValue{
				{Value: fmt.Sprint(l1), Measurand: types16.MeasurandPowerActiveImport, Phase: types16.PhaseL1},
				{Value: fmt.Sprint(l2), Measurand: types16.MeasurandPowerActiveImport, Phase: types16.PhaseL2},
				{Value: fmt.Sprint(l3), Measurand: types16.MeasurandPowerActiveImport, Phase: types16.PhaseL3},
			}}}})
		}
	} else {
		v := &handlerV201{h}
		f.power = func(w float64, at time.Time) {
			v.OnMeterValues("charger", &meter.MeterValuesRequest{EvseID: 1, MeterValue: []types201.MeterValue{{Timestamp: *types201.NewDateTime(at), SampledValue: []types201.SampledValue{{Value: w, Measurand: types201.MeasurandPowerActiveImport}}}}})
		}
		f.energy = func() {
			v.OnMeterValues("charger", &meter.MeterValuesRequest{EvseID: 1, MeterValue: []types201.MeterValue{{Timestamp: *types201.NewDateTime(time.Now()), SampledValue: []types201.SampledValue{{Value: 1234, Measurand: types201.MeasurandEnergyActiveImportRegister}}}}})
		}
		f.status = func() {
			v.OnStatusNotification("charger", &availability.StatusNotificationRequest{EvseID: 1, ConnectorID: 1, ConnectorStatus: availability.ConnectorStatusOccupied})
		}
		f.available = func() {
			v.OnStatusNotification("charger", &availability.StatusNotificationRequest{EvseID: 1, ConnectorID: 1, ConnectorStatus: availability.ConnectorStatusAvailable})
		}
		f.unavailable = func() {
			v.OnStatusNotification("charger", &availability.StatusNotificationRequest{EvseID: 1, ConnectorID: 1, ConnectorStatus: availability.ConnectorStatusUnavailable})
		}
		f.faulted = func() {
			v.OnStatusNotification("charger", &availability.StatusNotificationRequest{EvseID: 1, ConnectorID: 1, ConnectorStatus: availability.ConnectorStatusFaulted})
		}
		f.heartbeat = func() { v.OnHeartbeat("charger", &availability.HeartbeatRequest{}) }
		f.stop = func() {
			v.OnTransactionEvent("charger", &transactions.TransactionEventRequest{EventType: transactions.TransactionEventEnded})
		}
		f.phases = func(l1, l2, l3 float64, at time.Time) {
			v.OnMeterValues("charger", &meter.MeterValuesRequest{EvseID: 1, MeterValue: []types201.MeterValue{{Timestamp: *types201.NewDateTime(at), SampledValue: []types201.SampledValue{
				{Value: l1, Measurand: types201.MeasurandPowerActiveImport, Phase: types201.PhaseL1},
				{Value: l2, Measurand: types201.MeasurandPowerActiveImport, Phase: types201.PhaseL2},
				{Value: l3, Measurand: types201.MeasurandPowerActiveImport, Phase: types201.PhaseL3},
			}}}})
		}
	}
	return f
}
func (f forecastOCPPFixture) reading() telemetry.ForecastReading {
	f.tel.Update("site", telemetry.DerMeter, 5000, nil, nil)
	f.tel.RecordDriverSuccess("site")
	f.tel.Update("pv", telemetry.DerPV, -1000, nil, nil)
	f.tel.RecordDriverSuccess("pv")
	return f.tel.ForecastMeasurement(time.Now(), "site", telemetry.ForecastOptions{ExpectedFlows: []telemetry.ForecastFlow{{Driver: "charger", DerType: telemetry.DerEV}}, MaxSkew: time.Minute})
}

func TestForecastOCPPRequiresRealPowerInBothVersions(t *testing.T) {
	for _, version := range []string{"1.6", "2.0.1"} {
		t.Run(version, func(t *testing.T) {
			f := newForecastOCPPFixture(t, version)
			for _, stage := range []func(){func() {}, f.status, f.energy} {
				stage()
				r := f.reading()
				if r.Valid || !r.PVValid || !strings.HasPrefix(r.Reason, "unknown_power:charger:") {
					t.Fatalf("status/energy-only became measured zero: %+v", r)
				}
			}
			at := time.Now()
			f.power(0, at)
			r := f.reading()
			if !r.Valid || r.EVW != 0 || r.HouseholdW != 6000 {
				t.Fatalf("measured zero rejected: %+v", r)
			}
			f.stop()
			r = f.reading()
			if r.Valid || !r.PVValid {
				t.Fatalf("synthetic stop zero became a measured sample: %+v", r)
			}
			// Replaying a pre-stop sample must not revive a synthetic zero.
			f.power(0, at)
			if r = f.reading(); r.Valid {
				t.Fatal("duplicate timestamp revived stopped power")
			}
			f.h.OnDisconnect("charger")
			f.h.OnConnect("charger")
			f.status()
			if r = f.reading(); r.Valid {
				t.Fatal("reconnect reused prior-socket power")
			}
			time.Sleep(2 * time.Millisecond) // next distinct source timestamp
			f.power(0, time.Now())
			if r = f.reading(); !r.Valid {
				t.Fatalf("new socket's real zero rejected: %+v", r)
			}
		})
	}
}

func TestForecastOCPPStatusCannotRefreshOldPower(t *testing.T) {
	for _, version := range []string{"1.6", "2.0.1"} {
		t.Run(version, func(t *testing.T) {
			f := newForecastOCPPFixture(t, version)
			// The connection predates the delayed source sample; receipt is fresh.
			f.h.mu.Lock()
			f.h.chargers["charger"].powerConnectedAt = time.Now().Add(-time.Hour)
			f.h.mu.Unlock()
			f.power(700, time.Now().Add(-2*time.Minute))
			f.status()
			f.energy()
			r := f.reading()
			if r.Valid || !r.PVValid || !strings.HasPrefix(r.Reason, "stale:charger:") {
				t.Fatalf("later status/energy refreshed old power: %+v", r)
			}
		})
	}
}

func TestForecastOCPPRejectsFutureAndOutOfOrderSamples(t *testing.T) {
	for _, version := range []string{"1.6", "2.0.1"} {
		t.Run(version, func(t *testing.T) {
			f := newForecastOCPPFixture(t, version)
			f.power(900, time.Now().Add(time.Hour))
			if f.reading().Valid {
				t.Fatal("future source timestamp accepted")
			}
			at := time.Now()
			f.power(700, at)
			f.power(9000, at.Add(-time.Second))
			r := f.reading()
			if !r.Valid || r.EVW != 700 {
				t.Fatalf("out-of-order power replaced accepted measurement: %+v", r)
			}
			if raw := f.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 700 {
				t.Fatalf("out-of-order power replaced dispatch lastPowerW: %+v", raw)
			}
		})
	}
}

func TestForecastOCPP201TransactionPowerAndMissingPower(t *testing.T) {
	f := newForecastOCPPFixture(t, "2.0.1")
	v := &handlerV201{f.h}
	v.OnTransactionEvent("charger", &transactions.TransactionEventRequest{EventType: transactions.TransactionEventUpdated})
	if f.reading().Valid {
		t.Fatal("power-free transaction event became zero measurement")
	}
	at := time.Now()
	f.power(700, at)
	v.OnTransactionEvent("charger", &transactions.TransactionEventRequest{EventType: transactions.TransactionEventUpdated, MeterValue: []types201.MeterValue{{Timestamp: *types201.NewDateTime(at), SampledValue: []types201.SampledValue{
		{Value: 16, Measurand: types201.MeasurandCurrentImport},
		{Value: 1234, Measurand: types201.MeasurandEnergyActiveImportRegister},
	}}}})
	if raw := f.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 700 {
		t.Fatalf("energy-only transaction event wrote lastPowerW=%v", raw)
	}
	time.Sleep(2 * time.Millisecond)
	v.OnTransactionEvent("charger", &transactions.TransactionEventRequest{EventType: transactions.TransactionEventUpdated, MeterValue: []types201.MeterValue{{Timestamp: *types201.NewDateTime(time.Now()), SampledValue: []types201.SampledValue{{Value: 0, Measurand: types201.MeasurandPowerActiveImport}}}}})
	if r := f.reading(); !r.Valid || r.EVW != 0 {
		t.Fatalf("real transaction zero rejected: %+v", r)
	}
}

func TestOCPPLastPowerSharedAcceptRule(t *testing.T) {
	for _, version := range []string{"1.6", "2.0.1"} {
		t.Run(version, func(t *testing.T) {
			f := newForecastOCPPFixture(t, version)
			at := time.Now()
			f.power(700, at)
			f.energy()
			if raw := f.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 700 {
				t.Fatalf("energy-only sample overwrote last power: %+v", raw)
			}
			f.power(-50, at.Add(time.Millisecond))
			if raw := f.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 700 {
				t.Fatalf("negative import published: %+v", raw)
			}
			f.phases(300, 250, 150, at)
			if raw := f.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 700 {
				t.Fatalf("phased samples overwrote accepted total: %+v", raw)
			}
			f.available()
			if raw := f.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 0 {
				t.Fatalf("Available left lastPowerW set: %+v", raw)
			}
			if r := f.reading(); !r.Valid || r.EVW != 0 {
				t.Fatalf("Available is not a measured zero: %+v", r)
			}

			f2 := newForecastOCPPFixture(t, version)
			f2.phases(300, 250, 150, time.Now())
			if raw := f2.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 700 {
				t.Fatalf("phase-only samples not summed: %+v", raw)
			}

			f3 := newForecastOCPPFixture(t, version)
			f3.power(700, time.Now())
			f3.h.OnDisconnect("charger")
			if raw := f3.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 0 {
				t.Fatalf("disconnect left lastPowerW set: %+v", raw)
			}
		})
	}
}

func TestMeterPowerWPrefersUnphasedThenSum(t *testing.T) {
	w, ok := meterPowerW([]powerSample{{w: 700}, {w: 300, phase: "L1"}})
	if !ok || w != 700 {
		t.Fatalf("unphased+phase: got %v ok=%v", w, ok)
	}
	w, ok = meterPowerW([]powerSample{{w: 300, phase: "L1"}, {w: 250, phase: "L2"}, {w: 150, phase: "L3"}})
	if !ok || w != 700 {
		t.Fatalf("phase sum: got %v ok=%v", w, ok)
	}
	if _, ok := meterPowerW([]powerSample{{w: -1}, {w: math.NaN(), phase: "L1"}}); ok {
		t.Fatal("negative/NaN samples accepted")
	}
}

// live is the balance every client shows: the site meter, PV and the charger.
func (f forecastOCPPFixture) live() telemetry.ForecastReading {
	f.tel.Update("site", telemetry.DerMeter, 5000, nil, nil)
	f.tel.RecordDriverSuccess("site")
	f.tel.Update("pv", telemetry.DerPV, -1000, nil, nil)
	f.tel.RecordDriverSuccess("pv")
	return f.tel.LiveMeasurementNow("site", telemetry.ForecastOptions{ExpectedFlows: []telemetry.ForecastFlow{{Driver: "charger", DerType: telemetry.DerEV}}})
}

// age moves the published charger reading back, as if no message came for d.
func (f forecastOCPPFixture) age(d time.Duration) {
	f.h.mu.Lock()
	s := f.h.chargers["charger"]
	s.forecastPower.MeasuredAtMS -= d.Milliseconds()
	s.forecastPower.ReceivedAtMS -= d.Milliseconds()
	f.h.mu.Unlock()
	f.h.pushReading("charger", s)
}

func (f forecastOCPPFixture) wantMeasuredZero(t *testing.T, what string) {
	t.Helper()
	if r := f.live(); r.LoadQuality != telemetry.LoadMeasured || r.EVW != 0 || r.LoadW != 6000 {
		t.Fatalf("%s: live Home use %s (%s), EV %v W, load %v W", what, r.LoadQuality, r.LoadReason, r.EVW, r.LoadW)
	}
	if r := f.reading(); !r.Valid || !r.Known[telemetry.DerEV] || r.EVW != 0 {
		t.Fatalf("%s: forecast balance %+v", what, r)
	}
	if raw := f.tel.Get("charger", telemetry.DerEV); raw == nil || raw.RawW != 0 {
		t.Fatalf("%s: dispatch power %+v", what, raw)
	}
}

func TestOCPPAvailableIsAMeasuredZeroInBothVersions(t *testing.T) {
	for _, version := range []string{"1.6", "2.0.1"} {
		t.Run(version, func(t *testing.T) {
			f := newForecastOCPPFixture(t, version)
			f.available()
			f.wantMeasuredZero(t, "Available after boot")

			// No message for two minutes: the zero is as old as its evidence.
			f.age(2 * time.Minute)
			if r := f.live(); r.LoadQuality == telemetry.LoadMeasured || !strings.HasSuffix(r.LoadReason, ":charger:ev") {
				t.Fatalf("an old Available stayed current: %s (%s)", r.LoadQuality, r.LoadReason)
			}
			f.heartbeat()
			f.wantMeasuredZero(t, "heartbeat")

			// A car arrives. The zero is no longer held and ages out.
			f.status()
			f.age(2 * time.Minute)
			f.heartbeat()
			if r := f.live(); r.LoadQuality == telemetry.LoadMeasured {
				t.Fatalf("a heartbeat kept the zero after the car arrived: %s", r.LoadReason)
			}
			f.available()
			f.wantMeasuredZero(t, "Available again")

			// A new socket says nothing about the connector until it reports.
			f.h.OnDisconnect("charger")
			f.h.OnConnect("charger")
			f.heartbeat()
			if r := f.live(); r.LoadQuality == telemetry.LoadMeasured {
				t.Fatalf("a reconnect kept the zero before any status: %s", r.LoadReason)
			}
		})
	}
}

func TestOCPPAvailableAfterSessionIsAMeasuredZero(t *testing.T) {
	for _, version := range []string{"1.6", "2.0.1"} {
		for _, order := range []string{"stop first", "Available first"} {
			t.Run(version+" "+order, func(t *testing.T) {
				f := newForecastOCPPFixture(t, version)
				f.status()
				f.power(3000, time.Now())
				if r := f.live(); r.LoadQuality != telemetry.LoadMeasured || r.EVW != 3000 || r.LoadW != 3000 {
					t.Fatalf("charging with MeterValues: %s (%s), EV %v W, load %v W", r.LoadQuality, r.LoadReason, r.EVW, r.LoadW)
				}
				if r := f.reading(); !r.Valid || r.EVW != 3000 {
					t.Fatalf("charging forecast balance: %+v", r)
				}
				lastCharge := time.Now()
				time.Sleep(2 * time.Millisecond)
				if order == "stop first" {
					f.stop()
					f.available()
				} else {
					f.available()
					f.stop()
				}
				f.wantMeasuredZero(t, "after the session")

				// A meter sample from before the car left cannot bring its draw back.
				f.power(3000, lastCharge.Add(time.Millisecond))
				f.wantMeasuredZero(t, "late session sample")
				f.heartbeat()
				f.wantMeasuredZero(t, "heartbeat after the session")
			})
		}
	}
}

func TestOCPPUnavailableAndFaultedStayUnknown(t *testing.T) {
	for _, version := range []string{"1.6", "2.0.1"} {
		for name, status := range map[string]func(forecastOCPPFixture){
			"Unavailable": func(f forecastOCPPFixture) { f.unavailable() },
			"Faulted":     func(f forecastOCPPFixture) { f.faulted() },
		} {
			t.Run(version+" "+name, func(t *testing.T) {
				f := newForecastOCPPFixture(t, version)
				f.available()
				status(f)
				f.heartbeat()
				r := f.live()
				if r.LoadQuality != telemetry.LoadEstimated || !strings.HasPrefix(r.LoadReason, "unknown_power:charger:") {
					t.Fatalf("%s became known power: %s (%s)", name, r.LoadQuality, r.LoadReason)
				}
				if r := f.reading(); r.Valid || !strings.HasPrefix(r.Reason, "unknown_power:charger:") {
					t.Fatalf("%s became a measured balance: %+v", name, r)
				}
			})
		}
	}
}
