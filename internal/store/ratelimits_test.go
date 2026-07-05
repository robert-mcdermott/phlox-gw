package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRateLimitCRUDAndWindowUsage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if err := s.EnsureSeedData("hash"); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
	}
	admin, err := s.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	route, err := s.ResolveModel(ctx, "local-ollama/llama3.1:8b")
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	limits := []RateLimit{
		{ID: "limit_user", ScopeType: "user", ScopeValue: admin.ID, RPMLimit: 1, IsActive: true},
		{ID: "limit_department", ScopeType: "department", ScopeValue: admin.Department, RPMLimit: 2, IsActive: true},
		{ID: "limit_provider", ScopeType: "provider", ScopeValue: route.Provider.ID, TPMLimit: 50, IsActive: true},
		{ID: "limit_model", ScopeType: "model", ScopeValue: route.Model.Route, TPMLimit: 75, IsActive: true},
	}
	for _, limit := range limits {
		if err := s.CreateRateLimit(ctx, limit); err != nil {
			t.Fatalf("CreateRateLimit %s: %v", limit.ID, err)
		}
	}
	now := time.Now().UTC()
	if err := s.InsertUsage(ctx, UsageRecord{
		ID:          "usage_limit_1",
		RequestID:   "req_limit_1",
		UserID:      admin.ID,
		Username:    admin.Username,
		Department:  admin.Department,
		ProviderID:  route.Provider.ID,
		Model:       route.Model.Route,
		TotalTokens: 123,
		StatusCode:  200,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}
	applicable, err := s.ApplicableRateLimits(ctx, admin, route)
	if err != nil {
		t.Fatalf("ApplicableRateLimits: %v", err)
	}
	if len(applicable) != 4 {
		t.Fatalf("expected four applicable limits, got %#v", applicable)
	}
	for _, limit := range applicable {
		window, err := s.RateLimitWindowUsage(ctx, limit, now.Add(-time.Minute))
		if err != nil {
			t.Fatalf("RateLimitWindowUsage %s: %v", limit.ID, err)
		}
		if window.Requests != 1 || window.TotalTokens != 123 {
			t.Fatalf("unexpected usage for %s: %#v", limit.ID, window)
		}
	}
	updated := RateLimit{ID: "limit_user", ScopeType: "user", ScopeValue: admin.ID, RPMLimit: 3, TPMLimit: 300, IsActive: false}
	if err := s.UpdateRateLimit(ctx, updated); err != nil {
		t.Fatalf("UpdateRateLimit: %v", err)
	}
	listed, err := s.ListRateLimits(ctx)
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(listed) != 4 {
		t.Fatalf("expected four listed limits, got %d", len(listed))
	}
	if err := s.DeleteRateLimit(ctx, "limit_user"); err != nil {
		t.Fatalf("DeleteRateLimit: %v", err)
	}
}
