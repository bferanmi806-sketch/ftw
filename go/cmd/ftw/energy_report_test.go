package main

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/srcfl/ftw/go/internal/config"
	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

// The energy report has no question of its own. It sends only when the fleet
// report would, and never for a site a partner controls.
func TestTheEnergyReportTakesTheFleetReportsAnswer(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{}
	cfgMu := &sync.RWMutex{}
	energy, fleet := energyReportAllowed(cfg, cfgMu, st), projectReportsAllowed(cfg, cfgMu, st)
	check := func(name string, wantEnergy, wantFleet bool) {
		t.Helper()
		if energy() != wantEnergy || fleet() != wantFleet {
			t.Fatalf("%s: energy %v fleet %v, want %v %v", name, energy(), fleet(), wantEnergy, wantFleet)
		}
	}

	check("no answer yet", false, false)
	grant, _ := json.Marshal(map[string]any{"statsPolicy": cloudwire.StatsPolicy, "statsId": make([]byte, 16)})
	if err := st.SaveConfig("cloud/grant_v1", string(grant)); err != nil {
		t.Fatal(err)
	}
	check("the owner said yes", true, true)

	cfg.FleetPing = &config.FleetPing{Enabled: false}
	check("the daily report switched off", false, false)
	cfg.FleetPing.Enabled = true

	cfg.Nova = &config.Nova{Enabled: true}
	check("a partner controls the site", false, true)
	cfg.Nova = nil

	if err := st.SaveConfig("cloud/disabled_v1", "true"); err != nil {
		t.Fatal(err)
	}
	check("Cloud ended", false, false)
}
