package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBudgetStatusBlocksPricedModelsOverMonthlySpend(t *testing.T) {
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
	if err := s.CreateBudget(ctx, Budget{
		ID:         "budget_admin",
		ScopeType:  "user",
		ScopeValue: admin.ID,
		LimitUSD:   0.01,
		WarnPct:    90,
		IsActive:   true,
	}); err != nil {
		t.Fatalf("CreateBudget: %v", err)
	}
	if err := s.InsertUsage(ctx, UsageRecord{
		ID:         "usage_1",
		RequestID:  "req_1",
		UserID:     admin.ID,
		Username:   admin.Username,
		Department: admin.Department,
		Model:      "openai/test",
		CostUSD:    0.02,
		StatusCode: 200,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}
	status, err := s.BudgetStatus(ctx, admin, true)
	if err != nil {
		t.Fatalf("BudgetStatus: %v", err)
	}
	if !status.Blocked {
		t.Fatalf("expected blocked budget status, got %#v", status)
	}
}
