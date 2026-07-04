package httpapi

import (
	"context"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
	"github.com/robert-mcdermott/phlox-gw/internal/telemetry"
)

func (s *Server) recordUsage(ctx context.Context, requestID string, user store.User, key store.APIKey, route store.RoutedModel, protocol string, usage tokenUsage, latencyMS int64, status int, errText string, eventMeta requestEventMeta) {
	if usage.Total == 0 && (usage.Input > 0 || usage.Output > 0) {
		usage.Total = usage.Input + usage.Output
	}
	id, err := auth.RandomID("usage")
	if err != nil {
		s.logger.Warn("usage id failed", "error", err)
		return
	}
	now := time.Now().UTC()
	cost := store.Cost(usage.Input, usage.Output, route.Model)
	s.telemetry.ObserveUpstream(telemetry.UpstreamObservation{
		ProviderID:   route.Provider.ID,
		ProviderType: route.Provider.Type,
		ModelRoute:   route.Model.Route,
		Protocol:     protocol,
		Status:       status,
		Latency:      time.Duration(latencyMS) * time.Millisecond,
		InputTokens:  usage.Input,
		OutputTokens: usage.Output,
		TotalTokens:  usage.Total,
		CostUSD:      cost,
	})
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
		CostUSD:      cost,
		LatencyMS:    latencyMS,
		StatusCode:   status,
		ErrorText:    limitString(errText, 1000),
		CreatedAt:    now,
	}
	if err := s.store.InsertUsage(ctx, rec); err != nil {
		s.logger.Warn("usage insert failed", "error", err)
	}
	logID, err := auth.RandomID("reqlog")
	if err != nil {
		s.logger.Warn("request log id failed", "error", err)
		return
	}
	requestLog := store.RequestLogRecord{
		ID:              logID,
		RequestID:       requestID,
		UserID:          user.ID,
		Username:        user.Username,
		Department:      user.Department,
		APIKeyID:        key.ID,
		APIKeyPrefix:    key.Prefix,
		APIKeyName:      key.Name,
		ProviderID:      route.Provider.ID,
		ProviderType:    route.Provider.Type,
		ModelRoute:      route.Model.Route,
		UpstreamModelID: route.Model.ModelID,
		Protocol:        protocol,
		Method:          eventMeta.Method,
		Endpoint:        eventMeta.Endpoint,
		Streaming:       eventMeta.Streaming,
		InputTokens:     usage.Input,
		OutputTokens:    usage.Output,
		TotalTokens:     usage.Total,
		CostUSD:         cost,
		LatencyMS:       latencyMS,
		StatusCode:      status,
		ErrorText:       limitString(errText, 1000),
		ClientIP:        limitString(eventMeta.ClientIP, 200),
		UserAgent:       limitString(eventMeta.UserAgent, 500),
		CreatedAt:       now,
	}
	if err := s.store.InsertRequestLog(ctx, requestLog); err != nil {
		s.logger.Warn("request log insert failed", "error", err)
	}
}
