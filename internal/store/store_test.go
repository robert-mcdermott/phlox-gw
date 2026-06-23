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

func TestAPIKeyGovernanceControlsAndUsage(t *testing.T) {
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

	key := APIKey{
		ID:             "key_governed",
		UserID:         admin.ID,
		Name:           "Initial",
		Prefix:         "pgw-sk-test",
		KeyHash:        "hashed-key",
		BudgetUSD:      10,
		RPMLimit:       5,
		TPMLimit:       500,
		ModelAllowlist: "local-ollama/llama3.1:8b",
	}
	if err := s.CreateAPIKey(ctx, key); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	updated := APIKey{
		ID:             key.ID,
		Name:           "Production key",
		IsActive:       true,
		ExpiresAt:      &expires,
		BudgetUSD:      1.25,
		RPMLimit:       10,
		TPMLimit:       1000,
		ModelAllowlist: "openai/gpt-4o-mini, local-ollama/llama3.1:8b",
	}
	if err := s.UpdateAPIKeyControls(ctx, updated); err != nil {
		t.Fatalf("UpdateAPIKeyControls: %v", err)
	}
	keys, err := s.ListAPIKeys(ctx, admin.ID)
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected one key, got %d", len(keys))
	}
	got := keys[0]
	if got.Name != "Production key" || !got.IsActive || got.BudgetUSD != 1.25 || got.RPMLimit != 10 || got.TPMLimit != 1000 {
		t.Fatalf("unexpected key controls: %#v", got)
	}
	if got.ModelAllowlist != "openai/gpt-4o-mini\nlocal-ollama/llama3.1:8b" {
		t.Fatalf("unexpected normalized allowlist: %q", got.ModelAllowlist)
	}

	now := time.Now().UTC()
	if err := s.InsertUsage(ctx, UsageRecord{
		ID:          "usage_key_1",
		RequestID:   "req_key_1",
		UserID:      admin.ID,
		Username:    admin.Username,
		APIKeyID:    key.ID,
		Model:       "openai/gpt-4o-mini",
		TotalTokens: 123,
		CostUSD:     0.75,
		StatusCode:  200,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}
	start, end := monthBounds(now)
	spend, err := s.APIKeyMonthlySpend(ctx, key.ID, start, end)
	if err != nil {
		t.Fatalf("APIKeyMonthlySpend: %v", err)
	}
	if spend != 0.75 {
		t.Fatalf("APIKeyMonthlySpend = %v, want 0.75", spend)
	}
	window, err := s.APIKeyWindowUsage(ctx, key.ID, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("APIKeyWindowUsage: %v", err)
	}
	if window.Requests != 1 || window.TotalTokens != 123 {
		t.Fatalf("unexpected window usage: %#v", window)
	}
	adminKeys, err := s.ListAllAPIKeys(ctx)
	if err != nil {
		t.Fatalf("ListAllAPIKeys: %v", err)
	}
	if len(adminKeys) != 1 || adminKeys[0].Username != admin.Username || adminKeys[0].MonthlySpendUSD != 0.75 {
		t.Fatalf("unexpected admin key listing: %#v", adminKeys)
	}
}

func TestAuditLogInsertAndList(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.InsertAuditLog(ctx, AuditLog{
		ID:            "audit_1",
		ActorUserID:   "user_admin",
		ActorUsername: "admin",
		Action:        "provider.create",
		TargetType:    "provider",
		TargetID:      "local-vllm",
		TargetDisplay: "Local vLLM",
		Details:       `{"type":"openai","enabled":true}`,
		IPAddress:     "127.0.0.1",
		UserAgent:     "test",
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertAuditLog: %v", err)
	}
	items, err := s.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one audit item, got %d", len(items))
	}
	got := items[0]
	if got.Action != "provider.create" || got.TargetID != "local-vllm" || got.ActorUsername != "admin" || got.Details == "" {
		t.Fatalf("unexpected audit item: %#v", got)
	}
}
