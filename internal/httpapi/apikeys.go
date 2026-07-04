package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request, user store.User) {
	keys, err := s.store.ListAPIKeys(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, keys)
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request, user store.User) {
	var req struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expires_at"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = "API key"
	}
	expires, ok := parseAPIKeyExpiresAt(w, req.ExpiresAt, true)
	if !ok {
		return
	}
	plain, prefix, hash, err := auth.NewAPIKey()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not generate key")
		return
	}
	id, err := auth.RandomID("key")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not allocate key id")
		return
	}
	key := store.APIKey{ID: id, UserID: user.ID, Name: req.Name, Prefix: prefix, KeyHash: hash, ExpiresAt: expires}
	if err := s.store.CreateAPIKey(r.Context(), key); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, user, "api_key.create", "api_key", key.ID, key.Prefix, map[string]any{
		"name":       key.Name,
		"prefix":     key.Prefix,
		"expires_at": key.ExpiresAt,
	})
	respondJSON(w, http.StatusCreated, map[string]any{"key": plain, "record": key})
}

func (s *Server) updateAPIKeySelf(w http.ResponseWriter, r *http.Request, user store.User) {
	var req struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expires_at"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	expires, ok := parseAPIKeyExpiresAt(w, req.ExpiresAt, true)
	if !ok {
		return
	}
	key := store.APIKey{
		ID:        r.PathValue("id"),
		UserID:    user.ID,
		Name:      name,
		ExpiresAt: expires,
	}
	if err := s.store.UpdateAPIKeySelf(r.Context(), key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "key not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, user, "api_key.update_self", "api_key", key.ID, key.Name, map[string]any{
		"name":       key.Name,
		"expires_at": key.ExpiresAt,
	})
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) rotateAPIKey(w http.ResponseWriter, r *http.Request, user store.User) {
	keyID := r.PathValue("id")
	plain, prefix, hash, err := auth.NewAPIKey()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not generate key")
		return
	}
	if err := s.store.RotateAPIKey(r.Context(), user.ID, keyID, prefix, hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "active key not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, user, "api_key.rotate", "api_key", keyID, prefix, map[string]any{
		"scope":      "self",
		"new_prefix": prefix,
	})
	respondJSON(w, http.StatusOK, map[string]any{"key": plain, "prefix": prefix})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request, user store.User) {
	keyID := r.PathValue("id")
	if err := s.store.RevokeAPIKey(r.Context(), user.ID, keyID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "key not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, user, "api_key.revoke", "api_key", keyID, keyID, map[string]any{
		"scope": "self",
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminAPIKeys(w http.ResponseWriter, r *http.Request, _ store.User) {
	keys, err := s.store.ListAllAPIKeys(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, keys)
}

func (s *Server) updateAPIKeyControls(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req struct {
		Name           string  `json:"name"`
		IsActive       bool    `json:"is_active"`
		ExpiresAt      string  `json:"expires_at"`
		BudgetUSD      float64 `json:"budget_usd"`
		RPMLimit       int     `json:"rpm_limit"`
		TPMLimit       int     `json:"tpm_limit"`
		ModelAllowlist string  `json:"model_allowlist"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.BudgetUSD < 0 || req.RPMLimit < 0 || req.TPMLimit < 0 {
		respondError(w, http.StatusBadRequest, "budgets and rate limits cannot be negative")
		return
	}
	expires, ok := parseAPIKeyExpiresAt(w, req.ExpiresAt, false)
	if !ok {
		return
	}
	key := store.APIKey{
		ID:             r.PathValue("id"),
		Name:           strings.TrimSpace(req.Name),
		IsActive:       req.IsActive,
		ExpiresAt:      expires,
		BudgetUSD:      req.BudgetUSD,
		RPMLimit:       req.RPMLimit,
		TPMLimit:       req.TPMLimit,
		ModelAllowlist: req.ModelAllowlist,
	}
	if err := s.store.UpdateAPIKeyControls(r.Context(), key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "key not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "api_key.update_controls", "api_key", key.ID, key.Name, map[string]any{
		"name":                key.Name,
		"is_active":           key.IsActive,
		"budget_usd":          key.BudgetUSD,
		"rpm_limit":           key.RPMLimit,
		"tpm_limit":           key.TPMLimit,
		"has_model_allowlist": strings.TrimSpace(key.ModelAllowlist) != "",
		"expires_at":          key.ExpiresAt,
	})
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) rotateAPIKeyAdmin(w http.ResponseWriter, r *http.Request, admin store.User) {
	keyID := r.PathValue("id")
	plain, prefix, hash, err := auth.NewAPIKey()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not generate key")
		return
	}
	if err := s.store.RotateAPIKeyAdmin(r.Context(), keyID, prefix, hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "active key not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "api_key.rotate", "api_key", keyID, prefix, map[string]any{
		"scope":      "admin",
		"new_prefix": prefix,
	})
	respondJSON(w, http.StatusOK, map[string]any{"key": plain, "prefix": prefix})
}

func (s *Server) revokeAPIKeyAdmin(w http.ResponseWriter, r *http.Request, admin store.User) {
	keyID := r.PathValue("id")
	if err := s.store.RevokeAPIKeyAdmin(r.Context(), keyID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "key not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "api_key.revoke", "api_key", keyID, keyID, map[string]any{
		"scope": "admin",
	})
	w.WriteHeader(http.StatusNoContent)
}
