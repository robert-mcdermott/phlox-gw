package store

import (
	"context"
	"path/filepath"
	"testing"
)

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
	if err := s.UpdateProvider(ctx, provider, ProviderSecretUpdate{}); err != nil {
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
