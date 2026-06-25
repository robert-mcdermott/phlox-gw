package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	bedrockdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
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

func TestUsageFromAnthropicSSELine(t *testing.T) {
	start := usageFromAnthropicSSELine([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":8,"output_tokens":1}}}` + "\n"))
	if start.Input != 8 || start.Output != 1 || start.Total != 9 {
		t.Fatalf("message_start usage = %#v", start)
	}
	delta := usageFromAnthropicSSELine([]byte(`data: {"type":"message_delta","usage":{"output_tokens":5}}` + "\n"))
	got := mergeAnthropicUsage(start, delta)
	if got.Input != 8 || got.Output != 5 || got.Total != 13 {
		t.Fatalf("merged usage = %#v", got)
	}
	if got := usageFromAnthropicSSELine([]byte("event: ping\n")); got.Total != 0 || got.Input != 0 || got.Output != 0 {
		t.Fatalf("non-data line should not produce usage, got %#v", got)
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

func TestConfiguredRateLimitsBlockGatewayRequests(t *testing.T) {
	cases := []struct {
		name       string
		scopeType  string
		scopeValue func(store.User, store.RoutedModel) string
		want       string
	}{
		{name: "user", scopeType: "user", scopeValue: func(u store.User, _ store.RoutedModel) string { return u.ID }, want: "user"},
		{name: "department", scopeType: "department", scopeValue: func(u store.User, _ store.RoutedModel) string { return u.Department }, want: "department"},
		{name: "provider", scopeType: "provider", scopeValue: func(_ store.User, r store.RoutedModel) string { return r.Provider.ID }, want: "provider"},
		{name: "model", scopeType: "model", scopeValue: func(_ store.User, r store.RoutedModel) string { return r.Model.Route }, want: "model"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer st.Close()
			if err := st.EnsureSeedData("hash"); err != nil {
				t.Fatalf("EnsureSeedData: %v", err)
			}
			user, err := st.GetUserByUsername(ctx, "admin")
			if err != nil {
				t.Fatalf("GetUserByUsername: %v", err)
			}
			plain, prefix, keyHash, err := auth.NewAPIKey()
			if err != nil {
				t.Fatalf("NewAPIKey: %v", err)
			}
			if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_limit", UserID: user.ID, Name: "Limit key", Prefix: prefix, KeyHash: keyHash}); err != nil {
				t.Fatalf("CreateAPIKey: %v", err)
			}
			route, err := st.ResolveModel(ctx, "local-ollama/llama3.1:8b")
			if err != nil {
				t.Fatalf("ResolveModel: %v", err)
			}
			limit := store.RateLimit{
				ID:         "limit_" + tt.name,
				ScopeType:  tt.scopeType,
				ScopeValue: tt.scopeValue(user, route),
				RPMLimit:   1,
				IsActive:   true,
			}
			if err := st.CreateRateLimit(ctx, limit); err != nil {
				t.Fatalf("CreateRateLimit: %v", err)
			}
			if err := st.InsertUsage(ctx, store.UsageRecord{
				ID:          "usage_limit_" + tt.name,
				RequestID:   "req_limit_" + tt.name,
				UserID:      user.ID,
				Username:    user.Username,
				Department:  user.Department,
				APIKeyID:    "key_limit",
				ProviderID:  route.Provider.ID,
				Model:       route.Model.Route,
				TotalTokens: 10,
				StatusCode:  200,
				CreatedAt:   time.Now().UTC(),
			}); err != nil {
				t.Fatalf("InsertUsage: %v", err)
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
			resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
				"model":    route.Model.Route,
				"messages": []map[string]string{{"role": "user", "content": "hello"}},
			})
			if resp.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
			}
			if !strings.Contains(resp.Body.String(), tt.want) || !strings.Contains(resp.Body.String(), "requests per minute limit exceeded") {
				t.Fatalf("unexpected response body: %s", resp.Body.String())
			}
		})
	}
}

func TestSelfServiceAPIKeyExpirationAndRotation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_keys",
		Username:     "key-user",
		Role:         "user",
		PasswordHash: "unused",
		AuthProvider: "local",
		IsActive:     true,
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
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
	session := sessionToken(t, user)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	createResp := jsonRequest(t, handler, http.MethodPost, "/api/api-keys", session, map[string]any{
		"name":       "Original key",
		"expires_at": expires.Format(time.RFC3339),
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", createResp.Code, createResp.Body.String())
	}
	var createBody struct {
		Key    string       `json:"key"`
		Record store.APIKey `json:"record"`
	}
	decodeRecorder(t, createResp, &createBody)
	if createBody.Key == "" || createBody.Record.ID == "" || createBody.Record.ExpiresAt == nil {
		t.Fatalf("unexpected create body: %#v", createBody)
	}

	newExpires := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	patchResp := jsonRequest(t, handler, http.MethodPatch, "/api/api-keys/"+createBody.Record.ID, session, map[string]any{
		"name":       "Rotatable key",
		"expires_at": newExpires.Format(time.RFC3339),
	})
	if patchResp.Code != http.StatusOK {
		t.Fatalf("patch status = %d body = %s", patchResp.Code, patchResp.Body.String())
	}
	pastResp := jsonRequest(t, handler, http.MethodPatch, "/api/api-keys/"+createBody.Record.ID, session, map[string]any{
		"name":       "Bad expiration",
		"expires_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	})
	if pastResp.Code != http.StatusBadRequest {
		t.Fatalf("past expiration status = %d body = %s", pastResp.Code, pastResp.Body.String())
	}

	rotateResp := jsonRequest(t, handler, http.MethodPost, "/api/api-keys/"+createBody.Record.ID+"/rotate", session, map[string]any{})
	if rotateResp.Code != http.StatusOK {
		t.Fatalf("rotate status = %d body = %s", rotateResp.Code, rotateResp.Body.String())
	}
	var rotateBody struct {
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	decodeRecorder(t, rotateResp, &rotateBody)
	if rotateBody.Key == "" || rotateBody.Key == createBody.Key || rotateBody.Prefix == createBody.Record.Prefix {
		t.Fatalf("unexpected rotate body: %#v", rotateBody)
	}
	oldResp := jsonRequest(t, handler, http.MethodGet, "/v1/models", createBody.Key, nil)
	if oldResp.Code != http.StatusUnauthorized {
		t.Fatalf("old key status = %d body = %s", oldResp.Code, oldResp.Body.String())
	}
	newResp := jsonRequest(t, handler, http.MethodGet, "/v1/models", rotateBody.Key, nil)
	if newResp.Code != http.StatusOK {
		t.Fatalf("new key status = %d body = %s", newResp.Code, newResp.Body.String())
	}

	listResp := jsonRequest(t, handler, http.MethodGet, "/api/api-keys", session, nil)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", listResp.Code, listResp.Body.String())
	}
	var keys []store.APIKey
	decodeRecorder(t, listResp, &keys)
	if len(keys) != 1 || keys[0].Name != "Rotatable key" || keys[0].Prefix != rotateBody.Prefix || keys[0].LastUsedAt == nil {
		t.Fatalf("unexpected key list: %#v", keys)
	}
}

func TestAdminCanRotateActiveAPIKey(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	admin := store.User{ID: "user_admin", Username: "admin", Role: "admin", PasswordHash: "unused", AuthProvider: "local", IsActive: true}
	user := store.User{ID: "user_owner", Username: "owner", Role: "user", PasswordHash: "unused", AuthProvider: "local", IsActive: true}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}
	oldPlain, oldPrefix, oldHash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_rotate_admin", UserID: user.ID, Name: "Owned key", Prefix: oldPrefix, KeyHash: oldHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
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
	rotateResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/api-keys/key_rotate_admin/rotate", sessionToken(t, admin), map[string]any{})
	if rotateResp.Code != http.StatusOK {
		t.Fatalf("admin rotate status = %d body = %s", rotateResp.Code, rotateResp.Body.String())
	}
	var rotateBody struct {
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	decodeRecorder(t, rotateResp, &rotateBody)
	if rotateBody.Key == "" || rotateBody.Key == oldPlain || rotateBody.Prefix == oldPrefix {
		t.Fatalf("unexpected rotate body: %#v", rotateBody)
	}
	oldResp := jsonRequest(t, handler, http.MethodGet, "/v1/models", oldPlain, nil)
	if oldResp.Code != http.StatusUnauthorized {
		t.Fatalf("old key status = %d body = %s", oldResp.Code, oldResp.Body.String())
	}
	newResp := jsonRequest(t, handler, http.MethodGet, "/v1/models", rotateBody.Key, nil)
	if newResp.Code != http.StatusOK {
		t.Fatalf("new key status = %d body = %s", newResp.Code, newResp.Body.String())
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

func TestBedrockConverseInputMapsImagesAndTools(t *testing.T) {
	input, err := bedrockConverseInput("anthropic.claude-3-5-sonnet-20240620-v1:0", map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Describe this image"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aGVsbG8="}},
			}},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "lookup_cost_center",
					"description": "Find a cost center",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"department": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		"tool_choice": "required",
	})
	if err != nil {
		t.Fatalf("bedrockConverseInput: %v", err)
	}
	if len(input.Messages) != 1 || len(input.Messages[0].Content) != 2 {
		t.Fatalf("unexpected content blocks: %#v", input.Messages)
	}
	image := input.Messages[0].Content[1].(*types.ContentBlockMemberImage).Value
	if image.Format != types.ImageFormatPng || string(image.Source.(*types.ImageSourceMemberBytes).Value) != "hello" {
		t.Fatalf("unexpected image block: %#v", image)
	}
	if input.ToolConfig == nil || len(input.ToolConfig.Tools) != 1 {
		t.Fatalf("tool config not mapped: %#v", input.ToolConfig)
	}
	if _, ok := input.ToolConfig.ToolChoice.(*types.ToolChoiceMemberAny); !ok {
		t.Fatalf("tool choice not mapped to required/any: %#v", input.ToolConfig.ToolChoice)
	}
	streamInput, err := bedrockConverseStreamInput("anthropic.claude-3-5-sonnet-20240620-v1:0", map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
		"tools": []any{map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "lookup_cost_center"},
		}},
	})
	if err != nil {
		t.Fatalf("bedrockConverseStreamInput: %v", err)
	}
	if streamInput.ToolConfig == nil || streamInput.ToolConfig.Tools == nil {
		t.Fatalf("stream input did not preserve tool config: %#v", streamInput)
	}
}

func TestOpenAIResponseFromBedrockMapsToolUse(t *testing.T) {
	route := store.RoutedModel{Model: store.Model{Route: "bedrock/claude"}}
	response := openAIResponseFromBedrock(route, &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role: types.ConversationRoleAssistant,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
					ToolUseId: aws.String("tooluse_1"),
					Name:      aws.String("lookup_cost_center"),
					Input:     bedrockdocument.NewLazyDocument(map[string]any{"department": "AI"}),
				}},
			},
		}},
		StopReason: types.StopReasonToolUse,
	})
	choices := response["choices"].([]map[string]any)
	message := choices[0]["message"].(map[string]any)
	toolCalls := message["tool_calls"].([]map[string]any)
	if choices[0]["finish_reason"] != "tool_calls" || len(toolCalls) != 1 {
		t.Fatalf("unexpected tool response: %#v", response)
	}
	fn := toolCalls[0]["function"].(map[string]any)
	if toolCalls[0]["id"] != "tooluse_1" || fn["name"] != "lookup_cost_center" || !strings.Contains(fn["arguments"].(string), "department") {
		t.Fatalf("unexpected tool call mapping: %#v", toolCalls[0])
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
	requests, err := st.SearchRequestLogs(ctx, store.RequestLogQuery{ProviderID: provider.ID, Limit: 10})
	if err != nil {
		t.Fatalf("SearchRequestLogs: %v", err)
	}
	if requests.Total != 1 || len(requests.Items) != 1 {
		t.Fatalf("unexpected request logs: %#v", requests)
	}
	reqLog := requests.Items[0]
	if reqLog.RequestID == "" || reqLog.Username != user.Username || reqLog.APIKeyPrefix != prefix || reqLog.ProviderType != "bedrock" || reqLog.ModelRoute != model.Route || reqLog.UpstreamModelID != model.ModelID || reqLog.Endpoint != "/v1/chat/completions" || reqLog.Streaming {
		t.Fatalf("unexpected request metadata: %#v", reqLog)
	}
	if reqLog.TotalTokens != 15 || reqLog.CostUSD != 0.00002 || reqLog.StatusCode != http.StatusOK {
		t.Fatalf("unexpected request usage metadata: %#v", reqLog)
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

func TestOpenAIChatCompletionsStreamsBedrockAndRecordsUsage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_bedrock_stream",
		Username:     "bedrock-stream-user",
		Department:   "AI",
		Role:         "user",
		PasswordHash: "unused",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_bedrock_stream", UserID: user.ID, Name: "Bedrock stream key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "aws-bedrock-stream", Name: "AWS Bedrock Stream", Type: "bedrock", AWSRegion: "us-west-2", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_bedrock_stream",
		ProviderID:           provider.ID,
		ModelID:              "anthropic.claude-3-5-sonnet-20240620-v1:0",
		Route:                "aws-bedrock-stream/claude-sonnet",
		DisplayName:          "Bedrock Sonnet Stream",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	fake := &fakeBedrockClient{streamEvents: []types.ConverseStreamOutput{
		&types.ConverseStreamOutputMemberMessageStart{Value: types.MessageStartEvent{Role: types.ConversationRoleAssistant}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(0),
			Delta:             &types.ContentBlockDeltaMemberText{Value: "hello"},
		}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(0),
			Delta:             &types.ContentBlockDeltaMemberText{Value: " world"},
		}},
		&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonEndTurn}},
		&types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{Usage: &types.TokenUsage{
			InputTokens:  aws.Int32(4),
			OutputTokens: aws.Int32(2),
			TotalTokens:  aws.Int32(6),
		}}},
	}}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		BedrockClientFactory: func(_ context.Context, _ store.Provider) (BedrockConverseClient, error) {
			return fake, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":          model.Route,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []map[string]string{{"role": "user", "content": "Hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if fake.streamInput == nil || *fake.streamInput.ModelId != model.ModelID {
		t.Fatalf("stream input not captured correctly: %#v", fake.streamInput)
	}
	if got := resp.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
	body := resp.Body.String()
	for _, want := range []string{`"role":"assistant"`, `"content":"hello"`, `"content":" world"`, `"prompt_tokens":4`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 4 || usage.OutputTokens != 2 || usage.TotalTokens != 6 || usage.CostUSD != 0.000008 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestAnthropicMessagesStreamsBedrockWithToolUseAndRecordsUsage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_bedrock_anthropic_stream",
		Username:     "bedrock-anthropic-stream-user",
		Department:   "AI",
		Role:         "user",
		PasswordHash: "unused",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_bedrock_anthropic_stream", UserID: user.ID, Name: "Bedrock Anthropic stream key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "aws-bedrock-anthropic-stream", Name: "AWS Bedrock Anthropic Stream", Type: "bedrock", AWSRegion: "us-west-2", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_bedrock_anthropic_stream",
		ProviderID:           provider.ID,
		ModelID:              "anthropic.claude-3-5-sonnet-20240620-v1:0",
		Route:                "aws-bedrock-anthropic-stream/claude-sonnet",
		DisplayName:          "Bedrock Sonnet Anthropic Stream",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	fake := &fakeBedrockClient{streamEvents: []types.ConverseStreamOutput{
		&types.ConverseStreamOutputMemberMessageStart{Value: types.MessageStartEvent{Role: types.ConversationRoleAssistant}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(0),
			Delta:             &types.ContentBlockDeltaMemberText{Value: "I'll inspect the file."},
		}},
		&types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{
			ContentBlockIndex: aws.Int32(1),
			Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{
				ToolUseId: aws.String("tooluse_1"),
				Name:      aws.String("Read"),
			}},
		}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(1),
			Delta:             &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: aws.String(`{"file_path":"`)}},
		}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(1),
			Delta:             &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: aws.String(`README.md"}`)}},
		}},
		&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonToolUse}},
		&types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{Usage: &types.TokenUsage{
			InputTokens:  aws.Int32(7),
			OutputTokens: aws.Int32(3),
			TotalTokens:  aws.Int32(10),
		}}},
	}}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		BedrockClientFactory: func(_ context.Context, _ store.Provider) (BedrockConverseClient, error) {
			return fake, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp := jsonRequest(t, handler, http.MethodPost, "/anthropic/v1/messages", plain, map[string]any{
		"model":      model.Route,
		"stream":     true,
		"max_tokens": 128,
		"messages":   []map[string]string{{"role": "user", "content": "Read the README"}},
		"tools": []map[string]any{{
			"name":        "Read",
			"description": "Read a file from disk",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]string{"type": "string"},
				},
			},
		}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if fake.streamInput == nil || *fake.streamInput.ModelId != model.ModelID {
		t.Fatalf("stream input not captured correctly: %#v", fake.streamInput)
	}
	if fake.streamInput.ToolConfig == nil || len(fake.streamInput.ToolConfig.Tools) != 1 {
		t.Fatalf("tool config not mapped to Bedrock stream input: %#v", fake.streamInput.ToolConfig)
	}
	if got := resp.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
	body := resp.Body.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: message_stop",
		`"text":"I'll inspect the file."`,
		`"type":"tool_use"`,
		`"id":"tooluse_1"`,
		`"name":"Read"`,
		`"partial_json"`,
		`README.md`,
		`"stop_reason":"tool_use"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.TotalTokens != 10 || usage.CostUSD != 0.000013 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
	requests, err := st.SearchRequestLogs(ctx, store.RequestLogQuery{ProviderID: provider.ID, Limit: 10})
	if err != nil {
		t.Fatalf("SearchRequestLogs: %v", err)
	}
	if requests.Total != 1 || len(requests.Items) != 1 {
		t.Fatalf("unexpected request logs: %#v", requests)
	}
	reqLog := requests.Items[0]
	if reqLog.RequestID == "" || reqLog.Username != user.Username || reqLog.APIKeyPrefix != prefix || reqLog.ProviderType != "bedrock" || reqLog.ModelRoute != model.Route || reqLog.UpstreamModelID != model.ModelID || reqLog.Endpoint != "/anthropic/v1/messages" || !reqLog.Streaming {
		t.Fatalf("unexpected request metadata: %#v", reqLog)
	}
	if reqLog.TotalTokens != 10 || reqLog.CostUSD != 0.000013 || reqLog.StatusCode != http.StatusOK {
		t.Fatalf("unexpected request usage metadata: %#v", reqLog)
	}
}

func TestOpenAIChatCompletionsStreamsWithoutUsageEstimatesTokens(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{ID: "user_stream_estimate", Username: "stream-estimate", Department: "AI", Role: "user", AuthProvider: "local", IsActive: true}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	plain, prefix, keyHash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_stream_estimate", UserID: user.ID, Name: "Estimate key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "ollama-estimate", Name: "Ollama Estimate", Type: "openai", BaseURL: "http://ollama-estimate.test/v1", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_stream_estimate",
		ProviderID:           provider.ID,
		ModelID:              "gemma4:31b-cloud",
		Route:                "ollama-estimate/gemma4:31b-cloud",
		DisplayName:          "Gemma Estimate",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "ollama-estimate.test" || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if req["model"] != model.ModelID || req["stream"] != true {
			t.Fatalf("unexpected upstream request: %#v", req)
		}
		streamBody := strings.Join([]string{
			`data: {"id":"chunk_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n",
			`data: {"id":"chunk_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello world"}}]}` + "\n\n",
			"data: [DONE]\n\n",
		}, "")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(streamBody)),
			Request:    r,
		}, nil
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
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":    model.Route,
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "Hello there"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"content":"hello world"`) {
		t.Fatalf("stream body missing content: %s", resp.Body.String())
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens <= 0 || usage.OutputTokens <= 0 || usage.TotalTokens <= 0 || usage.CostUSD <= 0 {
		t.Fatalf("expected estimated usage and cost, got %#v", usage)
	}
	requests, err := st.SearchRequestLogs(ctx, store.RequestLogQuery{ProviderID: provider.ID, Limit: 10})
	if err != nil {
		t.Fatalf("SearchRequestLogs: %v", err)
	}
	if requests.Total != 1 || len(requests.Items) != 1 || !requests.Items[0].Streaming || requests.Items[0].TotalTokens <= 0 || requests.Items[0].CostUSD <= 0 {
		t.Fatalf("unexpected request metadata: %#v", requests)
	}
}

func TestOpenAIChatCompletionsGuardrailsRedactInputAndOutput(t *testing.T) {
	policy := store.DefaultGuardrailPolicy()
	policy.Enabled = true
	policy.InputAction = "redact"
	policy.OutputAction = "redact"
	policy.DetectPhone = false
	policy.DetectSSN = false
	policy.DetectCreditCard = false
	policy.DetectAPIKey = false
	fixture := newOpenAIGuardrailFixture(t, policy, func(r *http.Request) (*http.Response, error) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		body, _ := json.Marshal(req)
		if strings.Contains(string(body), "jane@example.com") || !strings.Contains(string(body), "[REDACTED]") {
			t.Fatalf("expected redacted upstream request, got %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl_guardrail",
				"object":"chat.completion",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Email jane@example.com for details."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":8,"completion_tokens":9,"total_tokens":17}
			}`)),
			Request: r,
		}, nil
	})
	resp := jsonRequest(t, fixture.Handler, http.MethodPost, "/v1/chat/completions", fixture.APIKey, map[string]any{
		"model": fixture.Route,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "My email is jane@example.com",
		}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "jane@example.com") || !strings.Contains(resp.Body.String(), "[REDACTED]") {
		t.Fatalf("expected redacted response, got %s", resp.Body.String())
	}
}

func TestOpenAIChatCompletionsGuardrailsCustomPatternRedactsInput(t *testing.T) {
	policy := store.DefaultGuardrailPolicy()
	policy.Enabled = true
	policy.InputAction = "redact"
	policy.OutputAction = "off"
	policy.DetectEmail = false
	policy.DetectPhone = false
	policy.DetectSSN = false
	policy.DetectCreditCard = false
	policy.DetectAPIKey = false
	policy.CustomPatterns = []store.GuardrailCustomPattern{{
		ID:            "employee-id",
		Name:          "Employee ID",
		Pattern:       `EMP-[0-9]+`,
		Action:        "redact",
		RedactionText: "[EMPLOYEE_ID]",
		Enabled:       true,
	}}
	fixture := newOpenAIGuardrailFixture(t, policy, func(r *http.Request) (*http.Response, error) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		body, _ := json.Marshal(req)
		if strings.Contains(string(body), "EMP-12345") || !strings.Contains(string(body), "[EMPLOYEE_ID]") {
			t.Fatalf("expected custom redacted upstream request, got %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl_guardrail_custom",
				"object":"chat.completion",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}
			}`)),
			Request: r,
		}, nil
	})
	resp := jsonRequest(t, fixture.Handler, http.MethodPost, "/v1/chat/completions", fixture.APIKey, map[string]any{
		"model": fixture.Route,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "Employee EMP-12345 needs access",
		}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func TestOpenAIChatCompletionsGuardrailsBlockInput(t *testing.T) {
	policy := store.DefaultGuardrailPolicy()
	policy.Enabled = true
	policy.InputAction = "block"
	policy.OutputAction = "off"
	upstreamHits := 0
	fixture := newOpenAIGuardrailFixture(t, policy, func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		return nil, errors.New("upstream should not be called")
	})
	resp := jsonRequest(t, fixture.Handler, http.MethodPost, "/v1/chat/completions", fixture.APIKey, map[string]any{
		"model": fixture.Route,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "SSN 123-45-6789",
		}},
	})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream called %d times", upstreamHits)
	}
	if !strings.Contains(resp.Body.String(), "content_policy_violation") || !strings.Contains(resp.Body.String(), "ssn") {
		t.Fatalf("unexpected guardrail body: %s", resp.Body.String())
	}
}

func TestOpenAIChatCompletionsGuardrailsCustomPatternBlocksInput(t *testing.T) {
	policy := store.DefaultGuardrailPolicy()
	policy.Enabled = true
	policy.InputAction = "redact"
	policy.OutputAction = "off"
	policy.DetectEmail = false
	policy.DetectPhone = false
	policy.DetectSSN = false
	policy.DetectCreditCard = false
	policy.DetectAPIKey = false
	policy.CustomPatterns = []store.GuardrailCustomPattern{{
		ID:      "internal-host",
		Name:    "Internal host",
		Pattern: `db-[0-9]+\.internal`,
		Action:  "block",
		Enabled: true,
	}}
	upstreamHits := 0
	fixture := newOpenAIGuardrailFixture(t, policy, func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		return nil, errors.New("upstream should not be called")
	})
	resp := jsonRequest(t, fixture.Handler, http.MethodPost, "/v1/chat/completions", fixture.APIKey, map[string]any{
		"model": fixture.Route,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "Connect to db-17.internal",
		}},
	})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream called %d times", upstreamHits)
	}
	if !strings.Contains(resp.Body.String(), "content_policy_violation") || !strings.Contains(resp.Body.String(), "custom:Internal host") {
		t.Fatalf("unexpected guardrail body: %s", resp.Body.String())
	}
}

func TestOpenAIChatCompletionsGuardrailsBlockNonStreamOutput(t *testing.T) {
	policy := store.DefaultGuardrailPolicy()
	policy.Enabled = true
	policy.InputAction = "off"
	policy.OutputAction = "block"
	fixture := newOpenAIGuardrailFixture(t, policy, func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl_guardrail_block",
				"object":"chat.completion",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Call 415-555-1212."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}
			}`)),
			Request: r,
		}, nil
	})
	resp := jsonRequest(t, fixture.Handler, http.MethodPost, "/v1/chat/completions", fixture.APIKey, map[string]any{
		"model":    fixture.Route,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "415-555-1212") || !strings.Contains(resp.Body.String(), "content_policy_violation") {
		t.Fatalf("unexpected guardrail response: %s", resp.Body.String())
	}
	usage, err := fixture.Store.UsageForUser(context.Background(), fixture.UserID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.TotalTokens != 10 || usage.CostUSD <= 0 {
		t.Fatalf("expected usage recorded for blocked output, got %#v", usage)
	}
}

func TestGuardrailPreviewCustomPatternAndInvalidRegex(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin := store.User{ID: "admin_guardrail_preview", Username: "admin", Role: "admin", PasswordHash: "unused", AuthProvider: "local", IsActive: true}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser: %v", err)
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
	policy := store.DefaultGuardrailPolicy()
	policy.Enabled = true
	policy.InputAction = "redact"
	policy.DetectEmail = false
	policy.DetectPhone = false
	policy.DetectSSN = false
	policy.DetectCreditCard = false
	policy.DetectAPIKey = false
	policy.CustomPatterns = []store.GuardrailCustomPattern{{
		ID:            "employee-id",
		Name:          "Employee ID",
		Pattern:       `EMP-[0-9]+`,
		Action:        "redact",
		RedactionText: "[EMPLOYEE_ID]",
		Enabled:       true,
	}}
	resp := jsonRequest(t, handler, http.MethodPost, "/api/admin/guardrails/test", sessionToken(t, admin), map[string]any{
		"phase":  "input",
		"text":   "Employee EMP-12345",
		"policy": policy,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var preview guardrailPreviewResponse
	decodeRecorder(t, resp, &preview)
	if !preview.Redacted || preview.Blocked || preview.Output != "Employee [EMPLOYEE_ID]" || len(preview.Findings) != 1 || preview.Findings[0] != "custom:Employee ID" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	policy.CustomPatterns[0].Pattern = `[`
	invalid := jsonRequest(t, handler, http.MethodPost, "/api/admin/guardrails/test", sessionToken(t, admin), map[string]any{
		"phase":  "input",
		"text":   "Employee EMP-12345",
		"policy": policy,
	})
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "invalid regex") {
		t.Fatalf("expected invalid regex error, status = %d body = %s", invalid.Code, invalid.Body.String())
	}
}

func TestOpenAIChatCompletionsGuardrailsRejectStreamingOutputBlock(t *testing.T) {
	policy := store.DefaultGuardrailPolicy()
	policy.Enabled = true
	policy.InputAction = "off"
	policy.OutputAction = "block"
	upstreamHits := 0
	fixture := newOpenAIGuardrailFixture(t, policy, func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		return nil, errors.New("upstream should not be called")
	})
	resp := jsonRequest(t, fixture.Handler, http.MethodPost, "/v1/chat/completions", fixture.APIKey, map[string]any{
		"model":    fixture.Route,
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream called %d times", upstreamHits)
	}
	if !strings.Contains(resp.Body.String(), "streaming requests are blocked") {
		t.Fatalf("unexpected guardrail body: %s", resp.Body.String())
	}
}

func TestAnthropicMessagesStreamsAndRecordsUsage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_anthropic_stream",
		Username:     "anthropic-stream-user",
		Department:   "AI",
		Role:         "user",
		PasswordHash: "unused",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_anthropic_stream", UserID: user.ID, Name: "Anthropic stream key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "anthropic-stream", Name: "Anthropic Stream", Type: "anthropic", BaseURL: "http://anthropic-stream.test", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_anthropic_stream",
		ProviderID:           provider.ID,
		ModelID:              "claude-upstream",
		Route:                "anthropic-stream/claude",
		DisplayName:          "Anthropic Stream",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	upstreamHits := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		if r.URL.Host != "anthropic-stream.test" || r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q", got)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if req["model"] != "claude-upstream" || req["stream"] != true {
			t.Fatalf("unexpected upstream body: %#v", req)
		}
		streamBody := strings.Join([]string{
			"event: message_start\n",
			`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":8,"output_tokens":1}}}` + "\n\n",
			"event: content_block_delta\n",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n",
			"event: message_delta\n",
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n",
			"event: message_stop\n",
			`data: {"type":"message_stop"}` + "\n\n",
		}, "")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(streamBody)),
			Request:    r,
		}, nil
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
	resp := jsonRequest(t, handler, http.MethodPost, "/anthropic/v1/messages", plain, map[string]any{
		"model":      model.Route,
		"stream":     true,
		"max_tokens": 32,
		"messages":   []map[string]string{{"role": "user", "content": "Hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits)
	}
	if got := resp.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
	body := resp.Body.String()
	for _, want := range []string{"event: message_start", `"text":"hello"`, "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 8 || usage.OutputTokens != 5 || usage.TotalTokens != 13 || usage.CostUSD != 0.000018 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestAnthropicMessagesAcceptsXAPIKeyAndTranslatesOpenAICompatibleRoute(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_anthropic_openai",
		Username:     "anthropic-openai-user",
		Department:   "AI",
		Role:         "user",
		PasswordHash: "unused",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_anthropic_openai", UserID: user.ID, Name: "Anthropic OpenAI key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "local-ollama", Name: "Local Ollama", Type: "openai", BaseURL: "http://ollama.test/v1", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_anthropic_openai",
		ProviderID:           provider.ID,
		ModelID:              "glm-5.2:cloud",
		Route:                "glm-5.2:cloud",
		DisplayName:          "GLM 5.2",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	upstreamHits := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		if r.URL.Host != "ollama.test" || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if req["model"] != "glm-5.2:cloud" || req["max_tokens"] != float64(64) {
			t.Fatalf("unexpected upstream request: %#v", req)
		}
		messages, _ := req["messages"].([]any)
		if len(messages) != 1 {
			t.Fatalf("upstream messages = %#v", req["messages"])
		}
		msg, _ := messages[0].(map[string]any)
		if msg["role"] != "user" || msg["content"] != "ping" {
			t.Fatalf("unexpected upstream message: %#v", msg)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl_anthropic_translate",
				"object":"chat.completion",
				"model":"glm-5.2:cloud",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning":"pong"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}
			}`)),
			Request: r,
		}, nil
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
	resp := jsonRequestWithHeaders(t, handler, http.MethodPost, "/anthropic/v1/messages", map[string]string{
		"x-api-key":         plain,
		"anthropic-version": "2023-06-01",
	}, map[string]any{
		"model":      model.Route,
		"max_tokens": 64,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits)
	}
	var body map[string]any
	decodeRecorder(t, resp, &body)
	if body["type"] != "message" || body["role"] != "assistant" || body["model"] != "glm-5.2:cloud" {
		t.Fatalf("unexpected anthropic response: %#v", body)
	}
	content, _ := body["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("unexpected content: %#v", body["content"])
	}
	textBlock, _ := content[0].(map[string]any)
	if textBlock["type"] != "text" || textBlock["text"] != "pong" {
		t.Fatalf("unexpected content block: %#v", textBlock)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 4 || usage.OutputTokens != 2 || usage.TotalTokens != 6 || usage.CostUSD != 0.000008 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestAnthropicMessagesStreamsOpenAICompatibleRoute(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_anthropic_openai_stream",
		Username:     "anthropic-openai-stream-user",
		Department:   "AI",
		Role:         "user",
		PasswordHash: "unused",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_anthropic_openai_stream", UserID: user.ID, Name: "Anthropic OpenAI stream key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "local-ollama-stream", Name: "Local Ollama Stream", Type: "openai", BaseURL: "http://ollama-stream.test/v1", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_anthropic_openai_stream",
		ProviderID:           provider.ID,
		ModelID:              "glm-5.2:cloud",
		Route:                "glm-5.2:cloud",
		DisplayName:          "GLM 5.2 Stream",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	upstreamHits := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		if r.URL.Host != "ollama-stream.test" || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if req["model"] != "glm-5.2:cloud" || req["stream"] != true {
			t.Fatalf("unexpected upstream request: %#v", req)
		}
		tools, _ := req["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("expected one translated tool, got %#v", req["tools"])
		}
		tool, _ := tools[0].(map[string]any)
		function, _ := tool["function"].(map[string]any)
		if tool["type"] != "function" || function["name"] != "Read" {
			t.Fatalf("unexpected translated tool: %#v", tool)
		}
		streamBody := strings.Join([]string{
			`data: {"id":"chatcmpl_stream","model":"glm-5.2:cloud","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl_stream","model":"glm-5.2:cloud","choices":[{"index":0,"delta":{"content":"hello "},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl_stream","model":"glm-5.2:cloud","choices":[{"index":0,"delta":{"reasoning":"thinking"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl_stream","model":"glm-5.2:cloud","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl_stream","model":"glm-5.2:cloud","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"file_path\":\"README.md\"}"}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl_stream","model":"glm-5.2:cloud","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16}}` + "\n\n",
			"data: [DONE]\n\n",
		}, "")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(streamBody)),
			Request:    r,
		}, nil
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
	resp := jsonRequestWithHeaders(t, handler, http.MethodPost, "/anthropic/v1/messages", map[string]string{
		"x-api-key":         plain,
		"anthropic-version": "2023-06-01",
	}, map[string]any{
		"model":      model.Route,
		"stream":     true,
		"max_tokens": 32,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"tools": []map[string]any{{
			"name":        "Read",
			"description": "Read a file",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
			},
		}},
		"tool_choice": map[string]any{"type": "auto"},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits)
	}
	if got := resp.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
	body := resp.Body.String()
	for _, want := range []string{
		"event: message_start",
		`"text":"hello "`,
		`"text":"thinking"`,
		`"type":"text_delta"`,
		`"type":"tool_use"`,
		`"name":"Read"`,
		`"type":"input_json_delta"`,
		`"partial_json":"{\"file_path\":\"README.md\"}"`,
		`"stop_reason":"tool_use"`,
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 10 || usage.OutputTokens != 6 || usage.TotalTokens != 16 || usage.CostUSD != 0.000022 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestPrometheusMetricsEndpointRecordsGatewayAndUpstreamMetrics(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_metrics",
		Username:     "metrics-user",
		Department:   "AI",
		Role:         "user",
		PasswordHash: "unused",
		AuthProvider: "local",
		IsActive:     true,
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	plain, _, keyHash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_metrics", UserID: user.ID, Name: "Metrics key", Prefix: "pgw-sk-met", KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "metrics-openai", Name: "Metrics OpenAI", Type: "openai", BaseURL: "http://metrics-openai.test/v1", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_metrics",
		ProviderID:           provider.ID,
		ModelID:              "upstream-metrics",
		Route:                "metrics/route",
		DisplayName:          "Metrics Route",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl_metrics",
				"object":"chat.completion",
				"model":"upstream-metrics",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
			}`)),
			Request: r,
		}, nil
	})
	handler, err := New(Options{
		Config: config.Config{
			SessionSecret: "test-secret",
			Telemetry: config.TelemetryConfig{
				MetricsEnabled: true,
				MetricsPath:    "/metrics",
				ServiceName:    "phlox-gw-test",
			},
		},
		Store: st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d body = %s", health.Code, health.Body.String())
	}
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":    model.Route,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("chat status = %d body = %s", resp.Code, resp.Body.String())
	}
	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status = %d body = %s", metrics.Code, metrics.Body.String())
	}
	body := metrics.Body.String()
	for _, want := range []string{
		`phlox_gw_http_requests_total{method="GET",route="/api/health",status="200"} 1`,
		`phlox_gw_http_requests_total{method="POST",route="/v1/chat/completions",status="200"} 1`,
		`phlox_gw_upstream_requests_total{model_route="metrics/route",protocol="openai",provider_id="metrics-openai",provider_type="openai",status="200"} 1`,
		`phlox_gw_upstream_tokens_total{direction="input",model_route="metrics/route",protocol="openai",provider_id="metrics-openai",provider_type="openai"} 3`,
		`phlox_gw_upstream_tokens_total{direction="output",model_route="metrics/route",protocol="openai",provider_id="metrics-openai",provider_type="openai"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestPrometheusMetricsEndpointDisabledByDefault(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
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
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("metrics status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func TestOpenAIChatCompletionsFallsBackAfterPrimaryProviderFailure(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_fallback",
		Username:     "fallback-user",
		Department:   "AI",
		Role:         "user",
		PasswordHash: "unused",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_fallback", UserID: user.ID, Name: "Fallback key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	primaryHits := 0
	fallbackHits := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		status := http.StatusOK
		switch r.URL.Host {
		case "primary.test":
			primaryHits++
			if r.URL.Path != "/v1/chat/completions" {
				t.Fatalf("unexpected primary path: %s", r.URL.Path)
			}
			status = http.StatusServiceUnavailable
			body = `{"error":{"message":"primary down"}}`
		case "fallback.test":
			fallbackHits++
			if r.URL.Path != "/v1/chat/completions" {
				t.Fatalf("unexpected fallback path: %s", r.URL.Path)
			}
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("fallback decode: %v", err)
			}
			if req["model"] != "backup-model" {
				t.Fatalf("fallback upstream model = %#v", req["model"])
			}
			body = `{"id":"chatcmpl_backup","object":"chat.completion","model":"backup-model","choices":[{"message":{"role":"assistant","content":"backup ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`
		default:
			t.Fatalf("unexpected upstream host: %s", r.URL.Host)
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})

	primaryProvider := store.Provider{ID: "primary-openai", Name: "Primary", Type: "openai", BaseURL: "http://primary.test/v1", Enabled: true}
	fallbackProvider := store.Provider{ID: "backup-openai", Name: "Backup", Type: "openai", BaseURL: "http://fallback.test/v1", Enabled: true}
	if err := st.CreateProvider(ctx, primaryProvider); err != nil {
		t.Fatalf("CreateProvider primary: %v", err)
	}
	if err := st.CreateProvider(ctx, fallbackProvider); err != nil {
		t.Fatalf("CreateProvider fallback: %v", err)
	}
	primaryModel := store.Model{
		ID:                   "model_primary_fallback",
		ProviderID:           primaryProvider.ID,
		ModelID:              "primary-model",
		Route:                "gateway/primary-model",
		DisplayName:          "Primary model",
		Enabled:              true,
		SupportsStreaming:    true,
		FallbackRoutes:       "backup-openai/backup-model",
		HealthRoutingEnabled: true,
	}
	fallbackModel := store.Model{
		ID:                   "model_backup_fallback",
		ProviderID:           fallbackProvider.ID,
		ModelID:              "backup-model",
		Route:                "backup-openai/backup-model",
		DisplayName:          "Backup model",
		Enabled:              true,
		SupportsStreaming:    true,
		HealthRoutingEnabled: true,
	}
	if err := st.CreateModel(ctx, primaryModel); err != nil {
		t.Fatalf("CreateModel primary: %v", err)
	}
	if err := st.CreateModel(ctx, fallbackModel); err != nil {
		t.Fatalf("CreateModel fallback: %v", err)
	}

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
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":    primaryModel.Route,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if primaryHits != 1 || fallbackHits != 1 {
		t.Fatalf("unexpected hit counts primary=%d fallback=%d", primaryHits, fallbackHits)
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	decodeRecorder(t, resp, &body)
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != "backup ok" {
		t.Fatalf("unexpected response body: %#v", body)
	}
	primaryHealth, err := st.GetProvider(ctx, primaryProvider.ID)
	if err != nil {
		t.Fatalf("GetProvider primary: %v", err)
	}
	if primaryHealth.ConsecutiveFailures != 1 || primaryHealth.HealthStatus != "degraded" {
		t.Fatalf("primary health not updated: %#v", primaryHealth)
	}
	fallbackHealth, err := st.GetProvider(ctx, fallbackProvider.ID)
	if err != nil {
		t.Fatalf("GetProvider fallback: %v", err)
	}
	if fallbackHealth.ConsecutiveFailures != 0 || fallbackHealth.HealthStatus != "healthy" {
		t.Fatalf("fallback health not updated: %#v", fallbackHealth)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 2 || usage.InputTokens != 3 || usage.OutputTokens != 4 || usage.TotalTokens != 7 {
		t.Fatalf("unexpected usage after fallback: %#v", usage)
	}
}

func TestOpenAIChatCompletionsUsesWeightedRoute(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{ID: "user_weighted", Username: "weighted", DisplayName: "Weighted", Role: "user", IsActive: true}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	plain, prefix, keyHash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_weighted", UserID: user.ID, Name: "Weighted key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	primaryHits := 0
	weightedHits := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Host {
		case "primary-weighted.test":
			primaryHits++
			body = `{"error":{"message":"primary should not be selected"}}`
		case "weighted.test":
			weightedHits++
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("weighted decode: %v", err)
			}
			if req["model"] != "weighted-model" {
				t.Fatalf("weighted upstream model = %#v", req["model"])
			}
			body = `{"id":"chatcmpl_weighted","object":"chat.completion","model":"weighted-model","choices":[{"message":{"role":"assistant","content":"weighted ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`
		default:
			t.Fatalf("unexpected upstream host: %s", r.URL.Host)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})

	primaryProvider := store.Provider{ID: "primary-weighted", Name: "Primary", Type: "openai", BaseURL: "http://primary-weighted.test/v1", Enabled: true}
	weightedProvider := store.Provider{ID: "weighted-openai", Name: "Weighted", Type: "openai", BaseURL: "http://weighted.test/v1", Enabled: true}
	if err := st.CreateProvider(ctx, primaryProvider); err != nil {
		t.Fatalf("CreateProvider primary: %v", err)
	}
	if err := st.CreateProvider(ctx, weightedProvider); err != nil {
		t.Fatalf("CreateProvider weighted: %v", err)
	}
	primaryModel := store.Model{
		ID:                   "model_primary_weighted",
		ProviderID:           primaryProvider.ID,
		ModelID:              "primary-model",
		Route:                "gateway/weighted-model",
		DisplayName:          "Weighted gateway model",
		Enabled:              true,
		SupportsStreaming:    true,
		WeightedRoutes:       "weighted-openai/weighted-model 100",
		HealthRoutingEnabled: true,
	}
	weightedModel := store.Model{
		ID:                   "model_weighted_target",
		ProviderID:           weightedProvider.ID,
		ModelID:              "weighted-model",
		Route:                "weighted-openai/weighted-model",
		DisplayName:          "Weighted target",
		Enabled:              true,
		SupportsStreaming:    true,
		HealthRoutingEnabled: true,
	}
	if err := st.CreateModel(ctx, primaryModel); err != nil {
		t.Fatalf("CreateModel primary: %v", err)
	}
	if err := st.CreateModel(ctx, weightedModel); err != nil {
		t.Fatalf("CreateModel weighted: %v", err)
	}

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
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":    primaryModel.Route,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if primaryHits != 0 || weightedHits != 1 {
		t.Fatalf("unexpected hit counts primary=%d weighted=%d", primaryHits, weightedHits)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 5 || usage.OutputTokens != 6 || usage.TotalTokens != 11 {
		t.Fatalf("unexpected weighted usage: %#v", usage)
	}
}

type fakeBedrockClient struct {
	input        *bedrockruntime.ConverseInput
	streamInput  *bedrockruntime.ConverseStreamInput
	streamEvents []types.ConverseStreamOutput
	streamErr    error
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

func (f *fakeBedrockClient) ConverseStream(_ context.Context, input *bedrockruntime.ConverseStreamInput, _ ...func(*bedrockruntime.Options)) (BedrockConverseEventStream, error) {
	f.streamInput = input
	return newFakeBedrockStream(f.streamEvents, f.streamErr), nil
}

type fakeBedrockStream struct {
	events chan types.ConverseStreamOutput
	err    error
}

func newFakeBedrockStream(events []types.ConverseStreamOutput, err error) *fakeBedrockStream {
	ch := make(chan types.ConverseStreamOutput, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return &fakeBedrockStream{events: ch, err: err}
}

func (s *fakeBedrockStream) Events() <-chan types.ConverseStreamOutput {
	return s.events
}

func (s *fakeBedrockStream) Close() error {
	return nil
}

func (s *fakeBedrockStream) Err() error {
	return s.err
}

func TestOIDCLoginCallbackProvisionsUserAndIssuesSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	fake := &fakeOIDCAuthenticator{claims: OIDCClaims{
		Subject: "oidc-subject-1",
		Values: map[string]any{
			"preferred_username": "sso.user@example.com",
			"email":              "sso.user@example.com",
			"name":               "SSO User",
			"department":         "Finance",
			"groups":             []any{"phlox-admins"},
		},
	}}
	handler, err := New(Options{
		Config: config.Config{
			SessionSecret: "test-secret",
			OIDC: config.OIDCConfig{
				Enabled:         true,
				DisplayName:     "Entra ID",
				IssuerURL:       "https://login.example/tenant/v2.0",
				ClientID:        "client-id",
				ClientSecret:    "client-secret",
				Scopes:          []string{"openid", "profile", "email"},
				UsernameClaim:   "preferred_username",
				DepartmentClaim: "department",
				GroupsClaim:     "groups",
				AdminGroups:     []string{"phlox-admins"},
				AutoProvision:   true,
			},
		},
		Store: st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		OIDCAuthenticator: fake,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	configResp := httptest.NewRecorder()
	handler.ServeHTTP(configResp, httptest.NewRequest(http.MethodGet, "http://phlox.example/api/auth/oidc/config", nil))
	if configResp.Code != http.StatusOK {
		t.Fatalf("config status = %d body = %s", configResp.Code, configResp.Body.String())
	}

	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, httptest.NewRequest(http.MethodGet, "http://phlox.example/api/auth/oidc/login?return_to=/admin", nil))
	if loginResp.Code != http.StatusFound {
		t.Fatalf("login status = %d body = %s", loginResp.Code, loginResp.Body.String())
	}
	if fake.state == "" || fake.nonce == "" || fake.redirectURL != "http://phlox.example/api/auth/oidc/callback" {
		t.Fatalf("fake authenticator was not called correctly: %#v", fake)
	}
	cookies := loginResp.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != oidcStateCookieName {
		t.Fatalf("expected state cookie, got %#v", cookies)
	}

	callbackReq := httptest.NewRequest(http.MethodGet, "http://phlox.example/api/auth/oidc/callback?code=auth-code&state="+fake.state, nil)
	callbackReq.AddCookie(cookies[0])
	callbackResp := httptest.NewRecorder()
	handler.ServeHTTP(callbackResp, callbackReq)
	if callbackResp.Code != http.StatusOK {
		t.Fatalf("callback status = %d body = %s", callbackResp.Code, callbackResp.Body.String())
	}
	token := tokenFromOIDCHTML(t, callbackResp.Body.String())
	meResp := jsonRequest(t, handler, http.MethodGet, "/api/auth/me", token, nil)
	if meResp.Code != http.StatusOK {
		t.Fatalf("me status = %d body = %s", meResp.Code, meResp.Body.String())
	}
	var me struct {
		Username     string `json:"username"`
		DisplayName  string `json:"display_name"`
		Email        string `json:"email"`
		Department   string `json:"department"`
		Role         string `json:"role"`
		AuthProvider string `json:"auth_provider"`
	}
	decodeRecorder(t, meResp, &me)
	if me.Username != "sso.user@example.com" || me.DisplayName != "SSO User" || me.Email != "sso.user@example.com" || me.Department != "Finance" || me.Role != "admin" || me.AuthProvider != "oidc" {
		t.Fatalf("unexpected user profile: %#v", me)
	}
	if !strings.Contains(callbackResp.Body.String(), `window.location.replace("/admin")`) {
		t.Fatalf("callback did not preserve return target: %s", callbackResp.Body.String())
	}
}

type fakeOIDCAuthenticator struct {
	state       string
	nonce       string
	redirectURL string
	claims      OIDCClaims
}

func (f *fakeOIDCAuthenticator) AuthCodeURL(_ context.Context, state, nonce, redirectURL string) (string, error) {
	f.state = state
	f.nonce = nonce
	f.redirectURL = redirectURL
	return "https://login.example/authorize", nil
}

func (f *fakeOIDCAuthenticator) Exchange(_ context.Context, code, nonce, redirectURL string) (OIDCClaims, error) {
	if code != "auth-code" {
		return OIDCClaims{}, errors.New("unexpected auth code")
	}
	if nonce != f.nonce {
		return OIDCClaims{}, errors.New("unexpected nonce")
	}
	if redirectURL != f.redirectURL {
		return OIDCClaims{}, errors.New("unexpected redirect url")
	}
	return f.claims, nil
}

func tokenFromOIDCHTML(t *testing.T, body string) string {
	t.Helper()
	marker := "localStorage.setItem('phlox_gw_token', "
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("missing token marker in %s", body)
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, ");")
	if end < 0 {
		t.Fatalf("missing token terminator in %s", body)
	}
	var token string
	if err := json.Unmarshal([]byte(rest[:end]), &token); err != nil {
		t.Fatalf("token JSON: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	return token
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

func TestAdminRequestLogSearchAndCSV(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
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
	if err := st.InsertRequestLog(ctx, store.RequestLogRecord{
		ID:              "reqlog_admin_api",
		RequestID:       "req_search_1",
		UserID:          admin.ID,
		Username:        admin.Username,
		Department:      admin.Department,
		APIKeyID:        "key_search",
		APIKeyPrefix:    "pgw-sk-test",
		APIKeyName:      "Search key",
		ProviderID:      "openai",
		ProviderType:    "openai",
		ModelRoute:      "openai/gpt-4o-mini",
		UpstreamModelID: "gpt-4o-mini",
		Protocol:        "openai",
		Method:          "POST",
		Endpoint:        "/v1/chat/completions",
		StatusCode:      429,
		ErrorText:       "rate limited",
		ClientIP:        "203.0.113.9",
		UserAgent:       "test-agent",
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertRequestLog: %v", err)
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
	token := sessionToken(t, admin)
	resp := jsonRequest(t, handler, http.MethodGet, "/api/admin/request-log?q=rate&status=error&limit=10", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("request log status = %d body = %s", resp.Code, resp.Body.String())
	}
	var result store.RequestLogSearchResult
	decodeRecorder(t, resp, &result)
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].RequestID != "req_search_1" {
		t.Fatalf("unexpected request log result: %#v", result)
	}
	csvResp := jsonRequest(t, handler, http.MethodGet, "/api/admin/request-log/export.csv?q=rate", token, nil)
	if csvResp.Code != http.StatusOK {
		t.Fatalf("request log csv status = %d body = %s", csvResp.Code, csvResp.Body.String())
	}
	if got := csvResp.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("csv content type = %q", got)
	}
	if body := csvResp.Body.String(); !strings.Contains(body, "req_search_1") || strings.Contains(body, "prompt") {
		t.Fatalf("unexpected csv body: %s", body)
	}
}

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

type openAIGuardrailFixture struct {
	Handler http.Handler
	APIKey  string
	Route   string
	Store   *store.Store
	UserID  string
}

func newOpenAIGuardrailFixture(t *testing.T, policy store.GuardrailPolicy, roundTrip roundTripFunc) openAIGuardrailFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user := store.User{ID: "user_guardrail", Username: "guardrail-user", Department: "Security", Role: "user", AuthProvider: "local", IsActive: true}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	plain, prefix, keyHash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_guardrail", UserID: user.ID, Name: "Guardrail key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "guardrail-openai", Name: "Guardrail OpenAI", Type: "openai", BaseURL: "http://guardrail-openai.test/v1", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_guardrail",
		ProviderID:           provider.ID,
		ModelID:              "upstream-guardrail",
		Route:                "guardrail/chat",
		DisplayName:          "Guardrail Chat",
		InputCostPerMillion:  1,
		OutputCostPerMillion: 2,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if _, err := st.UpdateGuardrailPolicy(ctx, policy); err != nil {
		t.Fatalf("UpdateGuardrailPolicy: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		HTTPClient: &http.Client{Transport: roundTrip},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return openAIGuardrailFixture{Handler: handler, APIKey: plain, Route: model.Route, Store: st, UserID: user.ID}
}

func jsonRequest(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return jsonRequestWithHeaders(t, handler, method, path, headers, body)
}

func jsonRequestWithHeaders(t *testing.T, handler http.Handler, method, path string, headers map[string]string, body any) *httptest.ResponseRecorder {
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
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func sessionToken(t *testing.T, user store.User) string {
	t.Helper()
	token, err := auth.SignSession(auth.Claims{
		Subject:  user.ID,
		Username: user.Username,
		Role:     user.Role,
		IssuedAt: time.Now().UTC().Unix(),
		Expires:  time.Now().UTC().Add(time.Hour).Unix(),
	}, "test-secret")
	if err != nil {
		t.Fatalf("SignSession: %v", err)
	}
	return token
}

func decodeRecorder(t *testing.T, resp *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		t.Fatalf("Decode: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
