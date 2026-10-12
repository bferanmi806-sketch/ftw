package api

import (
	"context"
	"encoding/json"
	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/internal/telemetry"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestPartialHistoryAPIKeepsKnownFlowsAndNullsMissingValues(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.EnableHistoryAggregation(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Minute).UnixMilli()
	for i := 0; i < 4; i++ {
		p := state.HistoryPoint{TsMs: now + int64(i*10000), GridW: 1200, PVW: -300, Missing: state.MissingBattery | state.MissingLoad | state.MissingSoC, JSON: `{"drivers":{"meter":{"meter_w":1200},"battery":{"bat_w":null}}}`}
		if err := st.EnqueueTelemetryTick(&p, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.FlushHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := New(&Deps{State: st})
	rr := httptest.NewRecorder()
	srv.handleHistory(rr, httptest.NewRequest("GET", "/api/history?range=5m&points=1", nil))
	var h struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if len(h.Items) != 1 {
		t.Fatal(rr.Body.String())
	}
	p := h.Items[0]
	if p["grid_w"] != float64(1200) || p["pv_w"] != float64(-300) || p["bat_w"] != nil || p["bat_soc"] != nil || p["load_w"] != nil || p["history_quality"] != "incomplete" {
		t.Fatal(p)
	}
	rr = httptest.NewRecorder()
	srv.handleEnergyDaily(rr, httptest.NewRequest("GET", "/api/energy/daily?days=7", nil))
	var d struct {
		Days []map[string]any `json:"days"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	today := d.Days[len(d.Days)-1]
	if today["import_wh"] != float64(10) || today["pv_wh"] != float64(2.5) || today["load_wh"] != nil || today["bat_charged_wh"] != nil || today["history_quality"] != "incomplete" {
		t.Fatal(today)
	}
	if d.Days[0]["import_wh"] != nil {
		t.Fatal("empty day shown as measured zero", d.Days[0])
	}
}

func TestLiveHistoryQualitySeparatesOfflineFromInvalidBalance(t *testing.T) {
	tel := telemetry.NewStore()
	for _, name := range []string{"meter", "battery"} {
		tel.EnsureDriverHealth(name)
	}
	tel.Update("meter", telemetry.DerMeter, 1200, nil, nil)
	tel.RecordDriverSuccess("meter")
	tel.DriverHealthMut("battery").SetOffline()
	srv := New(&Deps{Tel: tel, HistoryMeasurementSources: func() telemetry.ForecastOptions {
		return telemetry.ForecastOptions{ExpectedFlows: []telemetry.ForecastFlow{{Driver: "battery", DerType: telemetry.DerBattery}}}
	}})
	resp := map[string]any{}
	srv.qualifyStatusHistory(resp, "meter")
	if resp["grid_w"] != float64(1200) || resp["bat_w"] != nil || resp["load_w"] != nil || resp["history_quality"] != "incomplete" {
		t.Fatal(resp)
	}
	tel.Update("battery", telemetry.DerBattery, 2000, nil, nil)
	tel.RecordDriverSuccess("battery")
	srv.qualifyStatusHistory(resp, "meter")
	if resp["bat_w"] != float64(2000) || resp["load_w"] != nil || resp["history_quality"] != "balance_mismatch" {
		t.Fatal(resp)
	}
	tel.Update("battery", telemetry.DerBattery, 0, nil, nil)
	tel.RecordDriverSuccess("battery")
	srv.qualifyStatusHistory(resp, "meter")
	if resp["bat_w"] != float64(0) || resp["load_w"] != float64(1200) || resp["history_quality"] != "complete" {
		t.Fatal(resp)
	}
}
