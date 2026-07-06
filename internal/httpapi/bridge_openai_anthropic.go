package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

// defaultOpenAIAnthropicMaxTokens is used when an OpenAI-protocol request
// omits max_tokens/max_completion_tokens; the Anthropic Messages API requires
// max_tokens on every request.
const defaultOpenAIAnthropicMaxTokens = 4096

func (s *Server) callOpenAIViaAnthropicNonStreaming(parent context.Context, route store.RoutedModel, raw map[string]any, timeout time.Duration) upstreamResult {
	anthropicRaw, err := openAIRequestToAnthropic(raw)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusBadRequest, ErrorText: err.Error()}
	}
	result := s.callAnthropicNonStreaming(parent, route, anthropicRaw, http.Header{}, timeout)
	// OpenAI clients often hardcode sampling parameters that newer Claude
	// models reject; retry with the offending parameter removed.
	for attempt := 0; attempt < 2; attempt++ {
		adjusted, ok := anthropicPayloadForUnsupportedParams(anthropicRaw, result.Status, string(result.Body))
		if !ok {
			break
		}
		anthropicRaw = adjusted
		result = s.callAnthropicNonStreaming(parent, route, anthropicRaw, http.Header{}, timeout)
	}
	if result.Status < 200 || result.Status >= 300 {
		return upstreamResult{
			Route:     route,
			Protocol:  "anthropic",
			Status:    result.Status,
			ErrorText: providerErrorText(result.Body, result.ErrorText),
			LatencyMS: result.LatencyMS,
		}
	}
	body, err := openAIResponseFromAnthropic(route, result.Body)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusBadGateway, ErrorText: "could not translate Anthropic response: " + err.Error(), LatencyMS: result.LatencyMS}
	}
	return upstreamResult{
		Route:     route,
		Protocol:  "anthropic",
		Status:    result.Status,
		Headers:   http.Header{"Content-Type": []string{"application/json"}},
		Body:      body,
		LatencyMS: result.LatencyMS,
	}
}

func openAIRequestToAnthropic(raw map[string]any) (map[string]any, error) {
	out := map[string]any{
		"model":  raw["model"],
		"stream": false,
	}
	maxTokens := intValue(firstPresent(raw, "max_completion_tokens", "max_tokens"))
	if maxTokens <= 0 {
		maxTokens = defaultOpenAIAnthropicMaxTokens
	}
	out["max_tokens"] = maxTokens
	for _, key := range []string{"temperature", "top_p"} {
		if v, ok := raw[key]; ok {
			out[key] = v
		}
	}
	if stops := openAIStopToAnthropic(raw["stop"]); len(stops) > 0 {
		out["stop_sequences"] = stops
	}
	system, messages, err := openAIMessagesToAnthropic(raw["messages"])
	if err != nil {
		return nil, err
	}
	if system != "" {
		out["system"] = system
	}
	out["messages"] = messages
	if tools, ok := openAIToolsToAnthropic(raw["tools"]); ok {
		out["tools"] = tools
	}
	if choice, ok := openAIToolChoiceToAnthropic(raw["tool_choice"]); ok {
		out["tool_choice"] = choice
	}
	return out, nil
}

func openAIStopToAnthropic(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// openAIMessagesToAnthropic splits an OpenAI messages array into the
// Anthropic system prompt and messages array. Consecutive turns that convert
// to the same Anthropic role are merged into one message, since a run of
// OpenAI tool-role results must land in a single user turn.
func openAIMessagesToAnthropic(value any) (string, []any, error) {
	rawMessages, ok := value.([]any)
	if !ok || len(rawMessages) == 0 {
		return "", nil, errors.New("messages must be a non-empty array")
	}
	systemParts := make([]string, 0, 1)
	messages := make([]any, 0, len(rawMessages))
	appendBlocks := func(role string, blocks []map[string]any) {
		if len(blocks) == 0 {
			return
		}
		if len(messages) > 0 {
			last, _ := messages[len(messages)-1].(map[string]any)
			if lastRole, _ := last["role"].(string); lastRole == role {
				existing, _ := last["content"].([]map[string]any)
				last["content"] = append(existing, blocks...)
				return
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}
	for _, item := range rawMessages {
		msg, ok := item.(map[string]any)
		if !ok {
			return "", nil, errors.New("messages entries must be objects")
		}
		role, _ := msg["role"].(string)
		switch role {
		case "system", "developer":
			if text := openAIContentText(msg["content"]); text != "" {
				systemParts = append(systemParts, text)
			}
		case "assistant":
			blocks, err := openAIAssistantMessageToAnthropic(msg)
			if err != nil {
				return "", nil, err
			}
			appendBlocks("assistant", blocks)
		case "tool":
			appendBlocks("user", []map[string]any{openAIToolMessageToAnthropic(msg)})
		default:
			blocks, err := openAIUserMessageToAnthropic(msg)
			if err != nil {
				return "", nil, err
			}
			appendBlocks("user", blocks)
		}
	}
	if len(messages) == 0 {
		return "", nil, errors.New("messages must include at least one user or assistant message")
	}
	return strings.Join(systemParts, "\n\n"), messages, nil
}

func openAIContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, _ := block["text"].(string); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func openAIUserMessageToAnthropic(msg map[string]any) ([]map[string]any, error) {
	switch content := msg["content"].(type) {
	case string:
		if strings.TrimSpace(content) == "" {
			return nil, nil
		}
		return []map[string]any{{"type": "text", "text": content}}, nil
	case []any:
		blocks := make([]map[string]any, 0, len(content))
		for _, item := range content {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text":
				if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": text})
				}
			case "image_url":
				block, err := openAIImageURLToAnthropic(part["image_url"])
				if err != nil {
					return nil, err
				}
				if block != nil {
					blocks = append(blocks, block)
				}
			}
		}
		return blocks, nil
	default:
		return nil, nil
	}
}

func openAIImageURLToAnthropic(raw any) (map[string]any, error) {
	imageURL, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	url, _ := imageURL["url"].(string)
	if strings.TrimSpace(url) == "" {
		return nil, nil
	}
	if !strings.HasPrefix(url, "data:") {
		return map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "url", "url": url},
		}, nil
	}
	meta, data, found := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	mediaType, ok := strings.CutSuffix(meta, ";base64")
	if !found || !ok || mediaType == "" || data == "" {
		return nil, errors.New("image_url data URLs must use the data:<media-type>;base64,<data> form")
	}
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": mediaType,
			"data":       data,
		},
	}, nil
}

func openAIAssistantMessageToAnthropic(msg map[string]any) ([]map[string]any, error) {
	blocks := make([]map[string]any, 0, 2)
	if text := openAIContentText(msg["content"]); strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	rawCalls, _ := msg["tool_calls"].([]any)
	for _, item := range rawCalls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		function, _ := call["function"].(map[string]any)
		name, _ := function["name"].(string)
		if strings.TrimSpace(name) == "" {
			name = "tool"
		}
		input, err := openAIToolCallArgumentsToAnthropicInput(function["arguments"])
		if err != nil {
			return nil, err
		}
		id, _ := call["id"].(string)
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    fallbackString(id, "toolu_"+requestID()),
			"name":  name,
			"input": input,
		})
	}
	return blocks, nil
}

func openAIToolMessageToAnthropic(msg map[string]any) map[string]any {
	toolCallID, _ := msg["tool_call_id"].(string)
	block := map[string]any{
		"type":        "tool_result",
		"tool_use_id": fallbackString(toolCallID, "toolu_"+requestID()),
		"content":     openAIContentText(msg["content"]),
	}
	if isError, _ := msg["is_error"].(bool); isError {
		block["is_error"] = true
	}
	return block
}

func openAIToolsToAnthropic(value any) ([]any, bool) {
	rawTools, ok := value.([]any)
	if !ok || len(rawTools) == 0 {
		return nil, false
	}
	tools := make([]any, 0, len(rawTools))
	for _, item := range rawTools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		function, _ := tool["function"].(map[string]any)
		name, _ := function["name"].(string)
		if name == "" {
			continue
		}
		schema := function["parameters"]
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		converted := map[string]any{
			"name":         name,
			"input_schema": schema,
		}
		if description, _ := function["description"].(string); strings.TrimSpace(description) != "" {
			converted["description"] = description
		}
		tools = append(tools, converted)
	}
	return tools, len(tools) > 0
}

func openAIToolChoiceToAnthropic(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}, true
		case "none":
			return map[string]any{"type": "none"}, true
		case "required":
			return map[string]any{"type": "any"}, true
		default:
			return nil, false
		}
	case map[string]any:
		if typ, _ := v["type"].(string); typ != "function" {
			return nil, false
		}
		function, _ := v["function"].(map[string]any)
		name, _ := function["name"].(string)
		if name == "" {
			return nil, false
		}
		return map[string]any{"type": "tool", "name": name}, true
	default:
		return nil, false
	}
}

// anthropicPayloadForUnsupportedParams returns a copy of an Anthropic
// Messages payload adjusted after an upstream 400 that rejects a sampling
// parameter. Newer Claude models (Opus 4.7+, Sonnet 5) removed
// temperature/top_p/top_k, and the upstream error names the offending
// parameter. ok is false when no adjustment applies.
func anthropicPayloadForUnsupportedParams(payload map[string]any, status int, responseBody string) (map[string]any, bool) {
	if status != http.StatusBadRequest {
		return nil, false
	}
	adjusted := cloneJSONMap(payload)
	changed := false
	for _, key := range []string{"temperature", "top_p", "top_k"} {
		if _, ok := adjusted[key]; !ok {
			continue
		}
		if !strings.Contains(responseBody, "`"+key+"`") && !strings.Contains(responseBody, "'"+key+"'") {
			continue
		}
		delete(adjusted, key)
		changed = true
	}
	if !changed {
		return nil, false
	}
	return adjusted, true
}

func openAIFinishReasonFromAnthropic(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

func openAIResponseFromAnthropic(route store.RoutedModel, body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	blocks, _ := raw["content"].([]any)
	textParts := make([]string, 0, len(blocks))
	toolCalls := make([]map[string]any, 0)
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			if text, _ := block["text"].(string); text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			name, _ := block["name"].(string)
			args, err := anthropicToolUseArguments(block["input"])
			if err != nil {
				return nil, err
			}
			id, _ := block["id"].(string)
			toolCalls = append(toolCalls, map[string]any{
				"id":   fallbackString(id, "toolu_"+requestID()),
				"type": "function",
				"function": map[string]any{
					"name":      fallbackString(name, "tool"),
					"arguments": args,
				},
			})
		}
	}
	message := map[string]any{
		"role":    "assistant",
		"content": strings.Join(textParts, "\n"),
	}
	if len(toolCalls) > 0 {
		if len(textParts) == 0 {
			message["content"] = nil
		}
		message["tool_calls"] = toolCalls
	}
	stopReason, _ := raw["stop_reason"].(string)
	usage, _ := raw["usage"].(map[string]any)
	inputTokens := intValue(usage["input_tokens"])
	outputTokens := intValue(usage["output_tokens"])
	id, _ := raw["id"].(string)
	if id == "" {
		id = "chatcmpl_" + requestID()
	}
	model, _ := raw["model"].(string)
	if model == "" {
		model = route.Model.Route
	}
	response := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": openAIFinishReasonFromAnthropic(stopReason),
		}},
		"usage": map[string]int{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
	}
	return json.Marshal(response)
}

func (s *Server) proxyOpenAIViaAnthropicStream(w http.ResponseWriter, r *http.Request, route store.RoutedModel, raw map[string]any, guardrails store.GuardrailPolicy) (int, []byte, string) {
	anthropicRaw, err := openAIRequestToAnthropic(raw)
	if err != nil {
		openAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return http.StatusBadRequest, nil, err.Error()
	}
	anthropicRaw["stream"] = true
	anthropicRaw["model"] = route.Model.ModelID
	endpoint := anthropicMessagesEndpoint(route.Provider)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "openai", "chat.completions.stream.translate_anthropic")
	start := time.Now()
	var resp *http.Response
	// OpenAI clients often hardcode sampling parameters that newer Claude
	// models reject; retry with adjusted parameters before anything is
	// written to the client.
	for attempt := 0; ; attempt++ {
		body, err := json.Marshal(anthropicRaw)
		if err != nil {
			openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return http.StatusInternalServerError, nil, err.Error()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			finishTrace(http.StatusInternalServerError, err.Error(), 0)
			openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return http.StatusInternalServerError, nil, err.Error()
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Phlox-GW/0.1")
		req.Header.Set("anthropic-version", "2023-06-01")
		s.setUpstreamAnthropicBetaHeader(req, route.Provider, r.Header)
		setAnthropicAuthHeader(req, route.Provider)
		resp, err = s.httpClient.Do(req)
		if err != nil {
			finishTrace(http.StatusBadGateway, err.Error(), time.Since(start))
			openAIError(w, http.StatusBadGateway, err.Error(), "provider_error")
			return http.StatusBadGateway, nil, err.Error()
		}
		if resp.StatusCode < 400 {
			break
		}
		responseBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			finishTrace(resp.StatusCode, readErr.Error(), time.Since(start))
			openAIError(w, resp.StatusCode, readErr.Error(), "provider_error")
			return resp.StatusCode, nil, readErr.Error()
		}
		if attempt < 2 {
			if adjusted, ok := anthropicPayloadForUnsupportedParams(anthropicRaw, resp.StatusCode, string(responseBody)); ok {
				anthropicRaw = adjusted
				continue
			}
		}
		message := providerErrorText(responseBody, string(responseBody))
		finishTrace(resp.StatusCode, message, time.Since(start))
		openAIError(w, resp.StatusCode, message, "provider_error")
		return resp.StatusCode, responseBody, message
	}
	defer resp.Body.Close()
	for k, values := range resp.Header {
		if shouldProxyHeader(k) && !strings.EqualFold(k, "Content-Type") {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(resp.StatusCode)

	usage, n, err := proxyAnthropicStreamAsOpenAI(w, resp.Body, route, raw, guardrails)
	responseBody := openAIUsageBody(usage, n)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), time.Since(start))
		return resp.StatusCode, responseBody, err.Error()
	}
	finishTrace(resp.StatusCode, "", time.Since(start))
	return resp.StatusCode, responseBody, ""
}

// proxyAnthropicStreamAsOpenAI re-emits an upstream Anthropic Messages SSE
// stream as OpenAI chat-completion chunks. Anthropic identifies tool blocks
// by content-block index with the input JSON streamed separately, so the
// translation tracks which OpenAI tool_calls index each Anthropic block maps
// to. Thinking blocks have no OpenAI equivalent and are dropped.
func proxyAnthropicStreamAsOpenAI(w http.ResponseWriter, body io.Reader, route store.RoutedModel, raw map[string]any, guardrails store.GuardrailPolicy) (tokenUsage, int64, error) {
	flusher := httpFlusher(w)
	id := "chatcmpl_" + requestID()
	created := time.Now().Unix()
	includeUsage := openAIStreamIncludeUsage(raw)
	toolIndexByBlock := map[int]int{}
	toolBlockCount := 0
	finishReason := "stop"
	finished := false
	var usage tokenUsage
	var written int64

	writeChunk := func(choices []map[string]any, usageField map[string]int) error {
		chunk := openAIStreamChunk(route, id, created, choices, usageField)
		chunk = applyGuardrailToStreamPayload(guardrails, chunk)
		n, err := writeOpenAIStreamData(w, flusher, chunk)
		written += int64(n)
		return err
	}
	finish := func() error {
		if finished {
			return nil
		}
		finished = true
		if err := writeChunk([]map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": finishReason,
		}}, nil); err != nil {
			return err
		}
		if includeUsage {
			if err := writeChunk([]map[string]any{}, map[string]int{
				"prompt_tokens":     usage.Input,
				"completion_tokens": usage.Output,
				"total_tokens":      usage.Total,
			}); err != nil {
				return err
			}
		}
		n, err := writeOpenAIStreamDone(w, flusher)
		written += int64(n)
		return err
	}

	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			usage = mergeAnthropicUsage(usage, usageFromAnthropicSSELine(line))
			if payload, ok := openAISSEPayload(line); ok && payload != "[DONE]" {
				var event map[string]any
				if json.Unmarshal([]byte(payload), &event) == nil {
					switch event["type"] {
					case "message_start":
						if err := writeChunk([]map[string]any{{
							"index": 0,
							"delta": map[string]any{"role": "assistant"},
						}}, nil); err != nil {
							return usage, written, err
						}
					case "content_block_start":
						block, _ := event["content_block"].(map[string]any)
						if block["type"] != "tool_use" {
							break
						}
						blockIndex := intValue(event["index"])
						toolIndex := toolBlockCount
						toolBlockCount++
						toolIndexByBlock[blockIndex] = toolIndex
						toolID, _ := block["id"].(string)
						toolName, _ := block["name"].(string)
						if err := writeChunk([]map[string]any{{
							"index": 0,
							"delta": map[string]any{
								"tool_calls": []map[string]any{{
									"index": toolIndex,
									"id":    fallbackString(toolID, fmt.Sprintf("toolu_%s_%d", id, toolIndex)),
									"type":  "function",
									"function": map[string]any{
										"name":      fallbackString(toolName, "tool"),
										"arguments": "",
									},
								}},
							},
						}}, nil); err != nil {
							return usage, written, err
						}
					case "content_block_delta":
						delta, _ := event["delta"].(map[string]any)
						switch delta["type"] {
						case "text_delta":
							text, _ := delta["text"].(string)
							if text == "" {
								break
							}
							if err := writeChunk([]map[string]any{{
								"index": 0,
								"delta": map[string]any{"content": text},
							}}, nil); err != nil {
								return usage, written, err
							}
						case "input_json_delta":
							partial, _ := delta["partial_json"].(string)
							toolIndex, ok := toolIndexByBlock[intValue(event["index"])]
							if partial == "" || !ok {
								break
							}
							if err := writeChunk([]map[string]any{{
								"index": 0,
								"delta": map[string]any{
									"tool_calls": []map[string]any{{
										"index":    toolIndex,
										"function": map[string]any{"arguments": partial},
									}},
								},
							}}, nil); err != nil {
								return usage, written, err
							}
						}
					case "message_delta":
						delta, _ := event["delta"].(map[string]any)
						if reason, _ := delta["stop_reason"].(string); reason != "" {
							finishReason = openAIFinishReasonFromAnthropic(reason)
						}
					case "message_stop":
						if err := finish(); err != nil {
							return usage, written, err
						}
					case "error":
						errObj, _ := event["error"].(map[string]any)
						message, _ := errObj["message"].(string)
						return usage, written, errors.New(fallbackString(message, "upstream Anthropic stream error"))
					}
				}
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			if err := finish(); err != nil {
				return usage, written, err
			}
			return usage, written, nil
		}
		return usage, written, err
	}
}
