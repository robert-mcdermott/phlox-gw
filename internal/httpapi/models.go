package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

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

func providerStatusIsFailure(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode >= 500
}

func providerCircuitOpen(p store.Provider, now time.Time) (bool, string) {
	if p.CircuitOpenUntil == nil || !p.CircuitOpenUntil.After(now) {
		return false, ""
	}
	return true, "provider circuit is open until " + p.CircuitOpenUntil.UTC().Format(time.RFC3339)
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
