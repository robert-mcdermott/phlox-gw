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

func TestClusterNodeUpsertAndList(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	started := time.Now().UTC().Add(-time.Minute)
	if err := s.UpsertClusterNode(ctx, ClusterNode{
		InstanceID:     "node-1",
		Hostname:       "host-a",
		Version:        "test",
		Addr:           "127.0.0.1:8081",
		DeploymentMode: "cluster-postgres",
		DBDriver:       "postgres",
		Status:         "starting",
		StartedAt:      started,
		LastSeenAt:     started,
		Metadata:       `{"role":"test"}`,
	}); err != nil {
		t.Fatalf("UpsertClusterNode insert: %v", err)
	}
	seen := started.Add(time.Minute)
	if err := s.UpsertClusterNode(ctx, ClusterNode{
		InstanceID:     "node-1",
		Hostname:       "host-a",
		Version:        "test",
		Addr:           "127.0.0.1:8082",
		DeploymentMode: "cluster-postgres",
		DBDriver:       "postgres",
		Status:         "ready",
		StartedAt:      started,
		LastSeenAt:     seen,
		Metadata:       `{"role":"test"}`,
	}); err != nil {
		t.Fatalf("UpsertClusterNode update: %v", err)
	}
	nodes, err := s.ListClusterNodes(ctx)
	if err != nil {
		t.Fatalf("ListClusterNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("node count = %d, want 1", len(nodes))
	}
	if nodes[0].Status != "ready" || nodes[0].Addr != "127.0.0.1:8082" || !nodes[0].LastSeenAt.Equal(seen) {
		t.Fatalf("unexpected node: %#v", nodes[0])
	}
	if err := s.MarkClusterNodeStatus(ctx, "node-1", "stopped", seen.Add(time.Second)); err != nil {
		t.Fatalf("MarkClusterNodeStatus: %v", err)
	}
	nodes, err = s.ListClusterNodes(ctx)
	if err != nil {
		t.Fatalf("ListClusterNodes after mark: %v", err)
	}
	if nodes[0].Status != "stopped" {
		t.Fatalf("status = %q, want stopped", nodes[0].Status)
	}
}

func TestGuardrailPolicyDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	policy, err := s.GetGuardrailPolicy(ctx)
	if err != nil {
		t.Fatalf("GetGuardrailPolicy default: %v", err)
	}
	if policy.ID != "default" || policy.Enabled || policy.InputAction != "redact" || policy.OutputAction != "redact" || !policy.DetectEmail || policy.RedactionText == "" {
		t.Fatalf("unexpected default guardrail policy: %#v", policy)
	}
	updated, err := s.UpdateGuardrailPolicy(ctx, GuardrailPolicy{
		Enabled:          true,
		InputAction:      "block",
		OutputAction:     "redact",
		DetectEmail:      true,
		DetectPhone:      false,
		DetectSSN:        true,
		DetectCreditCard: true,
		DetectAPIKey:     true,
		CustomPatterns: []GuardrailCustomPattern{{
			ID:            "employee-id",
			Name:          "Employee ID",
			Pattern:       `EMP-[0-9]+`,
			Action:        "redact",
			RedactionText: "[EMPLOYEE_ID]",
			Enabled:       true,
		}},
		RedactionText: "[PRIVATE]",
	})
	if err != nil {
		t.Fatalf("UpdateGuardrailPolicy: %v", err)
	}
	if !updated.Enabled || updated.InputAction != "block" || updated.OutputAction != "redact" || updated.DetectPhone || updated.RedactionText != "[PRIVATE]" || updated.StreamingBlockMode != "reject" {
		t.Fatalf("unexpected updated guardrail policy: %#v", updated)
	}
	if len(updated.CustomPatterns) != 1 || updated.CustomPatterns[0].Pattern != `EMP-[0-9]+` || updated.CustomPatterns[0].RedactionText != "[EMPLOYEE_ID]" {
		t.Fatalf("unexpected updated guardrail policy: %#v", updated)
	}
}

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
		RetryAttempts:        2,
		RequestTimeoutMS:     30000,
		HealthRoutingEnabled: true,
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
	if routed.Model.RetryAttempts != 2 || routed.Model.RequestTimeoutMS != 30000 || !routed.Model.HealthRoutingEnabled {
		t.Fatalf("model reliability fields were not persisted: %#v", routed.Model)
	}
	fallbackProvider := Provider{
		ID:      "backup-vllm",
		Name:    "Backup vLLM",
		Type:    "openai",
		BaseURL: "http://127.0.0.1:8001/v1",
		Enabled: true,
	}
	if err := s.CreateProvider(ctx, fallbackProvider); err != nil {
		t.Fatalf("CreateProvider fallback: %v", err)
	}
	fallback := Model{
		ID:                   "model_backup_qwen",
		ProviderID:           fallbackProvider.ID,
		ModelID:              "qwen3:32b",
		Route:                "backup-vllm/qwen3:32b",
		DisplayName:          "Qwen 32B Backup",
		SupportsStreaming:    true,
		Enabled:              true,
		HealthRoutingEnabled: true,
	}
	if err := s.CreateModel(ctx, fallback); err != nil {
		t.Fatalf("CreateModel fallback: %v", err)
	}
	model.FallbackRoutes = fallback.Route
	model.WeightedRoutes = fallback.Route + " 25"
	if err := s.UpdateModel(ctx, model); err != nil {
		t.Fatalf("UpdateModel routing policies: %v", err)
	}
	candidates, err := s.ResolveModelCandidates(ctx, model.Route)
	if err != nil {
		t.Fatalf("ResolveModelCandidates: %v", err)
	}
	if len(candidates) != 2 || candidates[0].Model.Route != model.Route || candidates[1].Model.Route != fallback.Route {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
	routed, err = s.ResolveModel(ctx, model.Route)
	if err != nil {
		t.Fatalf("ResolveModel after routing policy update: %v", err)
	}
	if routed.Model.WeightedRoutes != model.WeightedRoutes {
		t.Fatalf("model weighted routes were not persisted: %#v", routed.Model)
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

func TestBudgetBurnDownProjectsMonthEndSpend(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	if err := s.CreateBudget(ctx, Budget{ID: "budget_ai", ScopeType: "department", ScopeValue: "AI", LimitUSD: 20, WarnPct: 75, IsActive: true}); err != nil {
		t.Fatalf("CreateBudget: %v", err)
	}
	if err := s.InsertUsage(ctx, UsageRecord{
		ID:         "usage_burndown_1",
		RequestID:  "req_burndown_1",
		Department: "AI",
		CostUSD:    10,
		StatusCode: 200,
		CreatedAt:  now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}
	items, err := s.BudgetBurnDown(ctx, now)
	if err != nil {
		t.Fatalf("BudgetBurnDown: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one budget item, got %d", len(items))
	}
	item := items[0]
	if item.SpendUSD != 10 || item.RemainingUSD != 10 || item.Ratio != 0.5 {
		t.Fatalf("unexpected spend fields: %#v", item)
	}
	if item.ProjectedMonthEndUSD <= item.SpendUSD || item.DailyAverageUSD <= 0 || item.DaysElapsed <= 0 || item.DaysRemaining <= 0 {
		t.Fatalf("unexpected projection fields: %#v", item)
	}
	if item.Blocked || item.Warning {
		t.Fatalf("budget should not be blocked or warning yet: %#v", item)
	}
}

func TestUsageDrilldownsAggregateProvidersAndModels(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	records := []UsageRecord{
		{ID: "usage_drill_1", RequestID: "req_drill_1", ProviderID: "openai", Model: "openai/gpt-4o-mini", InputTokens: 10, OutputTokens: 20, TotalTokens: 30, CostUSD: 0.01, LatencyMS: 100, StatusCode: 200, CreatedAt: now.Add(-time.Hour)},
		{ID: "usage_drill_2", RequestID: "req_drill_2", ProviderID: "openai", Model: "openai/gpt-4o-mini", InputTokens: 5, OutputTokens: 15, TotalTokens: 20, CostUSD: 0.02, LatencyMS: 300, StatusCode: 500, ErrorText: "upstream", CreatedAt: now},
		{ID: "usage_drill_3", RequestID: "req_drill_3", ProviderID: "local-vllm", Model: "local-vllm/llama", InputTokens: 1, OutputTokens: 2, TotalTokens: 3, CostUSD: 0.001, LatencyMS: 50, StatusCode: 200, CreatedAt: now.AddDate(0, 0, -40)},
	}
	for _, record := range records {
		if err := s.InsertUsage(ctx, record); err != nil {
			t.Fatalf("InsertUsage %s: %v", record.ID, err)
		}
	}
	drilldowns, err := s.UsageDrilldowns(ctx, 30, now)
	if err != nil {
		t.Fatalf("UsageDrilldowns: %v", err)
	}
	if len(drilldowns.Providers) != 1 || drilldowns.Providers[0].ProviderID != "openai" {
		t.Fatalf("unexpected providers: %#v", drilldowns.Providers)
	}
	provider := drilldowns.Providers[0]
	if provider.Requests != 2 || provider.Errors != 1 || provider.ErrorRate != 0.5 || provider.TotalTokens != 50 || provider.CostUSD != 0.03 || provider.AvgLatencyMS != 200 {
		t.Fatalf("unexpected provider aggregate: %#v", provider)
	}
	if len(drilldowns.Models) != 1 || drilldowns.Models[0].Model != "openai/gpt-4o-mini" {
		t.Fatalf("unexpected models: %#v", drilldowns.Models)
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
