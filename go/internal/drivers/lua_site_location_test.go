package drivers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/srcfl/ftw/go/internal/telemetry"
)

// host.site_location gives the site's coordinates to a vehicle driver only,
// and nil when the site has none.
func TestLuaSiteLocationIsForVehicleDriversOnly(t *testing.T) {
	for _, tc := range []struct {
		name, caps string
		set, ok    bool
		want       string
	}{
		{"vehicle", `{ "vehicle" }`, true, true, "59.5,17.25"},
		{"vehicle on a site with no position", `{ "vehicle" }`, true, false, "nil"},
		{"vehicle on a host without it", `{ "vehicle" }`, false, false, "nil"},
		{"a charger", `{ "ev" }`, true, true, "nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := `DRIVER = { capabilities = ` + tc.caps + ` }
function driver_init(config) end
function driver_poll()
  local lat, lon = host.site_location()
  if lat == nil then SITE = "nil" else SITE = lat .. "," .. lon end
end
function driver_default_mode() end`
			path := filepath.Join(t.TempDir(), "car.lua")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			env := NewHostEnv("car", telemetry.NewStore())
			if tc.set {
				env.SiteLocation = func() (float64, float64, bool) { return 59.5, 17.25, tc.ok }
			}
			d, err := NewLuaDriver(path, env)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Cleanup()
			if err := d.Init(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := d.L.GetGlobal("SITE").String(); got != tc.want {
				t.Fatalf("site_location %s, want %s", got, tc.want)
			}
		})
	}
}
