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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestAdminPlaygroundChat(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	admin := store.User{ID: "user_admin", Username: "admin", Role: "admin", PasswordHash: "unused", AuthProvider: "local", IsActive: true}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	provider := store.Provider{ID: "pg-openai", Name: "Playground OpenAI", Type: "openai", BaseURL: "http://pg-openai.test/v1", Enabled: true}
	if err := st.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	model := store.Model{
		ID:         "model_pg_openai",
		ProviderID: provider.ID,
		ModelID:    "test-model",
		Route:      "pg-openai/test-model",
		Enabled:    true,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	bedrockProvider := store.Provider{ID: "pg-bedrock", Name: "Playground Bedrock", Type: "bedrock", AWSRegion: "us-west-2", AWSAuthMethod: "chain", Enabled: true}
	if err := st.CreateProvider(ctx, bedrockProvider); err != nil {
		t.Fatalf("CreateProvider bedrock: %v", err)
	}
	bedrockModel := store.Model{
		ID:         "model_pg_bedrock",
		ProviderID: bedrockProvider.ID,
		ModelID:    "us.anthropic.test-model",
		Route:      "pg-bedrock/test-model",
		Enabled:    true,
	}
	if err := st.CreateModel(ctx, bedrockModel); err != nil {
		t.Fatalf("CreateModel bedrock: %v", err)
	}

	upstreamStatus := http.StatusOK
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "pg-openai.test" || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream target: %s", r.URL.String())
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("upstream decode: %v", err)
		}
		messages, _ := req["messages"].([]any)
		if len(messages) != 4 {
			t.Fatalf("upstream messages = %#v", req["messages"])
		}
		first, _ := messages[0].(map[string]any)
		if first["role"] != "system" || first["content"] != "Answer tersely." {
			t.Fatalf("system message not forwarded: %#v", first)
		}
		if upstreamStatus != http.StatusOK {
			return &http.Response{
				StatusCode: upstreamStatus,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream exploded"}}`)),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}
			}`)),
			Request: r,
		}, nil
	})
	fakeBedrock := &fakeBedrockClient{}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		HTTPClient: &http.Client{Transport: transport},
		BedrockClientFactory: func(ctx context.Context, p store.Provider) (BedrockConverseClient, error) {
			return fakeBedrock, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	token := sessionToken(t, admin)

	body := map[string]any{
		"route":  model.Route,
		"system": "Answer tersely.",
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
			{"role": "assistant", "content": "pong"},
			{"role": "user", "content": "ping again"},
		},
	}
	resp := jsonRequest(t, handler, http.MethodPost, "/api/admin/playground/chat", token, body)
	if resp.Code != http.StatusOK {
		t.Fatalf("playground status = %d body = %s", resp.Code, resp.Body.String())
	}
	var result playgroundChatResult
	decodeRecorder(t, resp, &result)
	if !result.OK || result.Content != "pong" || result.ProviderID != provider.ID || result.StatusCode != http.StatusOK {
		t.Fatalf("unexpected playground result: %#v", result)
	}
	if result.InputTokens != 12 || result.OutputTokens != 3 || result.TotalTokens != 15 {
		t.Fatalf("unexpected playground usage: %#v", result)
	}

	bedrockResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/playground/chat", token, map[string]any{
		"route":    bedrockModel.Route,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	})
	if bedrockResp.Code != http.StatusOK {
		t.Fatalf("bedrock playground status = %d body = %s", bedrockResp.Code, bedrockResp.Body.String())
	}
	var bedrockResult playgroundChatResult
	decodeRecorder(t, bedrockResp, &bedrockResult)
	if !bedrockResult.OK || bedrockResult.Content != "bedrock says hi" || bedrockResult.TotalTokens != 15 {
		t.Fatalf("unexpected bedrock playground result: %#v", bedrockResult)
	}
	if fakeBedrock.input == nil || aws.ToString(fakeBedrock.input.ModelId) != "us.anthropic.test-model" {
		t.Fatalf("unexpected bedrock converse input: %#v", fakeBedrock.input)
	}

	// Upstream failures come back as ok=false with the error text, not an HTTP error.
	upstreamStatus = http.StatusInternalServerError
	failResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/playground/chat", token, body)
	if failResp.Code != http.StatusOK {
		t.Fatalf("failed playground status = %d body = %s", failResp.Code, failResp.Body.String())
	}
	var failResult playgroundChatResult
	decodeRecorder(t, failResp, &failResult)
	if failResult.OK || failResult.StatusCode != http.StatusInternalServerError || !strings.Contains(failResult.Error, "upstream exploded") {
		t.Fatalf("unexpected failed playground result: %#v", failResult)
	}

	badRole := jsonRequest(t, handler, http.MethodPost, "/api/admin/playground/chat", token, map[string]any{
		"route":    model.Route,
		"messages": []map[string]string{{"role": "system", "content": "sneaky"}},
	})
	if badRole.Code != http.StatusBadRequest {
		t.Fatalf("bad role status = %d body = %s", badRole.Code, badRole.Body.String())
	}
	missingRoute := jsonRequest(t, handler, http.MethodPost, "/api/admin/playground/chat", token, map[string]any{
		"route":    "nope/missing",
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	})
	if missingRoute.Code != http.StatusNotFound {
		t.Fatalf("missing route status = %d body = %s", missingRoute.Code, missingRoute.Body.String())
	}

	// Playground traffic must not appear in the usage ledger.
	usage, err := st.UsageAll(ctx)
	if err != nil {
		t.Fatalf("UsageAll: %v", err)
	}
	if usage.Requests != 0 {
		t.Fatalf("playground traffic should not be recorded in usage: %#v", usage)
	}
}
