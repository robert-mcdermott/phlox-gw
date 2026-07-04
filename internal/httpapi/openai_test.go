package httpapi

import (
	"net/http"
	"testing"
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

func TestOpenAIPayloadForUnsupportedParams(t *testing.T) {
	payload := map[string]any{"model": "gpt-5.5", "max_tokens": 8, "temperature": 0}
	maxTokensErr := `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`
	adjusted, ok := openAIPayloadForUnsupportedParams(payload, http.StatusBadRequest, maxTokensErr)
	if !ok {
		t.Fatal("expected adjustment for max_tokens rejection")
	}
	if _, has := adjusted["max_tokens"]; has {
		t.Fatalf("max_tokens should be removed: %#v", adjusted)
	}
	if adjusted["max_completion_tokens"] != 8 {
		t.Fatalf("max_completion_tokens = %#v", adjusted["max_completion_tokens"])
	}
	if payload["max_tokens"] != 8 {
		t.Fatal("original payload must not be mutated")
	}

	temperatureErr := `{"error":{"message":"Unsupported value: 'temperature' does not support 0 with this model. Only the default (1) value is supported.","type":"invalid_request_error","param":"temperature","code":"unsupported_value"}}`
	adjusted, ok = openAIPayloadForUnsupportedParams(adjusted, http.StatusBadRequest, temperatureErr)
	if !ok {
		t.Fatal("expected adjustment for temperature rejection")
	}
	if _, has := adjusted["temperature"]; has {
		t.Fatalf("temperature should be removed: %#v", adjusted)
	}

	if _, ok := openAIPayloadForUnsupportedParams(payload, http.StatusInternalServerError, maxTokensErr); ok {
		t.Fatal("non-400 status must not trigger adjustment")
	}
	if _, ok := openAIPayloadForUnsupportedParams(map[string]any{"model": "m"}, http.StatusBadRequest, maxTokensErr); ok {
		t.Fatal("payload without offending params must not trigger adjustment")
	}
	if _, ok := openAIPayloadForUnsupportedParams(payload, http.StatusBadRequest, `{"error":{"message":"invalid role"}}`); ok {
		t.Fatal("unrelated 400 must not trigger adjustment")
	}
}
