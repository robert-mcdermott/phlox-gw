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

func TestProviderHealthCircuitState(t *testing.T) {
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
	now := time.Now().UTC()
	if _, err := s.RecordProviderFailure(ctx, provider.ID, 2, time.Minute, now, "upstream failed"); err != nil {
		t.Fatalf("RecordProviderFailure first: %v", err)
	}
	down, err := s.RecordProviderFailure(ctx, provider.ID, 2, time.Minute, now.Add(time.Second), "upstream failed again")
	if err != nil {
		t.Fatalf("RecordProviderFailure second: %v", err)
	}
	if down.HealthStatus != "down" || down.ConsecutiveFailures != 2 || down.CircuitOpenUntil == nil {
		t.Fatalf("expected open circuit after threshold, got %#v", down)
	}
	if err := s.RecordProviderSuccess(ctx, provider.ID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("RecordProviderSuccess: %v", err)
	}
	healthy, err := s.GetProvider(ctx, provider.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if healthy.HealthStatus != "healthy" || healthy.ConsecutiveFailures != 0 || healthy.CircuitOpenUntil != nil || healthy.LastError != "" {
		t.Fatalf("expected reset health after success, got %#v", healthy)
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

func TestUsageTimeSeriesFillsDaysAndAggregatesMetrics(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	if err := s.InsertUsage(ctx, UsageRecord{
		ID:           "usage_series_1",
		RequestID:    "req_series_1",
		Model:        "openai/test",
		InputTokens:  10,
		OutputTokens: 20,
		TotalTokens:  30,
		CostUSD:      0.01,
		LatencyMS:    100,
		StatusCode:   200,
		CreatedAt:    now.AddDate(0, 0, -2),
	}); err != nil {
		t.Fatalf("InsertUsage 1: %v", err)
	}
	if err := s.InsertUsage(ctx, UsageRecord{
		ID:           "usage_series_2",
		RequestID:    "req_series_2",
		Model:        "openai/test",
		InputTokens:  5,
		OutputTokens: 15,
		TotalTokens:  20,
		CostUSD:      0.02,
		LatencyMS:    300,
		StatusCode:   503,
		ErrorText:    "provider error",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("InsertUsage 2: %v", err)
	}

	points, err := s.UsageTimeSeries(ctx, 3, now)
	if err != nil {
		t.Fatalf("UsageTimeSeries: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(points))
	}
	if points[0].Date != "2026-06-21" || points[0].Requests != 1 || points[0].TotalTokens != 30 || points[0].CostUSD != 0.01 || points[0].AvgLatencyMS != 100 {
		t.Fatalf("unexpected first point: %#v", points[0])
	}
	if points[1].Date != "2026-06-22" || points[1].Requests != 0 {
		t.Fatalf("expected empty middle day, got %#v", points[1])
	}
	if points[2].Date != "2026-06-23" || points[2].Requests != 1 || points[2].Errors != 1 || points[2].TotalTokens != 20 || points[2].CostUSD != 0.02 || points[2].AvgLatencyMS != 300 {
		t.Fatalf("unexpected final point: %#v", points[2])
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
