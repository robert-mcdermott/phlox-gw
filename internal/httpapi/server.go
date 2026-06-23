package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

type Options struct {
	Config     config.Config
	Store      *store.Store
	Frontend   fs.FS
	Logger     *slog.Logger
	HTTPClient *http.Client
}

type Server struct {
	cfg        config.Config
	store      *store.Store
	logger     *slog.Logger
	httpClient *http.Client
	frontend   fs.FS
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
		cfg:        opts.Config,
		store:      opts.Store,
		logger:     opts.Logger,
		httpClient: opts.HTTPClient,
		frontend:   sub,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/me", s.requireSession(s.me))
	mux.HandleFunc("GET /api/models", s.requireSession(s.models))
	mux.HandleFunc("GET /api/usage", s.requireSession(s.usage))
	mux.HandleFunc("GET /api/usage/budget", s.requireSession(s.budgetStatus))
	mux.HandleFunc("GET /api/api-keys", s.requireSession(s.listAPIKeys))
	mux.HandleFunc("POST /api/api-keys", s.requireSession(s.createAPIKey))
	mux.HandleFunc("DELETE /api/api-keys/{id}", s.requireSession(s.revokeAPIKey))
	mux.HandleFunc("GET /api/admin/users", s.requireAdmin(s.listUsers))
	mux.HandleFunc("POST /api/admin/users", s.requireAdmin(s.createUser))
	mux.HandleFunc("GET /api/admin/providers", s.requireAdmin(s.providers))
	mux.HandleFunc("POST /api/admin/providers", s.requireAdmin(s.createProvider))
	mux.HandleFunc("PUT /api/admin/providers/{id}", s.requireAdmin(s.updateProvider))
	mux.HandleFunc("GET /api/admin/models", s.requireAdmin(s.adminModels))
	mux.HandleFunc("POST /api/admin/models", s.requireAdmin(s.createModel))
	mux.HandleFunc("PUT /api/admin/models/{id}", s.requireAdmin(s.updateModel))
	mux.HandleFunc("GET /api/admin/budgets", s.requireAdmin(s.listBudgets))
	mux.HandleFunc("POST /api/admin/budgets", s.requireAdmin(s.createBudget))
	mux.HandleFunc("DELETE /api/admin/budgets/{id}", s.requireAdmin(s.deleteBudget))
	mux.HandleFunc("GET /api/admin/usage/summary", s.requireAdmin(s.adminUsage))
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
	now := time.Now().UTC()
	claims := auth.Claims{
		Subject:  user.ID,
		Username: user.Username,
		Role:     user.Role,
		IssuedAt: now.Unix(),
		Expires:  now.Add(12 * time.Hour).Unix(),
	}
	token, err := auth.SignSession(claims, s.cfg.SessionSecret)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not sign session")
		return
	}
	_ = s.store.TouchLogin(r.Context(), user.ID, now)
	respondJSON(w, http.StatusOK, map[string]any{"token": token, "user": publicUser(user)})
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

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, _ store.User) {
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
	respondJSON(w, http.StatusCreated, publicUser(user))
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
	var expires *time.Time
	if strings.TrimSpace(req.ExpiresAt) != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			respondError(w, http.StatusBadRequest, "expires_at must be RFC3339")
			return
		}
		expires = &t
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
	respondJSON(w, http.StatusCreated, map[string]any{"key": plain, "record": key})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request, user store.User) {
	if err := s.store.RevokeAPIKey(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "key not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
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

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request, _ store.User) {
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
	respondJSON(w, http.StatusCreated, p)
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, _ store.User) {
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
	respondJSON(w, http.StatusOK, p)
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

func (s *Server) createModel(w http.ResponseWriter, r *http.Request, _ store.User) {
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
	respondJSON(w, http.StatusCreated, m)
}

func (s *Server) updateModel(w http.ResponseWriter, r *http.Request, _ store.User) {
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
	respondJSON(w, http.StatusOK, m)
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

func (s *Server) createBudget(w http.ResponseWriter, r *http.Request, _ store.User) {
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
	respondJSON(w, http.StatusCreated, b)
}

func (s *Server) deleteBudget(w http.ResponseWriter, r *http.Request, _ store.User) {
	if err := s.store.DeleteBudget(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "budget not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) openAIModels(w http.ResponseWriter, r *http.Request, user store.User, _ store.APIKey) {
	models, err := s.store.ListModels(r.Context(), false)
	if err != nil {
		openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		status, _ := s.store.BudgetStatus(r.Context(), user, store.IsPriced(m))
		data = append(data, map[string]any{
			"id":            m.Route,
			"object":        "model",
			"owned_by":      m.ProviderID,
			"phlox_blocked": status.Blocked,
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) openAIChatCompletions(w http.ResponseWriter, r *http.Request, user store.User, key store.APIKey) {
	body, raw, ok := readObjectBody(w, r, true)
	if !ok {
		return
	}
	modelName, _ := raw["model"].(string)
	route, err := s.store.ResolveModel(r.Context(), modelName)
	if err != nil {
		openAIError(w, http.StatusBadRequest, "unknown or disabled model", "invalid_request_error")
		return
	}
	if route.Provider.Type != "openai" {
		openAIError(w, http.StatusNotImplemented, "model is not on an OpenAI-compatible provider", "unsupported_provider")
		return
	}
	if blocked, reason := s.checkBudget(r.Context(), user, route.Model); blocked {
		openAIError(w, http.StatusPaymentRequired, reason, "insufficient_quota")
		return
	}

	raw["model"] = route.Model.ModelID
	body, _ = json.Marshal(raw)
	start := time.Now()
	requestID := requestID()
	statusCode, responseBody, errText := s.proxyOpenAI(w, r, route, body, raw)
	latency := time.Since(start).Milliseconds()
	usage := parseOpenAIUsage(responseBody)
	s.recordUsage(r.Context(), requestID, user, key, route, "openai", usage, latency, statusCode, errText)
}

func (s *Server) anthropicMessages(w http.ResponseWriter, r *http.Request, user store.User, key store.APIKey) {
	body, raw, ok := readObjectBody(w, r, false)
	if !ok {
		return
	}
	modelName, _ := raw["model"].(string)
	route, err := s.store.ResolveModel(r.Context(), modelName)
	if err != nil {
		anthropicError(w, http.StatusBadRequest, "unknown or disabled model")
		return
	}
	if route.Provider.Type != "anthropic" {
		anthropicError(w, http.StatusNotImplemented, "model is not on an Anthropic-compatible provider")
		return
	}
	if blocked, reason := s.checkBudget(r.Context(), user, route.Model); blocked {
		anthropicError(w, http.StatusPaymentRequired, reason)
		return
	}
	raw["model"] = route.Model.ModelID
	body, _ = json.Marshal(raw)
	start := time.Now()
	requestID := requestID()
	statusCode, responseBody, errText := s.proxyAnthropic(w, r, route, body)
	latency := time.Since(start).Milliseconds()
	usage := parseAnthropicUsage(responseBody)
	s.recordUsage(r.Context(), requestID, user, key, route, "anthropic", usage, latency, statusCode, errText)
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
		n, err := io.Copy(w, resp.Body)
		if err != nil {
			return resp.StatusCode, nil, err.Error()
		}
		return resp.StatusCode, []byte(fmt.Sprintf(`{"streamed_bytes":%d}`, n)), ""
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
	route := strings.TrimSpace(req.Route)
	if route == "" {
		route = providerID + "/" + modelID
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = modelID
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
	}, true
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
