package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestUsageFromSSELine(t *testing.T) {
	line := []byte(`data: {"id":"chunk","object":"chat.completion.chunk","usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}` + "\n")
	got := usageFromSSELine(line)
	if got.Input != 12 || got.Output != 5 || got.Total != 17 {
		t.Fatalf("usageFromSSELine() = %#v", got)
	}
	if got := usageFromSSELine([]byte("data: [DONE]\n")); got.Total != 0 || got.Input != 0 || got.Output != 0 {
		t.Fatalf("[DONE] should not produce usage, got %#v", got)
	}
}

func TestCheckAPIKeyPolicy(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	s := &Server{store: st}
	route := store.RoutedModel{Model: store.Model{
		ID:                  "model_allowed",
		Route:               "local-ollama/gemma4:31b-cloud",
		ModelID:             "gemma4:31b-cloud",
		InputCostPerMillion: 1,
	}}

	blocked, status, _, typ := s.checkAPIKeyPolicy(ctx, store.APIKey{ID: "key_1", ModelAllowlist: "openai/gpt-4o-mini"}, route)
	if !blocked || status != http.StatusForbidden || typ != "permission_error" {
		t.Fatalf("expected allowlist block, got blocked=%v status=%d type=%q", blocked, status, typ)
	}

	key := store.APIKey{ID: "key_budget", ModelAllowlist: route.Model.Route, BudgetUSD: 0.01}
	if err := st.InsertUsage(ctx, store.UsageRecord{
		ID:         "usage_budget",
		RequestID:  "req_budget",
		APIKeyID:   key.ID,
		Model:      route.Model.Route,
		CostUSD:    0.02,
		StatusCode: 200,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertUsage budget: %v", err)
	}
	blocked, status, _, typ = s.checkAPIKeyPolicy(ctx, key, route)
	if !blocked || status != http.StatusPaymentRequired || typ != "insufficient_quota" {
		t.Fatalf("expected budget block, got blocked=%v status=%d type=%q", blocked, status, typ)
	}

	key = store.APIKey{ID: "key_rpm", ModelAllowlist: route.Model.Route, RPMLimit: 1}
	if err := st.InsertUsage(ctx, store.UsageRecord{
		ID:          "usage_rpm",
		RequestID:   "req_rpm",
		APIKeyID:    key.ID,
		Model:       route.Model.Route,
		TotalTokens: 20,
		StatusCode:  200,
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertUsage rpm: %v", err)
	}
	blocked, status, _, typ = s.checkAPIKeyPolicy(ctx, key, route)
	if !blocked || status != http.StatusTooManyRequests || typ != "rate_limit_exceeded" {
		t.Fatalf("expected rpm block, got blocked=%v status=%d type=%q", blocked, status, typ)
	}
}

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

func TestBedrockConverseInputFromOpenAI(t *testing.T) {
	input, err := bedrockConverseInput("anthropic.claude-3-5-sonnet-20240620-v1:0", map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are concise."},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Hello"},
				map[string]any{"type": "text", "text": "World"},
			}},
			map[string]any{"role": "assistant", "content": "Hi"},
		},
		"max_tokens":  float64(64),
		"temperature": float64(0.25),
		"top_p":       float64(0.9),
		"stop":        []any{"END"},
	})
	if err != nil {
		t.Fatalf("bedrockConverseInput: %v", err)
	}
	if got := *input.ModelId; got != "anthropic.claude-3-5-sonnet-20240620-v1:0" {
		t.Fatalf("model id = %q", got)
	}
	if len(input.System) != 1 || input.System[0].(*types.SystemContentBlockMemberText).Value != "You are concise." {
		t.Fatalf("unexpected system blocks: %#v", input.System)
	}
	if len(input.Messages) != 2 {
		t.Fatalf("messages len = %d", len(input.Messages))
	}
	if input.Messages[0].Role != types.ConversationRoleUser || input.Messages[0].Content[0].(*types.ContentBlockMemberText).Value != "Hello\nWorld" {
		t.Fatalf("unexpected user message: %#v", input.Messages[0])
	}
	if input.InferenceConfig == nil || *input.InferenceConfig.MaxTokens != 64 || *input.InferenceConfig.Temperature != 0.25 || *input.InferenceConfig.TopP != 0.9 {
		t.Fatalf("unexpected inference config: %#v", input.InferenceConfig)
	}
	if len(input.InferenceConfig.StopSequences) != 1 || input.InferenceConfig.StopSequences[0] != "END" {
		t.Fatalf("unexpected stop sequences: %#v", input.InferenceConfig.StopSequences)
	}
}

func TestOpenAIChatCompletionsRoutesToBedrockAndRecordsUsage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	hash, err := auth.HashPassword("pass")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user := store.User{
		ID:           "user_bedrock",
		Username:     "bedrock-user",
		Department:   "AI",
		Role:         "user",
		PasswordHash: hash,
		AuthProvider: "local",
		IsActive:     true,
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	plain, prefix, keyHash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_bedrock", UserID: user.ID, Name: "Bedrock key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "aws-bedrock-test", Name: "AWS Bedrock Test", Type: "bedrock", AWSRegion: "us-west-2", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_bedrock_test",
		ProviderID:           provider.ID,
		ModelID:              "anthropic.claude-3-5-sonnet-20240620-v1:0",
		Route:                "aws-bedrock-test/claude-sonnet",
		DisplayName:          "Bedrock Sonnet",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	fake := &fakeBedrockClient{}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		BedrockClientFactory: func(_ context.Context, p store.Provider) (BedrockConverseClient, error) {
			if p.ID != provider.ID || p.AWSRegion != "us-west-2" {
				t.Fatalf("unexpected provider passed to factory: %#v", p)
			}
			return fake, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":       model.Route,
		"messages":    []map[string]string{{"role": "user", "content": "Hello"}},
		"max_tokens":  16,
		"temperature": 0,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if fake.input == nil || *fake.input.ModelId != model.ModelID {
		t.Fatalf("fake input not captured correctly: %#v", fake.input)
	}
	var body struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	decodeRecorder(t, resp, &body)
	if body.Model != model.Route || len(body.Choices) != 1 || body.Choices[0].Message.Content != "bedrock says hi" {
		t.Fatalf("unexpected response body: %#v", body)
	}
	if body.Usage.PromptTokens != 10 || body.Usage.CompletionTokens != 5 || body.Usage.TotalTokens != 15 {
		t.Fatalf("unexpected usage: %#v", body.Usage)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 10 || usage.OutputTokens != 5 || usage.TotalTokens != 15 || usage.CostUSD != 0.00002 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

type fakeBedrockClient struct {
	input *bedrockruntime.ConverseInput
}

func (f *fakeBedrockClient) Converse(_ context.Context, input *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	f.input = input
	return &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "bedrock says hi"}},
		}},
		StopReason: types.StopReasonEndTurn,
		Usage: &types.TokenUsage{
			InputTokens:  aws.Int32(10),
			OutputTokens: aws.Int32(5),
			TotalTokens:  aws.Int32(15),
		},
	}, nil
}

func TestAdminActionCreatesAuditLog(t *testing.T) {
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
	if login.Token == "" {
		t.Fatal("missing login token")
	}

	username := "audit_user"
	createResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/users", login.Token, map[string]any{
		"username":     username,
		"password":     "Passw0rd!",
		"email":        "audit_user@localhost",
		"display_name": "Audit User",
		"department":   "Audit",
		"role":         "user",
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create user status = %d body = %s", createResp.Code, createResp.Body.String())
	}

	resp := jsonRequest(t, handler, http.MethodGet, "/api/admin/audit-log?limit=20", login.Token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("audit status = %d body = %s", resp.Code, resp.Body.String())
	}
	var items []store.AuditLog
	decodeRecorder(t, resp, &items)
	found := false
	for _, item := range items {
		if item.Action == "user.create" && item.TargetDisplay == username && item.ActorUsername == "admin" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("did not find user.create audit event in %#v", items)
	}
}

func jsonRequest(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeRecorder(t *testing.T, resp *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		t.Fatalf("Decode: %v", err)
	}
}
