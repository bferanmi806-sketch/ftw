package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/redact"
	"github.com/srcfl/ftw/go/protocol"
)

// ServeAgentRead runs only a catalogued JSON read through the usual HTTP
// handler. It buffers and redacts the whole answer on the box before any
// byte leaves for FTW Cloud. The caller comes from the Noise session.
func (s *Server) ServeAgentRead(w http.ResponseWriter, request *http.Request, redactor *redact.Redactor) {
	caller, named := apiauth.FromRequest(request)
	route, known := protocol.AgentAPIPath(request.URL.Path)
	facts := s.Route(request)
	if !named || redactor == nil || !known || request.Method != http.MethodGet || facts.Static || facts.Tier != apiauth.TierRead ||
		!caller.Scopes.Has(protocol.ScopeDiagRun) || !caller.Scopes.Has(route.Scope) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this diagnostic read is not granted"})
		return
	}
	query := request.URL.Query()
	for name, values := range query {
		if _, ok := route.Query[name]; !ok || len(values) != 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown or repeated query parameter"})
			return
		}
	}
	// Resolve only ids issued by this box's redactor. A real hardware id or
	// driver name is never accepted in an agent query.
	for _, field := range []string{"asset_id", "driver"} {
		id := query.Get(field)
		if id == "" {
			continue
		}
		name, ok := s.agentReadID(field, id, redactor)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown opaque id; read the asset or series catalogue"})
			return
		}
		query.Set(field, name)
	}
	request = request.Clone(request.Context())
	request.URL.RawQuery = query.Encode()
	if request.URL.Path == "/api/series" && query.Get("points") == "" {
		query.Set("points", "500")
		request.URL.RawQuery = query.Encode()
	}
	if raw := query.Get("points"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 5000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "points must be between 1 and 5000"})
			return
		}
	}
	answer := &agentReadBuffer{header: make(http.Header), status: http.StatusOK}
	s.ServeHTTP(answer, request)
	if answer.large {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "answer is too large; narrow the time range, points or limit"})
		return
	}
	var body any
	dec := json.NewDecoder(bytes.NewReader(answer.body.Bytes()))
	dec.UseNumber()
	if !json.Valid(answer.body.Bytes()) || dec.Decode(&body) != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the diagnostic route did not return JSON"})
		return
	}
	writeJSON(w, answer.status, redactor.Data(body))
}

func (s *Server) agentReadID(field, id string, redactor *redact.Redactor) (string, bool) {
	if s.deps.State == nil {
		return "", false
	}
	if field == "asset_id" {
		assets, err := s.deps.State.EnergyAssets()
		if err == nil {
			for _, asset := range assets {
				if redactor.ID(asset.AssetID) == id {
					return asset.AssetID, true
				}
			}
		}
	} else {
		names, err := s.deps.State.DriverNames()
		if err == nil {
			for _, name := range names {
				if redactor.ID(name) == id {
					return name, true
				}
			}
		}
	}
	return "", false
}

type agentReadBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
	large  bool
	wrote  bool
}

func (b *agentReadBuffer) Header() http.Header { return b.header }
func (b *agentReadBuffer) WriteHeader(status int) {
	if !b.wrote {
		b.status, b.wrote = status, true
	}
}
func (b *agentReadBuffer) Write(body []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	if b.large || b.body.Len()+len(body) > protocol.AgentAPIMaxBytes {
		b.large = true
		return 0, errors.New("diagnostic answer too large")
	}
	return b.body.Write(body)
}
