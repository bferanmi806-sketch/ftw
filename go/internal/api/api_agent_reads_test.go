package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/redact"
	"github.com/srcfl/ftw/go/protocol"
)

func agentReadRequest(path string, scopes ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	return r.WithContext(apiauth.WithCaller(r.Context(), apiauth.Caller{Kind: apiauth.KindAgent, Role: apiauth.RoleAgent, Subject: "agent:test", Scopes: apiauth.NewScopeSet(scopes...)}))
}

func TestAgentReadsTheEnergyLedgerByItsOpaqueAssetID(t *testing.T) {
	st, base, asset := seedAPIEnergyHistory(t)
	t.Cleanup(func() { _ = st.Close() })
	srv := New(&Deps{State: st})
	rd := redact.New([]byte("agent-key"), []string{"Site meter"})
	catalog := httptest.NewRecorder()
	srv.ServeAgentRead(catalog, agentReadRequest("/api/energy/assets", protocol.ScopeDiagRun, protocol.ScopeHistoryRead), rd)
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), rd.ID(asset)) || strings.Contains(catalog.Body.String(), "Site meter") || strings.Contains(catalog.Body.String(), "maker:api-meter") {
		t.Fatalf("asset catalogue: %d %s", catalog.Code, catalog.Body.String())
	}
	q := url.Values{"scope": {"asset"}, "asset_id": {rd.ID(asset)}, "since": {formatInt64(base)}, "until": {formatInt64(base + 300_000)}, "bucket": {"5m"}}
	answer := httptest.NewRecorder()
	srv.ServeAgentRead(answer, agentReadRequest("/api/energy/history?"+q.Encode(), protocol.ScopeDiagRun, protocol.ScopeHistoryRead), rd)
	var body struct {
		Points []struct {
			AssetID    string  `json:"asset_id"`
			Energy     float64 `json:"energy_wh"`
			Quality    string  `json:"quality"`
			Source     string  `json:"source"`
			Provenance string  `json:"provenance"`
		} `json:"points"`
	}
	if answer.Code != http.StatusOK || json.Unmarshal(answer.Body.Bytes(), &body) != nil || len(body.Points) == 0 {
		t.Fatalf("energy ledger: %d %s", answer.Code, answer.Body.String())
	}
	var measured float64
	for _, p := range body.Points {
		if p.AssetID != rd.ID(asset) {
			t.Fatal("ledger exposed another id")
		}
		if p.Quality == "measured" {
			measured += p.Energy
			if p.Source != "hardware_counter" || p.Provenance != "counter" {
				t.Fatalf("measurement provenance changed: %+v", p)
			}
		}
	}
	if measured != 12 {
		t.Fatalf("measured energy = %v, want 12 Wh", measured)
	}
	q.Set("asset_id", asset)
	refused := httptest.NewRecorder()
	srv.ServeAgentRead(refused, agentReadRequest("/api/energy/history?"+q.Encode(), protocol.ScopeDiagRun, protocol.ScopeHistoryRead), rd)
	if refused.Code != http.StatusNotFound {
		t.Fatalf("raw asset id accepted: %d", refused.Code)
	}
}

func TestDiagnosticReadsRequireBothScopesAndTheReadTier(t *testing.T) {
	srv := New(&Deps{})
	rd := redact.New([]byte("agent-key"), nil)
	for _, tc := range []struct {
		path   string
		scopes []string
	}{
		{"/api/energy/history", []string{protocol.ScopeDiagRun}},
		{"/api/energy/history", []string{protocol.ScopeHistoryRead}},
		{"/api/config", []string{protocol.ScopeDiagRun, protocol.ScopeConfigRead}},
		{"/api/scan", []string{protocol.ScopeDiagRun, protocol.ScopeHealthRead}},
		{"/api/oauth/myuplink/start", []string{protocol.ScopeDiagRun, protocol.ScopeConfigRead}},
	} {
		answer := httptest.NewRecorder()
		srv.ServeAgentRead(answer, agentReadRequest(tc.path, tc.scopes...), rd)
		if answer.Code != http.StatusForbidden {
			t.Errorf("%s: %d", tc.path, answer.Code)
		}
	}
	for _, route := range protocol.AgentAPIRoutes {
		facts := srv.Route(httptest.NewRequest(http.MethodGet, route.Path, nil))
		if facts.Tier != apiauth.TierRead || facts.Static {
			t.Errorf("catalogue entry %s is not a Read route: %+v", route.Path, facts)
		}
	}
	answer := httptest.NewRecorder()
	srv.ServeAgentRead(answer, agentReadRequest("/api/series?format=csv", protocol.ScopeDiagRun, protocol.ScopeHistoryRead), rd)
	if answer.Code != http.StatusBadRequest {
		t.Fatal("an unlisted query parameter was accepted")
	}
}

func TestDiagnosticAPIDoesNotPublishAnOversizedAnswer(t *testing.T) {
	srv := New(&Deps{})
	srv.mux = http.NewServeMux()
	srv.marks = make(map[string]routeMark)
	srv.handle("GET /api/status", Read, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"` + strings.Repeat("x", protocol.AgentAPIMaxBytes) + `"}`))
	})
	answer := httptest.NewRecorder()
	srv.ServeAgentRead(answer, agentReadRequest("/api/status", protocol.ScopeDiagRun, protocol.ScopeLiveRead), redact.New([]byte("agent-key"), nil))
	if answer.Code != http.StatusRequestEntityTooLarge || strings.Contains(answer.Body.String(), "xxxxxxxx") {
		t.Fatalf("oversized read: %d %s", answer.Code, answer.Body.String())
	}
}
