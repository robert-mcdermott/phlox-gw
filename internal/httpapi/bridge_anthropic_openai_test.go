package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestAnthropicResponseFromOpenAIMapsToolCalls(t *testing.T) {
	body, err := anthropicResponseFromOpenAI(store.RoutedModel{Model: store.Model{Route: "bedrock/claude"}}, []byte(`{
		"id":"chatcmpl_tool",
		"model":"bedrock/claude",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":null,
				"tool_calls":[{
					"id":"call_read",
					"type":"function",
					"function":{
						"name":"Read",
						"arguments":"{\"file_path\":\"README.md\"}"
					}
				}]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}
	}`))
	if err != nil {
		t.Fatalf("anthropicResponseFromOpenAI: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %#v, want tool_use", resp["stop_reason"])
	}
	content, _ := resp["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one tool_use block", resp["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "tool_use" || block["id"] != "call_read" || block["name"] != "Read" {
		t.Fatalf("unexpected tool_use block: %#v", block)
	}
	input, _ := block["input"].(map[string]any)
	if input["file_path"] != "README.md" {
		t.Fatalf("unexpected tool input: %#v", input)
	}
}
