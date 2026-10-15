package cloudwire

import "testing"

func TestShardHostOKStaysOnTheServicesDomain(t *testing.T) {
	for _, c := range []struct {
		base, host string
		ok         bool
	}{
		{"cloud.ftw.energy", "cloud.ftw.energy", true},
		{"cloud.ftw.energy", "s2.cloud.ftw.energy", true},
		{"cloud.ftw.energy", "eu-2.cloud.ftw.energy", true},
		{"cloud.ftw.energy", "a.b.cloud.ftw.energy", false},
		{"cloud.ftw.energy", "S2.cloud.ftw.energy", false},
		{"cloud.ftw.energy", "-s2.cloud.ftw.energy", false},
		{"cloud.ftw.energy", "s2-.cloud.ftw.energy", false},
		{"cloud.ftw.energy", ".cloud.ftw.energy", false},
		{"cloud.ftw.energy", "s2cloud.ftw.energy", false},
		{"cloud.ftw.energy", "evil.example", false},
		{"cloud.ftw.energy", "cloud.ftw.energy.evil.example", false},
		{"cloud.ftw.energy", "s2.cloud.ftw.energy:8443", false},
		{"cloud.ftw.energy", "s2.cloud.ftw.energy/v1/box", false},
		{"cloud.ftw.energy", "", false},
		{"127.0.0.1:8443", "127.0.0.1:8443", false},
		{"", "cloud.ftw.energy", false},
	} {
		if got := ShardHostOK(c.base, c.host); got != c.ok {
			t.Errorf("ShardHostOK(%q, %q) = %v, want %v", c.base, c.host, got, c.ok)
		}
	}
}

func TestRestartSpreadReadsTheReason(t *testing.T) {
	for reason, want := range map[string]int64{
		"2500":   2500,
		" 800 ":  800,
		"0":      0,
		"":       RestartSpreadDefaultMs,
		"soon":   RestartSpreadDefaultMs,
		"-1":     RestartSpreadDefaultMs,
		"999999": RestartSpreadMaxMs,
	} {
		if got := RestartSpreadMs(reason); got != want {
			t.Errorf("RestartSpreadMs(%q) = %d, want %d", reason, got, want)
		}
	}
}
