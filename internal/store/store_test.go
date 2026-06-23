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

func TestCostUsesInputAndOutputPricing(t *testing.T) {
	got := Cost(1_000, 2_000, Model{InputCostPerMillion: 3, OutputCostPerMillion: 15})
	want := 0.033
	if got != want {
		t.Fatalf("Cost() = %v, want %v", got, want)
	}
}

func TestProviderAndModelCRUD(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	provider := Provider{
		ID:      "local-vllm",
		Name:    "Local vLLM",
		Type:    "openai",
		BaseURL: "http://127.0.0.1:8000/v1",
		Enabled: true,
	}
	if err := s.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	provider.Name = "Local vLLM Updated"
	provider.APIKeyEnv = "VLLM_API_KEY"
	if err := s.UpdateProvider(ctx, provider, false); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}

	model := Model{
		ID:                   "model_vllm_qwen",
		ProviderID:           provider.ID,
		ModelID:              "qwen3:32b",
		Route:                "local-vllm/qwen3:32b",
		DisplayName:          "Qwen 32B",
		InputCostPerMillion:  0.15,
		OutputCostPerMillion: 0.60,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := s.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	model.InputCostPerMillion = 0.25
	if err := s.UpdateModel(ctx, model); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	routed, err := s.ResolveModel(ctx, model.Route)
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	if routed.Provider.Name != provider.Name || routed.Model.InputCostPerMillion != 0.25 {
		t.Fatalf("unexpected routed model: %#v", routed)
	}
}
