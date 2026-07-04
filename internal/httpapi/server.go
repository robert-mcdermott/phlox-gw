package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
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
	"net/http"
	urlpkg "net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	smithybearer "github.com/aws/smithy-go/auth/bearer"
	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
	"github.com/robert-mcdermott/phlox-gw/internal/telemetry"
)

type Options struct {
	Config               config.Config
	Store                *store.Store
	Frontend             fs.FS
	Logger               *slog.Logger
	HTTPClient           *http.Client
	BedrockClientFactory BedrockClientFactory
	OIDCAuthenticator    OIDCAuthenticator
	Telemetry            *telemetry.Telemetry
}

type Server struct {
	cfg                  config.Config
	store                *store.Store
	logger               *slog.Logger
	httpClient           *http.Client
	frontend             fs.FS
	bedrockClientFactory BedrockClientFactory
	oidcAuthenticator    OIDCAuthenticator
	telemetry            *telemetry.Telemetry
	startedAt            time.Time
	hostname             string
	clusterMu            sync.RWMutex
	lastHeartbeatAt      time.Time
	lastHeartbeatErr     string
}

const providerFailureThreshold = 3
const providerCircuitCooldown = 5 * time.Minute

type BedrockConverseClient interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(context.Context, *bedrockruntime.ConverseStreamInput, ...func(*bedrockruntime.Options)) (BedrockConverseEventStream, error)
}

type BedrockClientFactory func(context.Context, store.Provider) (BedrockConverseClient, error)

type BedrockConverseEventStream interface {
	Events() <-chan types.ConverseStreamOutput
	Close() error
	Err() error
}

type awsBedrockConverseClient struct {
	client *bedrockruntime.Client
}

func (c awsBedrockConverseClient) Converse(ctx context.Context, input *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return c.client.Converse(ctx, input, optFns...)
}

func (c awsBedrockConverseClient) ConverseStream(ctx context.Context, input *bedrockruntime.ConverseStreamInput, optFns ...func(*bedrockruntime.Options)) (BedrockConverseEventStream, error) {
	output, err := c.client.ConverseStream(ctx, input, optFns...)
	if err != nil {
		return nil, err
	}
	return output.GetStream(), nil
}

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
	if opts.Telemetry == nil {
		tel, err := telemetry.New(context.Background(), opts.Config.Telemetry, opts.Logger)
		if err != nil {
			return nil, err
		}
		opts.Telemetry = tel
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
		telemetry:            opts.Telemetry,
	}
	if s.oidcAuthenticator == nil && s.cfg.OIDC.Enabled {
		s.oidcAuthenticator = newDefaultOIDCAuthenticator(s.cfg.OIDC, s.httpClient)
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	s.hostname = hostname
	s.startedAt = time.Now().UTC()
	s.applyRuntimeDefaults()
	if err := s.updateClusterHeartbeat(context.Background(), "ready"); err != nil {
		return nil, fmt.Errorf("register cluster node: %w", err)
	}
	// The heartbeat runs in every deployment mode so this node's own lease
	// stays fresh; only readiness gating on it is cluster-specific.
	go s.clusterHeartbeatLoop()

	mux := http.NewServeMux()
	if s.telemetry.MetricsEnabled() {
		mux.Handle("GET "+s.telemetry.MetricsPath(), s.telemetry.MetricsHandler())
	} else {
		mux.HandleFunc("GET "+s.telemetry.MetricsPath(), http.NotFound)
	}
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /ready", s.ready)
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/ready", s.ready)
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
	mux.HandleFunc("POST /api/admin/playground/chat", s.requireAdmin(s.playgroundChat))
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
	mux.HandleFunc("GET /api/admin/cluster/status", s.requireAdmin(s.clusterStatus))
	mux.HandleFunc("GET /api/admin/cluster/nodes", s.requireAdmin(s.clusterNodes))
	mux.HandleFunc("GET /api/admin/config/export", s.requireAdmin(s.adminConfigExport))
	mux.HandleFunc("GET /api/admin/request-log", s.requireAdmin(s.requestLogSearch))
	mux.HandleFunc("GET /api/admin/request-log/export.csv", s.requireAdmin(s.requestLogCSV))
	mux.HandleFunc("GET /api/admin/guardrails", s.requireAdmin(s.guardrailPolicy))
	mux.HandleFunc("PUT /api/admin/guardrails", s.requireAdmin(s.updateGuardrailPolicy))
	mux.HandleFunc("POST /api/admin/guardrails/test", s.requireAdmin(s.previewGuardrailPolicy))
	mux.HandleFunc("GET /api/admin/usage/summary", s.requireAdmin(s.adminUsage))
	mux.HandleFunc("GET /api/admin/usage/timeseries", s.requireAdmin(s.adminUsageTimeSeries))
	mux.HandleFunc("GET /api/admin/usage/drilldowns", s.requireAdmin(s.adminUsageDrilldowns))
	mux.HandleFunc("GET /api/admin/budgets/burndown", s.requireAdmin(s.adminBudgetBurnDown))
	mux.HandleFunc("GET /api/admin/chargeback", s.requireAdmin(s.chargebackReport))
	mux.HandleFunc("GET /api/admin/chargeback/export.csv", s.requireAdmin(s.chargebackCSV))
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
		ctx, span := s.telemetry.StartHTTPRequestFromHeaders(r.Context(), r.Header, r.Method, r.URL.Path)
		r = r.WithContext(ctx)
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		duration := time.Since(start)
		route := requestMetricRoute(r)
		s.telemetry.ObserveHTTPRequest(r.Method, route, rw.status, duration)
		s.telemetry.FinishHTTPRequest(span, r.Method, route, rw.status, duration)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/anthropic/") {
			s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rw.status, "duration_ms", duration.Milliseconds())
		}
	})
}

func requestMetricRoute(r *http.Request) string {
	if r.Pattern != "" {
		if _, route, ok := strings.Cut(r.Pattern, " "); ok {
			return route
		}
		return r.Pattern
	}
	path := strings.TrimSpace(r.URL.Path)
	if path == "" {
		return "/"
	}
	return path
}

func (s *Server) applyRuntimeDefaults() {
	if s.cfg.Database.Driver == "" {
		s.cfg.Database.Driver = s.store.Driver()
	}
	if s.cfg.Deployment.Mode == "" {
		if s.cfg.Database.Driver == "postgres" {
			s.cfg.Deployment.Mode = "single-postgres"
		} else {
			s.cfg.Deployment.Mode = "single-sqlite"
		}
	}
	if strings.TrimSpace(s.cfg.Deployment.InstanceID) == "" {
		host := strings.TrimSpace(s.hostname)
		if host == "" {
			host = "localhost"
		}
		s.cfg.Deployment.InstanceID = fmt.Sprintf("%s-%d", strings.NewReplacer(" ", "-", ".", "-").Replace(strings.ToLower(host)), os.Getpid())
	}
	if s.cfg.Deployment.HeartbeatInterval <= 0 {
		s.cfg.Deployment.HeartbeatInterval = 10 * time.Second
	}
	if s.cfg.Deployment.NodeStaleAfter <= s.cfg.Deployment.HeartbeatInterval {
		s.cfg.Deployment.NodeStaleAfter = 45 * time.Second
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"name":            "phlox-gw",
		"time":            time.Now().UTC(),
		"deployment_mode": s.cfg.Deployment.Mode,
		"instance_id":     s.cfg.Deployment.InstanceID,
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		respondJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"error":  err.Error(),
		})
		return
	}
	if s.cfg.Deployment.Mode == "cluster-postgres" {
		last, lastErr := s.clusterHeartbeatState()
		if lastErr != "" {
			respondJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "unavailable",
				"error":  lastErr,
			})
			return
		}
		if last.IsZero() || time.Since(last) > s.cfg.Deployment.NodeStaleAfter {
			respondJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "unavailable",
				"error":  "cluster heartbeat is stale",
			})
			return
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"status":          "ready",
		"deployment_mode": s.cfg.Deployment.Mode,
		"instance_id":     s.cfg.Deployment.InstanceID,
	})
}

// clusterNodeRetention is how long an unrefreshed node row survives before
// garbage collection. It is much longer than the stale threshold so operators
// can still see recently departed nodes before they age out of the registry.

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
		providers[i].HasAWSSecret = providers[i].AWSSecretAccessKey != ""
		providers[i].HasBedrockAPIKey = providers[i].BedrockAPIKey != ""
	}
	respondJSON(w, http.StatusOK, providers)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request, admin store.User) {
	p, secrets, ok := s.providerFromRequest(w, r, "")
	if !ok {
		return
	}
	if !secrets.APIKey {
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
	s.audit(r, admin, "provider.create", "provider", p.ID, p.Name, providerAuditDetails(p, secrets))
	respondJSON(w, http.StatusCreated, p)
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, admin store.User) {
	p, secrets, ok := s.providerFromRequest(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.store.UpdateProvider(r.Context(), p, secrets); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "provider not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "provider.update", "provider", p.ID, p.Name, providerAuditDetails(p, secrets))
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

const (
	playgroundMaxMessages  = 50
	playgroundMaxChars     = 64 * 1024
	playgroundDefaultToken = 1024
	playgroundMaxTokensCap = 8192
)

type playgroundChatResult struct {
	OK            bool   `json:"ok"`
	ProviderID    string `json:"provider_id"`
	ProviderType  string `json:"provider_type"`
	ModelRoute    string `json:"model_route"`
	UpstreamModel string `json:"upstream_model"`
	StatusCode    int    `json:"status_code"`
	LatencyMS     int64  `json:"latency_ms"`
	Content       string `json:"content,omitempty"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	TotalTokens   int    `json:"total_tokens"`
	Error         string `json:"error,omitempty"`
}

// playgroundChat sends an admin-authored conversation to a routed model so
// providers and models can be validated from the dashboard. Playground
// traffic bypasses API keys, budgets, rate limits, and guardrails, and is
// not recorded in the usage ledger; each call is audit-logged instead.
func (s *Server) playgroundChat(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req struct {
		Route     string `json:"route"`
		System    string `json:"system"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Route) == "" {
		respondError(w, http.StatusBadRequest, "route is required")
		return
	}
	if len(req.Messages) == 0 {
		respondError(w, http.StatusBadRequest, "messages must be a non-empty array")
		return
	}
	if len(req.Messages) > playgroundMaxMessages {
		respondError(w, http.StatusBadRequest, "conversation has too many messages")
		return
	}
	system := strings.TrimSpace(req.System)
	totalChars := len(system)
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "user" && role != "assistant" {
			respondError(w, http.StatusBadRequest, "message roles must be user or assistant")
			return
		}
		if strings.TrimSpace(m.Content) == "" {
			respondError(w, http.StatusBadRequest, "message content cannot be empty")
			return
		}
		totalChars += len(m.Content)
		messages = append(messages, map[string]any{"role": role, "content": m.Content})
	}
	if totalChars > playgroundMaxChars {
		respondError(w, http.StatusBadRequest, "conversation is too large")
		return
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = playgroundDefaultToken
	}
	if maxTokens > playgroundMaxTokensCap {
		maxTokens = playgroundMaxTokensCap
	}
	route, err := s.store.ResolveModel(r.Context(), strings.TrimSpace(req.Route))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "enabled model route not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := s.runPlaygroundChat(r.Context(), route, system, messages, maxTokens)
	s.audit(r, admin, "playground.chat", "model", route.Model.ID, route.Model.Route, map[string]any{
		"ok":          result.OK,
		"status_code": result.StatusCode,
		"latency_ms":  result.LatencyMS,
		"provider_id": result.ProviderID,
		"messages":    len(messages),
	})
	respondJSON(w, http.StatusOK, result)
}

func (s *Server) runPlaygroundChat(parent context.Context, route store.RoutedModel, system string, messages []map[string]any, maxTokens int) playgroundChatResult {
	result := playgroundChatResult{
		ProviderID:    route.Provider.ID,
		ProviderType:  route.Provider.Type,
		ModelRoute:    route.Model.Route,
		UpstreamModel: route.Model.ModelID,
	}
	timeout := 60 * time.Second
	if route.Model.RequestTimeoutMS > 0 {
		timeout = time.Duration(route.Model.RequestTimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if route.Provider.Type == "bedrock" {
		return s.runBedrockPlaygroundChat(ctx, route, system, messages, maxTokens, result)
	}

	protocol := providerProtocol(route.Provider)
	var endpoint string
	var payload map[string]any
	switch protocol {
	case "openai":
		endpoint = openAIChatEndpoint(route.Provider, route.Model.ModelID)
		withSystem := messages
		if system != "" {
			withSystem = append([]map[string]any{{"role": "system", "content": system}}, messages...)
		}
		payload = map[string]any{
			"model":      route.Model.ModelID,
			"messages":   withSystem,
			"max_tokens": maxTokens,
			"stream":     false,
		}
	case "anthropic":
		endpoint = anthropicMessagesEndpoint(route.Provider)
		payload = map[string]any{
			"model":      route.Model.ModelID,
			"max_tokens": maxTokens,
			"messages":   messages,
		}
		if system != "" {
			payload["system"] = system
		}
	default:
		result.Error = "unsupported provider type"
		return result
	}
	// Reasoning-family models reject max_tokens and pinned temperature;
	// retry with adjusted parameters when the upstream names the offender.
	for attempt := 0; ; attempt++ {
		body, err := json.Marshal(payload)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			result.Error = err.Error()
			return result
		}
		req.Header.Set("Content-Type", "application/json")
		if protocol == "anthropic" {
			req.Header.Set("anthropic-version", "2023-06-01")
			setAnthropicAuthHeader(req, route.Provider)
		} else {
			setOpenAIAuthHeader(req, route.Provider)
		}
		req.Header.Set("User-Agent", "Phlox-GW/0.1")

		start := time.Now()
		resp, err := s.httpClient.Do(req)
		result.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		result.StatusCode = resp.StatusCode
		responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if protocol == "openai" && attempt < 2 {
				if adjusted, ok := openAIPayloadForUnsupportedParams(payload, resp.StatusCode, string(responseBody)); ok {
					payload = adjusted
					continue
				}
			}
			result.Error = limitString(string(responseBody), 2000)
			return result
		}
		var usage tokenUsage
		if protocol == "anthropic" {
			result.Content = anthropicResponseText(responseBody)
			usage = parseAnthropicUsage(responseBody)
		} else {
			result.Content = openAIResponseText(responseBody)
			usage = parseOpenAIUsage(responseBody)
		}
		result.InputTokens = usage.Input
		result.OutputTokens = usage.Output
		result.TotalTokens = usage.Total
		result.OK = true
		return result
	}
}

func (s *Server) runBedrockPlaygroundChat(ctx context.Context, route store.RoutedModel, system string, messages []map[string]any, maxTokens int, result playgroundChatResult) playgroundChatResult {
	raw := make([]any, 0, len(messages)+1)
	if system != "" {
		raw = append(raw, map[string]any{"role": "system", "content": system})
	}
	for _, m := range messages {
		raw = append(raw, m)
	}
	input, err := bedrockConverseInput(route.Model.ModelID, map[string]any{
		"messages":   raw,
		"max_tokens": float64(maxTokens),
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
	result.Content = bedrockOutputText(output)
	usage := bedrockUsage(output)
	result.InputTokens = usage.Input
	result.OutputTokens = usage.Output
	result.TotalTokens = usage.Total
	result.OK = true
	return result
}

// openAIResponseText extracts the assistant text from a non-streaming OpenAI
// chat completion body, falling back to reasoning output for models that
// return their answer there.
func openAIResponseText(body []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	choices, _ := raw["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if text, err := openAIMessageText(message["content"]); err == nil && strings.TrimSpace(text) != "" {
		return text
	}
	reasoning, _ := firstPresent(message, "reasoning", "reasoning_content").(string)
	return reasoning
}

func anthropicResponseText(body []byte) string {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	var parts []string
	for _, block := range resp.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
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

func (s *Server) guardrailPolicy(w http.ResponseWriter, r *http.Request, _ store.User) {
	policy, err := s.store.GetGuardrailPolicy(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, policy)
}

func (s *Server) updateGuardrailPolicy(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req store.GuardrailPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !validGuardrailAction(req.InputAction) || !validGuardrailAction(req.OutputAction) {
		respondError(w, http.StatusBadRequest, "guardrail actions must be off, redact, or block")
		return
	}
	if strings.TrimSpace(req.RedactionText) == "" {
		req.RedactionText = "[REDACTED]"
	}
	req.StreamingBlockMode = "reject"
	if err := validateGuardrailCustomPatterns(req.CustomPatterns); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	policy, err := s.store.UpdateGuardrailPolicy(r.Context(), req)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "guardrail.update", "guardrail_policy", policy.ID, "default", guardrailAuditDetails(policy))
	respondJSON(w, http.StatusOK, policy)
}

type guardrailPreviewRequest struct {
	Policy store.GuardrailPolicy `json:"policy"`
	Text   string                `json:"text"`
	Phase  string                `json:"phase"`
}

type guardrailPreviewResponse struct {
	Phase    string   `json:"phase"`
	Action   string   `json:"action"`
	Findings []string `json:"findings"`
	Redacted bool     `json:"redacted"`
	Blocked  bool     `json:"blocked"`
	Output   string   `json:"output"`
}

func (s *Server) previewGuardrailPolicy(w http.ResponseWriter, r *http.Request, _ store.User) {
	var req guardrailPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !validGuardrailAction(req.Policy.InputAction) || !validGuardrailAction(req.Policy.OutputAction) {
		respondError(w, http.StatusBadRequest, "guardrail actions must be off, redact, or block")
		return
	}
	if strings.TrimSpace(req.Policy.RedactionText) == "" {
		req.Policy.RedactionText = "[REDACTED]"
	}
	req.Policy.StreamingBlockMode = "reject"
	if err := validateGuardrailCustomPatterns(req.Policy.CustomPatterns); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	phase := strings.ToLower(strings.TrimSpace(req.Phase))
	if phase != "output" {
		phase = "input"
	}
	action := guardrailAction(req.Policy, phase)
	result := applyGuardrailToText(req.Text, req.Policy, action, false)
	respondJSON(w, http.StatusOK, guardrailPreviewResponse{
		Phase:    phase,
		Action:   action,
		Findings: result.Findings,
		Redacted: result.Redacted,
		Blocked:  result.Blocked,
		Output:   result.Text,
	})
}

func (s *Server) requestLogSearch(w http.ResponseWriter, r *http.Request, _ store.User) {
	query, ok := parseRequestLogQuery(w, r, 100)
	if !ok {
		return
	}
	result, err := s.store.SearchRequestLogs(r.Context(), query)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (s *Server) requestLogCSV(w http.ResponseWriter, r *http.Request, _ store.User) {
	query, ok := parseRequestLogQuery(w, r, 100000)
	if !ok {
		return
	}
	rows, err := s.store.RequestLogExport(r.Context(), query)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filename := "phlox-gw-request-log-" + time.Now().UTC().Format("20060102") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"created_at", "request_id", "username", "department", "api_key_id", "api_key_prefix", "api_key_name", "provider_id", "provider_type", "model_route", "upstream_model_id", "protocol", "method", "endpoint", "streaming", "input_tokens", "output_tokens", "total_tokens", "cost_usd", "latency_ms", "status_code", "error", "client_ip", "user_agent"})
	for _, row := range rows {
		_ = cw.Write([]string{
			row.CreatedAt.Format(time.RFC3339Nano),
			row.RequestID,
			row.Username,
			row.Department,
			row.APIKeyID,
			row.APIKeyPrefix,
			row.APIKeyName,
			row.ProviderID,
			row.ProviderType,
			row.ModelRoute,
			row.UpstreamModelID,
			row.Protocol,
			row.Method,
			row.Endpoint,
			strconv.FormatBool(row.Streaming),
			strconv.Itoa(row.InputTokens),
			strconv.Itoa(row.OutputTokens),
			strconv.Itoa(row.TotalTokens),
			strconv.FormatFloat(row.CostUSD, 'f', 6, 64),
			strconv.FormatInt(row.LatencyMS, 10),
			strconv.Itoa(row.StatusCode),
			row.ErrorText,
			row.ClientIP,
			row.UserAgent,
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.logger.Warn("request log csv write failed", "error", err)
	}
}

func parseRequestLogQuery(w http.ResponseWriter, r *http.Request, defaultLimit int) (store.RequestLogQuery, bool) {
	values := r.URL.Query()
	query := store.RequestLogQuery{
		Search:       strings.TrimSpace(values.Get("q")),
		Username:     strings.TrimSpace(values.Get("username")),
		Department:   strings.TrimSpace(values.Get("department")),
		APIKeyID:     strings.TrimSpace(values.Get("api_key_id")),
		ProviderID:   strings.TrimSpace(values.Get("provider_id")),
		ProviderType: strings.TrimSpace(values.Get("provider_type")),
		ModelRoute:   strings.TrimSpace(values.Get("model")),
		Protocol:     strings.TrimSpace(values.Get("protocol")),
		Endpoint:     strings.TrimSpace(values.Get("endpoint")),
		Status:       strings.TrimSpace(values.Get("status")),
		Limit:        defaultLimit,
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return store.RequestLogQuery{}, false
		}
		query.Limit = limit
	}
	if raw := strings.TrimSpace(values.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			respondError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return store.RequestLogQuery{}, false
		}
		query.Offset = offset
	}
	if raw := strings.TrimSpace(values.Get("streaming")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "streaming must be true or false")
			return store.RequestLogQuery{}, false
		}
		query.Streaming = &parsed
	}
	if raw := strings.TrimSpace(values.Get("from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "from must be RFC3339")
			return store.RequestLogQuery{}, false
		}
		query.From = &parsed
	}
	if raw := strings.TrimSpace(values.Get("to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "to must be RFC3339")
			return store.RequestLogQuery{}, false
		}
		query.To = &parsed
	}
	if query.From == nil {
		if raw := strings.TrimSpace(values.Get("days")); raw != "" {
			days, err := strconv.Atoi(raw)
			if err != nil || days <= 0 {
				respondError(w, http.StatusBadRequest, "days must be a positive integer")
				return store.RequestLogQuery{}, false
			}
			from := time.Now().UTC().AddDate(0, 0, -days)
			query.From = &from
		}
	}
	return query, true
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

func chargebackMonthFromRequest(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("month"))
	if raw == "" {
		return time.Now().UTC(), true
	}
	month, err := time.Parse("2006-01", raw)
	if err != nil {
		respondError(w, http.StatusBadRequest, "month must be formatted YYYY-MM")
		return time.Time{}, false
	}
	return month, true
}

func (s *Server) chargebackReport(w http.ResponseWriter, r *http.Request, _ store.User) {
	month, ok := chargebackMonthFromRequest(w, r)
	if !ok {
		return
	}
	report, err := s.store.ChargebackReport(r.Context(), month, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, report)
}

func (s *Server) chargebackCSV(w http.ResponseWriter, r *http.Request, _ store.User) {
	month, ok := chargebackMonthFromRequest(w, r)
	if !ok {
		return
	}
	report, err := s.store.ChargebackReport(r.Context(), month, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filename := "phlox-gw-chargeback-" + report.Month + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"month", "department", "user_id", "username", "requests", "input_tokens", "output_tokens", "total_tokens", "cost_usd", "department_budget_usd"})
	for _, dept := range report.Departments {
		budget := strconv.FormatFloat(dept.BudgetUSD, 'f', 2, 64)
		if dept.BudgetUSD <= 0 {
			budget = ""
		}
		for _, user := range dept.Users {
			_ = cw.Write([]string{
				report.Month,
				dept.Department,
				user.UserID,
				user.Username,
				strconv.FormatInt(user.Requests, 10),
				strconv.FormatInt(user.InputTokens, 10),
				strconv.FormatInt(user.OutputTokens, 10),
				strconv.FormatInt(user.TotalTokens, 10),
				strconv.FormatFloat(user.CostUSD, 'f', 6, 64),
				budget,
			})
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.logger.Warn("chargeback csv write failed", "error", err)
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
	guardrails, err := s.store.GetGuardrailPolicy(r.Context())
	if err != nil {
		openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	raw, guardrailEval := applyGuardrailToMap(guardrails, "input", raw)
	if guardrailEval.Blocked {
		openAIError(w, http.StatusBadRequest, guardrailReason("input", guardrailEval.Findings), "content_policy_violation")
		return
	}
	if stream, _ := raw["stream"].(bool); stream && guardrailRejectsStreamingOutput(guardrails) {
		openAIError(w, http.StatusBadRequest, "streaming requests are blocked while output guardrail action is block", "content_policy_violation")
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
	if protocol := providerProtocol(route.Provider); protocol != "openai" && protocol != "bedrock" {
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
		eventMeta := requestEventFromHTTP(r, true)
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
			statusCode, responseBody, errText = s.proxyBedrockOpenAIStream(w, r, selected, raw, guardrails)
		} else {
			attemptRaw := cloneJSONMap(raw)
			attemptRaw["model"] = selected.Model.ModelID
			ensureStreamUsageOption(selected.Provider, attemptRaw)
			body, _ := json.Marshal(attemptRaw)
			statusCode, responseBody, errText = s.proxyOpenAI(w, r, selected, body, attemptRaw, guardrails)
		}
		latency := time.Since(start).Milliseconds()
		usage := parseOpenAIUsage(responseBody)
		s.recordProviderOutcome(r.Context(), selected.Provider.ID, statusCode, errText)
		s.recordUsage(r.Context(), requestID, user, key, selected, providerProtocol(selected.Provider), usage, latency, statusCode, errText, eventMeta)
		return
	}
	result := s.executeOpenAIPlan(r.Context(), candidates, raw, user, key, requestID, policy, requestEventFromHTTP(r, false), guardrails)
	writeOpenAIResult(w, result)
}

func (s *Server) anthropicMessages(w http.ResponseWriter, r *http.Request, user store.User, key store.APIKey) {
	_, raw, ok := readObjectBody(w, r, false)
	if !ok {
		return
	}
	guardrails, err := s.store.GetGuardrailPolicy(r.Context())
	if err != nil {
		anthropicError(w, http.StatusInternalServerError, err.Error())
		return
	}
	raw, guardrailEval := applyGuardrailToMap(guardrails, "input", raw)
	if guardrailEval.Blocked {
		anthropicError(w, http.StatusBadRequest, guardrailReason("input", guardrailEval.Findings))
		return
	}
	if stream, _ := raw["stream"].(bool); stream && guardrailRejectsStreamingOutput(guardrails) {
		anthropicError(w, http.StatusBadRequest, "streaming requests are blocked while output guardrail action is block")
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
	if protocol := providerProtocol(route.Provider); protocol != "anthropic" && protocol != "openai" && protocol != "bedrock" {
		anthropicError(w, http.StatusNotImplemented, "model is not on a supported provider")
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
	policy := reliabilityPolicy(plan.Requested.Model)
	requestID := requestID()
	if stream, _ := raw["stream"].(bool); stream {
		eventMeta := requestEventFromHTTP(r, true)
		selected, status, reason, ok := s.selectAnthropicStreamCandidate(r.Context(), candidates, user, key, policy)
		if !ok {
			anthropicError(w, status, reason)
			return
		}
		start := time.Now()
		var statusCode int
		var responseBody []byte
		var errText string
		if providerProtocol(selected.Provider) == "anthropic" {
			attemptRaw := cloneJSONMap(raw)
			attemptRaw["model"] = selected.Model.ModelID
			body, err := json.Marshal(attemptRaw)
			if err != nil {
				anthropicError(w, http.StatusInternalServerError, err.Error())
				return
			}
			statusCode, responseBody, errText = s.proxyAnthropicStream(w, r, selected, body, guardrails)
		} else if providerProtocol(selected.Provider) == "openai" {
			statusCode, responseBody, errText = s.proxyAnthropicViaOpenAIStream(w, r, selected, raw, guardrails)
		} else {
			statusCode, responseBody, errText = s.proxyAnthropicViaBedrockStream(w, r, selected, raw, guardrails)
		}
		latency := time.Since(start).Milliseconds()
		s.recordProviderOutcome(r.Context(), selected.Provider.ID, statusCode, errText)
		s.recordUsage(r.Context(), requestID, user, key, selected, "anthropic", parseAnthropicUsage(responseBody), latency, statusCode, errText, eventMeta)
		return
	}
	result := s.executeAnthropicPlan(r.Context(), candidates, raw, r.Header, user, key, requestID, policy, requestEventFromHTTP(r, false), guardrails)
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

func (s *Server) executeOpenAIPlan(ctx context.Context, candidates []store.RoutedModel, raw map[string]any, user store.User, key store.APIKey, requestID string, policy routeReliabilityPolicy, eventMeta requestEventMeta, guardrails store.GuardrailPolicy) upstreamResult {
	var last upstreamResult
	attemptSeq := 0
	for idx, route := range candidates {
		if protocol := providerProtocol(route.Provider); protocol != "openai" && protocol != "bedrock" {
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
			usage := parseOpenAIUsage(result.Body)
			if result.Protocol == "openai" {
				usage = openAIUsageWithFallback(raw, result.Body, result.Status)
			}
			result = applyOutputGuardrailsToResult(guardrails, result)
			s.recordUsage(ctx, attemptRequestID(requestID, attemptSeq), user, key, route, result.Protocol, usage, result.LatencyMS, result.Status, result.ErrorText, eventMeta)
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

func (s *Server) executeAnthropicPlan(ctx context.Context, candidates []store.RoutedModel, raw map[string]any, inbound http.Header, user store.User, key store.APIKey, requestID string, policy routeReliabilityPolicy, eventMeta requestEventMeta, guardrails store.GuardrailPolicy) upstreamResult {
	var last upstreamResult
	attemptSeq := 0
	for idx, route := range candidates {
		if protocol := providerProtocol(route.Provider); protocol != "anthropic" && protocol != "openai" && protocol != "bedrock" {
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
			var result upstreamResult
			if providerProtocol(route.Provider) == "anthropic" {
				result = s.callAnthropicNonStreaming(ctx, route, raw, inbound, policy.RequestTimeout)
			} else {
				result = s.callAnthropicViaOpenAINonStreaming(ctx, route, raw, policy.RequestTimeout)
			}
			s.recordProviderOutcome(ctx, route.Provider.ID, result.Status, result.ErrorText)
			usage := parseAnthropicUsage(result.Body)
			result = applyOutputGuardrailsToResult(guardrails, result)
			s.recordUsage(ctx, attemptRequestID(requestID, attemptSeq), user, key, route, "anthropic", usage, result.LatencyMS, result.Status, result.ErrorText, eventMeta)
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
		return upstreamResult{Status: http.StatusServiceUnavailable, ErrorText: "no available provider candidate for Anthropic-compatible request"}
	}
	return last
}

func (s *Server) selectOpenAIStreamCandidate(ctx context.Context, candidates []store.RoutedModel, user store.User, key store.APIKey, policy routeReliabilityPolicy) (store.RoutedModel, int, string, string, bool) {
	for idx, route := range candidates {
		if protocol := providerProtocol(route.Provider); protocol != "openai" && protocol != "bedrock" {
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
	return store.RoutedModel{}, http.StatusServiceUnavailable, "no available streaming OpenAI-compatible or Bedrock provider candidate", "provider_unavailable", false
}

func (s *Server) selectAnthropicStreamCandidate(ctx context.Context, candidates []store.RoutedModel, user store.User, key store.APIKey, policy routeReliabilityPolicy) (store.RoutedModel, int, string, bool) {
	for idx, route := range candidates {
		if protocol := providerProtocol(route.Provider); protocol != "anthropic" && protocol != "openai" && protocol != "bedrock" {
			continue
		}
		if policy.HealthRoutingEnabled {
			if open, reason := providerCircuitOpen(route.Provider, time.Now().UTC()); open {
				if idx == 0 && len(candidates) == 1 {
					return store.RoutedModel{}, http.StatusServiceUnavailable, reason, false
				}
				continue
			}
		}
		if idx > 0 {
			if blocked, status, reason, _ := s.checkRouteAdmission(ctx, user, key, route); blocked {
				if idx == 0 {
					return store.RoutedModel{}, status, reason, false
				}
				continue
			}
		}
		return route, 0, "", true
	}
	return store.RoutedModel{}, http.StatusServiceUnavailable, "no available streaming Anthropic-compatible provider candidate", false
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
	ctx, finishTrace := s.upstreamTrace(ctx, route, "openai", "chat.completions")
	endpoint := openAIChatEndpoint(route.Provider, route.Model.ModelID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
		return upstreamResult{Route: route, Protocol: "openai", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	setOpenAIAuthHeader(req, route.Provider)
	req.Header.Set("User-Agent", "Phlox-GW/0.1")
	start := time.Now()
	resp, err := s.httpClient.Do(req)
	latencyDuration := time.Since(start)
	latency := latencyDuration.Milliseconds()
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		finishTrace(status, err.Error(), latencyDuration)
		return upstreamResult{Route: route, Protocol: "openai", Status: status, ErrorText: err.Error(), LatencyMS: latency}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), latencyDuration)
		return upstreamResult{Route: route, Protocol: "openai", Status: resp.StatusCode, Headers: resp.Header.Clone(), ErrorText: err.Error(), LatencyMS: latency}
	}
	result := upstreamResult{Route: route, Protocol: "openai", Status: resp.StatusCode, Headers: resp.Header.Clone(), Body: responseBody, LatencyMS: latency}
	if resp.StatusCode >= 400 {
		result.ErrorText = string(responseBody)
	}
	finishTrace(result.Status, result.ErrorText, latencyDuration)
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
	ctx, finishTrace := s.upstreamTrace(ctx, route, "anthropic", "messages")
	endpoint := anthropicMessagesEndpoint(route.Provider)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
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
	setAnthropicAuthHeader(req, route.Provider)
	start := time.Now()
	resp, err := s.httpClient.Do(req)
	latencyDuration := time.Since(start)
	latency := latencyDuration.Milliseconds()
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		finishTrace(status, err.Error(), latencyDuration)
		return upstreamResult{Route: route, Protocol: "anthropic", Status: status, ErrorText: err.Error(), LatencyMS: latency}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), latencyDuration)
		return upstreamResult{Route: route, Protocol: "anthropic", Status: resp.StatusCode, Headers: resp.Header.Clone(), ErrorText: err.Error(), LatencyMS: latency}
	}
	result := upstreamResult{Route: route, Protocol: "anthropic", Status: resp.StatusCode, Headers: resp.Header.Clone(), Body: responseBody, LatencyMS: latency}
	if resp.StatusCode >= 400 {
		result.ErrorText = string(responseBody)
	}
	finishTrace(result.Status, result.ErrorText, latencyDuration)
	return result
}

func (s *Server) callAnthropicViaOpenAINonStreaming(parent context.Context, route store.RoutedModel, raw map[string]any, timeout time.Duration) upstreamResult {
	openAIRaw, err := anthropicRequestToOpenAI(raw)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusBadRequest, ErrorText: err.Error()}
	}
	result := s.callOpenAINonStreaming(parent, route, openAIRaw, timeout)
	// Anthropic requests always carry max_tokens, which reasoning-family
	// models reject in favor of max_completion_tokens; retry the authored
	// payload with adjusted parameters.
	for attempt := 0; attempt < 2; attempt++ {
		adjusted, ok := openAIPayloadForUnsupportedParams(openAIRaw, result.Status, string(result.Body))
		if !ok {
			break
		}
		openAIRaw = adjusted
		result = s.callOpenAINonStreaming(parent, route, openAIRaw, timeout)
	}
	if result.Status < 200 || result.Status >= 300 {
		return upstreamResult{
			Route:     route,
			Protocol:  "anthropic",
			Status:    result.Status,
			ErrorText: providerErrorText(result.Body, result.ErrorText),
			LatencyMS: result.LatencyMS,
		}
	}
	body, err := anthropicResponseFromOpenAI(route, result.Body)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusBadGateway, ErrorText: "could not translate OpenAI-compatible response: " + err.Error(), LatencyMS: result.LatencyMS}
	}
	return upstreamResult{
		Route:     route,
		Protocol:  "anthropic",
		Status:    result.Status,
		Headers:   http.Header{"Content-Type": []string{"application/json"}},
		Body:      body,
		LatencyMS: result.LatencyMS,
	}
}

func anthropicRequestToOpenAI(raw map[string]any) (map[string]any, error) {
	out := map[string]any{
		"model":  raw["model"],
		"stream": false,
	}
	messages, err := anthropicMessagesToOpenAI(raw)
	if err != nil {
		return nil, err
	}
	out["messages"] = messages
	if v, ok := raw["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	for _, key := range []string{"temperature", "top_p", "presence_penalty", "frequency_penalty"} {
		if v, ok := raw[key]; ok {
			out[key] = v
		}
	}
	if stop, ok := raw["stop_sequences"]; ok {
		out["stop"] = stop
	}
	if tools, ok := anthropicToolsToOpenAI(raw["tools"]); ok {
		out["tools"] = tools
	}
	if choice, ok := anthropicToolChoiceToOpenAI(raw["tool_choice"]); ok {
		out["tool_choice"] = choice
	}
	return out, nil
}

func anthropicMessagesToOpenAI(raw map[string]any) ([]any, error) {
	messages := make([]any, 0)
	if system, ok := raw["system"]; ok {
		if content, ok := anthropicContentToOpenAI(system); ok {
			messages = append(messages, map[string]any{"role": "system", "content": content})
		}
	}
	rawMessages, ok := raw["messages"].([]any)
	if !ok || len(rawMessages) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}
	for _, item := range rawMessages {
		msg, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("messages entries must be objects")
		}
		role, _ := msg["role"].(string)
		if role == "assistant" {
			converted, err := anthropicAssistantMessageToOpenAI(msg)
			if err != nil {
				return nil, err
			}
			messages = append(messages, converted)
			continue
		}
		converted, err := anthropicUserMessageToOpenAI(msg)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted...)
	}
	return messages, nil
}

func anthropicUserMessageToOpenAI(msg map[string]any) ([]any, error) {
	content := msg["content"]
	if text, ok := content.(string); ok {
		return []any{map[string]any{"role": "user", "content": text}}, nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return []any{map[string]any{"role": "user", "content": ""}}, nil
	}
	out := make([]any, 0, 1)
	var parts []any
	hasRichPart := false
	flushUserContent := func() {
		if len(parts) == 0 {
			return
		}
		var value any = parts
		if !hasRichPart {
			textParts := make([]string, 0, len(parts))
			for _, part := range parts {
				if block, ok := part.(map[string]any); ok {
					if text, _ := block["text"].(string); text != "" {
						textParts = append(textParts, text)
					}
				}
			}
			value = strings.Join(textParts, "\n")
		}
		out = append(out, map[string]any{"role": "user", "content": value})
		parts = nil
		hasRichPart = false
	}
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			if text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
		case "image":
			source, _ := block["source"].(map[string]any)
			if source["type"] != "base64" {
				continue
			}
			mediaType, _ := source["media_type"].(string)
			data, _ := source["data"].(string)
			if mediaType == "" || data == "" {
				continue
			}
			hasRichPart = true
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + mediaType + ";base64," + data,
				},
			})
		case "tool_result":
			flushUserContent()
			toolUseID, _ := block["tool_use_id"].(string)
			resultText := anthropicToolResultContentToText(block["content"])
			isError, _ := block["is_error"].(bool)
			if toolUseID == "" {
				out = append(out, map[string]any{"role": "user", "content": resultText})
				continue
			}
			toolMessage := map[string]any{"role": "tool", "tool_call_id": toolUseID, "content": resultText}
			if isError {
				toolMessage["is_error"] = true
			}
			out = append(out, toolMessage)
		}
	}
	flushUserContent()
	if len(out) == 0 {
		out = append(out, map[string]any{"role": "user", "content": ""})
	}
	return out, nil
}

func anthropicAssistantMessageToOpenAI(msg map[string]any) (map[string]any, error) {
	content := msg["content"]
	if text, ok := content.(string); ok {
		return map[string]any{"role": "assistant", "content": text}, nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return map[string]any{"role": "assistant", "content": ""}, nil
	}
	textParts := make([]string, 0)
	toolCalls := make([]any, 0)
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			if text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			name, _ := block["name"].(string)
			if name == "" {
				continue
			}
			id, _ := block["id"].(string)
			args, err := anthropicToolUseArguments(block["input"])
			if err != nil {
				return nil, err
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   fallbackString(id, "toolu_"+requestID()),
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			})
		}
	}
	out := map[string]any{
		"role":    "assistant",
		"content": strings.Join(textParts, "\n"),
	}
	if len(toolCalls) > 0 {
		out["tool_calls"] = toolCalls
	}
	return out, nil
}

func anthropicToolResultContentToText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, _ := block["text"].(string); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		body, _ := json.Marshal(v)
		return string(body)
	}
}

func anthropicToolUseArguments(input any) (string, error) {
	if input == nil {
		return "{}", nil
	}
	if text, ok := input.(string); ok {
		if strings.TrimSpace(text) == "" {
			return "{}", nil
		}
		return text, nil
	}
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func anthropicToolsToOpenAI(value any) ([]any, bool) {
	rawTools, ok := value.([]any)
	if !ok || len(rawTools) == 0 {
		return nil, false
	}
	tools := make([]any, 0, len(rawTools))
	for _, item := range rawTools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		if name == "" {
			continue
		}
		description, _ := tool["description"].(string)
		parameters := tool["input_schema"]
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": normalizedToolDescription(name, description),
				"parameters":  parameters,
			},
		})
	}
	return tools, len(tools) > 0
}

func normalizedToolDescription(name, description string) string {
	description = strings.TrimSpace(description)
	if description != "" {
		return description
	}
	name = strings.TrimSpace(name)
	if name != "" {
		return "Tool " + name + "."
	}
	return "No description provided."
}

func anthropicToolChoiceToOpenAI(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		switch v {
		case "auto", "none", "required":
			return v, true
		default:
			return nil, false
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "auto":
			return "auto", true
		case "none":
			return "none", true
		case "any":
			return "required", true
		case "tool":
			name, _ := v["name"].(string)
			if name == "" {
				return nil, false
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}, true
		default:
			return nil, false
		}
	default:
		return nil, false
	}
}

func anthropicContentToOpenAI(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		return v, strings.TrimSpace(v) != ""
	case []any:
		parts := make([]any, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				text, _ := block["text"].(string)
				parts = append(parts, map[string]any{"type": "text", "text": text})
			case "image":
				source, _ := block["source"].(map[string]any)
				if source["type"] != "base64" {
					continue
				}
				mediaType, _ := source["media_type"].(string)
				data, _ := source["data"].(string)
				if mediaType == "" || data == "" {
					continue
				}
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:" + mediaType + ";base64," + data,
					},
				})
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return parts, true
	default:
		return "", false
	}
}

func anthropicResponseFromOpenAI(route store.RoutedModel, body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	choices, _ := raw["choices"].([]any)
	var message map[string]any
	var finishReason string
	if len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		message, _ = choice["message"].(map[string]any)
		finishReason, _ = choice["finish_reason"].(string)
	}
	content := openAIContentToAnthropic(message["content"])
	if anthropicTextContentEmpty(content) {
		if reasoning, _ := firstPresent(message, "reasoning", "reasoning_content").(string); strings.TrimSpace(reasoning) != "" {
			content = []map[string]any{{"type": "text", "text": reasoning}}
		}
	}
	toolContent, err := openAIToolCallsToAnthropic(message["tool_calls"])
	if err != nil {
		return nil, err
	}
	if len(toolContent) > 0 {
		if anthropicTextContentEmpty(content) {
			content = nil
		}
		content = append(content, toolContent...)
	}
	usage, _ := raw["usage"].(map[string]any)
	inputTokens := intValue(firstPresent(usage, "prompt_tokens", "input_tokens"))
	outputTokens := intValue(firstPresent(usage, "completion_tokens", "output_tokens"))
	id, _ := raw["id"].(string)
	if id == "" {
		id = "msg_" + requestID()
	}
	model, _ := raw["model"].(string)
	if model == "" {
		model = route.Model.Route
	}
	response := map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   anthropicStopReason(finishReason),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	return json.Marshal(response)
}

func openAIContentToAnthropic(value any) []map[string]any {
	switch v := value.(type) {
	case string:
		return []map[string]any{{"type": "text", "text": v}}
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "text" {
				text, _ := block["text"].(string)
				out = append(out, map[string]any{"type": "text", "text": text})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []map[string]any{{"type": "text", "text": ""}}
}

func openAIToolCallsToAnthropic(value any) ([]map[string]any, error) {
	var items []any
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []any:
		items = v
	case []map[string]any:
		items = make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, item)
		}
	default:
		return nil, nil
	}

	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		block, ok, err := openAIToolCallToAnthropic(call)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, block)
		}
	}
	return out, nil
}

func openAIToolCallToAnthropic(call map[string]any) (map[string]any, bool, error) {
	function, _ := call["function"].(map[string]any)
	name, _ := function["name"].(string)
	if strings.TrimSpace(name) == "" {
		name = "tool"
	}
	input, err := openAIToolCallArgumentsToAnthropicInput(function["arguments"])
	if err != nil {
		return nil, false, err
	}
	id, _ := call["id"].(string)
	return map[string]any{
		"type":  "tool_use",
		"id":    fallbackString(id, "toolu_"+requestID()),
		"name":  name,
		"input": input,
	}, true, nil
}

func openAIToolCallArgumentsToAnthropicInput(value any) (map[string]any, error) {
	switch v := value.(type) {
	case nil:
		return map[string]any{}, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return map[string]any{}, nil
		}
		var out map[string]any
		decoder := json.NewDecoder(strings.NewReader(v))
		decoder.UseNumber()
		if err := decoder.Decode(&out); err != nil {
			return nil, fmt.Errorf("could not parse OpenAI tool call arguments: %w", err)
		}
		if out == nil {
			return map[string]any{}, nil
		}
		return out, nil
	case map[string]any:
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported OpenAI tool call arguments type %T", value)
	}
}

func anthropicTextContentEmpty(blocks []map[string]any) bool {
	if len(blocks) == 0 {
		return true
	}
	for _, block := range blocks {
		if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
			return false
		}
	}
	return true
}

func anthropicStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

func firstPresent(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func providerErrorText(body []byte, fallback string) string {
	if len(body) == 0 {
		return fallbackString(fallback, "provider unavailable")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil {
		if errObj, ok := raw["error"].(map[string]any); ok {
			if msg, ok := errObj["message"].(string); ok && msg != "" {
				return msg
			}
		}
		if msg, ok := raw["message"].(string); ok && msg != "" {
			return msg
		}
	}
	return fallbackString(fallback, string(body))
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
	traceCtx, finishTrace := s.upstreamTrace(ctx, route, "bedrock", "converse")
	start := time.Now()
	output, err := client.Converse(traceCtx, input)
	latencyDuration := time.Since(start)
	latency := latencyDuration.Milliseconds()
	if err != nil {
		status := bedrockErrorStatus(err)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		msg := bedrockErrorMessage(err)
		finishTrace(status, msg, latencyDuration)
		return upstreamResult{Route: route, Protocol: "bedrock", Status: status, ErrorText: msg, LatencyMS: latency}
	}
	response := openAIResponseFromBedrock(route, output)
	body, err := json.Marshal(response)
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), latencyDuration)
		return upstreamResult{Route: route, Protocol: "bedrock", Status: http.StatusInternalServerError, ErrorText: err.Error(), LatencyMS: latency}
	}
	finishTrace(http.StatusOK, "", latencyDuration)
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
	return upstreamResult{Route: route, Protocol: providerProtocol(route.Provider), Status: status, ErrorText: reason}
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

func (s *Server) upstreamTrace(ctx context.Context, route store.RoutedModel, protocol, operation string) (context.Context, func(int, string, time.Duration)) {
	ctx, span := s.telemetry.StartUpstream(ctx, route.Provider.ID, route.Provider.Type, route.Model.Route, route.Model.ModelID, protocol, operation)
	return ctx, func(status int, errText string, latency time.Duration) {
		s.telemetry.FinishUpstream(span, status, errText, latency)
	}
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

func (s *Server) proxyOpenAI(w http.ResponseWriter, r *http.Request, route store.RoutedModel, body []byte, raw map[string]any, guardrails store.GuardrailPolicy) (int, []byte, string) {
	endpoint := openAIChatEndpoint(route.Provider, route.Model.ModelID)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "openai", "chat.completions")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
		openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return http.StatusInternalServerError, nil, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	setOpenAIAuthHeader(req, route.Provider)
	req.Header.Set("User-Agent", "Phlox-GW/0.1")

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		finishTrace(http.StatusBadGateway, err.Error(), time.Since(start))
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
		fallbackInput := 0
		if resp.StatusCode < 400 {
			fallbackInput = estimateOpenAIInputTokens(raw)
		}
		usage, n, err := proxyOpenAIStream(w, resp.Body, fallbackInput, guardrails)
		if err != nil {
			finishTrace(resp.StatusCode, err.Error(), time.Since(start))
			return resp.StatusCode, nil, err.Error()
		}
		finishTrace(resp.StatusCode, "", time.Since(start))
		return resp.StatusCode, openAIUsageBody(usage, n), ""
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), time.Since(start))
		return resp.StatusCode, nil, err.Error()
	}
	_, _ = w.Write(responseBody)
	if resp.StatusCode >= 400 {
		finishTrace(resp.StatusCode, string(responseBody), time.Since(start))
		return resp.StatusCode, responseBody, string(responseBody)
	}
	finishTrace(resp.StatusCode, "", time.Since(start))
	return resp.StatusCode, responseBody, ""
}

func proxyOpenAIStream(w http.ResponseWriter, body io.Reader, fallbackInputTokens int, guardrails store.GuardrailPolicy) (tokenUsage, int64, error) {
	var usage tokenUsage
	estimatedOutputTokens := 0
	var written int64
	reader := bufio.NewReader(body)
	flusher, _ := w.(http.Flusher)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineToWrite := applyGuardrailToSSELine(guardrails, line)
			n, writeErr := w.Write(lineToWrite)
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
			if usage.Output == 0 {
				estimatedOutputTokens += estimateOpenAIStreamOutputTokens(line)
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			usage = mergeEstimatedUsage(usage, fallbackInputTokens, estimatedOutputTokens)
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

func estimateOpenAIStreamOutputTokens(line []byte) int {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return 0
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if payload == "" || payload == "[DONE]" {
		return 0
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          any    `json:"content"`
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return 0
	}
	total := 0
	for _, choice := range chunk.Choices {
		total += estimateOpenAIContentTokens(choice.Delta.Content)
		total += estimateTextTokens(choice.Delta.Reasoning)
		total += estimateTextTokens(choice.Delta.ReasoningContent)
		for _, call := range choice.Delta.ToolCalls {
			total += estimateTextTokens(call.Function.Arguments)
		}
	}
	return total
}

func openAIUsageWithFallback(raw map[string]any, body []byte, status int) tokenUsage {
	usage := parseOpenAIUsage(body)
	if status >= 400 {
		return usage
	}
	if usage.Input > 0 && usage.Output > 0 && usage.Total > 0 {
		return usage
	}
	return mergeEstimatedUsage(usage, estimateOpenAIInputTokens(raw), estimateOpenAIResponseTokens(body))
}

func mergeEstimatedUsage(usage tokenUsage, estimatedInputTokens, estimatedOutputTokens int) tokenUsage {
	if usage.Input == 0 && estimatedInputTokens > 0 {
		usage.Input = estimatedInputTokens
	}
	if usage.Output == 0 && estimatedOutputTokens > 0 {
		usage.Output = estimatedOutputTokens
	}
	if usage.Total == 0 || usage.Total < usage.Input+usage.Output {
		usage.Total = usage.Input + usage.Output
	}
	return usage
}

func estimateOpenAIInputTokens(raw map[string]any) int {
	total := 0
	if messages, ok := raw["messages"].([]any); ok {
		for _, item := range messages {
			msg, ok := item.(map[string]any)
			if !ok {
				continue
			}
			total += 4
			total += estimateOpenAIContentTokens(msg["content"])
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, call := range calls {
					if body, err := json.Marshal(call); err == nil {
						total += estimateTextTokens(string(body))
					}
				}
			}
		}
	}
	if tools, ok := raw["tools"]; ok && tools != nil {
		if body, err := json.Marshal(tools); err == nil {
			total += estimateTextTokens(string(body))
		}
	}
	return total
}

func estimateOpenAIResponseTokens(body []byte) int {
	var resp struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content   any `json:"content"`
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0
	}
	total := 0
	for _, choice := range resp.Choices {
		total += estimateTextTokens(choice.Text)
		total += estimateOpenAIContentTokens(choice.Message.Content)
		for _, call := range choice.Message.ToolCalls {
			total += estimateTextTokens(call.Function.Arguments)
		}
	}
	return total
}

func estimateOpenAIContentTokens(content any) int {
	switch v := content.(type) {
	case nil:
		return 0
	case string:
		return estimateTextTokens(v)
	case []any:
		total := 0
		for _, item := range v {
			switch part := item.(type) {
			case string:
				total += estimateTextTokens(part)
			case map[string]any:
				if text, _ := part["text"].(string); text != "" {
					total += estimateTextTokens(text)
				}
			}
		}
		return total
	default:
		return 0
	}
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	chars := utf8.RuneCountInString(text)
	byChars := int(math.Ceil(float64(chars) / 4.0))
	byWords := int(math.Ceil(float64(len(strings.Fields(text))) * 1.33))
	if byWords > byChars {
		byChars = byWords
	}
	if byChars < 1 {
		return 1
	}
	return byChars
}

func usageFromAnthropicSSELine(line []byte) tokenUsage {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return tokenUsage{}
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if payload == "" || payload == "[DONE]" {
		return tokenUsage{}
	}
	var event struct {
		Message struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return tokenUsage{}
	}
	usage := tokenUsage{
		Input:  event.Message.Usage.InputTokens,
		Output: event.Message.Usage.OutputTokens,
	}
	if event.Usage.InputTokens > 0 {
		usage.Input = event.Usage.InputTokens
	}
	if event.Usage.OutputTokens > 0 {
		usage.Output = event.Usage.OutputTokens
	}
	if event.Usage.TotalTokens > 0 {
		usage.Total = event.Usage.TotalTokens
	} else if usage.Input > 0 || usage.Output > 0 {
		usage.Total = usage.Input + usage.Output
	}
	return usage
}

func mergeAnthropicUsage(current, next tokenUsage) tokenUsage {
	if next.Input > 0 {
		current.Input = next.Input
	}
	if next.Output > 0 {
		current.Output = next.Output
	}
	if next.Total > 0 && next.Total >= current.Input+current.Output {
		current.Total = next.Total
	} else {
		current.Total = current.Input + current.Output
	}
	return current
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

func anthropicUsageBody(usage tokenUsage, streamedBytes int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"streamed_bytes": streamedBytes,
		"usage": map[string]int{
			"input_tokens":  usage.Input,
			"output_tokens": usage.Output,
		},
	})
	return body
}

func writeOpenAIStreamData(w http.ResponseWriter, flusher http.Flusher, payload map[string]any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	n, err := fmt.Fprintf(w, "data: %s\n\n", body)
	if flusher != nil {
		flusher.Flush()
	}
	return n, err
}

func writeOpenAIStreamDone(w http.ResponseWriter, flusher http.Flusher) (int, error) {
	n, err := io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return n, err
}

func openAIStreamChunk(route store.RoutedModel, id string, created int64, choices []map[string]any, usage map[string]int) map[string]any {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   route.Model.Route,
		"choices": choices,
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	return chunk
}

func openAIStreamIncludeUsage(raw map[string]any) bool {
	options, ok := raw["stream_options"].(map[string]any)
	if !ok {
		return false
	}
	include, _ := options["include_usage"].(bool)
	return include
}

func bedrockOpenAIStreamDelta(event types.ContentBlockDeltaEvent, toolCalls map[int32]int) map[string]any {
	switch delta := event.Delta.(type) {
	case *types.ContentBlockDeltaMemberText:
		if delta.Value == "" {
			return nil
		}
		return map[string]any{
			"index": 0,
			"delta": map[string]any{"content": delta.Value},
		}
	case *types.ContentBlockDeltaMemberToolUse:
		if delta.Value.Input == nil || *delta.Value.Input == "" {
			return nil
		}
		toolIndex, ok := toolCalls[aws.ToInt32(event.ContentBlockIndex)]
		if !ok {
			toolIndex = len(toolCalls)
			toolCalls[aws.ToInt32(event.ContentBlockIndex)] = toolIndex
		}
		return map[string]any{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index": toolIndex,
					"function": map[string]any{
						"arguments": *delta.Value.Input,
					},
				}},
			},
		}
	default:
		return nil
	}
}

func bedrockConversationRole(role types.ConversationRole) string {
	if role == types.ConversationRoleUser {
		return "user"
	}
	return "assistant"
}

func (s *Server) proxyAnthropic(w http.ResponseWriter, r *http.Request, route store.RoutedModel, body []byte) (int, []byte, string) {
	endpoint := anthropicMessagesEndpoint(route.Provider)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "anthropic", "messages")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
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
	setAnthropicAuthHeader(req, route.Provider)

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		finishTrace(http.StatusBadGateway, err.Error(), time.Since(start))
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
		finishTrace(resp.StatusCode, err.Error(), time.Since(start))
		return resp.StatusCode, nil, err.Error()
	}
	_, _ = w.Write(responseBody)
	if resp.StatusCode >= 400 {
		finishTrace(resp.StatusCode, string(responseBody), time.Since(start))
		return resp.StatusCode, responseBody, string(responseBody)
	}
	finishTrace(resp.StatusCode, "", time.Since(start))
	return resp.StatusCode, responseBody, ""
}

func (s *Server) proxyAnthropicStream(w http.ResponseWriter, r *http.Request, route store.RoutedModel, body []byte, guardrails store.GuardrailPolicy) (int, []byte, string) {
	endpoint := anthropicMessagesEndpoint(route.Provider)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "anthropic", "messages.stream")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
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
	setAnthropicAuthHeader(req, route.Provider)

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		finishTrace(http.StatusBadGateway, err.Error(), time.Since(start))
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
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			finishTrace(resp.StatusCode, err.Error(), time.Since(start))
			return resp.StatusCode, nil, err.Error()
		}
		_, _ = w.Write(responseBody)
		finishTrace(resp.StatusCode, string(responseBody), time.Since(start))
		return resp.StatusCode, responseBody, string(responseBody)
	}
	usage, n, err := proxyAnthropicStream(w, resp.Body, guardrails)
	body = anthropicUsageBody(usage, n)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), time.Since(start))
		return resp.StatusCode, body, err.Error()
	}
	finishTrace(resp.StatusCode, "", time.Since(start))
	return resp.StatusCode, body, ""
}

func proxyAnthropicStream(w http.ResponseWriter, body io.Reader, guardrails store.GuardrailPolicy) (tokenUsage, int64, error) {
	var usage tokenUsage
	var written int64
	reader := bufio.NewReader(body)
	flusher, _ := w.(http.Flusher)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineToWrite := applyGuardrailToSSELine(guardrails, line)
			n, writeErr := w.Write(lineToWrite)
			written += int64(n)
			if writeErr != nil {
				return usage, written, writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
			usage = mergeAnthropicUsage(usage, usageFromAnthropicSSELine(line))
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

func (s *Server) proxyAnthropicViaOpenAIStream(w http.ResponseWriter, r *http.Request, route store.RoutedModel, raw map[string]any, guardrails store.GuardrailPolicy) (int, []byte, string) {
	openAIRaw, err := anthropicRequestToOpenAI(raw)
	if err != nil {
		anthropicError(w, http.StatusBadRequest, err.Error())
		return http.StatusBadRequest, nil, err.Error()
	}
	openAIRaw["stream"] = true
	openAIRaw["model"] = route.Model.ModelID
	ensureStreamUsageOption(route.Provider, openAIRaw)
	endpoint := openAIChatEndpoint(route.Provider, route.Model.ModelID)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "anthropic", "messages.stream.translate_openai")
	start := time.Now()
	var resp *http.Response
	// The translated payload always carries max_tokens, which
	// reasoning-family models reject; retry with adjusted parameters before
	// anything is written to the client.
	for attempt := 0; ; attempt++ {
		body, err := json.Marshal(openAIRaw)
		if err != nil {
			anthropicError(w, http.StatusInternalServerError, err.Error())
			return http.StatusInternalServerError, nil, err.Error()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			finishTrace(http.StatusInternalServerError, err.Error(), 0)
			anthropicError(w, http.StatusInternalServerError, err.Error())
			return http.StatusInternalServerError, nil, err.Error()
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Phlox-GW/0.1")
		setOpenAIAuthHeader(req, route.Provider)
		resp, err = s.httpClient.Do(req)
		if err != nil {
			finishTrace(http.StatusBadGateway, err.Error(), time.Since(start))
			anthropicError(w, http.StatusBadGateway, err.Error())
			return http.StatusBadGateway, nil, err.Error()
		}
		if resp.StatusCode < 400 {
			break
		}
		responseBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			finishTrace(resp.StatusCode, readErr.Error(), time.Since(start))
			anthropicError(w, resp.StatusCode, readErr.Error())
			return resp.StatusCode, nil, readErr.Error()
		}
		if attempt < 2 {
			if adjusted, ok := openAIPayloadForUnsupportedParams(openAIRaw, resp.StatusCode, string(responseBody)); ok {
				openAIRaw = adjusted
				continue
			}
		}
		message := providerErrorText(responseBody, string(responseBody))
		finishTrace(resp.StatusCode, message, time.Since(start))
		anthropicError(w, resp.StatusCode, message)
		return resp.StatusCode, responseBody, message
	}
	defer resp.Body.Close()
	for k, values := range resp.Header {
		if shouldProxyHeader(k) && !strings.EqualFold(k, "Content-Type") {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(resp.StatusCode)

	usage, n, err := proxyOpenAIStreamAsAnthropic(w, resp.Body, route, estimateOpenAIInputTokens(openAIRaw), guardrails)
	responseBody := anthropicUsageBody(usage, n)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), time.Since(start))
		return resp.StatusCode, responseBody, err.Error()
	}
	finishTrace(resp.StatusCode, "", time.Since(start))
	return resp.StatusCode, responseBody, ""
}

func (s *Server) proxyAnthropicViaBedrockStream(w http.ResponseWriter, r *http.Request, route store.RoutedModel, raw map[string]any, guardrails store.GuardrailPolicy) (int, []byte, string) {
	openAIRaw, err := anthropicRequestToOpenAI(raw)
	if err != nil {
		anthropicError(w, http.StatusBadRequest, err.Error())
		return http.StatusBadRequest, nil, err.Error()
	}
	openAIRaw["stream"] = true
	openAIRaw["model"] = route.Model.ModelID
	input, err := bedrockConverseStreamInput(route.Model.ModelID, openAIRaw)
	if err != nil {
		anthropicError(w, http.StatusBadRequest, err.Error())
		return http.StatusBadRequest, nil, err.Error()
	}
	client, err := s.bedrockClient(r.Context(), route.Provider)
	if err != nil {
		msg := "Bedrock configuration failed: " + err.Error()
		anthropicError(w, http.StatusBadGateway, msg)
		return http.StatusBadGateway, nil, msg
	}
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "anthropic", "messages.stream.translate_bedrock")
	start := time.Now()
	stream, err := client.ConverseStream(ctx, input)
	if err != nil {
		status := bedrockErrorStatus(err)
		msg := bedrockErrorMessage(err)
		finishTrace(status, msg, time.Since(start))
		anthropicError(w, status, msg)
		return status, nil, msg
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	state := &anthropicOpenAIStreamState{
		w:              w,
		flusher:        httpFlusher(w),
		route:          route,
		id:             "msg_" + requestID(),
		inputTokens:    estimateOpenAIInputTokens(openAIRaw),
		guardrails:     guardrails,
		textBlockIndex: -1,
		toolBlocks:     map[int]*anthropicOpenAIStreamToolBlock{},
	}
	if err := state.writeMessageStart(); err != nil {
		finishTrace(http.StatusOK, err.Error(), time.Since(start))
		return http.StatusOK, anthropicUsageBody(tokenUsage{}, state.written), err.Error()
	}

	var usage tokenUsage
	estimatedOutputTokens := 0
	stopReason := "end_turn"
	for event := range stream.Events() {
		switch ev := event.(type) {
		case *types.ConverseStreamOutputMemberContentBlockStart:
			if toolStart, ok := ev.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
				blockIndex := int(aws.ToInt32(ev.Value.ContentBlockIndex))
				block := state.toolBlocks[blockIndex]
				if block == nil {
					block = &anthropicOpenAIStreamToolBlock{openAIIndex: blockIndex, blockIndex: -1}
					state.toolBlocks[blockIndex] = block
					state.toolOrder = append(state.toolOrder, blockIndex)
				}
				block.id = aws.ToString(toolStart.Value.ToolUseId)
				block.name = aws.ToString(toolStart.Value.Name)
				if err := state.startToolBlock(block); err != nil {
					finishTrace(http.StatusOK, err.Error(), time.Since(start))
					return http.StatusOK, anthropicUsageBody(usage, state.written), err.Error()
				}
			}
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			switch delta := ev.Value.Delta.(type) {
			case *types.ContentBlockDeltaMemberText:
				if delta.Value != "" {
					estimatedOutputTokens += estimateTextTokens(delta.Value)
					if err := state.writeTextDelta(delta.Value); err != nil {
						finishTrace(http.StatusOK, err.Error(), time.Since(start))
						return http.StatusOK, anthropicUsageBody(usage, state.written), err.Error()
					}
				}
			case *types.ContentBlockDeltaMemberToolUse:
				inputDelta := aws.ToString(delta.Value.Input)
				if inputDelta == "" {
					continue
				}
				estimatedOutputTokens += estimateTextTokens(inputDelta)
				blockIndex := int(aws.ToInt32(ev.Value.ContentBlockIndex))
				block := state.toolBlocks[blockIndex]
				if block == nil {
					block = &anthropicOpenAIStreamToolBlock{openAIIndex: blockIndex, blockIndex: -1}
					state.toolBlocks[blockIndex] = block
					state.toolOrder = append(state.toolOrder, blockIndex)
				}
				if !block.started {
					if err := state.startToolBlock(block); err != nil {
						finishTrace(http.StatusOK, err.Error(), time.Since(start))
						return http.StatusOK, anthropicUsageBody(usage, state.written), err.Error()
					}
				}
				if err := state.writeToolInputDelta(block, inputDelta); err != nil {
					finishTrace(http.StatusOK, err.Error(), time.Since(start))
					return http.StatusOK, anthropicUsageBody(usage, state.written), err.Error()
				}
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			stopReason = anthropicStopReason(bedrockFinishReason(ev.Value.StopReason))
		case *types.ConverseStreamOutputMemberMetadata:
			usage = bedrockTokenUsage(ev.Value.Usage)
		}
	}
	if err := stream.Err(); err != nil {
		msg := bedrockErrorMessage(err)
		finishTrace(http.StatusBadGateway, msg, time.Since(start))
		return http.StatusBadGateway, anthropicUsageBody(usage, state.written), msg
	}
	usage = mergeEstimatedUsage(usage, state.inputTokens, estimatedOutputTokens)
	if err := state.finish(stopReason, usage.Output); err != nil {
		finishTrace(http.StatusOK, err.Error(), time.Since(start))
		return http.StatusOK, anthropicUsageBody(usage, state.written), err.Error()
	}
	finishTrace(http.StatusOK, "", time.Since(start))
	return http.StatusOK, anthropicUsageBody(usage, state.written), ""
}

type anthropicOpenAIStreamToolBlock struct {
	openAIIndex int
	blockIndex  int
	id          string
	name        string
	pendingJSON string
	started     bool
}

type anthropicOpenAIStreamState struct {
	w              http.ResponseWriter
	flusher        http.Flusher
	route          store.RoutedModel
	id             string
	inputTokens    int
	guardrails     store.GuardrailPolicy
	written        int64
	nextBlockIndex int
	textBlockIndex int
	textStarted    bool
	toolBlocks     map[int]*anthropicOpenAIStreamToolBlock
	toolOrder      []int
}

func proxyOpenAIStreamAsAnthropic(w http.ResponseWriter, body io.Reader, route store.RoutedModel, fallbackInputTokens int, guardrails store.GuardrailPolicy) (tokenUsage, int64, error) {
	state := &anthropicOpenAIStreamState{
		w:              w,
		flusher:        httpFlusher(w),
		route:          route,
		id:             "msg_" + requestID(),
		inputTokens:    fallbackInputTokens,
		guardrails:     guardrails,
		textBlockIndex: -1,
		toolBlocks:     map[int]*anthropicOpenAIStreamToolBlock{},
	}
	if err := state.writeMessageStart(); err != nil {
		return tokenUsage{}, state.written, err
	}
	var usage tokenUsage
	estimatedOutputTokens := 0
	stopReason := "end_turn"
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			payload, ok := openAISSEPayload(line)
			if ok {
				if payload == "[DONE]" {
					usage = mergeEstimatedUsage(usage, fallbackInputTokens, estimatedOutputTokens)
					if err := state.finish(stopReason, usage.Output); err != nil {
						return usage, state.written, err
					}
					return usage, state.written, nil
				}
				if parsed := parseOpenAIUsage([]byte(payload)); parsed.Total > 0 || parsed.Input > 0 || parsed.Output > 0 {
					usage = parsed
				}
				if usage.Output == 0 {
					estimatedOutputTokens += estimateOpenAIStreamOutputTokens(line)
				}
				var chunk map[string]any
				if json.Unmarshal([]byte(payload), &chunk) == nil {
					if err := state.writeOpenAIChunk(chunk, &stopReason); err != nil {
						return usage, state.written, err
					}
				}
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			usage = mergeEstimatedUsage(usage, fallbackInputTokens, estimatedOutputTokens)
			if err := state.finish(stopReason, usage.Output); err != nil {
				return usage, state.written, err
			}
			return usage, state.written, nil
		}
		return usage, state.written, err
	}
}

func httpFlusher(w http.ResponseWriter) http.Flusher {
	flusher, _ := w.(http.Flusher)
	return flusher
}

func openAISSEPayload(line []byte) (string, bool) {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return "", false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	return payload, payload != ""
}

func (s *anthropicOpenAIStreamState) writeMessageStart() error {
	return s.writeEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.id,
			"type":          "message",
			"role":          "assistant",
			"model":         s.route.Model.Route,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": s.inputTokens, "output_tokens": 0},
		},
	})
}

func (s *anthropicOpenAIStreamState) writeOpenAIChunk(chunk map[string]any, stopReason *string) error {
	choices, _ := chunk["choices"].([]any)
	for _, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		for _, text := range openAIStreamDeltaTextFragments(delta) {
			if err := s.writeTextDelta(text); err != nil {
				return err
			}
		}
		if err := s.writeToolCallDeltas(delta); err != nil {
			return err
		}
		if finish, _ := choice["finish_reason"].(string); finish != "" {
			*stopReason = anthropicStopReason(finish)
		}
	}
	return nil
}

func openAIStreamDeltaTextFragments(delta map[string]any) []string {
	fragments := make([]string, 0, 1)
	for _, key := range []string{"content", "reasoning", "reasoning_content"} {
		switch v := delta[key].(type) {
		case string:
			if v != "" {
				fragments = append(fragments, v)
			}
		case []any:
			for _, item := range v {
				block, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if text, _ := block["text"].(string); text != "" {
					fragments = append(fragments, text)
				}
			}
		}
	}
	return fragments
}

func (s *anthropicOpenAIStreamState) writeTextDelta(text string) error {
	if text == "" {
		return nil
	}
	if !s.textStarted {
		s.textBlockIndex = s.nextBlockIndex
		s.nextBlockIndex++
		s.textStarted = true
		if err := s.writeEvent("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         s.textBlockIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		}); err != nil {
			return err
		}
	}
	return s.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.textBlockIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

func (s *anthropicOpenAIStreamState) writeToolCallDeltas(delta map[string]any) error {
	rawCalls, _ := delta["tool_calls"].([]any)
	for _, item := range rawCalls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		openAIIndex := intValue(call["index"])
		block := s.toolBlocks[openAIIndex]
		if block == nil {
			block = &anthropicOpenAIStreamToolBlock{openAIIndex: openAIIndex, blockIndex: -1}
			s.toolBlocks[openAIIndex] = block
			s.toolOrder = append(s.toolOrder, openAIIndex)
		}
		if id, _ := call["id"].(string); id != "" {
			block.id = id
		}
		function, _ := call["function"].(map[string]any)
		if name, _ := function["name"].(string); name != "" {
			block.name = name
		}
		if !block.started && (block.id != "" || block.name != "") {
			if err := s.startToolBlock(block); err != nil {
				return err
			}
		}
		if args, _ := function["arguments"].(string); args != "" {
			if block.started {
				if err := s.writeToolInputDelta(block, args); err != nil {
					return err
				}
			} else {
				block.pendingJSON += args
			}
		}
	}
	return nil
}

func (s *anthropicOpenAIStreamState) startToolBlock(block *anthropicOpenAIStreamToolBlock) error {
	if block.started {
		return nil
	}
	if s.textStarted {
		if err := s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.textBlockIndex}); err != nil {
			return err
		}
		s.textStarted = false
		s.textBlockIndex = -1
	}
	block.blockIndex = s.nextBlockIndex
	s.nextBlockIndex++
	block.started = true
	if block.id == "" {
		block.id = fmt.Sprintf("toolu_%s_%d", s.id, block.openAIIndex)
	}
	if block.name == "" {
		block.name = "tool"
	}
	if err := s.writeEvent("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": block.blockIndex,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    block.id,
			"name":  block.name,
			"input": map[string]any{},
		},
	}); err != nil {
		return err
	}
	if block.pendingJSON != "" {
		pending := block.pendingJSON
		block.pendingJSON = ""
		return s.writeToolInputDelta(block, pending)
	}
	return nil
}

func (s *anthropicOpenAIStreamState) writeToolInputDelta(block *anthropicOpenAIStreamToolBlock, partialJSON string) error {
	return s.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": block.blockIndex,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": partialJSON},
	})
}

func (s *anthropicOpenAIStreamState) finish(stopReason string, outputTokens int) error {
	if s.textStarted {
		if err := s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.textBlockIndex}); err != nil {
			return err
		}
	}
	for _, openAIIndex := range s.toolOrder {
		block := s.toolBlocks[openAIIndex]
		if block == nil {
			continue
		}
		if !block.started {
			if err := s.startToolBlock(block); err != nil {
				return err
			}
		}
		if err := s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": block.blockIndex}); err != nil {
			return err
		}
	}
	if stopReason == "" {
		stopReason = "end_turn"
	}
	if err := s.writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]int{"output_tokens": outputTokens},
	}); err != nil {
		return err
	}
	return s.writeEvent("message_stop", map[string]any{"type": "message_stop"})
}

func (s *anthropicOpenAIStreamState) writeEvent(event string, payload map[string]any) error {
	payload = applyGuardrailToStreamPayload(s.guardrails, payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	n, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, body)
	s.written += int64(n)
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return err
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
	traceCtx, finishTrace := s.upstreamTrace(r.Context(), route, "bedrock", "converse")
	start := time.Now()
	output, err := client.Converse(traceCtx, input)
	if err != nil {
		status := bedrockErrorStatus(err)
		msg := bedrockErrorMessage(err)
		finishTrace(status, msg, time.Since(start))
		openAIError(w, status, msg, "provider_error")
		return status, nil, msg
	}
	response := openAIResponseFromBedrock(route, output)
	body, err := json.Marshal(response)
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), time.Since(start))
		openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return http.StatusInternalServerError, nil, err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	finishTrace(http.StatusOK, "", time.Since(start))
	return http.StatusOK, body, ""
}

func (s *Server) proxyBedrockOpenAIStream(w http.ResponseWriter, r *http.Request, route store.RoutedModel, raw map[string]any, guardrails store.GuardrailPolicy) (int, []byte, string) {
	input, err := bedrockConverseStreamInput(route.Model.ModelID, raw)
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
	traceCtx, finishTrace := s.upstreamTrace(r.Context(), route, "bedrock", "converse_stream")
	start := time.Now()
	stream, err := client.ConverseStream(traceCtx, input)
	if err != nil {
		status := bedrockErrorStatus(err)
		msg := bedrockErrorMessage(err)
		finishTrace(status, msg, time.Since(start))
		openAIError(w, status, msg, "provider_error")
		return status, nil, msg
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	id := requestID()
	created := time.Now().Unix()
	includeUsage := openAIStreamIncludeUsage(raw)
	toolCalls := map[int32]int{}
	var usage tokenUsage
	var written int64

	writeChunk := func(chunk map[string]any) error {
		chunk = applyGuardrailToStreamPayload(guardrails, chunk)
		n, err := writeOpenAIStreamData(w, flusher, chunk)
		written += int64(n)
		return err
	}
	for event := range stream.Events() {
		switch ev := event.(type) {
		case *types.ConverseStreamOutputMemberMessageStart:
			chunk := openAIStreamChunk(route, id, created, []map[string]any{{
				"index": 0,
				"delta": map[string]any{"role": bedrockConversationRole(ev.Value.Role)},
			}}, nil)
			if err := writeChunk(chunk); err != nil {
				finishTrace(http.StatusOK, err.Error(), time.Since(start))
				return http.StatusOK, openAIUsageBody(usage, written), err.Error()
			}
		case *types.ConverseStreamOutputMemberContentBlockStart:
			if toolStart, ok := ev.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
				blockIndex := aws.ToInt32(ev.Value.ContentBlockIndex)
				toolIndex := len(toolCalls)
				toolCalls[blockIndex] = toolIndex
				chunk := openAIStreamChunk(route, id, created, []map[string]any{{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []map[string]any{{
							"index": toolIndex,
							"id":    aws.ToString(toolStart.Value.ToolUseId),
							"type":  "function",
							"function": map[string]any{
								"name":      aws.ToString(toolStart.Value.Name),
								"arguments": "",
							},
						}},
					},
				}}, nil)
				if err := writeChunk(chunk); err != nil {
					finishTrace(http.StatusOK, err.Error(), time.Since(start))
					return http.StatusOK, openAIUsageBody(usage, written), err.Error()
				}
			}
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			choice := bedrockOpenAIStreamDelta(ev.Value, toolCalls)
			if choice == nil {
				continue
			}
			chunk := openAIStreamChunk(route, id, created, []map[string]any{choice}, nil)
			if err := writeChunk(chunk); err != nil {
				finishTrace(http.StatusOK, err.Error(), time.Since(start))
				return http.StatusOK, openAIUsageBody(usage, written), err.Error()
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			chunk := openAIStreamChunk(route, id, created, []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": bedrockFinishReason(ev.Value.StopReason),
			}}, nil)
			if err := writeChunk(chunk); err != nil {
				finishTrace(http.StatusOK, err.Error(), time.Since(start))
				return http.StatusOK, openAIUsageBody(usage, written), err.Error()
			}
		case *types.ConverseStreamOutputMemberMetadata:
			usage = bedrockTokenUsage(ev.Value.Usage)
		}
	}
	if err := stream.Err(); err != nil {
		msg := bedrockErrorMessage(err)
		finishTrace(http.StatusBadGateway, msg, time.Since(start))
		return http.StatusBadGateway, openAIUsageBody(usage, written), msg
	}
	if includeUsage {
		chunk := openAIStreamChunk(route, id, created, []map[string]any{}, map[string]int{
			"prompt_tokens":     usage.Input,
			"completion_tokens": usage.Output,
			"total_tokens":      usage.Total,
		})
		if err := writeChunk(chunk); err != nil {
			finishTrace(http.StatusOK, err.Error(), time.Since(start))
			return http.StatusOK, openAIUsageBody(usage, written), err.Error()
		}
	}
	n, err := writeOpenAIStreamDone(w, flusher)
	written += int64(n)
	if err != nil {
		finishTrace(http.StatusOK, err.Error(), time.Since(start))
		return http.StatusOK, openAIUsageBody(usage, written), err.Error()
	}
	finishTrace(http.StatusOK, "", time.Since(start))
	return http.StatusOK, openAIUsageBody(usage, written), ""
}

func (s *Server) bedrockClient(ctx context.Context, p store.Provider) (BedrockConverseClient, error) {
	if s.bedrockClientFactory != nil {
		return s.bedrockClientFactory(ctx, p)
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithHTTPClient(s.httpClient)}
	if strings.TrimSpace(p.AWSRegion) != "" {
		opts = append(opts, awsconfig.WithRegion(strings.TrimSpace(p.AWSRegion)))
	}
	var clientOpts []func(*bedrockruntime.Options)
	switch p.AWSAuthMethod {
	case "keys":
		access := strings.TrimSpace(p.AWSAccessKeyID)
		secret := strings.TrimSpace(p.AWSSecretAccessKey)
		if access == "" || secret == "" {
			return nil, errors.New("bedrock provider uses access-key auth but the access key or secret key is not configured")
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider(access, secret, strings.TrimSpace(p.AWSSessionToken))))
	case "api_key":
		key := strings.TrimSpace(p.BedrockAPIKey)
		if key == "" {
			return nil, errors.New("bedrock provider uses API-key auth but no API key is configured")
		}
		clientOpts = append(clientOpts, func(o *bedrockruntime.Options) {
			o.BearerAuthTokenProvider = smithybearer.TokenProviderFunc(func(context.Context) (smithybearer.Token, error) {
				return smithybearer.Token{Value: key}, nil
			})
			o.AuthSchemePreference = []string{"httpBearerAuth"}
		})
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return awsBedrockConverseClient{client: bedrockruntime.NewFromConfig(cfg, clientOpts...)}, nil
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

func (s *Server) runModelHealthCheck(parent context.Context, route store.RoutedModel) modelHealthResult {
	result := modelHealthResult{
		ProviderID: route.Provider.ID,
		Model:      route.Model.Route,
		Protocol:   providerProtocol(route.Provider),
	}
	if route.Provider.Type == "bedrock" {
		return s.runBedrockHealthCheck(parent, route, result)
	}

	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	protocol := providerProtocol(route.Provider)
	var endpoint string
	var payload map[string]any
	switch protocol {
	case "openai":
		endpoint = openAIChatEndpoint(route.Provider, route.Model.ModelID)
		payload = map[string]any{
			"model":       route.Model.ModelID,
			"messages":    []map[string]string{{"role": "user", "content": "Reply with exactly: OK"}},
			"temperature": 0,
			"max_tokens":  8,
			"stream":      false,
		}
	case "anthropic":
		endpoint = anthropicMessagesEndpoint(route.Provider)
		payload = map[string]any{
			"model":      route.Model.ModelID,
			"max_tokens": 8,
			"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: OK"}},
		}
	default:
		result.Error = "unsupported provider type"
		return result
	}

	// Reasoning-family models reject max_tokens and pinned temperature, and
	// report one bad parameter per response, so allow a couple of retries
	// with adjusted parameters before treating the failure as real.
	for attempt := 0; ; attempt++ {
		body, err := json.Marshal(payload)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			result.Error = err.Error()
			return result
		}
		req.Header.Set("Content-Type", "application/json")
		if protocol == "anthropic" {
			req.Header.Set("anthropic-version", "2023-06-01")
			setAnthropicAuthHeader(req, route.Provider)
		} else {
			setOpenAIAuthHeader(req, route.Provider)
		}
		req.Header.Set("User-Agent", "Phlox-GW/0.1")

		start := time.Now()
		resp, err := s.httpClient.Do(req)
		result.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		result.StatusCode = resp.StatusCode
		responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		if protocol == "openai" && attempt < 2 {
			if adjusted, ok := openAIPayloadForUnsupportedParams(payload, resp.StatusCode, string(responseBody)); ok {
				payload = adjusted
				continue
			}
		}
		result.Snippet = limitString(string(responseBody), 800)
		result.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
		if !result.OK && result.Error == "" {
			result.Error = result.Snippet
		}
		return result
	}
}

func (s *Server) runBedrockHealthCheck(parent context.Context, route store.RoutedModel, result modelHealthResult) modelHealthResult {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	input, err := bedrockConverseInput(route.Model.ModelID, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Reply with exactly: OK"},
		},
		"max_tokens": float64(8),
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

func (s *Server) providerFromRequest(w http.ResponseWriter, r *http.Request, pathID string) (store.Provider, store.ProviderSecretUpdate, bool) {
	var req struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		Type               string `json:"type"`
		BaseURL            string `json:"base_url"`
		APIKey             string `json:"api_key"`
		APIKeyEnv          string `json:"api_key_env"`
		AzureAPIVersion    string `json:"azure_api_version"`
		AWSRegion          string `json:"aws_region"`
		AWSAuthMethod      string `json:"aws_auth_method"`
		AWSAccessKeyID     string `json:"aws_access_key_id"`
		AWSSecretAccessKey string `json:"aws_secret_access_key"`
		AWSSessionToken    string `json:"aws_session_token"`
		BedrockAPIKey      string `json:"bedrock_api_key"`
		Enabled            bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	isCreate := pathID == ""
	id := strings.TrimSpace(req.ID)
	if pathID != "" {
		id = pathID
	}
	if id == "" || strings.TrimSpace(req.Name) == "" {
		respondError(w, http.StatusBadRequest, "provider id and name are required")
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	switch req.Type {
	case "openai", "anthropic", "azure-openai", "azure-anthropic", "google", "bedrock":
	default:
		respondError(w, http.StatusBadRequest, "provider type must be openai, anthropic, azure-openai, azure-anthropic, google, or bedrock")
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	if req.Type == "google" && strings.TrimSpace(req.BaseURL) == "" {
		// The Gemini API has a fixed public endpoint; a blank base URL means
		// the standard OpenAI-compatible surface.
		req.BaseURL = defaultGoogleBaseURL
	}
	if req.Type != "bedrock" && strings.TrimSpace(req.BaseURL) == "" {
		respondError(w, http.StatusBadRequest, "base_url is required for non-Bedrock providers")
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	p := store.Provider{
		ID:                 id,
		Name:               strings.TrimSpace(req.Name),
		Type:               req.Type,
		BaseURL:            strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		APIKey:             req.APIKey,
		APIKeyEnv:          strings.TrimSpace(req.APIKeyEnv),
		AzureAPIVersion:    strings.TrimSpace(req.AzureAPIVersion),
		AWSRegion:          strings.TrimSpace(req.AWSRegion),
		AWSAuthMethod:      strings.TrimSpace(req.AWSAuthMethod),
		AWSAccessKeyID:     strings.TrimSpace(req.AWSAccessKeyID),
		AWSSecretAccessKey: strings.TrimSpace(req.AWSSecretAccessKey),
		AWSSessionToken:    strings.TrimSpace(req.AWSSessionToken),
		BedrockAPIKey:      strings.TrimSpace(req.BedrockAPIKey),
		Enabled:            req.Enabled,
	}
	if p.Type != "azure-openai" {
		// api-version only applies to Azure OpenAI deployment endpoints.
		p.AzureAPIVersion = ""
	}
	secrets := store.ProviderSecretUpdate{APIKey: strings.TrimSpace(req.APIKey) != ""}
	if p.Type != "bedrock" {
		// Wipe Bedrock-only settings so stale credentials do not linger
		// after a provider changes type.
		p.AWSRegion = ""
		p.AWSAuthMethod = ""
		p.AWSAccessKeyID = ""
		p.AWSSecretAccessKey = ""
		p.AWSSessionToken = ""
		p.BedrockAPIKey = ""
		secrets.AWSCredentials = true
		secrets.BedrockAPIKey = true
		return p, secrets, true
	}
	// Bedrock providers authenticate via AWS, not a base URL or bearer env var.
	p.BaseURL = ""
	p.APIKey = ""
	p.APIKeyEnv = ""
	secrets.APIKey = true
	if p.AWSAuthMethod == "" {
		p.AWSAuthMethod = "chain"
	}
	switch p.AWSAuthMethod {
	case "chain":
		p.AWSAccessKeyID = ""
		p.AWSSecretAccessKey = ""
		p.AWSSessionToken = ""
		p.BedrockAPIKey = ""
		secrets.AWSCredentials = true
		secrets.BedrockAPIKey = true
	case "keys":
		if p.AWSAccessKeyID == "" {
			respondError(w, http.StatusBadRequest, "aws_access_key_id is required for access-key auth")
			return store.Provider{}, store.ProviderSecretUpdate{}, false
		}
		if isCreate && p.AWSSecretAccessKey == "" {
			respondError(w, http.StatusBadRequest, "aws_secret_access_key is required for access-key auth")
			return store.Provider{}, store.ProviderSecretUpdate{}, false
		}
		secrets.AWSCredentials = p.AWSSecretAccessKey != ""
		p.BedrockAPIKey = ""
		secrets.BedrockAPIKey = true
	case "api_key":
		if isCreate && p.BedrockAPIKey == "" {
			respondError(w, http.StatusBadRequest, "bedrock_api_key is required for API-key auth")
			return store.Provider{}, store.ProviderSecretUpdate{}, false
		}
		secrets.BedrockAPIKey = p.BedrockAPIKey != ""
		p.AWSAccessKeyID = ""
		p.AWSSecretAccessKey = ""
		p.AWSSessionToken = ""
		secrets.AWSCredentials = true
	default:
		respondError(w, http.StatusBadRequest, "aws_auth_method must be chain, keys, or api_key")
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	return p, secrets, true
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

func providerAuditDetails(p store.Provider, secrets store.ProviderSecretUpdate) map[string]any {
	return map[string]any{
		"id":                      p.ID,
		"name":                    p.Name,
		"type":                    p.Type,
		"base_url":                p.BaseURL,
		"api_key_env":             p.APIKeyEnv,
		"azure_api_version":       p.AzureAPIVersion,
		"direct_secret_updated":   secrets.APIKey,
		"aws_region":              p.AWSRegion,
		"aws_auth_method":         p.AWSAuthMethod,
		"aws_credentials_updated": secrets.AWSCredentials,
		"bedrock_api_key_updated": secrets.BedrockAPIKey,
		"enabled":                 p.Enabled,
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

func guardrailAuditDetails(p store.GuardrailPolicy) map[string]any {
	return map[string]any{
		"enabled":              p.Enabled,
		"input_action":         p.InputAction,
		"output_action":        p.OutputAction,
		"detect_email":         p.DetectEmail,
		"detect_phone":         p.DetectPhone,
		"detect_ssn":           p.DetectSSN,
		"detect_credit_card":   p.DetectCreditCard,
		"detect_api_key":       p.DetectAPIKey,
		"custom_patterns":      len(p.CustomPatterns),
		"streaming_block_mode": p.StreamingBlockMode,
	}
}

func validGuardrailAction(action string) bool {
	return action == "off" || action == "redact" || action == "block"
}

func budgetDisplay(b store.Budget) string {
	return b.ScopeType + ":" + b.ScopeValue
}

func rateLimitDisplay(rl store.RateLimit) string {
	return rl.ScopeType + ":" + rl.ScopeValue
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
	var pendingToolResults []types.ContentBlock
	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		messages = append(messages, types.Message{Role: types.ConversationRoleUser, Content: pendingToolResults})
		pendingToolResults = nil
	}
	for _, item := range messagesRaw {
		msg, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("messages entries must be objects")
		}
		role, _ := msg["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))
		switch role {
		case "system", "developer":
			text, err := openAIMessageText(msg["content"])
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			system = append(system, &types.SystemContentBlockMemberText{Value: text})
		case "user", "assistant":
			flushToolResults()
			content, err := openAIContentBlocks(msg["content"], role)
			if err != nil {
				return nil, err
			}
			if role == "assistant" {
				toolCalls, err := openAIToolCallsToBedrock(msg["tool_calls"])
				if err != nil {
					return nil, err
				}
				content = append(content, toolCalls...)
			}
			if len(content) == 0 {
				continue
			}
			bedrockRole := types.ConversationRoleUser
			if role == "assistant" {
				bedrockRole = types.ConversationRoleAssistant
			}
			messages = append(messages, types.Message{
				Role:    bedrockRole,
				Content: content,
			})
		case "tool":
			content, err := openAIToolResultBlocks(msg)
			if err != nil {
				return nil, err
			}
			pendingToolResults = append(pendingToolResults, content...)
		default:
			return nil, fmt.Errorf("Bedrock adapter supports user, assistant, system, developer, and tool messages only; got role %q", role)
		}
	}
	flushToolResults()
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
	toolConfig, err := bedrockToolConfig(raw)
	if err != nil {
		return nil, err
	}
	input.ToolConfig = toolConfig
	return input, nil
}

func bedrockConverseStreamInput(modelID string, raw map[string]any) (*bedrockruntime.ConverseStreamInput, error) {
	input, err := bedrockConverseInput(modelID, raw)
	if err != nil {
		return nil, err
	}
	return &bedrockruntime.ConverseStreamInput{
		ModelId:         input.ModelId,
		Messages:        input.Messages,
		System:          input.System,
		InferenceConfig: input.InferenceConfig,
		ToolConfig:      input.ToolConfig,
	}, nil
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
			return "", fmt.Errorf("Bedrock system/developer messages only support text content parts; got %q", typ)
		}
		return strings.Join(parts, "\n"), nil
	default:
		return "", errors.New("message content must be a string or text content parts")
	}
}

func openAIContentBlocks(content any, role string) ([]types.ContentBlock, error) {
	switch v := content.(type) {
	case nil:
		return nil, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		return []types.ContentBlock{&types.ContentBlockMemberText{Value: v}}, nil
	case []any:
		var blocks []types.ContentBlock
		var textParts []string
		flushText := func() {
			if len(textParts) == 0 {
				return
			}
			blocks = append(blocks, &types.ContentBlockMemberText{Value: strings.Join(textParts, "\n")})
			textParts = nil
		}
		for _, item := range v {
			part, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("message content parts must be objects")
			}
			typ, _ := part["type"].(string)
			switch typ {
			case "", "text":
				text, _ := part["text"].(string)
				if text != "" {
					textParts = append(textParts, text)
				}
			case "image_url":
				if role != "user" {
					return nil, errors.New("Bedrock adapter only supports image_url content on user messages")
				}
				flushText()
				block, err := openAIImageURLToBedrock(part["image_url"])
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, block)
			default:
				return nil, fmt.Errorf("Bedrock adapter supports text and image_url content parts; got %q", typ)
			}
		}
		flushText()
		return blocks, nil
	default:
		return nil, errors.New("message content must be a string or content parts")
	}
}

func openAIImageURLToBedrock(raw any) (types.ContentBlock, error) {
	var url string
	switch v := raw.(type) {
	case string:
		url = v
	case map[string]any:
		url, _ = v["url"].(string)
	}
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("image_url.url is required")
	}
	meta, data, ok := strings.Cut(url, ",")
	if !ok || !strings.HasPrefix(strings.ToLower(meta), "data:") {
		return nil, errors.New("Bedrock adapter supports image_url data URLs only")
	}
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return nil, errors.New("image_url data URL must be base64 encoded")
	}
	format, err := bedrockImageFormat(strings.TrimPrefix(strings.ToLower(strings.Split(meta, ";")[0]), "data:"))
	if err != nil {
		return nil, err
	}
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, errors.New("image_url data URL contains invalid base64")
	}
	return &types.ContentBlockMemberImage{Value: types.ImageBlock{
		Format: format,
		Source: &types.ImageSourceMemberBytes{Value: bytes},
	}}, nil
}

func bedrockImageFormat(mimeType string) (types.ImageFormat, error) {
	switch mimeType {
	case "image/png":
		return types.ImageFormatPng, nil
	case "image/jpeg", "image/jpg":
		return types.ImageFormatJpeg, nil
	case "image/gif":
		return types.ImageFormatGif, nil
	case "image/webp":
		return types.ImageFormatWebp, nil
	default:
		return "", fmt.Errorf("unsupported Bedrock image format %q", mimeType)
	}
}

func openAIToolCallsToBedrock(raw any) ([]types.ContentBlock, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("assistant tool_calls must be an array")
	}
	blocks := make([]types.ContentBlock, 0, len(items))
	for _, item := range items {
		call, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("assistant tool_calls entries must be objects")
		}
		function, _ := call["function"].(map[string]any)
		name, _ := function["name"].(string)
		id, _ := call["id"].(string)
		if strings.TrimSpace(name) == "" || strings.TrimSpace(id) == "" {
			return nil, errors.New("assistant tool_calls require id and function.name")
		}
		var input any = map[string]any{}
		if args, _ := function["arguments"].(string); strings.TrimSpace(args) != "" {
			dec := json.NewDecoder(strings.NewReader(args))
			dec.UseNumber()
			if err := dec.Decode(&input); err != nil {
				return nil, errors.New("assistant tool_calls function.arguments must be valid JSON")
			}
		}
		blocks = append(blocks, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
			ToolUseId: aws.String(id),
			Name:      aws.String(name),
			Input:     bedrockdocument.NewLazyDocument(input),
		}})
	}
	return blocks, nil
}

func openAIToolResultBlocks(msg map[string]any) ([]types.ContentBlock, error) {
	toolUseID, _ := msg["tool_call_id"].(string)
	if strings.TrimSpace(toolUseID) == "" {
		return nil, errors.New("tool messages require tool_call_id")
	}
	text, err := openAIMessageText(msg["content"])
	if err != nil {
		return nil, err
	}
	result := types.ToolResultBlock{
		ToolUseId: aws.String(toolUseID),
		Content:   []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: text}},
	}
	if isError, _ := msg["is_error"].(bool); isError {
		result.Status = types.ToolResultStatusError
	}
	return []types.ContentBlock{&types.ContentBlockMemberToolResult{Value: result}}, nil
}

func bedrockToolConfig(raw map[string]any) (*types.ToolConfiguration, error) {
	if choice, _ := raw["tool_choice"].(string); strings.EqualFold(choice, "none") {
		return nil, nil
	}
	rawTools, ok := raw["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		return nil, nil
	}
	tools := make([]types.Tool, 0, len(rawTools))
	for _, item := range rawTools {
		tool, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("tools entries must be objects")
		}
		typ, _ := tool["type"].(string)
		if typ != "" && typ != "function" {
			return nil, fmt.Errorf("Bedrock adapter only supports function tools; got %q", typ)
		}
		function, ok := tool["function"].(map[string]any)
		if !ok {
			return nil, errors.New("function tool entries require a function object")
		}
		name, _ := function["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("function tools require function.name")
		}
		parameters := function["parameters"]
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		description, _ := function["description"].(string)
		tools = append(tools, &types.ToolMemberToolSpec{Value: types.ToolSpecification{
			Name:        aws.String(name),
			Description: aws.String(normalizedToolDescription(name, description)),
			InputSchema: &types.ToolInputSchemaMemberJson{Value: bedrockdocument.NewLazyDocument(parameters)},
		}})
	}
	if len(tools) == 0 {
		return nil, nil
	}
	config := &types.ToolConfiguration{Tools: tools}
	switch choice := raw["tool_choice"].(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "", "auto":
			config.ToolChoice = &types.ToolChoiceMemberAuto{Value: types.AutoToolChoice{}}
		case "required":
			config.ToolChoice = &types.ToolChoiceMemberAny{Value: types.AnyToolChoice{}}
		case "none":
			return nil, nil
		default:
			return nil, fmt.Errorf("unsupported tool_choice %q", choice)
		}
	case map[string]any:
		if typ, _ := choice["type"].(string); typ != "function" {
			return nil, errors.New("object tool_choice must use type function")
		}
		function, _ := choice["function"].(map[string]any)
		name, _ := function["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("function tool_choice requires function.name")
		}
		config.ToolChoice = &types.ToolChoiceMemberTool{Value: types.SpecificToolChoice{Name: aws.String(name)}}
	}
	return config, nil
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
	message := bedrockOpenAIMessage(output)
	return map[string]any{
		"id":      requestID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   route.Model.Route,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
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
	return bedrockTokenUsage(output.Usage)
}

func bedrockTokenUsage(raw *types.TokenUsage) tokenUsage {
	if raw == nil {
		return tokenUsage{}
	}
	usage := tokenUsage{}
	if raw.InputTokens != nil {
		usage.Input = int(*raw.InputTokens)
	}
	if raw.OutputTokens != nil {
		usage.Output = int(*raw.OutputTokens)
	}
	if raw.TotalTokens != nil {
		usage.Total = int(*raw.TotalTokens)
	}
	if usage.Total == 0 {
		usage.Total = usage.Input + usage.Output
	}
	return usage
}

func bedrockOpenAIMessage(output *bedrockruntime.ConverseOutput) map[string]any {
	message := map[string]any{
		"role":    "assistant",
		"content": bedrockOutputText(output),
	}
	toolCalls := bedrockOutputToolCalls(output)
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	return message
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

func bedrockOutputToolCalls(output *bedrockruntime.ConverseOutput) []map[string]any {
	if output == nil {
		return nil
	}
	message, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return nil
	}
	var calls []map[string]any
	for _, content := range message.Value.Content {
		toolUse, ok := content.(*types.ContentBlockMemberToolUse)
		if !ok {
			continue
		}
		calls = append(calls, map[string]any{
			"id":   aws.ToString(toolUse.Value.ToolUseId),
			"type": "function",
			"function": map[string]any{
				"name":      aws.ToString(toolUse.Value.Name),
				"arguments": bedrockDocumentJSON(toolUse.Value.Input),
			},
		})
	}
	return calls
}

func bedrockDocumentJSON(value any) string {
	if value == nil {
		return "{}"
	}
	if marshaler, ok := value.(interface {
		MarshalSmithyDocument() ([]byte, error)
	}); ok {
		body, err := marshaler.MarshalSmithyDocument()
		if err == nil && len(body) > 0 {
			return string(body)
		}
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 {
		return "{}"
	}
	return string(body)
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

// defaultAzureAPIVersion is the Azure OpenAI data-plane api-version used when
// a provider does not pin one. 2024-10-21 is the latest GA version.
const defaultAzureAPIVersion = "2024-10-21"

// defaultGoogleBaseURL is the Gemini API's OpenAI-compatible surface. Google
// maintains this compatibility layer for the Gemini Developer API (AI Studio
// keys), covering chat completions, streaming, and tool calls with standard
// Bearer authentication.
const defaultGoogleBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

// providerProtocol maps a provider type to the wire protocol it speaks.
// Azure OpenAI deployments and the Gemini API's OpenAI-compatible surface
// speak the OpenAI chat-completions protocol; Claude deployments in Azure AI
// Foundry speak the Anthropic Messages protocol. These types differ only in
// endpoint shape and auth headers.
func providerProtocol(p store.Provider) string {
	switch p.Type {
	case "azure-openai", "google":
		return "openai"
	case "azure-anthropic":
		return "anthropic"
	default:
		return p.Type
	}
}

// ensureStreamUsageOption asks the upstream to include usage in the final
// stream chunk on providers that support but do not default to it, so
// streamed requests are billed from real token counts instead of estimates.
func ensureStreamUsageOption(p store.Provider, raw map[string]any) {
	if p.Type != "google" {
		return
	}
	if stream, _ := raw["stream"].(bool); !stream {
		return
	}
	if _, ok := raw["stream_options"]; ok {
		return
	}
	raw["stream_options"] = map[string]any{"include_usage": true}
}

func azureAPIVersion(p store.Provider) string {
	if v := strings.TrimSpace(p.AzureAPIVersion); v != "" {
		return v
	}
	return defaultAzureAPIVersion
}

// openAIChatEndpoint returns the upstream chat-completions URL for an
// OpenAI-protocol provider. Azure OpenAI routes through per-deployment paths
// (the upstream model id is the deployment name) with a required api-version
// query parameter.
func openAIChatEndpoint(p store.Provider, upstreamModel string) string {
	base := strings.TrimRight(p.BaseURL, "/")
	if p.Type == "azure-openai" {
		return base + "/openai/deployments/" + urlpkg.PathEscape(upstreamModel) + "/chat/completions?api-version=" + urlpkg.QueryEscape(azureAPIVersion(p))
	}
	return base + "/chat/completions"
}

func anthropicMessagesEndpoint(p store.Provider) string {
	return strings.TrimRight(p.BaseURL, "/") + "/v1/messages"
}

// openAIPayloadForUnsupportedParams returns a copy of an OpenAI-protocol
// chat-completions payload adjusted after an upstream 400 that rejects legacy
// sampling parameters. Reasoning-family models (o-series, GPT-5) accept only
// max_completion_tokens and the default temperature; the upstream error names
// the offending parameter. ok is false when no adjustment applies.
func openAIPayloadForUnsupportedParams(payload map[string]any, status int, responseBody string) (map[string]any, bool) {
	if status != http.StatusBadRequest {
		return nil, false
	}
	adjusted := cloneJSONMap(payload)
	changed := false
	if strings.Contains(responseBody, "max_completion_tokens") {
		if v, ok := adjusted["max_tokens"]; ok {
			delete(adjusted, "max_tokens")
			adjusted["max_completion_tokens"] = v
			changed = true
		}
	}
	if strings.Contains(responseBody, "'temperature'") {
		if _, ok := adjusted["temperature"]; ok {
			delete(adjusted, "temperature")
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	return adjusted, true
}

func setOpenAIAuthHeader(req *http.Request, p store.Provider) {
	apiKey := providerAPIKey(p)
	if apiKey == "" {
		return
	}
	if p.Type == "azure-openai" {
		req.Header.Set("api-key", apiKey)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

func setAnthropicAuthHeader(req *http.Request, p store.Provider) {
	apiKey := providerAPIKey(p)
	if apiKey == "" {
		return
	}
	req.Header.Set("x-api-key", apiKey)
	if p.Type == "azure-anthropic" {
		// Azure AI Foundry accepts the Anthropic-style x-api-key header, but
		// some gateway configurations only honor Azure's api-key header, so
		// send both.
		req.Header.Set("api-key", apiKey)
	}
}

func providerAPIKey(p store.Provider) string {
	if p.APIKeyEnv != "" {
		if value := os.Getenv(p.APIKeyEnv); value != "" {
			return value
		}
	}
	return p.APIKey
}

var _ = sql.ErrNoRows
