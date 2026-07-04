package httpapi

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestAdminConfigExportIsSignedAndRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSeedData("admin-hash"); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
	}
	admin, err := st.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if err := st.CreateProvider(ctx, store.Provider{
		ID:        "export-direct",
		Name:      "Export Direct",
		Type:      "openai",
		BaseURL:   "https://example.invalid/v1",
		APIKey:    "sk-secret-direct-value",
		APIKeyEnv: "",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := st.CreateModel(ctx, store.Model{
		ID:                   "model_export_direct",
		ProviderID:           "export-direct",
		ModelID:              "example-model",
		Route:                "export/example-model",
		DisplayName:          "Export Example",
		InputCostPerMillion:  1.25,
		OutputCostPerMillion: 2.5,
		ContextWindow:        8192,
		SupportsStreaming:    true,
		Enabled:              true,
		FallbackRoutes:       "local-ollama/llama3.1:8b",
		WeightedRoutes:       "local-ollama/llama3.1:8b 10",
		RetryAttempts:        1,
		RequestTimeoutMS:     30000,
		HealthRoutingEnabled: true,
	}); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if err := st.CreateBudget(ctx, store.Budget{
		ID:         "budget_export",
		ScopeType:  "department",
		ScopeValue: "Research",
		LimitUSD:   25,
		WarnPct:    80,
		IsActive:   true,
	}); err != nil {
		t.Fatalf("CreateBudget: %v", err)
	}
	if err := st.CreateRateLimit(ctx, store.RateLimit{
		ID:         "limit_export",
		ScopeType:  "model",
		ScopeValue: "export/example-model",
		RPMLimit:   60,
		TPMLimit:   120000,
		IsActive:   true,
	}); err != nil {
		t.Fatalf("CreateRateLimit: %v", err)
	}
	if _, err := st.UpdateGuardrailPolicy(ctx, store.GuardrailPolicy{
		ID:                 "default",
		Enabled:            true,
		InputAction:        "redact",
		OutputAction:       "block",
		DetectEmail:        true,
		DetectPhone:        true,
		DetectSSN:          true,
		DetectCreditCard:   false,
		DetectAPIKey:       true,
		RedactionText:      "[REDACTED]",
		StreamingBlockMode: "reject",
		CustomPatterns: []store.GuardrailCustomPattern{{
			ID:            "employee-id",
			Name:          "Employee ID",
			Pattern:       `EMP-[0-9]+`,
			Action:        "redact",
			RedactionText: "[EMPLOYEE_ID]",
			Enabled:       true,
		}},
	}); err != nil {
		t.Fatalf("UpdateGuardrailPolicy: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret", DataDir: dataDir},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp := jsonRequest(t, handler, http.MethodGet, "/api/admin/config/export", sessionToken(t, admin), nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("config export status = %d body = %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"sk-secret-direct-value", "password_hash", "key_hash", "session_secret", "last_error", "consecutive_failures"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("export contains forbidden value %q: %s", forbidden, body)
		}
	}

	var bundle signedAdminConfigExport
	if err := json.Unmarshal([]byte(body), &bundle); err != nil {
		t.Fatalf("Unmarshal export: %v", err)
	}
	if bundle.Kind != adminConfigExportKind || bundle.Version != adminConfigExportVersion {
		t.Fatalf("unexpected bundle metadata: %#v", bundle)
	}
	if bundle.Signature.Algorithm != adminConfigSigningAlgorithm || bundle.Signature.KeyID == "" {
		t.Fatalf("unexpected signature metadata: %#v", bundle.Signature)
	}
	publicKey, err := base64.StdEncoding.DecodeString(bundle.Signature.PublicKey)
	if err != nil {
		t.Fatalf("Decode public key: %v", err)
	}
	signature, err := base64.StdEncoding.DecodeString(bundle.Signature.Value)
	if err != nil {
		t.Fatalf("Decode signature: %v", err)
	}
	signedContent, err := json.Marshal(bundle.signedContent())
	if err != nil {
		t.Fatalf("Marshal signed content: %v", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), signedContent, signature) {
		t.Fatal("config export signature did not verify")
	}

	var foundProvider bool
	for _, provider := range bundle.Payload.Providers {
		if provider.ID == "export-direct" {
			foundProvider = true
			if provider.SecretSource != "direct-redacted" {
				t.Fatalf("provider secret source = %q", provider.SecretSource)
			}
		}
	}
	if !foundProvider {
		t.Fatalf("exported providers did not include export-direct: %#v", bundle.Payload.Providers)
	}
	if len(bundle.Payload.Models) == 0 || len(bundle.Payload.Budgets) == 0 || len(bundle.Payload.RateLimits) == 0 {
		t.Fatalf("expected models, budgets, and limits in export: %#v", bundle.Payload)
	}
	if len(bundle.Payload.Guardrails.CustomPatterns) != 1 || bundle.Payload.Guardrails.CustomPatterns[0].Name != "Employee ID" {
		t.Fatalf("custom guardrail patterns missing from export: %#v", bundle.Payload.Guardrails)
	}
}
