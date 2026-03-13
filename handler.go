package arcana

import (
	"encoding/json"
	"errors"
	"net/http"
)

type errorResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

type successResponse struct {
	OK   bool `json:"ok"`
	Data any  `json:"data"`
}

// newHandler creates the HTTP handler mux for Arcana endpoints.
func newHandler(manager *Manager, registry *Registry, pool Querier, notifyFn func(Change), authFunc AuthFunc) http.Handler {
	mux := http.NewServeMux()

	h := &handlerSet{
		manager:  manager,
		registry: registry,
		pool:     pool,
		notifyFn: notifyFn,
	}

	mux.HandleFunc("POST /subscribe", h.handleSubscribe)
	mux.HandleFunc("POST /unsubscribe", h.handleUnsubscribe)
	mux.HandleFunc("POST /sync", h.handleSync)
	mux.HandleFunc("POST /mutate", h.handleMutate)
	mux.HandleFunc("GET /active", h.handleActive)
	mux.HandleFunc("GET /schema", h.handleSchema)
	mux.HandleFunc("GET /health", h.handleHealth)

	return authMiddleware(authFunc, mux)
}

type handlerSet struct {
	manager  *Manager
	registry *Registry
	pool     Querier
	notifyFn func(Change)
}

// POST /subscribe
func (h *handlerSet) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	identity := IdentityFromCtx(r.Context())
	if identity == nil {
		writeJSON(w, http.StatusConflict, errorResponse{OK: false, Error: "unauthorized"})
		return
	}

	var req struct {
		View   string         `json:"view"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{OK: false, Error: "invalid request body"})
		return
	}

	resp, err := h.manager.Subscribe(r.Context(), SubscribeRequest{
		GraphKey:    req.View,
		Params:      req.Params,
		SeanceID:    identity.SeanceID,
		UserID:      identity.UserID,
		WorkspaceID: identity.WorkspaceID,
	})
	if err != nil {
		status := errorToStatus(err)
		writeJSON(w, status, errorResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, successResponse{OK: true, Data: resp})
}

// POST /unsubscribe
func (h *handlerSet) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	identity := IdentityFromCtx(r.Context())
	if identity == nil {
		writeJSON(w, http.StatusConflict, errorResponse{OK: false, Error: "unauthorized"})
		return
	}

	var req struct {
		View       string `json:"view"`
		ParamsHash string `json:"params_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{OK: false, Error: "invalid request body"})
		return
	}

	err := h.manager.Unsubscribe(identity.SeanceID, req.ParamsHash)
	if err != nil {
		status := errorToStatus(err)
		writeJSON(w, status, errorResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, successResponse{OK: true, Data: nil})
}

// POST /sync
func (h *handlerSet) handleSync(w http.ResponseWriter, r *http.Request) {
	identity := IdentityFromCtx(r.Context())
	if identity == nil {
		writeJSON(w, http.StatusConflict, errorResponse{OK: false, Error: "unauthorized"})
		return
	}

	var req struct {
		Views []SyncViewRequest `json:"views"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{OK: false, Error: "invalid request body"})
		return
	}

	resp, err := h.manager.Sync(r.Context(), SyncRequest{
		SeanceID:    identity.SeanceID,
		WorkspaceID: identity.WorkspaceID,
		Views:       req.Views,
	})
	if err != nil {
		status := errorToStatus(err)
		writeJSON(w, status, errorResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, successResponse{OK: true, Data: resp})
}

// GET /active
func (h *handlerSet) handleActive(w http.ResponseWriter, r *http.Request) {
	identity := IdentityFromCtx(r.Context())
	if identity == nil {
		writeJSON(w, http.StatusConflict, errorResponse{OK: false, Error: "unauthorized"})
		return
	}

	subs := h.manager.GetActive(identity.SeanceID)

	type activeSub struct {
		GraphKey   string `json:"graph_key"`
		ParamsHash string `json:"params_hash"`
		Version    int64  `json:"version"`
	}

	result := make([]activeSub, 0, len(subs))
	for _, sub := range subs {
		result = append(result, activeSub{
			GraphKey:   sub.GraphKey,
			ParamsHash: sub.ParamsHash,
			Version:    sub.Version,
		})
	}

	writeJSON(w, http.StatusOK, successResponse{OK: true, Data: result})
}

// POST /mutate
func (h *handlerSet) handleMutate(w http.ResponseWriter, r *http.Request) {
	identity := IdentityFromCtx(r.Context())
	if identity == nil {
		writeJSON(w, http.StatusConflict, errorResponse{OK: false, Error: "unauthorized"})
		return
	}

	var req struct {
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{OK: false, Error: "invalid request body"})
		return
	}

	def, ok := h.registry.GetMutation(req.Action)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{OK: false, Error: "mutation not found"})
		return
	}

	result, err := executeMutation(r.Context(), def, h.pool, req.Params, h.notifyFn)
	if err != nil {
		status := errorToStatus(err)
		writeJSON(w, status, errorResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, MutateResponse{OK: true, Data: result.Data})
}

// GET /schema
func (h *handlerSet) handleSchema(w http.ResponseWriter, r *http.Request) {
	repTable := h.registry.RepTable()
	writeJSON(w, http.StatusOK, successResponse{OK: true, Data: repTable})
}

// GET /health
func (h *handlerSet) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, successResponse{
		OK: true,
		Data: map[string]any{
			"status":        "ok",
			"graphs":        h.registry.GraphCount(),
			"subscriptions": h.manager.SubscriptionCount(),
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func errorToStatus(err error) int {
	switch {
	case errors.Is(err, ErrForbidden):
		return http.StatusConflict // 409 per spec — don't reveal info
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalidParams):
		return http.StatusBadRequest
	case errors.Is(err, ErrTooManySubscriptions):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}
