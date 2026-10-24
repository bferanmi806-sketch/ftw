package reportwire

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/srcfl/ftw/go/protocol/energywire"
	"github.com/srcfl/ftw/go/protocol/fleetwire"
	"github.com/srcfl/ftw/go/protocol/ohttp"
)

// The largest report of either kind fills the same padded request, so the
// relay cannot tell a fleet report from an energy report, or one box's from
// another's, by its length.
func TestEveryReportHasOneLength(t *testing.T) {
	drivers := make([]string, 16)
	for i := range drivers {
		drivers[i] = strings.Repeat(string(rune('a'+i)), 64)
	}
	fleet, _ := json.Marshal(fleetwire.Report{Schema: fleetwire.Schema, Version: "v1234.1234.1234-beta.1234", Channel: "unknown",
		Drivers: drivers, Battery: "30+", Zone: strings.Repeat("Z", 16), Age: "unknown"})
	energy, _ := json.Marshal(energywire.Report{Schema: energywire.Schema, Date: "2026-10-23", PVGeneration: 1000, GridImport: 1000,
		GridExport: 1000, BatteryCharge: 1000, BatteryDischarge: 1000, VehicleCharge: 1000})
	small, _ := json.Marshal(energywire.Report{Schema: energywire.Schema, Date: "2026-10-23"})
	for _, body := range [][]byte{fleet, energy, small} {
		msg, ok := Request(body)
		if !ok || len(msg) != RequestSize {
			t.Fatalf("%d-byte report padded to %d", len(body), len(msg))
		}
		got, ok := Body(msg)
		if !ok || string(got) != string(body) || Schema(got) == "" {
			t.Fatalf("read back %q", got)
		}
	}
	if Schema(fleet) != fleetwire.Schema || Schema(energy) != energywire.Schema || Schema([]byte("x")) != "" {
		t.Fatal("schema")
	}
	if _, ok := Request(make([]byte, RequestSize)); ok {
		t.Fatal("an oversized report was padded")
	}
	if len(Response(204)) != ResponseSize {
		t.Fatal("response padding")
	}
}

func TestOnlyTheOneInnerRequestIsRead(t *testing.T) {
	good := ohttp.Request{Method: Method, Scheme: Scheme, Authority: Authority, Path: Path, Content: []byte("{}")}
	other := func(f func(*ohttp.Request)) []byte {
		r := good
		f(&r)
		return r.Marshal(RequestSize)
	}
	for name, msg := range map[string][]byte{
		"another path":   other(func(r *ohttp.Request) { r.Path = "/v1/energy/report" }),
		"another method": other(func(r *ohttp.Request) { r.Method = "PUT" }),
		"another host":   other(func(r *ohttp.Request) { r.Authority = "example.com" }),
		"plain http":     other(func(r *ohttp.Request) { r.Scheme = "http" }),
		"a header":       other(func(r *ohttp.Request) { r.Fields = []ohttp.Field{{Name: "x-home", Value: "1"}} }),
		"not padded":     good.Marshal(0),
		"padded more":    good.Marshal(RequestSize + 1),
		"a response":     Response(200),
	} {
		if _, ok := Body(msg); ok {
			t.Fatalf("%s was read", name)
		}
	}
}
