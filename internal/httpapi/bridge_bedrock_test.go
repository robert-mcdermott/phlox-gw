package httpapi

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

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

func TestAnthropicToBedrockToolDescriptionFallback(t *testing.T) {
	openAIRaw, err := anthropicRequestToOpenAI(map[string]any{
		"model":      "bedrock/claude",
		"max_tokens": float64(64),
		"messages": []any{
			map[string]any{"role": "user", "content": "Search the web"},
		},
		"tools": []any{
			map[string]any{
				"name":        "WebSearch",
				"description": "",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("anthropicRequestToOpenAI: %v", err)
	}
	openAITools, ok := openAIRaw["tools"].([]any)
	if !ok || len(openAITools) != 1 {
		t.Fatalf("unexpected OpenAI tools: %#v", openAIRaw["tools"])
	}
	openAITool, ok := openAITools[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected OpenAI tool entry: %#v", openAITools[0])
	}
	function, ok := openAITool["function"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected function entry: %#v", openAITool["function"])
	}
	if got := function["description"]; got != "Tool WebSearch." {
		t.Fatalf("OpenAI tool description = %#v, want fallback", got)
	}

	input, err := bedrockConverseInput("anthropic.claude-3-5-sonnet-20240620-v1:0", openAIRaw)
	if err != nil {
		t.Fatalf("bedrockConverseInput: %v", err)
	}
	if input.ToolConfig == nil || len(input.ToolConfig.Tools) != 1 {
		t.Fatalf("tool config not mapped: %#v", input.ToolConfig)
	}
	spec, ok := input.ToolConfig.Tools[0].(*types.ToolMemberToolSpec)
	if !ok {
		t.Fatalf("unexpected Bedrock tool type: %#v", input.ToolConfig.Tools[0])
	}
	if got := aws.ToString(spec.Value.Description); got != "Tool WebSearch." {
		t.Fatalf("Bedrock tool description = %q, want fallback", got)
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

func TestBedrockConverseInputGroupsToolResultsAndKeepsEmptyResults(t *testing.T) {
	input, err := bedrockConverseInput("anthropic.claude-3-5-sonnet-20240620-v1:0", map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Use tools"},
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{
					map[string]any{
						"id":   "tooluse_empty",
						"type": "function",
						"function": map[string]any{
							"name":      "Empty",
							"arguments": "{}",
						},
					},
					map[string]any{
						"id":   "tooluse_read",
						"type": "function",
						"function": map[string]any{
							"name":      "Read",
							"arguments": "{\"file_path\":\"README.md\"}",
						},
					},
				},
			},
			map[string]any{"role": "tool", "tool_call_id": "tooluse_empty", "content": "", "is_error": true},
			map[string]any{"role": "tool", "tool_call_id": "tooluse_read", "content": "contents"},
		},
	})
	if err != nil {
		t.Fatalf("bedrockConverseInput: %v", err)
	}
	if len(input.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3: %#v", len(input.Messages), input.Messages)
	}
	resultMessage := input.Messages[2]
	if resultMessage.Role != types.ConversationRoleUser || len(resultMessage.Content) != 2 {
		t.Fatalf("unexpected tool result message: %#v", resultMessage)
	}
	first := resultMessage.Content[0].(*types.ContentBlockMemberToolResult).Value
	firstText := first.Content[0].(*types.ToolResultContentBlockMemberText).Value
	if aws.ToString(first.ToolUseId) != "tooluse_empty" || firstText != "" || first.Status != types.ToolResultStatusError {
		t.Fatalf("unexpected first tool result: %#v", first)
	}
	second := resultMessage.Content[1].(*types.ContentBlockMemberToolResult).Value
	secondText := second.Content[0].(*types.ToolResultContentBlockMemberText).Value
	if aws.ToString(second.ToolUseId) != "tooluse_read" || secondText != "contents" {
		t.Fatalf("unexpected second tool result: %#v", second)
	}
}
