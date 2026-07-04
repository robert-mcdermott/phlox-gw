package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestLogSearchFiltersMetadata(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	now := time.Now().UTC()
	streaming := true
	records := []RequestLogRecord{
		{
			ID:              "reqlog_1",
			RequestID:       "req_success",
			UserID:          "user_1",
			Username:        "alice",
			Department:      "Research",
			APIKeyID:        "key_1",
			APIKeyPrefix:    "pgw-sk-alice",
			APIKeyName:      "Notebook",
			ProviderID:      "anthropic",
			ProviderType:    "anthropic",
			ModelRoute:      "anthropic/claude",
			UpstreamModelID: "claude-upstream",
			Protocol:        "anthropic",
			Method:          "POST",
			Endpoint:        "/anthropic/v1/messages",
			Streaming:       true,
			InputTokens:     8,
			OutputTokens:    5,
			TotalTokens:     13,
			CostUSD:         0.000018,
			LatencyMS:       120,
			StatusCode:      200,
			ClientIP:        "203.0.113.10",
			UserAgent:       "test-client",
			CreatedAt:       now,
		},
		{
			ID:         "reqlog_2",
			RequestID:  "req_error",
			Username:   "bob",
			Department: "IT",
			ProviderID: "openai",
			ModelRoute: "openai/gpt",
			Protocol:   "openai",
			Method:     "POST",
			Endpoint:   "/v1/chat/completions",
			StatusCode: 500,
			ErrorText:  "upstream failed",
			CreatedAt:  now.Add(-time.Hour),
		},
	}
	for _, rec := range records {
		if err := s.InsertRequestLog(ctx, rec); err != nil {
			t.Fatalf("InsertRequestLog: %v", err)
		}
	}
	result, err := s.SearchRequestLogs(ctx, RequestLogQuery{
		Search:     "claude",
		Department: "Research",
		Protocol:   "anthropic",
		Status:     "success",
		Streaming:  &streaming,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("SearchRequestLogs: %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].RequestID != "req_success" {
		t.Fatalf("unexpected request log result: %#v", result)
	}
	errors, err := s.SearchRequestLogs(ctx, RequestLogQuery{Status: "error", Limit: 10})
	if err != nil {
		t.Fatalf("SearchRequestLogs errors: %v", err)
	}
	if errors.Total != 1 || len(errors.Items) != 1 || errors.Items[0].RequestID != "req_error" {
		t.Fatalf("unexpected error result: %#v", errors)
	}
}
