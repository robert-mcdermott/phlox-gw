package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestAdminBedrockProviderAuthMethods(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := st.EnsureSeedData(hash); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	loginResp := jsonRequest(t, handler, http.MethodPost, "/api/auth/login", "", map[string]any{"username": "admin", "password": "admin"})
	var login struct {
		Token string `json:"token"`
	}
	decodeRecorder(t, loginResp, &login)

	// Create with explicit access keys.
	createResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/providers", login.Token, map[string]any{
		"id":                    "bedrock-keys",
		"name":                  "Bedrock Keys",
		"type":                  "bedrock",
		"aws_region":            "us-west-2",
		"aws_auth_method":       "keys",
		"aws_access_key_id":     "AKIAEXAMPLE",
		"aws_secret_access_key": "secret-1",
		"aws_session_token":     "session-1",
		"enabled":               true,
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create provider status = %d body = %s", createResp.Code, createResp.Body.String())
	}
	if body := createResp.Body.String(); strings.Contains(body, "secret-1") || strings.Contains(body, "session-1") {
		t.Fatalf("create response leaked AWS secrets: %s", body)
	}

	// Missing secret key on create is rejected.
	badResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/providers", login.Token, map[string]any{
		"id":                "bedrock-bad",
		"name":              "Bedrock Bad",
		"type":              "bedrock",
		"aws_auth_method":   "keys",
		"aws_access_key_id": "AKIAEXAMPLE",
	})
	if badResp.Code != http.StatusBadRequest {
		t.Fatalf("create without secret status = %d body = %s", badResp.Code, badResp.Body.String())
	}

	// List exposes secret presence flags without values.
	listResp := jsonRequest(t, handler, http.MethodGet, "/api/admin/providers", login.Token, nil)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list providers status = %d body = %s", listResp.Code, listResp.Body.String())
	}
	if body := listResp.Body.String(); strings.Contains(body, "secret-1") {
		t.Fatalf("provider list leaked AWS secret: %s", body)
	}
	var listed []store.Provider
	decodeRecorder(t, listResp, &listed)
	var created store.Provider
	for _, p := range listed {
		if p.ID == "bedrock-keys" {
			created = p
		}
	}
	if created.ID == "" || !created.HasAWSSecret || created.AWSAccessKeyID != "AKIAEXAMPLE" || created.AWSAuthMethod != "keys" {
		t.Fatalf("unexpected listed provider: %#v", created)
	}

	// Update with a blank secret keeps the stored one.
	updateResp := jsonRequest(t, handler, http.MethodPut, "/api/admin/providers/bedrock-keys", login.Token, map[string]any{
		"name":              "Bedrock Keys Renamed",
		"type":              "bedrock",
		"aws_region":        "us-east-1",
		"aws_auth_method":   "keys",
		"aws_access_key_id": "AKIAEXAMPLE",
		"enabled":           true,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update provider status = %d body = %s", updateResp.Code, updateResp.Body.String())
	}
	stored, err := st.GetProvider(context.Background(), "bedrock-keys")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if stored.AWSSecretAccessKey != "secret-1" || stored.AWSSessionToken != "session-1" || stored.AWSRegion != "us-east-1" {
		t.Fatalf("update should keep stored secrets: %#v", stored)
	}

	// Switching to the Bedrock API key method clears AWS access keys.
	switchResp := jsonRequest(t, handler, http.MethodPut, "/api/admin/providers/bedrock-keys", login.Token, map[string]any{
		"name":            "Bedrock API Key",
		"type":            "bedrock",
		"aws_region":      "us-east-1",
		"aws_auth_method": "api_key",
		"bedrock_api_key": "bedrock-token-1",
		"enabled":         true,
	})
	if switchResp.Code != http.StatusOK {
		t.Fatalf("switch auth status = %d body = %s", switchResp.Code, switchResp.Body.String())
	}
	stored, err = st.GetProvider(context.Background(), "bedrock-keys")
	if err != nil {
		t.Fatalf("GetProvider after switch: %v", err)
	}
	if stored.AWSAuthMethod != "api_key" || stored.BedrockAPIKey != "bedrock-token-1" {
		t.Fatalf("bedrock api key was not stored: %#v", stored)
	}
	if stored.AWSAccessKeyID != "" || stored.AWSSecretAccessKey != "" || stored.AWSSessionToken != "" {
		t.Fatalf("aws access keys should be cleared after switching auth method: %#v", stored)
	}

	// Invalid auth method is rejected.
	invalidResp := jsonRequest(t, handler, http.MethodPut, "/api/admin/providers/bedrock-keys", login.Token, map[string]any{
		"name":            "Bedrock API Key",
		"type":            "bedrock",
		"aws_auth_method": "magic",
		"enabled":         true,
	})
	if invalidResp.Code != http.StatusBadRequest {
		t.Fatalf("invalid auth method status = %d body = %s", invalidResp.Code, invalidResp.Body.String())
	}
}

func TestOpenAIChatEndpointAzure(t *testing.T) {
	p := store.Provider{Type: "azure-openai", BaseURL: "https://myres.openai.azure.com/"}
	got := openAIChatEndpoint(p, "gpt 4o/deploy")
	want := "https://myres.openai.azure.com/openai/deployments/gpt%204o%2Fdeploy/chat/completions?api-version=2024-10-21"
	if got != want {
		t.Fatalf("openAIChatEndpoint = %q, want %q", got, want)
	}
	p.AzureAPIVersion = "2025-04-01-preview"
	if got := openAIChatEndpoint(p, "gpt4o"); !strings.HasSuffix(got, "api-version=2025-04-01-preview") {
		t.Fatalf("pinned api-version not used: %q", got)
	}
	generic := store.Provider{Type: "openai", BaseURL: "http://localhost:8000/v1"}
	if got := openAIChatEndpoint(generic, "anything"); got != "http://localhost:8000/v1/chat/completions" {
		t.Fatalf("generic endpoint changed: %q", got)
	}
}

func TestAdminAzureProviderValidation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := st.EnsureSeedData(hash); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loginResp := jsonRequest(t, handler, http.MethodPost, "/api/auth/login", "", map[string]any{"username": "admin", "password": "admin"})
	var login struct {
		Token string `json:"token"`
	}
	decodeRecorder(t, loginResp, &login)

	// Azure providers require a base URL.
	badResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/providers", login.Token, map[string]any{
		"id":   "azure-missing-url",
		"name": "Azure Missing URL",
		"type": "azure-openai",
	})
	if badResp.Code != http.StatusBadRequest {
		t.Fatalf("create without base_url status = %d body = %s", badResp.Code, badResp.Body.String())
	}

	createResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/providers", login.Token, map[string]any{
		"id":                "azure-east",
		"name":              "Azure East",
		"type":              "azure-openai",
		"base_url":          "https://myres.openai.azure.com/",
		"api_key":           "azure-secret",
		"azure_api_version": "2024-10-21",
		"enabled":           true,
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create azure provider status = %d body = %s", createResp.Code, createResp.Body.String())
	}
	stored, err := st.GetProvider(context.Background(), "azure-east")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if stored.Type != "azure-openai" || stored.AzureAPIVersion != "2024-10-21" || stored.BaseURL != "https://myres.openai.azure.com" {
		t.Fatalf("unexpected stored provider: %#v", stored)
	}

	// Switching away from azure-openai clears the api-version.
	updateResp := jsonRequest(t, handler, http.MethodPut, "/api/admin/providers/azure-east", login.Token, map[string]any{
		"name":              "Now Generic",
		"type":              "openai",
		"base_url":          "https://myres.openai.azure.com/openai/v1",
		"azure_api_version": "2024-10-21",
		"enabled":           true,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update provider status = %d body = %s", updateResp.Code, updateResp.Body.String())
	}
	stored, err = st.GetProvider(context.Background(), "azure-east")
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if stored.Type != "openai" || stored.AzureAPIVersion != "" {
		t.Fatalf("api version should be cleared on type change: %#v", stored)
	}
}

func TestAdminProviderBetaHeaderPrefixes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := st.EnsureSeedData(hash); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loginResp := jsonRequest(t, handler, http.MethodPost, "/api/auth/login", "", map[string]any{"username": "admin", "password": "admin"})
	var login struct {
		Token string `json:"token"`
	}
	decodeRecorder(t, loginResp, &login)

	createResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/providers", login.Token, map[string]any{
		"id":                   "foundry-claude",
		"name":                 "Foundry Claude",
		"type":                 "azure-anthropic",
		"base_url":             "https://myres.services.ai.azure.com/anthropic",
		"api_key":              "foundry-secret",
		"beta_header_prefixes": " interleaved-thinking- ,\nadvisor-tool-\n",
		"enabled":              true,
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create provider status = %d body = %s", createResp.Code, createResp.Body.String())
	}
	stored, err := st.GetProvider(context.Background(), "foundry-claude")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if stored.BetaHeaderPrefixes != "interleaved-thinking-\nadvisor-tool-" {
		t.Fatalf("prefixes should be normalized one per line, got %q", stored.BetaHeaderPrefixes)
	}

	// Switching to a non-Anthropic protocol clears the allowlist.
	updateResp := jsonRequest(t, handler, http.MethodPut, "/api/admin/providers/foundry-claude", login.Token, map[string]any{
		"name":                 "Now OpenAI",
		"type":                 "openai",
		"base_url":             "https://example.com/v1",
		"beta_header_prefixes": "interleaved-thinking-",
		"enabled":              true,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update provider status = %d body = %s", updateResp.Code, updateResp.Body.String())
	}
	stored, err = st.GetProvider(context.Background(), "foundry-claude")
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if stored.BetaHeaderPrefixes != "" {
		t.Fatalf("beta prefixes should be cleared on protocol change: %#v", stored)
	}
}

func TestAdminGoogleProviderDefaultsBaseURL(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := st.EnsureSeedData(hash); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loginResp := jsonRequest(t, handler, http.MethodPost, "/api/auth/login", "", map[string]any{"username": "admin", "password": "admin"})
	var login struct {
		Token string `json:"token"`
	}
	decodeRecorder(t, loginResp, &login)

	createResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/providers", login.Token, map[string]any{
		"id":      "google",
		"name":    "Google Gemini",
		"type":    "google",
		"api_key": "gemini-secret",
		"enabled": true,
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create google provider status = %d body = %s", createResp.Code, createResp.Body.String())
	}
	stored, err := st.GetProvider(context.Background(), "google")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if stored.Type != "google" || stored.BaseURL != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("blank base_url should default to the Gemini endpoint: %#v", stored)
	}

	// An explicit base URL is kept as-is.
	updateResp := jsonRequest(t, handler, http.MethodPut, "/api/admin/providers/google", login.Token, map[string]any{
		"name":     "Google Gemini",
		"type":     "google",
		"base_url": "https://proxy.internal/gemini/openai/",
		"enabled":  true,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update provider status = %d body = %s", updateResp.Code, updateResp.Body.String())
	}
	stored, err = st.GetProvider(context.Background(), "google")
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if stored.BaseURL != "https://proxy.internal/gemini/openai" {
		t.Fatalf("explicit base_url not kept: %#v", stored)
	}
}
