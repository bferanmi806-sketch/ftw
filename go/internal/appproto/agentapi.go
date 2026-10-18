package appproto

import (
	"context"
	"net/http"
	"sync"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/redact"
	"github.com/srcfl/ftw/go/protocol"
)

type agentReadGateway interface {
	ServeAgentRead(http.ResponseWriter, *http.Request, *redact.Redactor)
}

func (h *Handler) onAPIRead(env Envelope) error {
	if env.ID == nil {
		return nil
	}
	read, err := protocol.DecodeAPIRead(env.B)
	if err != nil {
		return h.refuse(env.ID, ErrUnknownOp, map[string]any{"t": env.T})
	}
	route, known := protocol.AgentAPIPath(read.Path)
	request := APIReq{Method: APIGet, Path: read.Path, Query: read.Query, MaxBytes: read.MaxBytes}
	if !known || badAPIRequest(request) != "" || read.MaxBytes < 0 || read.MaxBytes > protocol.AgentAPIMaxBytes {
		return h.refuse(env.ID, ErrUnknownOp, map[string]any{"t": env.T})
	}
	gateway, available := h.cfg.API.(agentReadGateway)
	if !available || h.cfg.Redactor == nil {
		return h.refuse(env.ID, ErrUnavailable, map[string]any{"subsystem": "api"})
	}
	if !h.apiBusy.CompareAndSwap(false, true) {
		return h.refuse(env.ID, ErrUnavailable, map[string]any{"reason": "busy"})
	}
	id := *env.ID
	go func() {
		var once sync.Once
		free := func() { once.Do(func() { h.apiBusy.Store(false) }) }
		defer free()
		ctx, cancel := context.WithTimeout(h.ctx, apiHandlerTimeout)
		defer cancel()
		if h.isAgent() {
			_, refusal, end := h.agentGate()
			if refusal != nil {
				_ = h.sendError(&id, *refusal)
				if end != "" {
					_ = h.Terminate(end)
				}
				return
			}
		}
		caller, enrolled := h.liveCaller()
		if !enrolled || !caller.Scopes.Has(protocol.ScopeDiagRun) || !caller.Scopes.Has(route.Scope) {
			_ = h.refuse(&id, ErrScopeDenied, map[string]any{"needScope": route.Scope})
			return
		}
		req := buildAPIRequest(ctx, caller, request)
		facts := h.cfg.API.Route(req)
		if facts.Static || facts.Tier != apiauth.TierRead {
			_ = h.refuse(&id, ErrScopeDenied, map[string]any{"t": env.T})
			return
		}
		max := int64(protocol.AgentAPIMaxBytes)
		if read.MaxBytes > 0 {
			max = read.MaxBytes
		}
		writer := &apiWriter{h: h, id: id, ctx: ctx, max: max, free: free, header: http.Header{}, buf: make([]byte, 0, APIChunkBytes)}
		writer.recheck = func() *ErrorBody {
			if h.isAgent() {
				if _, refusal, _ := h.agentGate(); refusal != nil {
					return refusal
				}
			}
			current, ok := h.liveCaller()
			if !ok || !current.Scopes.Has(protocol.ScopeDiagRun) || !current.Scopes.Has(route.Scope) {
				return &ErrorBody{Code: ErrScopeDenied, Args: map[string]any{"needScope": route.Scope}}
			}
			return nil
		}
		func() {
			defer func() {
				if recover() != nil {
					writer.WriteHeader(http.StatusInternalServerError)
					_, _ = writer.Write([]byte(`{"error":"diagnostic read failed"}`))
				}
			}()
			gateway.ServeAgentRead(writer, req, h.cfg.Redactor())
		}()
		if err := writer.finish(); err != nil {
			h.log.Warn("diagnostic API read failed", "err", err)
		}
	}()
	return nil
}
