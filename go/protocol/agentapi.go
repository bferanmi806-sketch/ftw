package protocol

// APIRead carries a read from the diagnostic catalogue. It has no method,
// body, headers or step-up field. api.req remains closed to agents.
// The answer uses APIHeadMsg, APIChunk and APIEnd under the request id.
type APIRead struct {
	Path     string            `cbor:"path"`
	Query    map[string]string `cbor:"query,omitempty"`
	MaxBytes int64             `cbor:"maxBytes,omitempty"`
}

func DecodeAPIRead(body []byte) (APIRead, error) {
	var read APIRead
	err := strictDec.Unmarshal(body, &read)
	return read, err
}

const AgentAPIMaxBytes = 1 << 20

// AgentAPIRoute is a diagnostic read. The box checks both the api.read
// scope and this route's scope, and its HTTP router must still call it Read.
type AgentAPIRoute struct {
	Path        string            `json:"path"`
	Scope       string            `json:"scope"`
	Description string            `json:"description"`
	Query       map[string]string `json:"query,omitempty"`
}

// AgentAPIRoutes is the catalogue the box serves and MCP lists. Paths are
// exact; query keys not listed here are refused. Responses stay JSON.
var AgentAPIRoutes = []AgentAPIRoute{
	{Path: "/api/status", Scope: ScopeLiveRead, Description: "Current measurements, control state and freshness"},
	{Path: "/api/health", Scope: ScopeHealthRead, Description: "Box health"},
	{Path: "/api/system/info", Scope: ScopeHealthRead, Description: "Runtime and host diagnostics, with identifying fields removed"},
	{Path: "/api/storage/inventory", Scope: ScopeHealthRead, Description: "Storage use and retention"},
	{Path: "/api/components", Scope: ScopeHealthRead, Description: "Component versions and state"},
	{Path: "/api/components/history", Scope: ScopeHealthRead, Description: "Component changes"},
	{Path: "/api/drivers", Scope: ScopeHealthRead, Description: "Driver diagnostics by opaque id"},
	{Path: "/api/data-sources", Scope: ScopeHealthRead, Description: "Available data sources and freshness"},
	{Path: "/api/battery_models", Scope: ScopePlanRead, Description: "Learned battery model and efficiency"},
	{Path: "/api/loadmodel", Scope: ScopePlanRead, Description: "Load forecast model and learning"},
	{Path: "/api/pvmodel", Scope: ScopePlanRead, Description: "Solar forecast model and learning"},
	{Path: "/api/forecast", Scope: ScopePlanRead, Description: "Load and solar forecasts", Query: map[string]string{"range": "Time ahead, such as 24h"}},
	{Path: "/api/prices", Scope: ScopePlanRead, Description: "Stored import and export prices", Query: map[string]string{"range": "Time ahead, such as 24h", "since_ms": "Unix milliseconds", "until_ms": "Unix milliseconds"}},
	{Path: "/api/planner/prefs", Scope: ScopeConfigRead, Description: "Planner preferences"},
	{Path: "/api/mpc/plan", Scope: ScopePlanRead, Description: "Full current plan and per-asset slots"},
	{Path: "/api/mpc/diagnose", Scope: ScopePlanRead, Description: "Current planner inputs, constraints and result"},
	{Path: "/api/mpc/diagnose/history", Scope: ScopePlanRead, Description: "Recorded planning decisions", Query: map[string]string{"since": "Unix milliseconds", "until": "Unix milliseconds", "limit": "Maximum snapshots, up to 5000"}},
	{Path: "/api/mpc/diagnose/at", Scope: ScopePlanRead, Description: "The planning snapshot active at an incident", Query: map[string]string{"ts": "Required incident time, Unix milliseconds"}},
	{Path: "/api/history", Scope: ScopeHistoryRead, Description: "Recorded site measurements", Query: map[string]string{"range": "Time back, such as 1h", "points": "Maximum rows"}},
	{Path: "/api/energy/daily", Scope: ScopeHistoryRead, Description: "Daily measured energy and costs", Query: map[string]string{"days": "Number of days"}},
	{Path: "/api/energy/assets", Scope: ScopeHistoryRead, Description: "Energy-ledger assets by opaque id"},
	{Path: "/api/energy/history", Scope: ScopeHistoryRead, Description: "Energy-ledger entries in Wh, including flow, source, quality, provenance and sample count", Query: map[string]string{"scope": "system or asset", "asset_id": "Opaque id from /api/energy/assets", "since": "Unix milliseconds", "until": "Unix milliseconds", "bucket": "Bucket duration, at least 5m", "limit": "Maximum buckets, up to 5000"}},
	{Path: "/api/savings/daily", Scope: ScopeHistoryRead, Description: "Daily cost comparison and coverage", Query: map[string]string{"days": "Number of days"}},
	{Path: "/api/series/catalog", Scope: ScopeHistoryRead, Description: "Stored drivers, metric names and units"},
	{Path: "/api/series", Scope: ScopeHistoryRead, Description: "One driver's measured series with min, max, sample count, resolution and bounds", Query: map[string]string{"driver": "Required opaque driver id from /api/series/catalog", "metric": "Required metric name or comma-separated names from the catalogue", "range": "Time back, such as 6h", "since": "Unix milliseconds", "until": "Unix milliseconds", "points": "Maximum points per metric"}},
}

func AgentAPIPath(path string) (AgentAPIRoute, bool) {
	for _, route := range AgentAPIRoutes {
		if route.Path == path {
			return route, true
		}
	}
	return AgentAPIRoute{}, false
}
