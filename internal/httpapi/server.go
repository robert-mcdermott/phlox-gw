package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	oidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
	"golang.org/x/oauth2"
)

type Options struct {
	Config               config.Config
	Store                *store.Store
	Frontend             fs.FS
	Logger               *slog.Logger
	HTTPClient           *http.Client
	BedrockClientFactory BedrockClientFactory
	OIDCAuthenticator    OIDCAuthenticator
}

type Server struct {
	cfg                  config.Config
	store                *store.Store
	logger               *slog.Logger
	httpClient           *http.Client
	frontend             fs.FS
	bedrockClientFactory BedrockClientFactory
	oidcAuthenticator    OIDCAuthenticator
}

const providerFailureThreshold = 3
const providerCircuitCooldown = 5 * time.Minute

type BedrockConverseClient interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

type BedrockClientFactory func(context.Context, store.Provider) (BedrockConverseClient, error)

type OIDCAuthenticator interface {
	AuthCodeURL(ctx context.Context, state, nonce, redirectURL string) (string, error)
	Exchange(ctx context.Context, code, nonce, redirectURL string) (OIDCClaims, error)
}

type OIDCClaims struct {
	Subject string
	Values  map[string]any
}

type routeReliabilityPolicy struct {
	RetryAttempts        int
	RequestTimeout       time.Duration
	HealthRoutingEnabled bool
}

type routePlan struct {
	Requested  store.RoutedModel
	Candidates []store.RoutedModel
}

type weightedRoutePolicy struct {
	Route  string
	Weight int
}

type upstreamResult struct {
	Route     store.RoutedModel
	Protocol  string
	Status    int
	Headers   http.Header
	Body      []byte
	ErrorText string
	LatencyMS int64
}

func New(opts Options) (http.Handler, error) {
	if opts.Store == nil {
		return nil, errors.New("store is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Minute}
	}
	sub, err := fs.Sub(opts.Frontend, "frontend/dist")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:                  opts.Config,
		store:                opts.Store,
		logger:               opts.Logger,
		httpClient:           opts.HTTPClient,
		frontend:             sub,
		bedrockClientFactory: opts.BedrockClientFactory,
		oidcAuthenticator:    opts.OIDCAuthenticator,
	}
	if s.oidcAuthenticator == nil && s.cfg.OIDC.Enabled {
		s.oidcAuthenticator = newDefaultOIDCAuthenticator(s.cfg.OIDC, s.httpClient)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/oidc/config", s.oidcConfig)
	mux.HandleFunc("GET /api/auth/oidc/login", s.oidcLogin)
	mux.HandleFunc("GET /api/auth/oidc/callback", s.oidcCallback)
	mux.HandleFunc("GET /api/auth/me", s.requireSession(s.me))
	mux.HandleFunc("GET /api/models", s.requireSession(s.models))
	mux.HandleFunc("GET /api/usage", s.requireSession(s.usage))
	mux.HandleFunc("GET /api/usage/budget", s.requireSession(s.budgetStatus))
	mux.HandleFunc("GET /api/api-keys", s.requireSession(s.listAPIKeys))
	mux.HandleFunc("POST /api/api-keys", s.requireSession(s.createAPIKey))
	mux.HandleFunc("PATCH /api/api-keys/{id}", s.requireSession(s.updateAPIKeySelf))
	mux.HandleFunc("POST /api/api-keys/{id}/rotate", s.requireSession(s.rotateAPIKey))
	mux.HandleFunc("DELETE /api/api-keys/{id}", s.requireSession(s.revokeAPIKey))
	mux.HandleFunc("GET /api/admin/users", s.requireAdmin(s.listUsers))
	mux.HandleFunc("POST /api/admin/users", s.requireAdmin(s.createUser))
	mux.HandleFunc("PATCH /api/admin/users/{id}", s.requireAdmin(s.updateUser))
	mux.HandleFunc("POST /api/admin/users/{id}/reset-password", s.requireAdmin(s.resetUserPassword))
	mux.HandleFunc("DELETE /api/admin/users/{id}", s.requireAdmin(s.deleteUser))
	mux.HandleFunc("GET /api/admin/providers", s.requireAdmin(s.providers))
	mux.HandleFunc("POST /api/admin/providers", s.requireAdmin(s.createProvider))
	mux.HandleFunc("PUT /api/admin/providers/{id}", s.requireAdmin(s.updateProvider))
	mux.HandleFunc("DELETE /api/admin/providers/{id}", s.requireAdmin(s.deleteProvider))
	mux.HandleFunc("GET /api/admin/models", s.requireAdmin(s.adminModels))
	mux.HandleFunc("POST /api/admin/models", s.requireAdmin(s.createModel))
	mux.HandleFunc("PUT /api/admin/models/{id}", s.requireAdmin(s.updateModel))
	mux.HandleFunc("DELETE /api/admin/models/{id}", s.requireAdmin(s.deleteModel))
	mux.HandleFunc("POST /api/admin/models/{id}/test", s.requireAdmin(s.testModel))
	mux.HandleFunc("GET /api/admin/budgets", s.requireAdmin(s.listBudgets))
	mux.HandleFunc("POST /api/admin/budgets", s.requireAdmin(s.createBudget))
	mux.HandleFunc("PATCH /api/admin/budgets/{id}", s.requireAdmin(s.updateBudget))
	mux.HandleFunc("DELETE /api/admin/budgets/{id}", s.requireAdmin(s.deleteBudget))
	mux.HandleFunc("GET /api/admin/rate-limits", s.requireAdmin(s.listRateLimits))
	mux.HandleFunc("POST /api/admin/rate-limits", s.requireAdmin(s.createRateLimit))
	mux.HandleFunc("PATCH /api/admin/rate-limits/{id}", s.requireAdmin(s.updateRateLimit))
	mux.HandleFunc("DELETE /api/admin/rate-limits/{id}", s.requireAdmin(s.deleteRateLimit))
	mux.HandleFunc("GET /api/admin/api-keys", s.requireAdmin(s.adminAPIKeys))
	mux.HandleFunc("PATCH /api/admin/api-keys/{id}", s.requireAdmin(s.updateAPIKeyControls))
	mux.HandleFunc("POST /api/admin/api-keys/{id}/rotate", s.requireAdmin(s.rotateAPIKeyAdmin))
	mux.HandleFunc("DELETE /api/admin/api-keys/{id}", s.requireAdmin(s.revokeAPIKeyAdmin))
	mux.HandleFunc("GET /api/admin/audit-log", s.requireAdmin(s.auditLog))
	mux.HandleFunc("GET /api/admin/usage/summary", s.requireAdmin(s.adminUsage))
	mux.HandleFunc("GET /api/admin/usage/timeseries", s.requireAdmin(s.adminUsageTimeSeries))
	mux.HandleFunc("GET /api/admin/usage/drilldowns", s.requireAdmin(s.adminUsageDrilldowns))
	mux.HandleFunc("GET /api/admin/budgets/burndown", s.requireAdmin(s.adminBudgetBurnDown))
	mux.HandleFunc("GET /api/admin/usage/export.csv", s.requireAdmin(s.adminUsageCSV))
	mux.HandleFunc("GET /v1/models", s.requireAPIKey(s.openAIModels))
	mux.HandleFunc("POST /v1/chat/completions", s.requireAPIKey(s.openAIChatCompletions))
	mux.HandleFunc("POST /anthropic/v1/messages", s.requireAPIKey(s.anthropicMessages))
	mux.HandleFunc("/", s.static)

	return s.requestLog(mux), nil
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/anthropic/") {
			s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rw.status, "duration_ms", time.Since(start).Milliseconds())
		}
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"name":   "phlox-gw",
		"time":   time.Now().UTC(),
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := s.store.GetUserByUsername(r.Context(), req.Username)
	if err != nil || !user.IsActive || !auth.CheckPassword(user.PasswordHash, req.Password) {
		respondError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := s.issueSessionToken(user)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not sign session")
		return
	}
	now := time.Now().UTC()
	_ = s.store.TouchLogin(r.Context(), user.ID, now)
	s.audit(r, user, "auth.login", "user", user.ID, user.Username, map[string]any{
		"auth_provider": user.AuthProvider,
		"role":          user.Role,
	})
	respondJSON(w, http.StatusOK, map[string]any{"token": token, "user": publicUser(user)})
}

func (s *Server) oidcConfig(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"enabled":      s.cfg.OIDC.Enabled,
		"display_name": s.cfg.OIDC.DisplayName,
	})
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled || s.oidcAuthenticator == nil {
		respondError(w, http.StatusNotFound, "OIDC login is not enabled")
		return
	}
	state, err := randomOIDCToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not create OIDC state")
		return
	}
	nonce, err := randomOIDCToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not create OIDC nonce")
		return
	}
	loginState := oidcLoginState{
		State:    state,
		Nonce:    nonce,
		ReturnTo: cleanReturnTo(r.URL.Query().Get("return_to")),
		Expires:  time.Now().UTC().Add(10 * time.Minute).Unix(),
	}
	cookieValue, err := s.signOIDCState(loginState)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not sign OIDC state")
		return
	}
	http.SetCookie(w, s.oidcStateCookie(r, cookieValue, 10*60))
	authURL, err := s.oidcAuthenticator.AuthCodeURL(r.Context(), state, nonce, s.oidcRedirectURL(r))
	if err != nil {
		respondError(w, http.StatusBadGateway, "OIDC provider configuration failed: "+err.Error())
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled || s.oidcAuthenticator == nil {
		respondOIDCError(w, http.StatusNotFound, "OIDC login is not enabled")
		return
	}
	if providerError := strings.TrimSpace(r.URL.Query().Get("error")); providerError != "" {
		description := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if description != "" {
			providerError += ": " + description
		}
		respondOIDCError(w, http.StatusUnauthorized, providerError)
		return
	}
	loginState, ok := s.readOIDCState(r)
	if !ok {
		respondOIDCError(w, http.StatusUnauthorized, "OIDC state is missing or invalid")
		return
	}
	if loginState.Expires <= time.Now().UTC().Unix() {
		respondOIDCError(w, http.StatusUnauthorized, "OIDC state expired")
		return
	}
	if subtleState := strings.TrimSpace(r.URL.Query().Get("state")); subtleState == "" || !hmac.Equal([]byte(subtleState), []byte(loginState.State)) {
		respondOIDCError(w, http.StatusUnauthorized, "OIDC state mismatch")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		respondOIDCError(w, http.StatusBadRequest, "OIDC authorization code is missing")
		return
	}
	claims, err := s.oidcAuthenticator.Exchange(r.Context(), code, loginState.Nonce, s.oidcRedirectURL(r))
	if err != nil {
		respondOIDCError(w, http.StatusUnauthorized, "OIDC token exchange failed: "+err.Error())
		return
	}
	user, err := s.userFromOIDCClaims(r.Context(), claims)
	if err != nil {
		if errors.Is(err, errOIDCProvisioningDisabled) {
			respondOIDCError(w, http.StatusForbidden, "SSO user is not provisioned in Phlox-GW")
			return
		}
		if errors.Is(err, errOIDCUserDisabled) {
			respondOIDCError(w, http.StatusForbidden, "SSO user is disabled in Phlox-GW")
			return
		}
		respondOIDCError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := s.issueSessionToken(user)
	if err != nil {
		respondOIDCError(w, http.StatusInternalServerError, "could not sign session")
		return
	}
	http.SetCookie(w, s.oidcStateCookie(r, "", -1))
	_ = s.store.TouchLogin(r.Context(), user.ID, time.Now().UTC())
	s.audit(r, user, "auth.login", "user", user.ID, user.Username, map[string]any{
		"auth_provider": "oidc",
		"issuer":        s.cfg.OIDC.IssuerURL,
		"role":          user.Role,
		"department":    user.Department,
	})
	respondOIDCSuccess(w, token, loginState.ReturnTo)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, user store.User) {
	respondJSON(w, http.StatusOK, publicUser(user))
}

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
		ID:           id,
		Username:     req.Username,
		Email:        req.Email,
		DisplayName:  req.DisplayName,
		Department:   req.Department,
		Role:         role,
		PasswordHash: hash,
		AuthProvider: "local",
		IsActive:     true,
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
	if err := s.store.SetUserPassword(r.Context(), r.PathValue("id"), hash); err != nil {
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
	s.audit(r, admin, "user.password_reset", "user", r.PathValue("id"), targetDisplay, nil)
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

func (s *Server) providers(w http.ResponseWriter, r *http.Request, _ store.User) {
	providers, err := s.store.ListProviders(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range providers {
		if providers[i].APIKey != "" {
			providers[i].APIKeyEnv = providers[i].APIKeyEnv + secretMarker(providers[i].APIKeyEnv)
		}
	}
	respondJSON(w, http.StatusOK, providers)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request, admin store.User) {
	p, updateSecret, ok := s.providerFromRequest(w, r, "")
	if !ok {
		return
	}
	if !updateSecret {
		p.APIKey = ""
	}
	if err := s.store.CreateProvider(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "provider id already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "provider.create", "provider", p.ID, p.Name, providerAuditDetails(p, updateSecret))
	respondJSON(w, http.StatusCreated, p)
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, admin store.User) {
	p, updateSecret, ok := s.providerFromRequest(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.store.UpdateProvider(r.Context(), p, updateSecret); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "provider not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "provider.update", "provider", p.ID, p.Name, providerAuditDetails(p, updateSecret))
	respondJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request, admin store.User) {
	id := r.PathValue("id")
	if err := s.store.DeleteProvider(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "provider not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "provider.delete", "provider", id, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request, _ store.User) {
	models, err := s.store.ListModels(r.Context(), false)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, models)
}

func (s *Server) adminModels(w http.ResponseWriter, r *http.Request, _ store.User) {
	models, err := s.store.ListModels(r.Context(), true)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, models)
}

func (s *Server) createModel(w http.ResponseWriter, r *http.Request, admin store.User) {
	m, ok := s.modelFromRequest(w, r, "")
	if !ok {
		return
	}
	if err := s.store.CreateModel(r.Context(), m); err != nil {
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "model id or route already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "model.create", "model", m.ID, m.Route, modelAuditDetails(m))
	respondJSON(w, http.StatusCreated, m)
}

func (s *Server) updateModel(w http.ResponseWriter, r *http.Request, admin store.User) {
	m, ok := s.modelFromRequest(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.store.UpdateModel(r.Context(), m); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "model not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "model route already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "model.update", "model", m.ID, m.Route, modelAuditDetails(m))
	respondJSON(w, http.StatusOK, m)
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request, admin store.User) {
	id := r.PathValue("id")
	if err := s.store.DeleteModel(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "model not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "model.delete", "model", id, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testModel(w http.ResponseWriter, r *http.Request, admin store.User) {
	route, err := s.store.ResolveModelByID(r.Context(), r.PathValue("id"), true)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "enabled model not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := s.runModelHealthCheck(r.Context(), route)
	s.recordProviderHealthCheck(r.Context(), route.Provider.ID, result)
	status := http.StatusOK
	if !result.OK {
		status = http.StatusBadGateway
	}
	s.audit(r, admin, "model.test", "model", route.Model.ID, route.Model.Route, map[string]any{
		"ok":          result.OK,
		"status_code": result.StatusCode,
		"latency_ms":  result.LatencyMS,
		"provider_id": result.ProviderID,
	})
	respondJSON(w, status, result)
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request, user store.User) {
	summary, err := s.store.UsageForUser(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, summary)
}

func (s *Server) adminUsage(w http.ResponseWriter, r *http.Request, _ store.User) {
	summary, err := s.store.UsageAll(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, summary)
}

func (s *Server) adminUsageTimeSeries(w http.ResponseWriter, r *http.Request, _ store.User) {
	days, ok := parseDaysQuery(w, r, 30)
	if !ok {
		return
	}
	points, err := s.store.UsageTimeSeries(r.Context(), days, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, points)
}

func (s *Server) adminUsageDrilldowns(w http.ResponseWriter, r *http.Request, _ store.User) {
	days, ok := parseDaysQuery(w, r, 30)
	if !ok {
		return
	}
	drilldowns, err := s.store.UsageDrilldowns(r.Context(), days, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, drilldowns)
}

func (s *Server) adminBudgetBurnDown(w http.ResponseWriter, r *http.Request, _ store.User) {
	items, err := s.store.BudgetBurnDown(r.Context(), time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, items)
}

func parseDaysQuery(w http.ResponseWriter, r *http.Request, fallback int) (int, bool) {
	days := fallback
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			respondError(w, http.StatusBadRequest, "days must be a positive integer")
			return 0, false
		}
		days = parsed
	}
	return days, true
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request, _ store.User) {
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	items, err := s.store.ListAuditLogs(r.Context(), limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, items)
}

func (s *Server) adminUsageCSV(w http.ResponseWriter, r *http.Request, _ store.User) {
	rows, err := s.store.UsageExport(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filename := "phlox-gw-usage-" + time.Now().UTC().Format("20060102") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"created_at", "request_id", "username", "department", "api_key_id", "provider_id", "model", "protocol", "input_tokens", "output_tokens", "total_tokens", "cost_usd", "latency_ms", "status_code", "error"})
	for _, row := range rows {
		_ = cw.Write([]string{
			row.CreatedAt.Format(time.RFC3339Nano),
			row.RequestID,
			row.Username,
			row.Department,
			row.APIKeyID,
			row.ProviderID,
			row.Model,
			row.Protocol,
			strconv.Itoa(row.InputTokens),
			strconv.Itoa(row.OutputTokens),
			strconv.Itoa(row.TotalTokens),
			strconv.FormatFloat(row.CostUSD, 'f', 6, 64),
			strconv.FormatInt(row.LatencyMS, 10),
			strconv.Itoa(row.StatusCode),
			row.ErrorText,
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.logger.Warn("csv write failed", "error", err)
	}
}

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

func (s *Server) openAIModels(w http.ResponseWriter, r *http.Request, user store.User, key store.APIKey) {
	models, err := s.store.ListModels(r.Context(), false)
	if err != nil {
		openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		status, _ := s.store.BudgetStatus(r.Context(), user, store.IsPriced(m))
		blocked, reason := status.Blocked, status.Reason
		if !apiKeyAllowsModel(key, m) {
			blocked = true
			reason = "API key is not allowed to use this model"
		} else if store.IsPriced(m) && key.BudgetUSD > 0 {
			if keyBlocked, keyReason := s.checkAPIKeyMonthlyBudget(r.Context(), key); keyBlocked {
				blocked = true
				reason = keyReason
			}
		}
		if !blocked {
			route := store.RoutedModel{Model: m, Provider: store.Provider{ID: m.ProviderID}}
			if rateBlocked, _, rateReason, _ := s.checkRateLimits(r.Context(), user, route); rateBlocked {
				blocked = true
				reason = rateReason
			}
		}
		data = append(data, map[string]any{
			"id":                   m.Route,
			"object":               "model",
			"owned_by":             m.ProviderID,
			"phlox_blocked":        blocked,
			"phlox_blocked_reason": reason,
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) openAIChatCompletions(w http.ResponseWriter, r *http.Request, user store.User, key store.APIKey) {
	_, raw, ok := readObjectBody(w, r, true)
	if !ok {
		return
	}
	modelName, _ := raw["model"].(string)
	plan, err := s.resolveRoutePlan(r.Context(), modelName)
	if err != nil {
		openAIError(w, http.StatusBadRequest, "unknown or disabled model", "invalid_request_error")
		return
	}
	candidates := plan.Candidates
	route := candidates[0]
	if route.Provider.Type != "openai" && route.Provider.Type != "bedrock" {
		openAIError(w, http.StatusNotImplemented, "model is not on an OpenAI-compatible or Bedrock provider", "unsupported_provider")
		return
	}
	if blocked, status, reason, typ := s.checkAPIKeyPolicy(r.Context(), key, route); blocked {
		openAIError(w, status, reason, typ)
		return
	}
	if blocked, reason := s.checkBudget(r.Context(), user, route.Model); blocked {
		openAIError(w, http.StatusPaymentRequired, reason, "insufficient_quota")
		return
	}
	if blocked, status, reason, typ := s.checkRateLimits(r.Context(), user, route); blocked {
		openAIError(w, status, reason, typ)
		return
	}
	policy := reliabilityPolicy(plan.Requested.Model)
	requestID := requestID()
	if stream, _ := raw["stream"].(bool); stream {
		selected, status, reason, typ, ok := s.selectOpenAIStreamCandidate(r.Context(), candidates, user, key, policy)
		if !ok {
			openAIError(w, status, reason, typ)
			return
		}
		start := time.Now()
		var statusCode int
		var responseBody []byte
		var errText string
		if selected.Provider.Type == "bedrock" {
			statusCode, responseBody, errText = s.proxyBedrockOpenAI(w, r, selected, raw)
		} else {
			attemptRaw := cloneJSONMap(raw)
			attemptRaw["model"] = selected.Model.ModelID
			body, _ := json.Marshal(attemptRaw)
			statusCode, responseBody, errText = s.proxyOpenAI(w, r, selected, body, attemptRaw)
		}
		latency := time.Since(start).Milliseconds()
		usage := parseOpenAIUsage(responseBody)
		s.recordProviderOutcome(r.Context(), selected.Provider.ID, statusCode, errText)
		s.recordUsage(r.Context(), requestID, user, key, selected, selected.Provider.Type, usage, latency, statusCode, errText)
		return
	}
	result := s.executeOpenAIPlan(r.Context(), candidates, raw, user, key, requestID, policy)
	writeOpenAIResult(w, result)
}

func (s *Server) anthropicMessages(w http.ResponseWriter, r *http.Request, user store.User, key store.APIKey) {
	_, raw, ok := readObjectBody(w, r, false)
	if !ok {
		return
	}
	modelName, _ := raw["model"].(string)
	plan, err := s.resolveRoutePlan(r.Context(), modelName)
	if err != nil {
		anthropicError(w, http.StatusBadRequest, "unknown or disabled model")
		return
	}
	candidates := plan.Candidates
	route := candidates[0]
	if route.Provider.Type != "anthropic" {
		anthropicError(w, http.StatusNotImplemented, "model is not on an Anthropic-compatible provider")
		return
	}
	if blocked, status, reason, _ := s.checkAPIKeyPolicy(r.Context(), key, route); blocked {
		anthropicError(w, status, reason)
		return
	}
	if blocked, reason := s.checkBudget(r.Context(), user, route.Model); blocked {
		anthropicError(w, http.StatusPaymentRequired, reason)
		return
	}
	if blocked, status, reason, _ := s.checkRateLimits(r.Context(), user, route); blocked {
		anthropicError(w, status, reason)
		return
	}
	result := s.executeAnthropicPlan(r.Context(), candidates, raw, r.Header, user, key, requestID(), reliabilityPolicy(plan.Requested.Model))
	writeAnthropicResult(w, result)
}

func (s *Server) resolveRoutePlan(ctx context.Context, requested string) (routePlan, error) {
	primary, err := s.store.ResolveModel(ctx, requested)
	if err != nil {
		return routePlan{}, err
	}
	selected := primary
	if weighted, ok := s.selectWeightedCandidate(ctx, primary); ok {
		selected = weighted
	}
	candidates := []store.RoutedModel{selected}
	seen := map[string]bool{
		selected.Model.ID:    true,
		selected.Model.Route: true,
	}
	for _, fallback := range splitRouteList(primary.Model.FallbackRoutes) {
		if seen[fallback] {
			continue
		}
		route, err := s.store.ResolveModel(ctx, fallback)
		if err != nil {
			continue
		}
		if seen[route.Model.ID] || seen[route.Model.Route] {
			continue
		}
		seen[route.Model.ID] = true
		seen[route.Model.Route] = true
		candidates = append(candidates, route)
	}
	return routePlan{Requested: primary, Candidates: candidates}, nil
}

func (s *Server) selectWeightedCandidate(ctx context.Context, primary store.RoutedModel) (store.RoutedModel, bool) {
	entries, err := parseWeightedRoutes(primary.Model.WeightedRoutes)
	if err != nil || len(entries) == 0 {
		return store.RoutedModel{}, false
	}
	type weightedCandidate struct {
		route  store.RoutedModel
		weight int
	}
	var candidates []weightedCandidate
	total := 0
	for _, entry := range entries {
		route, err := s.store.ResolveModel(ctx, entry.Route)
		if err != nil {
			continue
		}
		candidates = append(candidates, weightedCandidate{route: route, weight: entry.Weight})
		total += entry.Weight
	}
	if total <= 0 || len(candidates) == 0 {
		return store.RoutedModel{}, false
	}
	pick := randomWeightedPick(total)
	for _, candidate := range candidates {
		if pick < candidate.weight {
			return candidate.route, true
		}
		pick -= candidate.weight
	}
	return candidates[len(candidates)-1].route, true
}

func (s *Server) executeOpenAIPlan(ctx context.Context, candidates []store.RoutedModel, raw map[string]any, user store.User, key store.APIKey, requestID string, policy routeReliabilityPolicy) upstreamResult {
	var last upstreamResult
	attemptSeq := 0
	for idx, route := range candidates {
		if route.Provider.Type != "openai" && route.Provider.Type != "bedrock" {
			continue
		}
		if policy.HealthRoutingEnabled {
			if open, reason := providerCircuitOpen(route.Provider, time.Now().UTC()); open {
				last = openAIPlanError(route, http.StatusServiceUnavailable, reason)
				continue
			}
		}
		if idx > 0 {
			if blocked, status, reason, typ := s.checkRouteAdmission(ctx, user, key, route); blocked {
				last = openAIPlanError(route, status, reason)
				if typ == "permission_error" {
					continue
				}
				continue
			}
		}
		attempts := policy.RetryAttempts + 1
		for attempt := 0; attempt < attempts; attempt++ {
			attemptSeq++
			result := s.callOpenAINonStreaming(ctx, route, raw, policy.RequestTimeout)
			s.recordProviderOutcome(ctx, route.Provider.ID, result.Status, result.ErrorText)
			s.recordUsage(ctx, attemptRequestID(requestID, attemptSeq), user, key, route, result.Protocol, parseOpenAIUsage(result.Body), result.LatencyMS, result.Status, result.ErrorText)
			last = result
			if !providerStatusIsFailure(result.Status) {
				return result
			}
			if attempt+1 < attempts && providerStatusAllowsRetry(result.Status) {
				continue
			}
			break
		}
	}
	if last.Status == 0 {
		return upstreamResult{Status: http.StatusServiceUnavailable, ErrorText: "no available OpenAI-compatible provider candidate"}
	}
	return last
}

func (s *Server) executeAnthropicPlan(ctx context.Context, candidates []store.RoutedModel, raw map[string]any, inbound http.Header, user store.User, key store.APIKey, requestID string, policy routeReliabilityPolicy) upstreamResult {
	var last upstreamResult
	attemptSeq := 0
	for idx, route := range candidates {
		if route.Provider.Type != "anthropic" {
			continue
		}
		if policy.HealthRoutingEnabled {
			if open, reason := providerCircuitOpen(route.Provider, time.Now().UTC()); open {
				last = upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusServiceUnavailable, ErrorText: reason}
				continue
			}
		}
		if idx > 0 {
			if blocked, status, reason, _ := s.checkRouteAdmission(ctx, user, key, route); blocked {
				last = upstreamResult{Route: route, Protocol: "anthropic", Status: status, ErrorText: reason}
				continue
			}
		}
		attempts := policy.RetryAttempts + 1
		for attempt := 0; attempt < attempts; attempt++ {
			attemptSeq++
			result := s.callAnthropicNonStreaming(ctx, route, raw, inbound, policy.RequestTimeout)
			s.recordProviderOutcome(ctx, route.Provider.ID, result.Status, result.ErrorText)
			s.recordUsage(ctx, attemptRequestID(requestID, attemptSeq), user, key, route, "anthropic", parseAnthropicUsage(result.Body), result.LatencyMS, result.Status, result.ErrorText)
			last = result
			if !providerStatusIsFailure(result.Status) {
				return result
			}
			if attempt+1 < attempts && providerStatusAllowsRetry(result.Status) {
				continue
			}
			break
		}
	}
	if last.Status == 0 {
		return upstreamResult{Status: http.StatusServiceUnavailable, ErrorText: "no available Anthropic-compatible provider candidate"}
	}
	return last
}

func (s *Server) selectOpenAIStreamCandidate(ctx context.Context, candidates []store.RoutedModel, user store.User, key store.APIKey, policy routeReliabilityPolicy) (store.RoutedModel, int, string, string, bool) {
	for idx, route := range candidates {
		if route.Provider.Type == "bedrock" {
			continue
		}
		if route.Provider.Type != "openai" {
			continue
		}
		if policy.HealthRoutingEnabled {
			if open, reason := providerCircuitOpen(route.Provider, time.Now().UTC()); open {
				if idx == 0 && len(candidates) == 1 {
					return store.RoutedModel{}, http.StatusServiceUnavailable, reason, "provider_unavailable", false
				}
				continue
			}
		}
		if idx > 0 {
			if blocked, status, reason, typ := s.checkRouteAdmission(ctx, user, key, route); blocked {
				if idx == 0 {
					return store.RoutedModel{}, status, reason, typ, false
				}
				continue
			}
		}
		return route, 0, "", "", true
	}
	return store.RoutedModel{}, http.StatusServiceUnavailable, "no available streaming OpenAI-compatible provider candidate", "provider_unavailable", false
}

func (s *Server) checkRouteAdmission(ctx context.Context, user store.User, key store.APIKey, route store.RoutedModel) (bool, int, string, string) {
	if blocked, status, reason, typ := s.checkAPIKeyPolicy(ctx, key, route); blocked {
		return true, status, reason, typ
	}
	if blocked, reason := s.checkBudget(ctx, user, route.Model); blocked {
		return true, http.StatusPaymentRequired, reason, "insufficient_quota"
	}
	if blocked, status, reason, typ := s.checkRateLimits(ctx, user, route); blocked {
		return true, status, reason, typ
	}
	return false, 0, "", ""
}

func (s *Server) callOpenAINonStreaming(parent context.Context, route store.RoutedModel, raw map[string]any, timeout time.Duration) upstreamResult {
	if route.Provider.Type == "bedrock" {
		return s.callBedrockOpenAINonStreaming(parent, route, raw, timeout)
	}
	attemptRaw := cloneJSONMap(raw)
	attemptRaw["model"] = route.Model.ModelID
	body, err := json.Marshal(attemptRaw)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "openai", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	ctx, cancel := contextWithOptionalTimeout(parent, timeout)
	defer cancel()
	endpoint := strings.TrimRight(route.Provider.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return upstreamResult{Route: route, Protocol: "openai", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := providerAPIKey(route.Provider); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("User-Agent", "Phlox-GW/0.1")
	start := time.Now()
	resp, err := s.httpClient.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		return upstreamResult{Route: route, Protocol: "openai", Status: status, ErrorText: err.Error(), LatencyMS: latency}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "openai", Status: resp.StatusCode, Headers: resp.Header.Clone(), ErrorText: err.Error(), LatencyMS: latency}
	}
	result := upstreamResult{Route: route, Protocol: "openai", Status: resp.StatusCode, Headers: resp.Header.Clone(), Body: responseBody, LatencyMS: latency}
	if resp.StatusCode >= 400 {
		result.ErrorText = string(responseBody)
	}
	return result
}

func (s *Server) callAnthropicNonStreaming(parent context.Context, route store.RoutedModel, raw map[string]any, inbound http.Header, timeout time.Duration) upstreamResult {
	attemptRaw := cloneJSONMap(raw)
	attemptRaw["model"] = route.Model.ModelID
	body, err := json.Marshal(attemptRaw)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	ctx, cancel := contextWithOptionalTimeout(parent, timeout)
	defer cancel()
	endpoint := strings.TrimRight(route.Provider.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Phlox-GW/0.1")
	version := inbound.Get("anthropic-version")
	if version == "" {
		version = "2023-06-01"
	}
	req.Header.Set("anthropic-version", version)
	if beta := inbound.Get("anthropic-beta"); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	if apiKey := providerAPIKey(route.Provider); apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	start := time.Now()
	resp, err := s.httpClient.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		return upstreamResult{Route: route, Protocol: "anthropic", Status: status, ErrorText: err.Error(), LatencyMS: latency}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: resp.StatusCode, Headers: resp.Header.Clone(), ErrorText: err.Error(), LatencyMS: latency}
	}
	result := upstreamResult{Route: route, Protocol: "anthropic", Status: resp.StatusCode, Headers: resp.Header.Clone(), Body: responseBody, LatencyMS: latency}
	if resp.StatusCode >= 400 {
		result.ErrorText = string(responseBody)
	}
	return result
}

func (s *Server) callBedrockOpenAINonStreaming(parent context.Context, route store.RoutedModel, raw map[string]any, timeout time.Duration) upstreamResult {
	input, err := bedrockConverseInput(route.Model.ModelID, raw)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "bedrock", Status: http.StatusBadRequest, ErrorText: err.Error()}
	}
	ctx, cancel := contextWithOptionalTimeout(parent, timeout)
	defer cancel()
	client, err := s.bedrockClient(ctx, route.Provider)
	if err != nil {
		msg := "Bedrock configuration failed: " + err.Error()
		return upstreamResult{Route: route, Protocol: "bedrock", Status: http.StatusBadGateway, ErrorText: msg}
	}
	start := time.Now()
	output, err := client.Converse(ctx, input)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		status := bedrockErrorStatus(err)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		return upstreamResult{Route: route, Protocol: "bedrock", Status: status, ErrorText: bedrockErrorMessage(err), LatencyMS: latency}
	}
	response := openAIResponseFromBedrock(route, output)
	body, err := json.Marshal(response)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "bedrock", Status: http.StatusInternalServerError, ErrorText: err.Error(), LatencyMS: latency}
	}
	return upstreamResult{
		Route:     route,
		Protocol:  "bedrock",
		Status:    http.StatusOK,
		Headers:   http.Header{"Content-Type": []string{"application/json"}},
		Body:      body,
		LatencyMS: latency,
	}
}

func writeOpenAIResult(w http.ResponseWriter, result upstreamResult) {
	if result.Body == nil {
		openAIError(w, resultStatus(result), fallbackString(result.ErrorText, "provider unavailable"), "provider_error")
		return
	}
	writeUpstreamResult(w, result)
}

func writeAnthropicResult(w http.ResponseWriter, result upstreamResult) {
	if result.Body == nil {
		anthropicError(w, resultStatus(result), fallbackString(result.ErrorText, "provider unavailable"))
		return
	}
	writeUpstreamResult(w, result)
}

func writeUpstreamResult(w http.ResponseWriter, result upstreamResult) {
	for k, values := range result.Headers {
		if shouldProxyHeader(k) {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resultStatus(result))
	_, _ = w.Write(result.Body)
}

func resultStatus(result upstreamResult) int {
	if result.Status == 0 {
		return http.StatusServiceUnavailable
	}
	return result.Status
}

func openAIPlanError(route store.RoutedModel, status int, reason string) upstreamResult {
	return upstreamResult{Route: route, Protocol: route.Provider.Type, Status: status, ErrorText: reason}
}

func attemptRequestID(requestID string, attempt int) string {
	if attempt <= 1 {
		return requestID
	}
	return fmt.Sprintf("%s-attempt-%d", requestID, attempt)
}

func reliabilityPolicy(m store.Model) routeReliabilityPolicy {
	retries := m.RetryAttempts
	if retries < 0 {
		retries = 0
	}
	if retries > 5 {
		retries = 5
	}
	var timeout time.Duration
	if m.RequestTimeoutMS > 0 {
		timeout = time.Duration(m.RequestTimeoutMS) * time.Millisecond
		if timeout > 30*time.Minute {
			timeout = 30 * time.Minute
		}
	}
	return routeReliabilityPolicy{
		RetryAttempts:        retries,
		RequestTimeout:       timeout,
		HealthRoutingEnabled: m.HealthRoutingEnabled,
	}
}

func contextWithOptionalTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(parent, timeout)
	}
	return context.WithCancel(parent)
}

func cloneJSONMap(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out
}

func splitRouteList(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseWeightedRoutes(raw string) ([]weightedRoutePolicy, error) {
	parts := splitRouteList(raw)
	out := make([]weightedRoutePolicy, 0, len(parts))
	for _, part := range parts {
		route, weightText, ok := splitWeightedRoute(part)
		if !ok {
			return nil, fmt.Errorf("weighted route entries must use route weight or route=weight")
		}
		weight, err := strconv.Atoi(weightText)
		if err != nil || weight <= 0 {
			return nil, fmt.Errorf("weighted route weights must be positive integers")
		}
		out = append(out, weightedRoutePolicy{Route: route, Weight: weight})
	}
	return out, nil
}

func splitWeightedRoute(entry string) (string, string, bool) {
	if before, after, found := strings.Cut(entry, "="); found {
		route := strings.TrimSpace(before)
		weight := strings.TrimSpace(after)
		return route, weight, route != "" && weight != ""
	}
	fields := strings.Fields(entry)
	if len(fields) == 1 {
		return fields[0], "1", true
	}
	if len(fields) == 2 {
		return fields[0], fields[1], true
	}
	return "", "", false
}

func randomWeightedPick(total int) int {
	if total <= 1 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
	if err != nil {
		return int(time.Now().UnixNano() % int64(total))
	}
	return int(n.Int64())
}

func providerStatusAllowsRetry(statusCode int) bool {
	return statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func fallbackString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func (s *Server) proxyOpenAI(w http.ResponseWriter, r *http.Request, route store.RoutedModel, body []byte, raw map[string]any) (int, []byte, string) {
	endpoint := strings.TrimRight(route.Provider.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return http.StatusInternalServerError, nil, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := providerAPIKey(route.Provider); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("User-Agent", "Phlox-GW/0.1")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		openAIError(w, http.StatusBadGateway, err.Error(), "provider_error")
		return http.StatusBadGateway, nil, err.Error()
	}
	defer resp.Body.Close()

	for k, values := range resp.Header {
		if shouldProxyHeader(k) {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)

	if stream, _ := raw["stream"].(bool); stream {
		usage, n, err := proxyOpenAIStream(w, resp.Body)
		if err != nil {
			return resp.StatusCode, nil, err.Error()
		}
		return resp.StatusCode, openAIUsageBody(usage, n), ""
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err.Error()
	}
	_, _ = w.Write(responseBody)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, responseBody, string(responseBody)
	}
	return resp.StatusCode, responseBody, ""
}

func proxyOpenAIStream(w http.ResponseWriter, body io.Reader) (tokenUsage, int64, error) {
	var usage tokenUsage
	var written int64
	reader := bufio.NewReader(body)
	flusher, _ := w.(http.Flusher)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			n, writeErr := w.Write(line)
			written += int64(n)
			if writeErr != nil {
				return usage, written, writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
			if parsed := usageFromSSELine(line); parsed.Total > 0 || parsed.Input > 0 || parsed.Output > 0 {
				usage = parsed
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return usage, written, nil
		}
		return usage, written, err
	}
}

func usageFromSSELine(line []byte) tokenUsage {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return tokenUsage{}
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if payload == "" || payload == "[DONE]" {
		return tokenUsage{}
	}
	return parseOpenAIUsage([]byte(payload))
}

func openAIUsageBody(usage tokenUsage, streamedBytes int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"streamed_bytes": streamedBytes,
		"usage": map[string]int{
			"prompt_tokens":     usage.Input,
			"completion_tokens": usage.Output,
			"total_tokens":      usage.Total,
		},
	})
	return body
}

func (s *Server) proxyAnthropic(w http.ResponseWriter, r *http.Request, route store.RoutedModel, body []byte) (int, []byte, string) {
	endpoint := strings.TrimRight(route.Provider.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		anthropicError(w, http.StatusInternalServerError, err.Error())
		return http.StatusInternalServerError, nil, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Phlox-GW/0.1")
	version := r.Header.Get("anthropic-version")
	if version == "" {
		version = "2023-06-01"
	}
	req.Header.Set("anthropic-version", version)
	if beta := r.Header.Get("anthropic-beta"); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	if apiKey := providerAPIKey(route.Provider); apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		anthropicError(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, nil, err.Error()
	}
	defer resp.Body.Close()
	for k, values := range resp.Header {
		if shouldProxyHeader(k) {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err.Error()
	}
	_, _ = w.Write(responseBody)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, responseBody, string(responseBody)
	}
	return resp.StatusCode, responseBody, ""
}

func (s *Server) proxyBedrockOpenAI(w http.ResponseWriter, r *http.Request, route store.RoutedModel, raw map[string]any) (int, []byte, string) {
	input, err := bedrockConverseInput(route.Model.ModelID, raw)
	if err != nil {
		openAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return http.StatusBadRequest, nil, err.Error()
	}
	client, err := s.bedrockClient(r.Context(), route.Provider)
	if err != nil {
		msg := "Bedrock configuration failed: " + err.Error()
		openAIError(w, http.StatusBadGateway, msg, "provider_error")
		return http.StatusBadGateway, nil, msg
	}
	output, err := client.Converse(r.Context(), input)
	if err != nil {
		status := bedrockErrorStatus(err)
		msg := bedrockErrorMessage(err)
		openAIError(w, status, msg, "provider_error")
		return status, nil, msg
	}
	response := openAIResponseFromBedrock(route, output)
	body, err := json.Marshal(response)
	if err != nil {
		openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return http.StatusInternalServerError, nil, err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return http.StatusOK, body, ""
}

func (s *Server) bedrockClient(ctx context.Context, p store.Provider) (BedrockConverseClient, error) {
	if s.bedrockClientFactory != nil {
		return s.bedrockClientFactory(ctx, p)
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithHTTPClient(s.httpClient)}
	if strings.TrimSpace(p.AWSRegion) != "" {
		opts = append(opts, awsconfig.WithRegion(strings.TrimSpace(p.AWSRegion)))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return bedrockruntime.NewFromConfig(cfg), nil
}

func (s *Server) recordUsage(ctx context.Context, requestID string, user store.User, key store.APIKey, route store.RoutedModel, protocol string, usage tokenUsage, latencyMS int64, status int, errText string) {
	id, err := auth.RandomID("usage")
	if err != nil {
		s.logger.Warn("usage id failed", "error", err)
		return
	}
	rec := store.UsageRecord{
		ID:           id,
		RequestID:    requestID,
		UserID:       user.ID,
		Username:     user.Username,
		Department:   user.Department,
		APIKeyID:     key.ID,
		ProviderID:   route.Provider.ID,
		Model:        route.Model.Route,
		Protocol:     protocol,
		InputTokens:  usage.Input,
		OutputTokens: usage.Output,
		TotalTokens:  usage.Total,
		CostUSD:      store.Cost(usage.Input, usage.Output, route.Model),
		LatencyMS:    latencyMS,
		StatusCode:   status,
		ErrorText:    limitString(errText, 1000),
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.store.InsertUsage(ctx, rec); err != nil {
		s.logger.Warn("usage insert failed", "error", err)
	}
}

func (s *Server) checkBudget(ctx context.Context, user store.User, model store.Model) (bool, string) {
	status, err := s.store.BudgetStatus(ctx, user, store.IsPriced(model))
	if err != nil {
		return true, "budget check failed"
	}
	if status.Blocked {
		if status.Reason != "" {
			return true, status.Reason
		}
		return true, "budget exceeded"
	}
	return false, ""
}

func (s *Server) recordProviderOutcome(ctx context.Context, providerID string, statusCode int, errText string) {
	now := time.Now().UTC()
	if providerStatusIsFailure(statusCode) {
		if _, err := s.store.RecordProviderFailure(ctx, providerID, providerFailureThreshold, providerCircuitCooldown, now, errText); err != nil {
			s.logger.Warn("provider failure update failed", "provider_id", providerID, "error", err)
		}
		return
	}
	if err := s.store.RecordProviderSuccess(ctx, providerID, now); err != nil {
		s.logger.Warn("provider success update failed", "provider_id", providerID, "error", err)
	}
}

func (s *Server) recordProviderHealthCheck(ctx context.Context, providerID string, result modelHealthResult) {
	now := time.Now().UTC()
	if result.OK {
		if err := s.store.RecordProviderSuccess(ctx, providerID, now); err != nil {
			s.logger.Warn("provider health success update failed", "provider_id", providerID, "error", err)
		}
		return
	}
	reason := result.Error
	if reason == "" {
		reason = result.Snippet
	}
	if reason == "" {
		reason = fmt.Sprintf("health check failed with status %d", result.StatusCode)
	}
	if _, err := s.store.RecordProviderFailure(ctx, providerID, providerFailureThreshold, providerCircuitCooldown, now, reason); err != nil {
		s.logger.Warn("provider health failure update failed", "provider_id", providerID, "error", err)
	}
}

func providerStatusIsFailure(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode >= 500
}

func providerCircuitOpen(p store.Provider, now time.Time) (bool, string) {
	if p.CircuitOpenUntil == nil || !p.CircuitOpenUntil.After(now) {
		return false, ""
	}
	return true, "provider circuit is open until " + p.CircuitOpenUntil.UTC().Format(time.RFC3339)
}

func (s *Server) audit(r *http.Request, actor store.User, action, targetType, targetID, targetDisplay string, details map[string]any) {
	id, err := auth.RandomID("audit")
	if err != nil {
		s.logger.Warn("audit id failed", "error", err)
		return
	}
	detailText := "{}"
	if details != nil {
		if body, err := json.Marshal(details); err == nil {
			detailText = string(body)
		}
	}
	item := store.AuditLog{
		ID:            id,
		ActorUserID:   actor.ID,
		ActorUsername: actor.Username,
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		TargetDisplay: targetDisplay,
		Details:       limitString(detailText, 2000),
		IPAddress:     requestIP(r),
		UserAgent:     limitString(r.UserAgent(), 500),
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.store.InsertAuditLog(r.Context(), item); err != nil {
		s.logger.Warn("audit insert failed", "error", err, "action", action, "target_type", targetType, "target_id", targetID)
	}
}

func requestIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) checkAPIKeyPolicy(ctx context.Context, key store.APIKey, route store.RoutedModel) (bool, int, string, string) {
	if !apiKeyAllowsModel(key, route.Model) {
		return true, http.StatusForbidden, "API key is not allowed to use this model", "permission_error"
	}
	if store.IsPriced(route.Model) && key.BudgetUSD > 0 {
		if blocked, reason := s.checkAPIKeyMonthlyBudget(ctx, key); blocked {
			return true, http.StatusPaymentRequired, reason, "insufficient_quota"
		}
	}
	if key.RPMLimit > 0 || key.TPMLimit > 0 {
		usage, err := s.store.APIKeyWindowUsage(ctx, key.ID, time.Now().UTC().Add(-time.Minute))
		if err != nil {
			return true, http.StatusInternalServerError, "rate limit check failed", "server_error"
		}
		if key.RPMLimit > 0 && usage.Requests >= int64(key.RPMLimit) {
			return true, http.StatusTooManyRequests, "API key requests per minute limit exceeded", "rate_limit_exceeded"
		}
		if key.TPMLimit > 0 && usage.TotalTokens >= int64(key.TPMLimit) {
			return true, http.StatusTooManyRequests, "API key tokens per minute limit exceeded", "rate_limit_exceeded"
		}
	}
	return false, 0, "", ""
}

func (s *Server) checkRateLimits(ctx context.Context, user store.User, route store.RoutedModel) (bool, int, string, string) {
	limits, err := s.store.ApplicableRateLimits(ctx, user, route)
	if err != nil {
		return true, http.StatusInternalServerError, "rate limit check failed", "server_error"
	}
	if len(limits) == 0 {
		return false, 0, "", ""
	}
	since := time.Now().UTC().Add(-time.Minute)
	for _, limit := range limits {
		if limit.RPMLimit <= 0 && limit.TPMLimit <= 0 {
			continue
		}
		usage, err := s.store.RateLimitWindowUsage(ctx, limit, since)
		if err != nil {
			return true, http.StatusInternalServerError, "rate limit check failed", "server_error"
		}
		if limit.RPMLimit > 0 && usage.Requests >= int64(limit.RPMLimit) {
			return true, http.StatusTooManyRequests, fmt.Sprintf("%s %s requests per minute limit exceeded", limit.ScopeType, limit.ScopeValue), "rate_limit_exceeded"
		}
		if limit.TPMLimit > 0 && usage.TotalTokens >= int64(limit.TPMLimit) {
			return true, http.StatusTooManyRequests, fmt.Sprintf("%s %s tokens per minute limit exceeded", limit.ScopeType, limit.ScopeValue), "rate_limit_exceeded"
		}
	}
	return false, 0, "", ""
}

func (s *Server) checkAPIKeyMonthlyBudget(ctx context.Context, key store.APIKey) (bool, string) {
	start, end := currentMonthBounds(time.Now().UTC())
	spend, err := s.store.APIKeyMonthlySpend(ctx, key.ID, start, end)
	if err != nil {
		return true, "API key budget check failed"
	}
	if spend >= key.BudgetUSD {
		return true, "API key monthly budget exceeded"
	}
	return false, ""
}

func apiKeyAllowsModel(key store.APIKey, model store.Model) bool {
	items := splitPolicyList(key.ModelAllowlist)
	if len(items) == 0 {
		return true
	}
	for _, item := range items {
		if item == model.Route || item == model.ModelID || item == model.ID {
			return true
		}
	}
	return false
}

func splitPolicyList(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func currentMonthBounds(t time.Time) (time.Time, time.Time) {
	start := time.Date(t.UTC().Year(), t.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

var errOIDCProvisioningDisabled = errors.New("oidc provisioning disabled")
var errOIDCUserDisabled = errors.New("oidc user disabled")

type defaultOIDCAuthenticator struct {
	cfg        config.OIDCConfig
	httpClient *http.Client
	mu         sync.Mutex
	provider   *oidc.Provider
}

func newDefaultOIDCAuthenticator(cfg config.OIDCConfig, httpClient *http.Client) *defaultOIDCAuthenticator {
	return &defaultOIDCAuthenticator{cfg: cfg, httpClient: httpClient}
}

func (a *defaultOIDCAuthenticator) AuthCodeURL(ctx context.Context, state, nonce, redirectURL string) (string, error) {
	provider, err := a.providerFor(ctx)
	if err != nil {
		return "", err
	}
	cfg := a.oauth2Config(provider, redirectURL)
	return cfg.AuthCodeURL(state, oidc.Nonce(nonce)), nil
}

func (a *defaultOIDCAuthenticator) Exchange(ctx context.Context, code, nonce, redirectURL string) (OIDCClaims, error) {
	provider, err := a.providerFor(ctx)
	if err != nil {
		return OIDCClaims{}, err
	}
	ctx = a.clientContext(ctx)
	cfg := a.oauth2Config(provider, redirectURL)
	oauthToken, err := cfg.Exchange(ctx, code)
	if err != nil {
		return OIDCClaims{}, err
	}
	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return OIDCClaims{}, errors.New("OIDC provider did not return an id_token")
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: a.cfg.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return OIDCClaims{}, err
	}
	if idToken.Nonce != nonce {
		return OIDCClaims{}, errors.New("OIDC nonce mismatch")
	}
	values := map[string]any{}
	if err := idToken.Claims(&values); err != nil {
		return OIDCClaims{}, err
	}
	return OIDCClaims{Subject: idToken.Subject, Values: values}, nil
}

func (a *defaultOIDCAuthenticator) providerFor(ctx context.Context) (*oidc.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provider != nil {
		return a.provider, nil
	}
	provider, err := oidc.NewProvider(a.clientContext(ctx), a.cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	a.provider = provider
	return provider, nil
}

func (a *defaultOIDCAuthenticator) clientContext(ctx context.Context) context.Context {
	if a.httpClient == nil {
		return ctx
	}
	return oidc.ClientContext(ctx, a.httpClient)
}

func (a *defaultOIDCAuthenticator) oauth2Config(provider *oidc.Provider, redirectURL string) oauth2.Config {
	return oauth2.Config{
		ClientID:     a.cfg.ClientID,
		ClientSecret: a.cfg.ClientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       a.cfg.Scopes,
	}
}

type oidcLoginState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	ReturnTo string `json:"return_to"`
	Expires  int64  `json:"expires"`
}

const oidcStateCookieName = "phlox_gw_oidc_state"

func (s *Server) userFromOIDCClaims(ctx context.Context, claims OIDCClaims) (store.User, error) {
	username := firstClaimString(claims.Values, s.cfg.OIDC.UsernameClaim, "preferred_username", "upn", "email")
	if username == "" {
		username = claims.Subject
	}
	if username == "" {
		return store.User{}, errors.New("OIDC subject or username claim is required")
	}
	email := firstClaimString(claims.Values, "email", "preferred_username", "upn")
	displayName := firstClaimString(claims.Values, "name", "given_name")
	department := firstClaimString(claims.Values, s.cfg.OIDC.DepartmentClaim)
	if displayName == "" {
		displayName = username
	}
	existing, err := s.store.GetUserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		if !s.cfg.OIDC.AutoProvision {
			return store.User{}, errOIDCProvisioningDisabled
		}
		id, idErr := auth.RandomID("user")
		if idErr != nil {
			return store.User{}, idErr
		}
		user := store.User{
			ID:           id,
			Username:     username,
			Email:        email,
			DisplayName:  displayName,
			Department:   department,
			Role:         s.oidcRoleForClaims("", claims.Values),
			PasswordHash: "",
			AuthProvider: "oidc",
			IsActive:     true,
		}
		if err := s.store.CreateUser(ctx, user); err != nil {
			return store.User{}, err
		}
		return s.store.GetUserByID(ctx, user.ID)
	}
	if err != nil {
		return store.User{}, err
	}
	if !existing.IsActive {
		return store.User{}, errOIDCUserDisabled
	}
	existing.Email = email
	existing.DisplayName = displayName
	existing.Department = department
	existing.Role = s.oidcRoleForClaims(existing.Role, claims.Values)
	if existing.AuthProvider == "" || existing.AuthProvider == "oidc" {
		existing.AuthProvider = "oidc"
	}
	if err := s.store.UpdateFederatedUser(ctx, existing); err != nil {
		return store.User{}, err
	}
	return s.store.GetUserByID(ctx, existing.ID)
}

func (s *Server) oidcRoleForClaims(existingRole string, values map[string]any) string {
	role := existingRole
	if role == "" {
		role = "user"
	}
	if groupsIntersect(claimStrings(values[s.cfg.OIDC.GroupsClaim]), s.cfg.OIDC.AdminGroups) {
		return "admin"
	}
	return role
}

func firstClaimString(values map[string]any, names ...string) string {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for _, value := range claimStrings(values[name]) {
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func claimStrings(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{strings.TrimSpace(v)}
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := item.(string); ok {
				if text = strings.TrimSpace(text); text != "" {
					out = append(out, text)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func groupsIntersect(claimGroups, adminGroups []string) bool {
	if len(claimGroups) == 0 || len(adminGroups) == 0 {
		return false
	}
	set := map[string]struct{}{}
	for _, group := range claimGroups {
		set[strings.ToLower(group)] = struct{}{}
	}
	for _, group := range adminGroups {
		if _, ok := set[strings.ToLower(strings.TrimSpace(group))]; ok {
			return true
		}
	}
	return false
}

func (s *Server) issueSessionToken(user store.User) (string, error) {
	now := time.Now().UTC()
	claims := auth.Claims{
		Subject:  user.ID,
		Username: user.Username,
		Role:     user.Role,
		IssuedAt: now.Unix(),
		Expires:  now.Add(12 * time.Hour).Unix(),
	}
	return auth.SignSession(claims, s.cfg.SessionSecret)
}

func (s *Server) oidcRedirectURL(r *http.Request) string {
	if s.cfg.OIDC.RedirectURL != "" {
		return s.cfg.OIDC.RedirectURL
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/auth/oidc/callback"
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func cleanReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return "/"
	}
	return value
}

func randomOIDCToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *Server) signOIDCState(state oidcLoginState) (string, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	payloadText := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(payloadText))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadText + "." + signature, nil
}

func (s *Server) readOIDCState(r *http.Request) (oidcLoginState, bool) {
	cookie, err := r.Cookie(oidcStateCookieName)
	if err != nil || cookie.Value == "" {
		return oidcLoginState{}, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return oidcLoginState{}, false
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return oidcLoginState{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return oidcLoginState{}, false
	}
	var state oidcLoginState
	if err := json.Unmarshal(payload, &state); err != nil {
		return oidcLoginState{}, false
	}
	return state, true
}

func (s *Server) oidcStateCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    value,
		Path:     "/api/auth/oidc",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	}
}

func respondOIDCSuccess(w http.ResponseWriter, token, returnTo string) {
	tokenJSON, _ := json.Marshal(token)
	returnJSON, _ := json.Marshal(cleanReturnTo(returnTo))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Signing in...</title></head><body><script>localStorage.setItem('phlox_gw_token', %s); window.location.replace(%s);</script></body></html>`, tokenJSON, returnJSON)
}

func respondOIDCError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	payload, _ := json.Marshal(message)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Sign in failed</title></head><body><main><h1>Sign in failed</h1><p id="message"></p><script>document.getElementById('message').textContent = %s;</script></main></body></html>`, payload)
}

type modelHealthResult struct {
	OK         bool   `json:"ok"`
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
	Protocol   string `json:"protocol"`
	StatusCode int    `json:"status_code"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
	Snippet    string `json:"snippet,omitempty"`
}

func (s *Server) runModelHealthCheck(parent context.Context, route store.RoutedModel) modelHealthResult {
	result := modelHealthResult{
		ProviderID: route.Provider.ID,
		Model:      route.Model.Route,
		Protocol:   route.Provider.Type,
	}
	if route.Provider.Type == "bedrock" {
		return s.runBedrockHealthCheck(parent, route, result)
	}

	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	var endpoint string
	var body []byte
	var err error
	var req *http.Request
	switch route.Provider.Type {
	case "openai":
		endpoint = strings.TrimRight(route.Provider.BaseURL, "/") + "/chat/completions"
		body, err = json.Marshal(map[string]any{
			"model":       route.Model.ModelID,
			"messages":    []map[string]string{{"role": "user", "content": "Reply with exactly: OK"}},
			"temperature": 0,
			"max_tokens":  8,
			"stream":      false,
		})
		if err != nil {
			result.Error = err.Error()
			return result
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if apiKey := providerAPIKey(route.Provider); apiKey != "" {
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
		}
	case "anthropic":
		endpoint = strings.TrimRight(route.Provider.BaseURL, "/") + "/v1/messages"
		body, err = json.Marshal(map[string]any{
			"model":      route.Model.ModelID,
			"max_tokens": 8,
			"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: OK"}},
		})
		if err != nil {
			result.Error = err.Error()
			return result
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("anthropic-version", "2023-06-01")
			if apiKey := providerAPIKey(route.Provider); apiKey != "" {
				req.Header.Set("x-api-key", apiKey)
			}
		}
	default:
		result.Error = "unsupported provider type"
		return result
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("User-Agent", "Phlox-GW/0.1")

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	result.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Snippet = limitString(string(responseBody), 800)
	result.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
	if !result.OK && result.Error == "" {
		result.Error = result.Snippet
	}
	return result
}

func (s *Server) runBedrockHealthCheck(parent context.Context, route store.RoutedModel, result modelHealthResult) modelHealthResult {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	input, err := bedrockConverseInput(route.Model.ModelID, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Reply with exactly: OK"},
		},
		"max_tokens":  float64(8),
		"temperature": float64(0),
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	client, err := s.bedrockClient(ctx, route.Provider)
	if err != nil {
		result.StatusCode = http.StatusBadGateway
		result.Error = err.Error()
		return result
	}
	start := time.Now()
	output, err := client.Converse(ctx, input)
	result.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		result.StatusCode = bedrockErrorStatus(err)
		result.Error = bedrockErrorMessage(err)
		return result
	}
	result.StatusCode = http.StatusOK
	result.Snippet = limitString(bedrockOutputText(output), 800)
	result.OK = true
	return result
}

func (s *Server) providerFromRequest(w http.ResponseWriter, r *http.Request, pathID string) (store.Provider, bool, bool) {
	var req struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		BaseURL   string `json:"base_url"`
		APIKey    string `json:"api_key"`
		APIKeyEnv string `json:"api_key_env"`
		AWSRegion string `json:"aws_region"`
		Enabled   bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return store.Provider{}, false, false
	}
	id := strings.TrimSpace(req.ID)
	if pathID != "" {
		id = pathID
	}
	if id == "" || strings.TrimSpace(req.Name) == "" {
		respondError(w, http.StatusBadRequest, "provider id and name are required")
		return store.Provider{}, false, false
	}
	if req.Type != "openai" && req.Type != "anthropic" && req.Type != "bedrock" {
		respondError(w, http.StatusBadRequest, "provider type must be openai, anthropic, or bedrock")
		return store.Provider{}, false, false
	}
	if req.Type != "bedrock" && strings.TrimSpace(req.BaseURL) == "" {
		respondError(w, http.StatusBadRequest, "base_url is required for OpenAI and Anthropic-compatible providers")
		return store.Provider{}, false, false
	}
	return store.Provider{
		ID:        id,
		Name:      strings.TrimSpace(req.Name),
		Type:      req.Type,
		BaseURL:   strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		APIKey:    req.APIKey,
		APIKeyEnv: strings.TrimSpace(req.APIKeyEnv),
		AWSRegion: strings.TrimSpace(req.AWSRegion),
		Enabled:   req.Enabled,
	}, strings.TrimSpace(req.APIKey) != "", true
}

func (s *Server) modelFromRequest(w http.ResponseWriter, r *http.Request, pathID string) (store.Model, bool) {
	var req struct {
		ID                   string  `json:"id"`
		ProviderID           string  `json:"provider_id"`
		ModelID              string  `json:"model_id"`
		Route                string  `json:"route"`
		DisplayName          string  `json:"display_name"`
		InputCostPerMillion  float64 `json:"input_cost_per_million"`
		OutputCostPerMillion float64 `json:"output_cost_per_million"`
		ContextWindow        int     `json:"context_window"`
		SupportsStreaming    bool    `json:"supports_streaming"`
		Enabled              bool    `json:"enabled"`
		FallbackRoutes       string  `json:"fallback_routes"`
		WeightedRoutes       string  `json:"weighted_routes"`
		RetryAttempts        int     `json:"retry_attempts"`
		RequestTimeoutMS     int     `json:"request_timeout_ms"`
		HealthRoutingEnabled *bool   `json:"health_routing_enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return store.Model{}, false
	}
	id := strings.TrimSpace(req.ID)
	if pathID != "" {
		id = pathID
	}
	if id == "" {
		var err error
		id, err = auth.RandomID("model")
		if err != nil {
			respondError(w, http.StatusInternalServerError, "could not allocate model id")
			return store.Model{}, false
		}
	}
	providerID := strings.TrimSpace(req.ProviderID)
	modelID := strings.TrimSpace(req.ModelID)
	if providerID == "" || modelID == "" {
		respondError(w, http.StatusBadRequest, "provider_id and model_id are required")
		return store.Model{}, false
	}
	if req.InputCostPerMillion < 0 || req.OutputCostPerMillion < 0 {
		respondError(w, http.StatusBadRequest, "model prices cannot be negative")
		return store.Model{}, false
	}
	if req.RetryAttempts < 0 || req.RetryAttempts > 5 {
		respondError(w, http.StatusBadRequest, "retry_attempts must be between 0 and 5")
		return store.Model{}, false
	}
	if req.RequestTimeoutMS < 0 {
		respondError(w, http.StatusBadRequest, "request_timeout_ms cannot be negative")
		return store.Model{}, false
	}
	if _, err := parseWeightedRoutes(req.WeightedRoutes); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return store.Model{}, false
	}
	route := strings.TrimSpace(req.Route)
	if route == "" {
		route = providerID + "/" + modelID
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = modelID
	}
	healthRoutingEnabled := true
	if req.HealthRoutingEnabled != nil {
		healthRoutingEnabled = *req.HealthRoutingEnabled
	}
	return store.Model{
		ID:                   id,
		ProviderID:           providerID,
		ModelID:              modelID,
		Route:                route,
		DisplayName:          displayName,
		InputCostPerMillion:  req.InputCostPerMillion,
		OutputCostPerMillion: req.OutputCostPerMillion,
		ContextWindow:        req.ContextWindow,
		SupportsStreaming:    req.SupportsStreaming,
		Enabled:              req.Enabled,
		FallbackRoutes:       req.FallbackRoutes,
		WeightedRoutes:       req.WeightedRoutes,
		RetryAttempts:        req.RetryAttempts,
		RequestTimeoutMS:     req.RequestTimeoutMS,
		HealthRoutingEnabled: healthRoutingEnabled,
	}, true
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

func providerAuditDetails(p store.Provider, directSecretUpdated bool) map[string]any {
	return map[string]any{
		"id":                    p.ID,
		"name":                  p.Name,
		"type":                  p.Type,
		"base_url":              p.BaseURL,
		"api_key_env":           p.APIKeyEnv,
		"direct_secret_updated": directSecretUpdated,
		"aws_region":            p.AWSRegion,
		"enabled":               p.Enabled,
	}
}

func modelAuditDetails(m store.Model) map[string]any {
	return map[string]any{
		"id":                      m.ID,
		"provider_id":             m.ProviderID,
		"model_id":                m.ModelID,
		"route":                   m.Route,
		"display_name":            m.DisplayName,
		"input_cost_per_million":  m.InputCostPerMillion,
		"output_cost_per_million": m.OutputCostPerMillion,
		"context_window":          m.ContextWindow,
		"supports_streaming":      m.SupportsStreaming,
		"enabled":                 m.Enabled,
		"fallback_routes":         m.FallbackRoutes,
		"weighted_routes":         m.WeightedRoutes,
		"retry_attempts":          m.RetryAttempts,
		"request_timeout_ms":      m.RequestTimeoutMS,
		"health_routing_enabled":  m.HealthRoutingEnabled,
	}
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

func rateLimitAuditDetails(rl store.RateLimit) map[string]any {
	return map[string]any{
		"scope_type":  rl.ScopeType,
		"scope_value": rl.ScopeValue,
		"rpm_limit":   rl.RPMLimit,
		"tpm_limit":   rl.TPMLimit,
		"is_active":   rl.IsActive,
	}
}

func budgetDisplay(b store.Budget) string {
	return b.ScopeType + ":" + b.ScopeValue
}

func rateLimitDisplay(rl store.RateLimit) string {
	return rl.ScopeType + ":" + rl.ScopeValue
}

func (s *Server) requireSession(next func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			respondError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := auth.VerifySession(token, s.cfg.SessionSecret, time.Now().UTC())
		if err != nil {
			respondError(w, http.StatusUnauthorized, "invalid session")
			return
		}
		user, err := s.store.GetUserByID(r.Context(), claims.Subject)
		if err != nil || !user.IsActive {
			respondError(w, http.StatusUnauthorized, "invalid session")
			return
		}
		next(w, r, user)
	}
}

func (s *Server) requireAdmin(next func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return s.requireSession(func(w http.ResponseWriter, r *http.Request, user store.User) {
		if user.Role != "admin" {
			respondError(w, http.StatusForbidden, "admin required")
			return
		}
		next(w, r, user)
	})
}

func (s *Server) requireAPIKey(next func(http.ResponseWriter, *http.Request, store.User, store.APIKey)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			openAIError(w, http.StatusUnauthorized, "missing API key", "authentication_error")
			return
		}
		user, key, err := s.store.ResolveAPIKey(r.Context(), auth.HashAPIKey(token), time.Now().UTC())
		if err != nil {
			openAIError(w, http.StatusUnauthorized, "invalid API key", "authentication_error")
			return
		}
		next(w, r, user, key)
	}
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	f, err := s.frontend.Open(path)
	if err != nil {
		path = "index.html"
		f, err = s.frontend.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	http.ServeContent(w, r, path, time.Time{}, readSeeker{f})
}

func readObjectBody(w http.ResponseWriter, r *http.Request, openAIShape bool) ([]byte, map[string]any, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		respondError(w, http.StatusBadRequest, "could not read body")
		return nil, nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		if openAIShape {
			openAIError(w, http.StatusBadRequest, "invalid JSON", "invalid_request_error")
		} else {
			anthropicError(w, http.StatusBadRequest, "invalid JSON")
		}
		return nil, nil, false
	}
	return body, raw, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func parseAPIKeyExpiresAt(w http.ResponseWriter, raw string, requireFuture bool) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		respondError(w, http.StatusBadRequest, "expires_at must be RFC3339")
		return nil, false
	}
	t = t.UTC()
	if requireFuture && !t.After(time.Now().UTC()) {
		respondError(w, http.StatusBadRequest, "expires_at must be in the future")
		return nil, false
	}
	return &t, true
}

func respondJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}

func openAIError(w http.ResponseWriter, status int, message, typ string) {
	respondJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": typ}})
}

func anthropicError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": message}})
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func publicUser(u store.User) map[string]any {
	return map[string]any{
		"id":            u.ID,
		"username":      u.Username,
		"email":         u.Email,
		"display_name":  u.DisplayName,
		"department":    u.Department,
		"role":          u.Role,
		"auth_provider": u.AuthProvider,
		"is_active":     u.IsActive,
	}
}

type tokenUsage struct {
	Input  int
	Output int
	Total  int
}

func bedrockConverseInput(modelID string, raw map[string]any) (*bedrockruntime.ConverseInput, error) {
	messagesRaw, ok := raw["messages"].([]any)
	if !ok || len(messagesRaw) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}
	var messages []types.Message
	var system []types.SystemContentBlock
	for _, item := range messagesRaw {
		msg, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("messages entries must be objects")
		}
		role, _ := msg["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))
		text, err := openAIMessageText(msg["content"])
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		switch role {
		case "system", "developer":
			system = append(system, &types.SystemContentBlockMemberText{Value: text})
		case "user":
			messages = append(messages, types.Message{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: text}},
			})
		case "assistant":
			messages = append(messages, types.Message{
				Role:    types.ConversationRoleAssistant,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: text}},
			})
		default:
			return nil, fmt.Errorf("Bedrock adapter supports user, assistant, system, and developer text messages only; got role %q", role)
		}
	}
	if len(messages) == 0 {
		return nil, errors.New("at least one non-empty user or assistant message is required")
	}
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(modelID),
		Messages: messages,
		System:   system,
	}
	inference := types.InferenceConfiguration{}
	if n, ok, err := int32Field(raw, "max_tokens"); err != nil {
		return nil, err
	} else if ok {
		inference.MaxTokens = aws.Int32(n)
	} else if n, ok, err := int32Field(raw, "max_completion_tokens"); err != nil {
		return nil, err
	} else if ok {
		inference.MaxTokens = aws.Int32(n)
	}
	if f, ok, err := float32Field(raw, "temperature"); err != nil {
		return nil, err
	} else if ok {
		inference.Temperature = aws.Float32(f)
	}
	if f, ok, err := float32Field(raw, "top_p"); err != nil {
		return nil, err
	} else if ok {
		inference.TopP = aws.Float32(f)
	}
	stops, err := stopSequences(raw["stop"])
	if err != nil {
		return nil, err
	}
	inference.StopSequences = stops
	if inference.MaxTokens != nil || inference.Temperature != nil || inference.TopP != nil || len(inference.StopSequences) > 0 {
		input.InferenceConfig = &inference
	}
	return input, nil
}

func openAIMessageText(content any) (string, error) {
	switch v := content.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []any:
		var parts []string
		for _, item := range v {
			part, ok := item.(map[string]any)
			if !ok {
				return "", errors.New("message content parts must be objects")
			}
			typ, _ := part["type"].(string)
			if typ == "" || typ == "text" {
				text, _ := part["text"].(string)
				if text != "" {
					parts = append(parts, text)
				}
				continue
			}
			return "", fmt.Errorf("Bedrock adapter only supports text content parts; got %q", typ)
		}
		return strings.Join(parts, "\n"), nil
	default:
		return "", errors.New("message content must be a string or text content parts")
	}
}

func int32Field(raw map[string]any, key string) (int32, bool, error) {
	value, ok := raw[key]
	if !ok || value == nil {
		return 0, false, nil
	}
	number, ok := value.(float64)
	if !ok || number < 0 || math.Trunc(number) != number || number > math.MaxInt32 {
		return 0, false, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return int32(number), true, nil
}

func float32Field(raw map[string]any, key string) (float32, bool, error) {
	value, ok := raw[key]
	if !ok || value == nil {
		return 0, false, nil
	}
	number, ok := value.(float64)
	if !ok {
		return 0, false, fmt.Errorf("%s must be a number", key)
	}
	return float32(number), true, nil
}

func stopSequences(value any) ([]string, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return []string{v}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("stop must be a string or array of strings")
			}
			if text != "" {
				out = append(out, text)
			}
		}
		return out, nil
	default:
		return nil, errors.New("stop must be a string or array of strings")
	}
}

func openAIResponseFromBedrock(route store.RoutedModel, output *bedrockruntime.ConverseOutput) map[string]any {
	usage := bedrockUsage(output)
	return map[string]any{
		"id":      requestID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   route.Model.Route,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": bedrockOutputText(output),
			},
			"finish_reason": bedrockFinishReason(output.StopReason),
		}},
		"usage": map[string]int{
			"prompt_tokens":     usage.Input,
			"completion_tokens": usage.Output,
			"total_tokens":      usage.Total,
		},
	}
}

func bedrockUsage(output *bedrockruntime.ConverseOutput) tokenUsage {
	if output == nil || output.Usage == nil {
		return tokenUsage{}
	}
	usage := tokenUsage{}
	if output.Usage.InputTokens != nil {
		usage.Input = int(*output.Usage.InputTokens)
	}
	if output.Usage.OutputTokens != nil {
		usage.Output = int(*output.Usage.OutputTokens)
	}
	if output.Usage.TotalTokens != nil {
		usage.Total = int(*output.Usage.TotalTokens)
	}
	if usage.Total == 0 {
		usage.Total = usage.Input + usage.Output
	}
	return usage
}

func bedrockOutputText(output *bedrockruntime.ConverseOutput) string {
	if output == nil {
		return ""
	}
	message, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(message.Value.Content))
	for _, content := range message.Value.Content {
		if text, ok := content.(*types.ContentBlockMemberText); ok && text.Value != "" {
			parts = append(parts, text.Value)
		}
	}
	return strings.Join(parts, "\n")
}

func bedrockFinishReason(reason types.StopReason) string {
	switch reason {
	case types.StopReasonMaxTokens, types.StopReasonModelContextWindowExceeded:
		return "length"
	case types.StopReasonContentFiltered, types.StopReasonGuardrailIntervened:
		return "content_filter"
	case types.StopReasonToolUse, types.StopReasonMalformedToolUse:
		return "tool_calls"
	default:
		return "stop"
	}
}

func bedrockErrorStatus(err error) int {
	var responseError interface{ HTTPStatusCode() int }
	if errors.As(err, &responseError) {
		status := responseError.HTTPStatusCode()
		if status >= 400 && status <= 599 {
			return status
		}
	}
	return http.StatusBadGateway
}

func bedrockErrorMessage(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorMessage() != "" {
			return apiErr.ErrorCode() + ": " + apiErr.ErrorMessage()
		}
		if apiErr.ErrorCode() != "" {
			return apiErr.ErrorCode()
		}
	}
	return err.Error()
}

func parseOpenAIUsage(body []byte) tokenUsage {
	var resp struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &resp)
	return tokenUsage{Input: resp.Usage.PromptTokens, Output: resp.Usage.CompletionTokens, Total: resp.Usage.TotalTokens}
}

func parseAnthropicUsage(body []byte) tokenUsage {
	var resp struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &resp)
	total := resp.Usage.InputTokens + resp.Usage.OutputTokens
	return tokenUsage{Input: resp.Usage.InputTokens, Output: resp.Usage.OutputTokens, Total: total}
}

func providerAPIKey(p store.Provider) string {
	if p.APIKeyEnv != "" {
		if value := os.Getenv(p.APIKeyEnv); value != "" {
			return value
		}
	}
	return p.APIKey
}

func shouldProxyHeader(name string) bool {
	switch strings.ToLower(name) {
	case "content-type", "cache-control", "x-request-id":
		return true
	default:
		return false
	}
}

func requestID() string {
	id, err := auth.RandomID("req")
	if err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return id
}

func limitString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func secretMarker(env string) string {
	if env == "" {
		return ""
	}
	return " (secret set)"
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

type readSeeker struct {
	fs.File
}

func (r readSeeker) Seek(offset int64, whence int) (int64, error) {
	if seeker, ok := r.File.(io.Seeker); ok {
		return seeker.Seek(offset, whence)
	}
	return 0, errors.New("file is not seekable")
}

var _ = sql.ErrNoRows
