package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

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
