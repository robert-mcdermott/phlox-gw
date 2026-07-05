package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCostUsesInputAndOutputPricing(t *testing.T) {
	got := Cost(1_000, 2_000, Model{InputCostPerMillion: 3, OutputCostPerMillion: 15})
	want := 0.033
	if got != want {
		t.Fatalf("Cost() = %v, want %v", got, want)
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

func TestChargebackReportGroupsByDepartmentAndUser(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	june := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	july := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	records := []UsageRecord{
		{ID: "u1", RequestID: "r1", UserID: "user_alice", Username: "alice", Department: "Engineering", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CostUSD: 1.00, CreatedAt: june},
		{ID: "u2", RequestID: "r2", UserID: "user_alice", Username: "alice", Department: "Engineering", InputTokens: 40, OutputTokens: 10, TotalTokens: 50, CostUSD: 0.50, CreatedAt: june.Add(time.Hour)},
		{ID: "u3", RequestID: "r3", UserID: "user_bob", Username: "bob", Department: "Engineering", InputTokens: 200, OutputTokens: 100, TotalTokens: 300, CostUSD: 2.00, CreatedAt: june.Add(2 * time.Hour)},
		{ID: "u4", RequestID: "r4", UserID: "user_carol", Username: "carol", Department: "Sales", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 0.25, CreatedAt: june.Add(3 * time.Hour)},
		{ID: "u5", RequestID: "r5", UserID: "user_alice", Username: "alice", Department: "Engineering", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 5.00, CreatedAt: july},
	}
	for _, record := range records {
		if err := s.InsertUsage(ctx, record); err != nil {
			t.Fatalf("InsertUsage %s: %v", record.ID, err)
		}
	}
	if err := s.CreateBudget(ctx, Budget{ID: "budget_eng", ScopeType: "department", ScopeValue: "Engineering", LimitUSD: 100, WarnPct: 90, IsActive: true}); err != nil {
		t.Fatalf("CreateBudget: %v", err)
	}

	report, err := s.ChargebackReport(ctx, time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), july)
	if err != nil {
		t.Fatalf("ChargebackReport: %v", err)
	}
	if report.Month != "2026-06" {
		t.Fatalf("month = %q", report.Month)
	}
	if report.Requests != 4 || report.CostUSD != 3.75 || report.TotalTokens != 515 {
		t.Fatalf("unexpected totals: %#v", report)
	}
	if len(report.Departments) != 2 {
		t.Fatalf("departments = %#v", report.Departments)
	}
	eng := report.Departments[0]
	if eng.Department != "Engineering" || eng.CostUSD != 3.5 || eng.Requests != 3 || eng.BudgetUSD != 100 {
		t.Fatalf("unexpected engineering rollup: %#v", eng)
	}
	if len(eng.Users) != 2 || eng.Users[0].Username != "bob" || eng.Users[0].CostUSD != 2.0 || eng.Users[1].Username != "alice" || eng.Users[1].CostUSD != 1.5 {
		t.Fatalf("unexpected engineering users: %#v", eng.Users)
	}
	sales := report.Departments[1]
	if sales.Department != "Sales" || sales.CostUSD != 0.25 || sales.BudgetUSD != 0 {
		t.Fatalf("unexpected sales rollup: %#v", sales)
	}
	if len(report.AvailableMonths) != 2 || report.AvailableMonths[0] != "2026-07" || report.AvailableMonths[1] != "2026-06" {
		t.Fatalf("unexpected months: %#v", report.AvailableMonths)
	}

	julyReport, err := s.ChargebackReport(ctx, july, july)
	if err != nil {
		t.Fatalf("ChargebackReport july: %v", err)
	}
	if julyReport.Requests != 1 || julyReport.CostUSD != 5.0 || len(julyReport.Departments) != 1 {
		t.Fatalf("unexpected july report: %#v", julyReport)
	}

	empty, err := s.ChargebackReport(ctx, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), july)
	if err != nil {
		t.Fatalf("ChargebackReport empty: %v", err)
	}
	if empty.Requests != 0 || len(empty.Departments) != 0 {
		t.Fatalf("expected empty report: %#v", empty)
	}
}
