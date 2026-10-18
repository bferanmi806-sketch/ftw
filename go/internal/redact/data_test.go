package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticDataKeepsEvidenceAndScrubsIdentity(t *testing.T) {
	r := New([]byte("box-agent-key"), []string{"Garage meter"})
	var doc any
	input := `{"asset_id":"hardware:secret-device","label":"Garage meter","source":"hardware_counter","quality":"measured","provenance":"counter","energy_wh":12.25,"sample_count":7,"at":"2026-10-18T15:00:00Z","first_seen_ms":1792328400000,"missing":null,"driver":"Garage meter","VIN":"ABC12345678901234","apiToken":"do-not-publish","endpoint":"http://192.168.1.7","input_json":"{\"power_w\":1234,\"password\":\"secret\"}"}`
	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	got := r.Data(doc).(map[string]any)
	if got["energy_wh"] != json.Number("12.25") || got["sample_count"] != json.Number("7") || got["first_seen_ms"] != json.Number("1792328400000") || got["at"] != "2026-10-18T15:00:00Z" || got["missing"] != nil {
		t.Fatalf("evidence changed: %+v", got)
	}
	if got["asset_id"] != r.ID("hardware:secret-device") || got["driver"] != r.ID("Garage meter") || got["source"] != "hardware_counter" || got["provenance"] != "counter" {
		t.Fatalf("identity or provenance: %+v", got)
	}
	body, _ := json.Marshal(got)
	for _, secret := range []string{"hardware:secret-device", "Garage meter", "ABC12345678901234", "do-not-publish", "192.168.1.7", "password", "secret"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("diagnostic answer contains %q: %s", secret, body)
		}
	}
	if !strings.Contains(got["input_json"].(string), `"power_w":1234`) {
		t.Fatalf("nested planner input lost its values: %s", body)
	}
	private := r.Data(map[string]any{"error": "read /private/house/config.yaml: failed", "192.168.2.7": 12, "clock": map[string]any{"source": "ntp"}, "SN": 123456789, "VIN": 987654321, "IPAddress": "10.0.0.1"}).(map[string]any)
	encoded, _ := json.Marshal(private)
	if strings.Contains(string(encoded), "/private") || strings.Contains(string(encoded), "192.168.2.7") || strings.Contains(string(encoded), "123456789") || strings.Contains(string(encoded), "987654321") || strings.Contains(string(encoded), "10.0.0.1") || private["clock"].(map[string]any)["source"] != "ntp" {
		t.Fatalf("network identity, path or clock source: %s", encoded)
	}
	metrics := r.Data(map[string]any{"metrics": []any{"pv_mppt1_w", "battery_2_soc"}, "metric": "hp_phase1_current_a"}).(map[string]any)
	if metrics["metric"] != "hp_phase1_current_a" || metrics["metrics"].([]any)[0] != "pv_mppt1_w" {
		t.Fatalf("metric names can no longer be queried: %+v", metrics)
	}
}
