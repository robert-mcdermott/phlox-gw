package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestOpenAIRequestToAnthropicMapsMessagesAndTools(t *testing.T) {
	out, err := openAIRequestToAnthropic(map[string]any{
		"model": "claude-sonnet",
		"stop":  "END",
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "read the readme"},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
				map[string]any{
					"id":   "call_read",
					"type": "function",
					"function": map[string]any{
						"name":      "Read",
						"arguments": `{"file_path":"README.md"}`,
					},
				},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_read", "content": "# Phlox"},
			map[string]any{"role": "tool", "tool_call_id": "call_read_2", "content": "second result"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "Read",
					"description": "Read a file",
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
					},
				},
			},
		},
		"tool_choice": "required",
	})
	if err != nil {
		t.Fatalf("openAIRequestToAnthropic: %v", err)
	}
	if out["system"] != "be brief" {
		t.Fatalf("system = %#v", out["system"])
	}
	if out["max_tokens"] != defaultOpenAIAnthropicMaxTokens {
		t.Fatalf("max_tokens = %#v, want default %d", out["max_tokens"], defaultOpenAIAnthropicMaxTokens)
	}
	stops, _ := out["stop_sequences"].([]string)
	if len(stops) != 1 || stops[0] != "END" {
		t.Fatalf("stop_sequences = %#v", out["stop_sequences"])
	}
	messages, _ := out["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v, want user/assistant/user turns", out["messages"])
	}
	assistant, _ := messages[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("unexpected second turn: %#v", assistant)
	}
	assistantBlocks, _ := assistant["content"].([]map[string]any)
	if len(assistantBlocks) != 1 || assistantBlocks[0]["type"] != "tool_use" || assistantBlocks[0]["name"] != "Read" {
		t.Fatalf("unexpected assistant blocks: %#v", assistant["content"])
	}
	input, _ := assistantBlocks[0]["input"].(map[string]any)
	if input["file_path"] != "README.md" {
		t.Fatalf("unexpected tool_use input: %#v", input)
	}
	// Both tool-role results must merge into one trailing user turn.
	results, _ := messages[2].(map[string]any)
	if results["role"] != "user" {
		t.Fatalf("unexpected third turn: %#v", results)
	}
	resultBlocks, _ := results["content"].([]map[string]any)
	if len(resultBlocks) != 2 || resultBlocks[0]["type"] != "tool_result" || resultBlocks[0]["tool_use_id"] != "call_read" || resultBlocks[0]["content"] != "# Phlox" {
		t.Fatalf("unexpected tool_result blocks: %#v", results["content"])
	}
	tools, _ := out["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", out["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "Read" || tool["description"] != "Read a file" || tool["input_schema"] == nil {
		t.Fatalf("unexpected translated tool: %#v", tool)
	}
	choice, _ := out["tool_choice"].(map[string]any)
	if choice["type"] != "any" {
		t.Fatalf("tool_choice = %#v, want type any", out["tool_choice"])
	}
}

func TestOpenAIRequestToAnthropicMapsImageParts(t *testing.T) {
	out, err := openAIRequestToAnthropic(map[string]any{
		"model":      "claude-sonnet",
		"max_tokens": 128,
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "what is this?"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aGVsbG8="}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("openAIRequestToAnthropic: %v", err)
	}
	if out["max_tokens"] != 128 {
		t.Fatalf("max_tokens = %#v", out["max_tokens"])
	}
	messages, _ := out["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", out["messages"])
	}
	blocks, _ := messages[0].(map[string]any)["content"].([]map[string]any)
	if len(blocks) != 2 || blocks[0]["type"] != "text" || blocks[1]["type"] != "image" {
		t.Fatalf("unexpected content blocks: %#v", blocks)
	}
	source, _ := blocks[1]["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "aGVsbG8=" {
		t.Fatalf("unexpected image source: %#v", source)
	}
}

func TestOpenAIRequestToAnthropicRejectsEmptyMessages(t *testing.T) {
	if _, err := openAIRequestToAnthropic(map[string]any{"model": "claude", "messages": []any{}}); err == nil {
		t.Fatal("expected error for empty messages")
	}
	if _, err := openAIRequestToAnthropic(map[string]any{"model": "claude", "messages": []any{
		map[string]any{"role": "system", "content": "only system"},
	}}); err == nil {
		t.Fatal("expected error when only system messages are present")
	}
}

func TestOpenAIResponseFromAnthropicMapsToolUse(t *testing.T) {
	body, err := openAIResponseFromAnthropic(store.RoutedModel{Model: store.Model{Route: "azure/claude"}}, []byte(`{
		"id":"msg_tool",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet",
		"content":[
			{"type":"text","text":"checking"},
			{"type":"tool_use","id":"toolu_read","name":"Read","input":{"file_path":"README.md"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":4}
	}`))
	if err != nil {
		t.Fatalf("openAIResponseFromAnthropic: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["object"] != "chat.completion" || resp["model"] != "claude-sonnet" {
		t.Fatalf("unexpected response envelope: %#v", resp)
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %#v", resp["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %#v, want tool_calls", choice["finish_reason"])
	}
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "checking" {
		t.Fatalf("content = %#v", message["content"])
	}
	calls, _ := message["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", message["tool_calls"])
	}
	call, _ := calls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if call["id"] != "toolu_read" || function["name"] != "Read" || function["arguments"] != `{"file_path":"README.md"}` {
		t.Fatalf("unexpected tool call: %#v", call)
	}
	usage, _ := resp["usage"].(map[string]any)
	if intValue(usage["prompt_tokens"]) != 10 || intValue(usage["completion_tokens"]) != 4 || intValue(usage["total_tokens"]) != 14 {
		t.Fatalf("unexpected usage: %#v", resp["usage"])
	}
}

func TestOpenAIResponseFromAnthropicMapsStopReasons(t *testing.T) {
	for reason, want := range map[string]string{
		"end_turn":      "stop",
		"stop_sequence": "stop",
		"max_tokens":    "length",
		"refusal":       "content_filter",
	} {
		body, err := openAIResponseFromAnthropic(store.RoutedModel{Model: store.Model{Route: "azure/claude"}}, []byte(`{
			"id":"msg_stop","type":"message","role":"assistant","model":"claude-sonnet",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"`+reason+`",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
		if err != nil {
			t.Fatalf("openAIResponseFromAnthropic(%s): %v", reason, err)
		}
		var resp map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		choices, _ := resp["choices"].([]any)
		choice, _ := choices[0].(map[string]any)
		if choice["finish_reason"] != want {
			t.Fatalf("finish_reason for %s = %#v, want %s", reason, choice["finish_reason"], want)
		}
	}
}

func TestAnthropicPayloadForUnsupportedParams(t *testing.T) {
	payload := map[string]any{
		"model":       "claude-sonnet-5",
		"max_tokens":  64,
		"temperature": 0.7,
		"top_p":       0.9,
	}
	adjusted, ok := anthropicPayloadForUnsupportedParams(payload, 400, "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"`temperature` is deprecated for this model.\"}}")
	if !ok {
		t.Fatal("expected adjustment for rejected temperature")
	}
	if _, present := adjusted["temperature"]; present {
		t.Fatalf("temperature not removed: %#v", adjusted)
	}
	if adjusted["top_p"] != 0.9 {
		t.Fatalf("top_p should be untouched when not named: %#v", adjusted)
	}
	if payload["temperature"] != 0.7 {
		t.Fatalf("original payload mutated: %#v", payload)
	}
	if _, ok := anthropicPayloadForUnsupportedParams(payload, 400, "unrelated error"); ok {
		t.Fatal("expected no adjustment when no parameter is named")
	}
	if _, ok := anthropicPayloadForUnsupportedParams(payload, 500, "`temperature` is deprecated"); ok {
		t.Fatal("expected no adjustment for non-400 status")
	}
	if _, ok := anthropicPayloadForUnsupportedParams(map[string]any{"model": "claude"}, 400, "`temperature` is deprecated"); ok {
		t.Fatal("expected no adjustment when payload lacks the parameter")
	}
}
