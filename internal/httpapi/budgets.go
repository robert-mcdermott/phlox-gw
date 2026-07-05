package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func (s *Server) budgetStatus(w http.ResponseWriter, r *http.Request, user store.User) {
	status, err := s.store.BudgetStatus(r.Context(), user, true)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, status)
}

func (s *Server) listBudgets(w http.ResponseWriter, r *http.Request, _ store.User) {
	budgets, err := s.store.ListBudgets(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, budgets)
}

func (s *Server) createBudget(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req struct {
		ScopeType  string  `json:"scope_type"`
		ScopeValue string  `json:"scope_value"`
		LimitUSD   float64 `json:"limit_usd"`
		WarnPct    float64 `json:"warn_pct"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ScopeType != "user" && req.ScopeType != "department" {
		respondError(w, http.StatusBadRequest, "scope_type must be user or department")
		return
	}
	if strings.TrimSpace(req.ScopeValue) == "" || req.LimitUSD <= 0 {
		respondError(w, http.StatusBadRequest, "scope_value and positive limit_usd are required")
		return
	}
	if req.WarnPct <= 0 {
		req.WarnPct = 90
	}
	id, err := auth.RandomID("budget")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not allocate budget id")
		return
	}
	b := store.Budget{ID: id, ScopeType: req.ScopeType, ScopeValue: req.ScopeValue, LimitUSD: req.LimitUSD, WarnPct: req.WarnPct, IsActive: true}
	if err := s.store.CreateBudget(r.Context(), b); err != nil {
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "budget already exists for this scope")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "budget.create", "budget", b.ID, budgetDisplay(b), budgetAuditDetails(b))
	respondJSON(w, http.StatusCreated, b)
}

func (s *Server) updateBudget(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req struct {
		ScopeType  string  `json:"scope_type"`
		ScopeValue string  `json:"scope_value"`
		LimitUSD   float64 `json:"limit_usd"`
		WarnPct    float64 `json:"warn_pct"`
		IsActive   bool    `json:"is_active"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ScopeType != "user" && req.ScopeType != "department" {
		respondError(w, http.StatusBadRequest, "scope_type must be user or department")
		return
	}
	if strings.TrimSpace(req.ScopeValue) == "" || req.LimitUSD <= 0 {
		respondError(w, http.StatusBadRequest, "scope_value and positive limit_usd are required")
		return
	}
	if req.WarnPct <= 0 {
		req.WarnPct = 90
	}
	b := store.Budget{ID: r.PathValue("id"), ScopeType: req.ScopeType, ScopeValue: req.ScopeValue, LimitUSD: req.LimitUSD, WarnPct: req.WarnPct, IsActive: req.IsActive}
	if err := s.store.UpdateBudget(r.Context(), b); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "budget not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "budget already exists for this scope")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "budget.update", "budget", b.ID, budgetDisplay(b), budgetAuditDetails(b))
	respondJSON(w, http.StatusOK, b)
}

func (s *Server) deleteBudget(w http.ResponseWriter, r *http.Request, admin store.User) {
	id := r.PathValue("id")
	if err := s.store.DeleteBudget(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "budget not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "budget.delete", "budget", id, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func budgetAuditDetails(b store.Budget) map[string]any {
	return map[string]any{
		"scope_type":  b.ScopeType,
		"scope_value": b.ScopeValue,
		"limit_usd":   b.LimitUSD,
		"warn_pct":    b.WarnPct,
		"is_active":   b.IsActive,
	}
}

func budgetDisplay(b store.Budget) string {
	return b.ScopeType + ":" + b.ScopeValue
}
