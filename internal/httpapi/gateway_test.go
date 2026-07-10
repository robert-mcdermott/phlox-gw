package httpapi

import (
	"context"
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
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

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
		if strings.Contains(string(body), "jane@example.com") || !strings.Contains(string(body), "[EMAIL]") {
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
	if strings.Contains(resp.Body.String(), "jane@example.com") || !strings.Contains(resp.Body.String(), "[EMAIL]") {
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
				ServiceVersion: "v0.1.0-test",
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
	var healthBody struct {
		Version string `json:"version"`
	}
	decodeRecorder(t, health, &healthBody)
	if healthBody.Version != "v0.1.0-test" {
		t.Fatalf("health version = %q, want v0.1.0-test", healthBody.Version)
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

func (s *fakeBedrockStream) Events() <-chan types.ConverseStreamOutput {
	return s.events
}

func (s *fakeBedrockStream) Close() error {
	return nil
}

func (s *fakeBedrockStream) Err() error {
	return s.err
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

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestOpenAIChatCompletionsRoutesToAzureOpenAI(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_azure",
		Username:     "azure-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_azure", UserID: user.ID, Name: "Azure key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{
		ID:      "azure-east",
		Name:    "Azure OpenAI East",
		Type:    "azure-openai",
		BaseURL: "https://myres.openai.azure.com",
		APIKey:  "azure-secret",
		Enabled: true,
	}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_azure_gpt",
		ProviderID:           provider.ID,
		ModelID:              "gpt-4o-deploy",
		Route:                "azure-east/gpt-4o",
		DisplayName:          "Azure GPT-4o",
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
		if r.URL.Host != "myres.openai.azure.com" {
			t.Fatalf("unexpected upstream host: %s", r.URL.String())
		}
		if r.URL.Path != "/openai/deployments/gpt-4o-deploy/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("api-version"); got != "2024-10-21" {
			t.Fatalf("api-version = %q", got)
		}
		if got := r.Header.Get("api-key"); got != "azure-secret" {
			t.Fatalf("api-key header = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization header should be empty for Azure, got %q", got)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if req["model"] != "gpt-4o-deploy" {
			t.Fatalf("unexpected upstream model: %#v", req["model"])
		}
		body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
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
		"messages": []map[string]string{{"role": "user", "content": "Hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestAnthropicMessagesRoutesToAzureAnthropic(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_azure_claude",
		Username:     "azure-claude-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_azure_claude", UserID: user.ID, Name: "Azure Claude key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{
		ID:      "azure-claude",
		Name:    "Azure Claude",
		Type:    "azure-anthropic",
		BaseURL: "https://myres.services.ai.azure.com/anthropic",
		APIKey:  "foundry-secret",
		Enabled: true,
	}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_azure_claude",
		ProviderID:           provider.ID,
		ModelID:              "claude-sonnet-deploy",
		Route:                "azure-claude/sonnet",
		DisplayName:          "Azure Claude Sonnet",
		InputCostPerMillion:  3,
		OutputCostPerMillion: 15,
		SupportsStreaming:    true,
		Enabled:              true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	upstreamHits := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		if r.URL.Host != "myres.services.ai.azure.com" || r.URL.Path != "/anthropic/v1/messages" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		if got := r.Header.Get("x-api-key"); got != "foundry-secret" {
			t.Fatalf("x-api-key header = %q", got)
		}
		if got := r.Header.Get("api-key"); got != "foundry-secret" {
			t.Fatalf("api-key header = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != "interleaved-thinking-2025-05-14" {
			t.Fatalf("anthropic-beta should be filtered to Foundry-supported values, got %q", got)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if req["model"] != "claude-sonnet-deploy" {
			t.Fatalf("unexpected upstream model: %#v", req["model"])
		}
		body := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":9,"output_tokens":4}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
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
		"Authorization":  "Bearer " + plain,
		"anthropic-beta": "advisor-tool-2026-03-01,interleaved-thinking-2025-05-14,claude-code-20250219",
	}, map[string]any{
		"model":      model.Route,
		"max_tokens": 32,
		"messages":   []map[string]string{{"role": "user", "content": "Hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 9 || usage.OutputTokens != 4 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestAnthropicMessagesTranslationRetriesReasoningModelParams(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_translate_retry",
		Username:     "translate-retry-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_translate_retry", UserID: user.ID, Name: "Translate retry key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{
		ID:      "azure-reasoning-2",
		Name:    "Azure Reasoning 2",
		Type:    "azure-openai",
		BaseURL: "https://myres.openai.azure.com",
		APIKey:  "azure-secret",
		Enabled: true,
	}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:         "model_gpt55_translate",
		ProviderID: provider.ID,
		ModelID:    "gpt-5.5",
		Route:      "azure/gpt-5.5-translate",
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
		if req["max_completion_tokens"] != float64(32) {
			t.Fatalf("expected max_completion_tokens 32, got %#v", req)
		}
		body := `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
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
	resp := jsonRequest(t, handler, http.MethodPost, "/anthropic/v1/messages", plain, map[string]any{
		"model":      model.Route,
		"max_tokens": 32,
		"messages":   []map[string]string{{"role": "user", "content": "Hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d", upstreamHits)
	}
	if !strings.Contains(resp.Body.String(), `"text":"hi"`) {
		t.Fatalf("unexpected translated body: %s", resp.Body.String())
	}
}

func TestOpenAIChatCompletionsRoutesToGoogleGemini(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_google",
		Username:     "google-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_google", UserID: user.ID, Name: "Google key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{
		ID:      "google",
		Name:    "Google Gemini",
		Type:    "google",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKey:  "gemini-secret",
		Enabled: true,
	}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                "model_gemini_flash",
		ProviderID:        provider.ID,
		ModelID:           "gemini-3.5-flash",
		Route:             "google/gemini-3.5-flash",
		DisplayName:       "Gemini 3.5 Flash",
		SupportsStreaming: true,
		Enabled:           true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	upstreamHits := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamHits++
		if r.URL.Host != "generativelanguage.googleapis.com" || r.URL.Path != "/v1beta/openai/chat/completions" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gemini-secret" {
			t.Fatalf("Authorization header = %q", got)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if req["model"] != "gemini-3.5-flash" {
			t.Fatalf("unexpected upstream model: %#v", req["model"])
		}
		if _, has := req["stream_options"]; has {
			t.Fatalf("stream_options must not be injected for non-streaming requests: %#v", req)
		}
		body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":2,"total_tokens":8}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
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
		"messages": []map[string]string{{"role": "user", "content": "Hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 6 || usage.OutputTokens != 2 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestOpenAIChatCompletionsStreamsGoogleWithUsageOption(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_google_stream",
		Username:     "google-stream-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_google_stream", UserID: user.ID, Name: "Google stream key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{
		ID:      "google-stream",
		Name:    "Google Gemini Stream",
		Type:    "google",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKey:  "gemini-secret",
		Enabled: true,
	}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_gemini_stream",
		ProviderID:           provider.ID,
		ModelID:              "gemini-3.5-flash",
		Route:                "google-stream/gemini-3.5-flash",
		DisplayName:          "Gemini Stream",
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
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		opts, ok := req["stream_options"].(map[string]any)
		if !ok || opts["include_usage"] != true {
			t.Fatalf("stream_options.include_usage not injected: %#v", req)
		}
		streamBody := strings.Join([]string{
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}` + "\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}` + "\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}` + "\n\n",
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
		"messages": []map[string]string{{"role": "user", "content": "Hello"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits)
	}
	if body := resp.Body.String(); !strings.Contains(body, `"content":"hel"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("unexpected stream body: %s", body)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 9 || usage.OutputTokens != 4 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestOpenAIChatCompletionsTranslatesAnthropicRoute(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_openai_anthropic",
		Username:     "openai-anthropic-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_openai_anthropic", UserID: user.ID, Name: "OpenAI Anthropic key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "claude-direct", Name: "Claude Direct", Type: "anthropic", BaseURL: "http://claude.test", APIKey: "sk-ant-test", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_openai_anthropic",
		ProviderID:           provider.ID,
		ModelID:              "claude-sonnet-latest",
		Route:                "anthropic/claude-sonnet",
		DisplayName:          "Claude Sonnet",
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
		if r.URL.Host != "claude.test" || r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Fatalf("missing x-api-key header")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Fatalf("missing anthropic-version header")
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if req["model"] != "claude-sonnet-latest" || req["max_tokens"] != float64(64) {
			t.Fatalf("unexpected upstream request: %#v", req)
		}
		if req["system"] != "be brief" {
			t.Fatalf("unexpected upstream system prompt: %#v", req["system"])
		}
		messages, _ := req["messages"].([]any)
		if len(messages) != 1 {
			t.Fatalf("upstream messages = %#v", req["messages"])
		}
		msg, _ := messages[0].(map[string]any)
		blocks, _ := msg["content"].([]any)
		block, _ := blocks[0].(map[string]any)
		if msg["role"] != "user" || block["type"] != "text" || block["text"] != "ping" {
			t.Fatalf("unexpected upstream message: %#v", msg)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"msg_translate",
				"type":"message",
				"role":"assistant",
				"model":"claude-sonnet-latest",
				"content":[{"type":"text","text":"pong"}],
				"stop_reason":"end_turn",
				"usage":{"input_tokens":4,"output_tokens":2}
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
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":      model.Route,
		"max_tokens": 64,
		"messages": []map[string]string{
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "ping"},
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits)
	}
	var body map[string]any
	decodeRecorder(t, resp, &body)
	if body["object"] != "chat.completion" || body["model"] != "claude-sonnet-latest" {
		t.Fatalf("unexpected openai response: %#v", body)
	}
	choices, _ := body["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("unexpected choices: %#v", body["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["role"] != "assistant" || message["content"] != "pong" || choice["finish_reason"] != "stop" {
		t.Fatalf("unexpected choice: %#v", choice)
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 4 || usage.OutputTokens != 2 || usage.TotalTokens != 6 || usage.CostUSD != 0.000008 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}

func TestOpenAIChatCompletionsStreamsAzureAnthropicRoute(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_openai_anthropic_stream",
		Username:     "openai-anthropic-stream-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_openai_anthropic_stream", UserID: user.ID, Name: "OpenAI Anthropic stream key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "azure-claude", Name: "Azure Claude", Type: "azure-anthropic", BaseURL: "http://foundry.test", APIKey: "azure-key", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_openai_anthropic_stream",
		ProviderID:           provider.ID,
		ModelID:              "claude-sonnet-azure",
		Route:                "azure/claude-sonnet",
		DisplayName:          "Azure Claude Sonnet",
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
		if r.URL.Host != "foundry.test" || r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		if r.Header.Get("x-api-key") != "azure-key" || r.Header.Get("api-key") != "azure-key" {
			t.Fatalf("missing azure-anthropic auth headers")
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if req["model"] != "claude-sonnet-azure" || req["stream"] != true {
			t.Fatalf("unexpected upstream request: %#v", req)
		}
		tools, _ := req["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("expected one translated tool, got %#v", req["tools"])
		}
		tool, _ := tools[0].(map[string]any)
		if tool["name"] != "Read" || tool["input_schema"] == nil {
			t.Fatalf("unexpected translated tool: %#v", tool)
		}
		streamBody := strings.Join([]string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","model":"claude-sonnet-azure","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n",
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}` + "\n\n",
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n",
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_read","name":"Read","input":{}}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"README.md\"}"}}` + "\n\n",
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n",
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":6}}` + "\n\n",
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
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
		"model":          model.Route,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"max_tokens":     32,
		"messages":       []map[string]string{{"role": "user", "content": "read the readme"}},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "Read",
				"description": "Read a file",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
				},
			},
		}},
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
		`"role":"assistant"`,
		`"content":"hello "`,
		`"id":"toolu_read"`,
		`"name":"Read"`,
		`"arguments":"{\"file_path\":\"README.md\"}"`,
		`"finish_reason":"tool_calls"`,
		`"prompt_tokens":10`,
		`"completion_tokens":6`,
		"data: [DONE]",
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

func TestOpenAIChatCompletionsRetriesDeprecatedSamplingParamsOnAnthropicRoute(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_openai_anthropic_retry",
		Username:     "openai-anthropic-retry-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_openai_anthropic_retry", UserID: user.ID, Name: "OpenAI Anthropic retry key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "azure-claude-retry", Name: "Azure Claude Retry", Type: "azure-anthropic", BaseURL: "http://foundry-retry.test", APIKey: "azure-key", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_openai_anthropic_retry",
		ProviderID:           provider.ID,
		ModelID:              "claude-sonnet-5",
		Route:                "azure/claude-sonnet-5",
		DisplayName:          "Azure Claude Sonnet 5",
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
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if upstreamHits == 1 {
			if _, ok := req["temperature"]; !ok {
				t.Fatalf("first attempt should include temperature: %#v", req)
			}
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader("{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"`temperature` is deprecated for this model.\"}}")),
				Request:    r,
			}, nil
		}
		if _, ok := req["temperature"]; ok {
			t.Fatalf("retry should not include temperature: %#v", req)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"msg_retry",
				"type":"message",
				"role":"assistant",
				"model":"claude-sonnet-5",
				"content":[{"type":"text","text":"pong"}],
				"stop_reason":"end_turn",
				"usage":{"input_tokens":4,"output_tokens":2}
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
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":       model.Route,
		"max_tokens":  64,
		"temperature": 0.7,
		"messages":    []map[string]string{{"role": "user", "content": "ping"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 2 {
		t.Fatalf("upstream hits = %d, want 2 (initial + retry)", upstreamHits)
	}
	var body map[string]any
	decodeRecorder(t, resp, &body)
	choices, _ := body["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "pong" {
		t.Fatalf("unexpected response after retry: %#v", body)
	}
}

func TestOpenAIChatCompletionsRetriesReasoningModelParamsOnPassthrough(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_openai_passthrough_retry",
		Username:     "openai-passthrough-retry-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_openai_passthrough_retry", UserID: user.ID, Name: "Passthrough retry key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "azure-gpt", Name: "Azure GPT", Type: "azure-openai", BaseURL: "http://azure-gpt.test", APIKey: "azure-key", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_openai_passthrough_retry",
		ProviderID:           provider.ID,
		ModelID:              "gpt-5.5",
		Route:                "azure/gpt-5.5",
		DisplayName:          "Azure GPT 5.5",
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
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if upstreamHits == 1 {
			if _, ok := req["max_tokens"]; !ok {
				t.Fatalf("first attempt should include max_tokens: %#v", req)
			}
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`)),
				Request:    r,
			}, nil
		}
		if _, ok := req["max_tokens"]; ok {
			t.Fatalf("retry should not include max_tokens: %#v", req)
		}
		if req["max_completion_tokens"] != float64(64) {
			t.Fatalf("retry should carry max_completion_tokens: %#v", req)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl_retry",
				"object":"chat.completion",
				"model":"gpt-5.5",
				"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
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
	resp := jsonRequest(t, handler, http.MethodPost, "/v1/chat/completions", plain, map[string]any{
		"model":      model.Route,
		"max_tokens": 64,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 2 {
		t.Fatalf("upstream hits = %d, want 2 (initial + retry)", upstreamHits)
	}
	var body map[string]any
	decodeRecorder(t, resp, &body)
	choices, _ := body["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "pong" {
		t.Fatalf("unexpected response after retry: %#v", body)
	}
}

func TestOpenAIChatCompletionsStreamRetriesReasoningModelParamsOnPassthrough(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_openai_passthrough_stream_retry",
		Username:     "openai-passthrough-stream-retry-user",
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
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_openai_passthrough_stream_retry", UserID: user.ID, Name: "Passthrough stream retry key", Prefix: prefix, KeyHash: keyHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	provider := store.Provider{ID: "azure-gpt-stream", Name: "Azure GPT Stream", Type: "azure-openai", BaseURL: "http://azure-gpt-stream.test", APIKey: "azure-key", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:                   "model_openai_passthrough_stream_retry",
		ProviderID:           provider.ID,
		ModelID:              "gpt-5.5",
		Route:                "azure/gpt-5.5-stream",
		DisplayName:          "Azure GPT 5.5 Stream",
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
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		if upstreamHits == 1 {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`)),
				Request:    r,
			}, nil
		}
		if _, ok := req["max_tokens"]; ok {
			t.Fatalf("retry should not include max_tokens: %#v", req)
		}
		if req["stream"] != true {
			t.Fatalf("retry should preserve stream flag: %#v", req)
		}
		streamBody := strings.Join([]string{
			`data: {"id":"chatcmpl_stream_retry","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl_stream_retry","model":"gpt-5.5","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl_stream_retry","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n",
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
		"model":      model.Route,
		"stream":     true,
		"max_tokens": 64,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 2 {
		t.Fatalf("upstream hits = %d, want 2 (initial + retry)", upstreamHits)
	}
	body := resp.Body.String()
	for _, want := range []string{`"content":"pong"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
	usage, err := st.UsageForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("UsageForUser: %v", err)
	}
	if usage.Requests != 1 || usage.InputTokens != 4 || usage.OutputTokens != 2 {
		t.Fatalf("unexpected stored usage: %#v", usage)
	}
}
