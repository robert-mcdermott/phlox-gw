package httpapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

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
