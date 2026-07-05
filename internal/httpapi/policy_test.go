package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestCheckAPIKeyPolicy(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	s := &Server{store: st}
	route := store.RoutedModel{Model: store.Model{
		ID:                  "model_allowed",
		Route:               "local-ollama/gemma4:31b-cloud",
		ModelID:             "gemma4:31b-cloud",
		InputCostPerMillion: 1,
	}}

	blocked, status, _, typ := s.checkAPIKeyPolicy(ctx, store.APIKey{ID: "key_1", ModelAllowlist: "openai/gpt-4o-mini"}, route)
	if !blocked || status != http.StatusForbidden || typ != "permission_error" {
		t.Fatalf("expected allowlist block, got blocked=%v status=%d type=%q", blocked, status, typ)
	}

	key := store.APIKey{ID: "key_budget", ModelAllowlist: route.Model.Route, BudgetUSD: 0.01}
	if err := st.InsertUsage(ctx, store.UsageRecord{
		ID:         "usage_budget",
		RequestID:  "req_budget",
		APIKeyID:   key.ID,
		Model:      route.Model.Route,
		CostUSD:    0.02,
		StatusCode: 200,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertUsage budget: %v", err)
	}
	blocked, status, _, typ = s.checkAPIKeyPolicy(ctx, key, route)
	if !blocked || status != http.StatusPaymentRequired || typ != "insufficient_quota" {
		t.Fatalf("expected budget block, got blocked=%v status=%d type=%q", blocked, status, typ)
	}

	key = store.APIKey{ID: "key_rpm", ModelAllowlist: route.Model.Route, RPMLimit: 1}
	if err := st.InsertUsage(ctx, store.UsageRecord{
		ID:          "usage_rpm",
		RequestID:   "req_rpm",
		APIKeyID:    key.ID,
		Model:       route.Model.Route,
		TotalTokens: 20,
		StatusCode:  200,
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertUsage rpm: %v", err)
	}
	blocked, status, _, typ = s.checkAPIKeyPolicy(ctx, key, route)
	if !blocked || status != http.StatusTooManyRequests || typ != "rate_limit_exceeded" {
		t.Fatalf("expected rpm block, got blocked=%v status=%d type=%q", blocked, status, typ)
	}
}
