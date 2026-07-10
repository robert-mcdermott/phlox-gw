package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, _ store.User) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]any, 0, len(users))
	for _, user := range users {
		out = append(out, publicUser(user))
	}
	respondJSON(w, http.StatusOK, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Department  string `json:"department"`
		Role        string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Username) == "" || strings.TrimSpace(req.Password) == "" {
		respondError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	role := req.Role
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "admin" {
		respondError(w, http.StatusBadRequest, "role must be user or admin")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	id, err := auth.RandomID("user")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not allocate user id")
		return
	}
	user := store.User{
		ID:                 id,
		Username:           req.Username,
		Email:              req.Email,
		DisplayName:        req.DisplayName,
		Department:         req.Department,
		Role:               role,
		PasswordHash:       hash,
		AuthProvider:       "local",
		IsActive:           true,
		MustChangePassword: true,
	}
	if err := s.store.CreateUser(r.Context(), user); err != nil {
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "username already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "user.create", "user", user.ID, user.Username, map[string]any{
		"username":   user.Username,
		"email":      user.Email,
		"department": user.Department,
		"role":       user.Role,
	})
	respondJSON(w, http.StatusCreated, publicUser(user))
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Department  string `json:"department"`
		Role        string `json:"role"`
		IsActive    bool   `json:"is_active"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role != "user" && req.Role != "admin" {
		respondError(w, http.StatusBadRequest, "role must be user or admin")
		return
	}
	if r.PathValue("id") == admin.ID && (req.Role != "admin" || !req.IsActive) {
		respondError(w, http.StatusBadRequest, "cannot demote or deactivate your current admin session")
		return
	}
	user := store.User{
		ID:          r.PathValue("id"),
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Department:  req.Department,
		Role:        req.Role,
		IsActive:    req.IsActive,
	}
	if err := s.store.UpdateUser(r.Context(), user); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "user not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.store.GetUserByID(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "user.update", "user", updated.ID, updated.Username, map[string]any{
		"email":      updated.Email,
		"department": updated.Department,
		"role":       updated.Role,
		"is_active":  updated.IsActive,
	})
	respondJSON(w, http.StatusOK, publicUser(updated))
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		respondError(w, http.StatusBadRequest, "password is required")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.store.SetUserPassword(r.Context(), r.PathValue("id"), hash, true); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "user not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	target, err := s.store.GetUserByID(r.Context(), r.PathValue("id"))
	targetDisplay := r.PathValue("id")
	if err == nil {
		targetDisplay = target.Username
	}
	s.audit(r, admin, "user.password_reset", "user", r.PathValue("id"), targetDisplay, map[string]any{"must_change_password": true})
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, admin store.User) {
	id := r.PathValue("id")
	if id == admin.ID {
		respondError(w, http.StatusBadRequest, "cannot delete your current admin session")
		return
	}
	target, targetErr := s.store.GetUserByID(r.Context(), id)
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "user not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	targetDisplay := id
	details := map[string]any{}
	if targetErr == nil {
		targetDisplay = target.Username
		details["username"] = target.Username
		details["department"] = target.Department
		details["role"] = target.Role
	}
	s.audit(r, admin, "user.delete", "user", id, targetDisplay, details)
	w.WriteHeader(http.StatusNoContent)
}
