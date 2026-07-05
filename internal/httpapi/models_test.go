package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestProviderCircuitOpen(t *testing.T) {
	now := time.Now().UTC()
	openUntil := now.Add(time.Minute)
	open, reason := providerCircuitOpen(store.Provider{CircuitOpenUntil: &openUntil}, now)
	if !open || !strings.Contains(reason, openUntil.UTC().Format(time.RFC3339)) {
		t.Fatalf("expected open circuit reason, got open=%v reason=%q", open, reason)
	}
	open, reason = providerCircuitOpen(store.Provider{CircuitOpenUntil: &openUntil}, now.Add(2*time.Minute))
	if open || reason != "" {
		t.Fatalf("expected closed circuit after expiry, got open=%v reason=%q", open, reason)
	}
}

func TestBedrockHealthCheckOmitsTemperature(t *testing.T) {
	fake := &fakeBedrockClient{}
	s := &Server{
		bedrockClientFactory: func(_ context.Context, _ store.Provider) (BedrockConverseClient, error) {
			return fake, nil
		},
	}
	route := store.RoutedModel{
		Provider: store.Provider{ID: "aws-bedrock-health", Type: "bedrock", AWSRegion: "us-west-2", Enabled: true},
		Model: store.Model{
			ID:         "model_bedrock_health",
			ProviderID: "aws-bedrock-health",
			ModelID:    "us.anthropic.claude-sonnet-4-6",
			Route:      "bedrock/sonnet-4-6",
			Enabled:    true,
		},
	}
	result := s.runModelHealthCheck(context.Background(), route)
	if !result.OK {
		t.Fatalf("health check failed: %#v", result)
	}
	if fake.input == nil || fake.input.InferenceConfig == nil {
		t.Fatalf("bedrock input missing inference config: %#v", fake.input)
	}
	if fake.input.InferenceConfig.MaxTokens == nil || *fake.input.InferenceConfig.MaxTokens != 8 {
		t.Fatalf("unexpected max tokens: %#v", fake.input.InferenceConfig)
	}
	if fake.input.InferenceConfig.Temperature != nil {
		t.Fatalf("health check should not set temperature: %#v", fake.input.InferenceConfig)
	}
}

func TestModelHealthCheckRetriesReasoningModelParams(t *testing.T) {
	ctx := context.Background()
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
	provider := store.Provider{
		ID:      "azure-reasoning",
		Name:    "Azure Reasoning",
		Type:    "azure-openai",
		BaseURL: "https://myres.openai.azure.com",
		APIKey:  "azure-secret",
		Enabled: true,
	}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:         "model_gpt55",
		ProviderID: provider.ID,
		ModelID:    "gpt-5.5",
		Route:      "azure/gpt-5.5",
		Enabled:    true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	upstreamHits := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if _, has := req["max_tokens"]; has {
			body := `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens","code":"unsupported_parameter"}}`
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		if _, has := req["temperature"]; has {
			body := `{"error":{"message":"Unsupported value: 'temperature' does not support 0 with this model. Only the default (1) value is supported.","param":"temperature","code":"unsupported_value"}}`
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		if req["max_completion_tokens"] != float64(8) {
			t.Fatalf("expected max_completion_tokens 8, got %#v", req)
		}
		body := `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loginResp := jsonRequest(t, handler, http.MethodPost, "/api/auth/login", "", map[string]any{"username": "admin", "password": "admin"})
	var login struct {
		Token string `json:"token"`
	}
	decodeRecorder(t, loginResp, &login)
	resp := jsonRequest(t, handler, http.MethodPost, "/api/admin/models/model_gpt55/test", login.Token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("test endpoint status = %d body = %s", resp.Code, resp.Body.String())
	}
	var result struct {
		OK         bool   `json:"ok"`
		StatusCode int    `json:"status_code"`
		Error      string `json:"error"`
	}
	decodeRecorder(t, resp, &result)
	if !result.OK || result.StatusCode != http.StatusOK {
		t.Fatalf("health check should pass after retries: %+v body=%s", result, resp.Body.String())
	}
	if upstreamHits != 3 {
		t.Fatalf("expected 3 upstream attempts (max_tokens, temperature, success), got %d", upstreamHits)
	}
}
