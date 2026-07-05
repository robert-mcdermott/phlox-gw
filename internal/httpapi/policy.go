package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

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
