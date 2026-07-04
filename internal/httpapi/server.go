package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	smithybearer "github.com/aws/smithy-go/auth/bearer"
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

var _ = sql.ErrNoRows
