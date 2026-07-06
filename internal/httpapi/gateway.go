package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

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
	if protocol := providerProtocol(route.Provider); protocol != "openai" && protocol != "bedrock" && protocol != "anthropic" {
		openAIError(w, http.StatusNotImplemented, "model is not on a supported provider", "unsupported_provider")
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
		} else if providerProtocol(selected.Provider) == "anthropic" {
			statusCode, responseBody, errText = s.proxyOpenAIViaAnthropicStream(w, r, selected, raw, guardrails)
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

func cloneJSONMap(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out
}
