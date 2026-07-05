package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func (s *Server) listRateLimits(w http.ResponseWriter, r *http.Request, _ store.User) {
	limits, err := s.store.ListRateLimits(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, limits)
}

func (s *Server) createRateLimit(w http.ResponseWriter, r *http.Request, admin store.User) {
	rl, ok := s.rateLimitFromRequest(w, r, "")
	if !ok {
		return
	}
	id, err := auth.RandomID("limit")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not allocate rate limit id")
		return
	}
	rl.ID = id
	rl.IsActive = true
	if err := s.store.CreateRateLimit(r.Context(), rl); err != nil {
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "rate limit already exists for this scope")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "rate_limit.create", "rate_limit", rl.ID, rateLimitDisplay(rl), rateLimitAuditDetails(rl))
	respondJSON(w, http.StatusCreated, rl)
}

func (s *Server) updateRateLimit(w http.ResponseWriter, r *http.Request, admin store.User) {
	rl, ok := s.rateLimitFromRequest(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.store.UpdateRateLimit(r.Context(), rl); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "rate limit not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "rate limit already exists for this scope")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "rate_limit.update", "rate_limit", rl.ID, rateLimitDisplay(rl), rateLimitAuditDetails(rl))
	respondJSON(w, http.StatusOK, rl)
}

func (s *Server) deleteRateLimit(w http.ResponseWriter, r *http.Request, admin store.User) {
	id := r.PathValue("id")
	if err := s.store.DeleteRateLimit(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "rate limit not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "rate_limit.delete", "rate_limit", id, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rateLimitFromRequest(w http.ResponseWriter, r *http.Request, pathID string) (store.RateLimit, bool) {
	var req struct {
		ID         string `json:"id"`
		ScopeType  string `json:"scope_type"`
		ScopeValue string `json:"scope_value"`
		RPMLimit   int    `json:"rpm_limit"`
		TPMLimit   int    `json:"tpm_limit"`
		IsActive   bool   `json:"is_active"`
	}
	if !decodeJSON(w, r, &req) {
		return store.RateLimit{}, false
	}
	id := strings.TrimSpace(req.ID)
	if pathID != "" {
		id = pathID
	}
	scopeType := strings.TrimSpace(req.ScopeType)
	scopeValue := strings.TrimSpace(req.ScopeValue)
	if !validRateLimitScope(scopeType) {
		respondError(w, http.StatusBadRequest, "scope_type must be user, department, provider, or model")
		return store.RateLimit{}, false
	}
	if scopeValue == "" {
		respondError(w, http.StatusBadRequest, "scope_value is required")
		return store.RateLimit{}, false
	}
	if req.RPMLimit < 0 || req.TPMLimit < 0 {
		respondError(w, http.StatusBadRequest, "rate limits cannot be negative")
		return store.RateLimit{}, false
	}
	if req.RPMLimit == 0 && req.TPMLimit == 0 {
		respondError(w, http.StatusBadRequest, "at least one rate limit must be positive")
		return store.RateLimit{}, false
	}
	return store.RateLimit{
		ID:         id,
		ScopeType:  scopeType,
		ScopeValue: scopeValue,
		RPMLimit:   req.RPMLimit,
		TPMLimit:   req.TPMLimit,
		IsActive:   req.IsActive,
	}, true
}

func validRateLimitScope(scopeType string) bool {
	return scopeType == "user" || scopeType == "department" || scopeType == "provider" || scopeType == "model"
}

func rateLimitAuditDetails(rl store.RateLimit) map[string]any {
	return map[string]any{
		"scope_type":  rl.ScopeType,
		"scope_value": rl.ScopeValue,
		"rpm_limit":   rl.RPMLimit,
		"tpm_limit":   rl.TPMLimit,
		"is_active":   rl.IsActive,
	}
}

func rateLimitDisplay(rl store.RateLimit) string {
	return rl.ScopeType + ":" + rl.ScopeValue
}
