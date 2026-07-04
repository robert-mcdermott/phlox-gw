package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
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

func writeAnthropicResult(w http.ResponseWriter, result upstreamResult) {
	if result.Body == nil {
		anthropicError(w, resultStatus(result), fallbackString(result.ErrorText, "provider unavailable"))
		return
	}
	writeUpstreamResult(w, result)
}

func cloneJSONMap(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out
}

var _ = sql.ErrNoRows
