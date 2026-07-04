package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBedrockProviderCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	provider := Provider{
		ID:                 "bedrock-keys",
		Name:               "Bedrock (keys)",
		Type:               "bedrock",
		AWSRegion:          "us-west-2",
		AWSAuthMethod:      "keys",
		AWSAccessKeyID:     "AKIAEXAMPLE",
		AWSSecretAccessKey: "secret-1",
		AWSSessionToken:    "session-1",
		Enabled:            true,
	}
	if err := s.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	got, err := s.GetProvider(ctx, provider.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.AWSAuthMethod != "keys" || got.AWSAccessKeyID != "AKIAEXAMPLE" || got.AWSSecretAccessKey != "secret-1" || got.AWSSessionToken != "session-1" {
		t.Fatalf("bedrock credentials were not persisted: %#v", got)
	}

	// Update without touching secrets keeps the stored secret and token.
	got.Name = "Bedrock (renamed)"
	got.AWSSecretAccessKey = ""
	got.AWSSessionToken = ""
	if err := s.UpdateProvider(ctx, got, ProviderSecretUpdate{}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	kept, err := s.GetProvider(ctx, provider.ID)
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if kept.Name != "Bedrock (renamed)" || kept.AWSSecretAccessKey != "secret-1" || kept.AWSSessionToken != "session-1" {
		t.Fatalf("secrets should be preserved when not updated: %#v", kept)
	}

	// Switching to API-key auth replaces the credential set.
	kept.AWSAuthMethod = "api_key"
	kept.AWSAccessKeyID = ""
	kept.AWSSecretAccessKey = ""
	kept.AWSSessionToken = ""
	kept.BedrockAPIKey = "bedrock-token-1"
	if err := s.UpdateProvider(ctx, kept, ProviderSecretUpdate{AWSCredentials: true, BedrockAPIKey: true}); err != nil {
		t.Fatalf("UpdateProvider auth switch: %v", err)
	}
	switched, err := s.GetProvider(ctx, provider.ID)
	if err != nil {
		t.Fatalf("GetProvider after auth switch: %v", err)
	}
	if switched.AWSAuthMethod != "api_key" || switched.BedrockAPIKey != "bedrock-token-1" {
		t.Fatalf("bedrock api key auth was not persisted: %#v", switched)
	}
	if switched.AWSSecretAccessKey != "" || switched.AWSSessionToken != "" {
		t.Fatalf("aws credentials should be cleared after auth switch: %#v", switched)
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

func TestAzureProviderRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	provider := Provider{
		ID:              "azure-openai-east",
		Name:            "Azure OpenAI East",
		Type:            "azure-openai",
		BaseURL:         "https://myres.openai.azure.com",
		APIKey:          "azure-secret",
		AzureAPIVersion: "2024-10-21",
		Enabled:         true,
	}
	if err := s.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider azure-openai: %v", err)
	}
	if err := s.CreateProvider(ctx, Provider{
		ID:      "azure-claude",
		Name:    "Azure Claude",
		Type:    "azure-anthropic",
		BaseURL: "https://myres.services.ai.azure.com/anthropic",
		Enabled: true,
	}); err != nil {
		t.Fatalf("CreateProvider azure-anthropic: %v", err)
	}
	stored, err := s.GetProvider(ctx, provider.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if stored.Type != "azure-openai" || stored.AzureAPIVersion != "2024-10-21" {
		t.Fatalf("unexpected stored provider: %#v", stored)
	}
	stored.AzureAPIVersion = "2025-01-01-preview"
	if err := s.UpdateProvider(ctx, stored, ProviderSecretUpdate{}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	stored, err = s.GetProvider(ctx, provider.ID)
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if stored.AzureAPIVersion != "2025-01-01-preview" || stored.APIKey != "azure-secret" {
		t.Fatalf("update lost fields: %#v", stored)
	}
}
