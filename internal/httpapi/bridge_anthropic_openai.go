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

func (s *Server) callAnthropicViaOpenAINonStreaming(parent context.Context, route store.RoutedModel, raw map[string]any, timeout time.Duration) upstreamResult {
	openAIRaw, err := anthropicRequestToOpenAI(raw)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusBadRequest, ErrorText: err.Error()}
	}
	result := s.callOpenAINonStreaming(parent, route, openAIRaw, timeout)
	// Anthropic requests always carry max_tokens, which reasoning-family
	// models reject in favor of max_completion_tokens; retry the authored
	// payload with adjusted parameters.
	for attempt := 0; attempt < 2; attempt++ {
		adjusted, ok := openAIPayloadForUnsupportedParams(openAIRaw, result.Status, string(result.Body))
		if !ok {
			break
		}
		openAIRaw = adjusted
		result = s.callOpenAINonStreaming(parent, route, openAIRaw, timeout)
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
	body, err := anthropicResponseFromOpenAI(route, result.Body)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusBadGateway, ErrorText: "could not translate OpenAI-compatible response: " + err.Error(), LatencyMS: result.LatencyMS}
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

func anthropicRequestToOpenAI(raw map[string]any) (map[string]any, error) {
	out := map[string]any{
		"model":  raw["model"],
		"stream": false,
	}
	messages, err := anthropicMessagesToOpenAI(raw)
	if err != nil {
		return nil, err
	}
	out["messages"] = messages
	if v, ok := raw["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	for _, key := range []string{"temperature", "top_p", "presence_penalty", "frequency_penalty"} {
		if v, ok := raw[key]; ok {
			out[key] = v
		}
	}
	if stop, ok := raw["stop_sequences"]; ok {
		out["stop"] = stop
	}
	if tools, ok := anthropicToolsToOpenAI(raw["tools"]); ok {
		out["tools"] = tools
	}
	if choice, ok := anthropicToolChoiceToOpenAI(raw["tool_choice"]); ok {
		out["tool_choice"] = choice
	}
	return out, nil
}

func anthropicMessagesToOpenAI(raw map[string]any) ([]any, error) {
	messages := make([]any, 0)
	if system, ok := raw["system"]; ok {
		if content, ok := anthropicContentToOpenAI(system); ok {
			messages = append(messages, map[string]any{"role": "system", "content": content})
		}
	}
	rawMessages, ok := raw["messages"].([]any)
	if !ok || len(rawMessages) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}
	for _, item := range rawMessages {
		msg, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("messages entries must be objects")
		}
		role, _ := msg["role"].(string)
		if role == "assistant" {
			converted, err := anthropicAssistantMessageToOpenAI(msg)
			if err != nil {
				return nil, err
			}
			messages = append(messages, converted)
			continue
		}
		converted, err := anthropicUserMessageToOpenAI(msg)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted...)
	}
	return messages, nil
}

func anthropicUserMessageToOpenAI(msg map[string]any) ([]any, error) {
	content := msg["content"]
	if text, ok := content.(string); ok {
		return []any{map[string]any{"role": "user", "content": text}}, nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return []any{map[string]any{"role": "user", "content": ""}}, nil
	}
	out := make([]any, 0, 1)
	var parts []any
	hasRichPart := false
	flushUserContent := func() {
		if len(parts) == 0 {
			return
		}
		var value any = parts
		if !hasRichPart {
			textParts := make([]string, 0, len(parts))
			for _, part := range parts {
				if block, ok := part.(map[string]any); ok {
					if text, _ := block["text"].(string); text != "" {
						textParts = append(textParts, text)
					}
				}
			}
			value = strings.Join(textParts, "\n")
		}
		out = append(out, map[string]any{"role": "user", "content": value})
		parts = nil
		hasRichPart = false
	}
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			if text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
		case "image":
			source, _ := block["source"].(map[string]any)
			if source["type"] != "base64" {
				continue
			}
			mediaType, _ := source["media_type"].(string)
			data, _ := source["data"].(string)
			if mediaType == "" || data == "" {
				continue
			}
			hasRichPart = true
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + mediaType + ";base64," + data,
				},
			})
		case "tool_result":
			flushUserContent()
			toolUseID, _ := block["tool_use_id"].(string)
			resultText := anthropicToolResultContentToText(block["content"])
			isError, _ := block["is_error"].(bool)
			if toolUseID == "" {
				out = append(out, map[string]any{"role": "user", "content": resultText})
				continue
			}
			toolMessage := map[string]any{"role": "tool", "tool_call_id": toolUseID, "content": resultText}
			if isError {
				toolMessage["is_error"] = true
			}
			out = append(out, toolMessage)
		}
	}
	flushUserContent()
	if len(out) == 0 {
		out = append(out, map[string]any{"role": "user", "content": ""})
	}
	return out, nil
}

func anthropicAssistantMessageToOpenAI(msg map[string]any) (map[string]any, error) {
	content := msg["content"]
	if text, ok := content.(string); ok {
		return map[string]any{"role": "assistant", "content": text}, nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return map[string]any{"role": "assistant", "content": ""}, nil
	}
	textParts := make([]string, 0)
	toolCalls := make([]any, 0)
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			if text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			name, _ := block["name"].(string)
			if name == "" {
				continue
			}
			id, _ := block["id"].(string)
			args, err := anthropicToolUseArguments(block["input"])
			if err != nil {
				return nil, err
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   fallbackString(id, "toolu_"+requestID()),
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			})
		}
	}
	out := map[string]any{
		"role":    "assistant",
		"content": strings.Join(textParts, "\n"),
	}
	if len(toolCalls) > 0 {
		out["tool_calls"] = toolCalls
	}
	return out, nil
}

func anthropicToolResultContentToText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
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
		body, _ := json.Marshal(v)
		return string(body)
	}
}

func anthropicToolUseArguments(input any) (string, error) {
	if input == nil {
		return "{}", nil
	}
	if text, ok := input.(string); ok {
		if strings.TrimSpace(text) == "" {
			return "{}", nil
		}
		return text, nil
	}
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func anthropicToolsToOpenAI(value any) ([]any, bool) {
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
		name, _ := tool["name"].(string)
		if name == "" {
			continue
		}
		description, _ := tool["description"].(string)
		parameters := tool["input_schema"]
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": normalizedToolDescription(name, description),
				"parameters":  parameters,
			},
		})
	}
	return tools, len(tools) > 0
}

func normalizedToolDescription(name, description string) string {
	description = strings.TrimSpace(description)
	if description != "" {
		return description
	}
	name = strings.TrimSpace(name)
	if name != "" {
		return "Tool " + name + "."
	}
	return "No description provided."
}

func anthropicToolChoiceToOpenAI(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		switch v {
		case "auto", "none", "required":
			return v, true
		default:
			return nil, false
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "auto":
			return "auto", true
		case "none":
			return "none", true
		case "any":
			return "required", true
		case "tool":
			name, _ := v["name"].(string)
			if name == "" {
				return nil, false
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}, true
		default:
			return nil, false
		}
	default:
		return nil, false
	}
}

func anthropicContentToOpenAI(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		return v, strings.TrimSpace(v) != ""
	case []any:
		parts := make([]any, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				text, _ := block["text"].(string)
				parts = append(parts, map[string]any{"type": "text", "text": text})
			case "image":
				source, _ := block["source"].(map[string]any)
				if source["type"] != "base64" {
					continue
				}
				mediaType, _ := source["media_type"].(string)
				data, _ := source["data"].(string)
				if mediaType == "" || data == "" {
					continue
				}
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:" + mediaType + ";base64," + data,
					},
				})
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return parts, true
	default:
		return "", false
	}
}

func anthropicResponseFromOpenAI(route store.RoutedModel, body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	choices, _ := raw["choices"].([]any)
	var message map[string]any
	var finishReason string
	if len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		message, _ = choice["message"].(map[string]any)
		finishReason, _ = choice["finish_reason"].(string)
	}
	content := openAIContentToAnthropic(message["content"])
	if anthropicTextContentEmpty(content) {
		if reasoning, _ := firstPresent(message, "reasoning", "reasoning_content").(string); strings.TrimSpace(reasoning) != "" {
			content = []map[string]any{{"type": "text", "text": reasoning}}
		}
	}
	toolContent, err := openAIToolCallsToAnthropic(message["tool_calls"])
	if err != nil {
		return nil, err
	}
	if len(toolContent) > 0 {
		if anthropicTextContentEmpty(content) {
			content = nil
		}
		content = append(content, toolContent...)
	}
	usage, _ := raw["usage"].(map[string]any)
	inputTokens := intValue(firstPresent(usage, "prompt_tokens", "input_tokens"))
	outputTokens := intValue(firstPresent(usage, "completion_tokens", "output_tokens"))
	id, _ := raw["id"].(string)
	if id == "" {
		id = "msg_" + requestID()
	}
	model, _ := raw["model"].(string)
	if model == "" {
		model = route.Model.Route
	}
	response := map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   anthropicStopReason(finishReason),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	return json.Marshal(response)
}

func openAIContentToAnthropic(value any) []map[string]any {
	switch v := value.(type) {
	case string:
		return []map[string]any{{"type": "text", "text": v}}
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "text" {
				text, _ := block["text"].(string)
				out = append(out, map[string]any{"type": "text", "text": text})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []map[string]any{{"type": "text", "text": ""}}
}

func openAIToolCallsToAnthropic(value any) ([]map[string]any, error) {
	var items []any
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []any:
		items = v
	case []map[string]any:
		items = make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, item)
		}
	default:
		return nil, nil
	}

	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		block, ok, err := openAIToolCallToAnthropic(call)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, block)
		}
	}
	return out, nil
}

func openAIToolCallToAnthropic(call map[string]any) (map[string]any, bool, error) {
	function, _ := call["function"].(map[string]any)
	name, _ := function["name"].(string)
	if strings.TrimSpace(name) == "" {
		name = "tool"
	}
	input, err := openAIToolCallArgumentsToAnthropicInput(function["arguments"])
	if err != nil {
		return nil, false, err
	}
	id, _ := call["id"].(string)
	return map[string]any{
		"type":  "tool_use",
		"id":    fallbackString(id, "toolu_"+requestID()),
		"name":  name,
		"input": input,
	}, true, nil
}

func openAIToolCallArgumentsToAnthropicInput(value any) (map[string]any, error) {
	switch v := value.(type) {
	case nil:
		return map[string]any{}, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return map[string]any{}, nil
		}
		var out map[string]any
		decoder := json.NewDecoder(strings.NewReader(v))
		decoder.UseNumber()
		if err := decoder.Decode(&out); err != nil {
			return nil, fmt.Errorf("could not parse OpenAI tool call arguments: %w", err)
		}
		if out == nil {
			return map[string]any{}, nil
		}
		return out, nil
	case map[string]any:
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported OpenAI tool call arguments type %T", value)
	}
}

func (s *Server) proxyAnthropicViaOpenAIStream(w http.ResponseWriter, r *http.Request, route store.RoutedModel, raw map[string]any, guardrails store.GuardrailPolicy) (int, []byte, string) {
	openAIRaw, err := anthropicRequestToOpenAI(raw)
	if err != nil {
		anthropicError(w, http.StatusBadRequest, err.Error())
		return http.StatusBadRequest, nil, err.Error()
	}
	openAIRaw["stream"] = true
	openAIRaw["model"] = route.Model.ModelID
	ensureStreamUsageOption(route.Provider, openAIRaw)
	endpoint := openAIChatEndpoint(route.Provider, route.Model.ModelID)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "anthropic", "messages.stream.translate_openai")
	start := time.Now()
	var resp *http.Response
	// The translated payload always carries max_tokens, which
	// reasoning-family models reject; retry with adjusted parameters before
	// anything is written to the client.
	for attempt := 0; ; attempt++ {
		body, err := json.Marshal(openAIRaw)
		if err != nil {
			anthropicError(w, http.StatusInternalServerError, err.Error())
			return http.StatusInternalServerError, nil, err.Error()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			finishTrace(http.StatusInternalServerError, err.Error(), 0)
			anthropicError(w, http.StatusInternalServerError, err.Error())
			return http.StatusInternalServerError, nil, err.Error()
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Phlox-GW/0.1")
		setOpenAIAuthHeader(req, route.Provider)
		resp, err = s.httpClient.Do(req)
		if err != nil {
			finishTrace(http.StatusBadGateway, err.Error(), time.Since(start))
			anthropicError(w, http.StatusBadGateway, err.Error())
			return http.StatusBadGateway, nil, err.Error()
		}
		if resp.StatusCode < 400 {
			break
		}
		responseBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			finishTrace(resp.StatusCode, readErr.Error(), time.Since(start))
			anthropicError(w, resp.StatusCode, readErr.Error())
			return resp.StatusCode, nil, readErr.Error()
		}
		if attempt < 2 {
			if adjusted, ok := openAIPayloadForUnsupportedParams(openAIRaw, resp.StatusCode, string(responseBody)); ok {
				openAIRaw = adjusted
				continue
			}
		}
		message := providerErrorText(responseBody, string(responseBody))
		finishTrace(resp.StatusCode, message, time.Since(start))
		anthropicError(w, resp.StatusCode, message)
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

	usage, n, err := proxyOpenAIStreamAsAnthropic(w, resp.Body, route, estimateOpenAIInputTokens(openAIRaw), guardrails)
	responseBody := anthropicUsageBody(usage, n)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), time.Since(start))
		return resp.StatusCode, responseBody, err.Error()
	}
	finishTrace(resp.StatusCode, "", time.Since(start))
	return resp.StatusCode, responseBody, ""
}

type anthropicOpenAIStreamToolBlock struct {
	openAIIndex int
	blockIndex  int
	id          string
	name        string
	pendingJSON string
	started     bool
}

type anthropicOpenAIStreamState struct {
	w              http.ResponseWriter
	flusher        http.Flusher
	route          store.RoutedModel
	id             string
	inputTokens    int
	guardrails     store.GuardrailPolicy
	written        int64
	nextBlockIndex int
	textBlockIndex int
	textStarted    bool
	toolBlocks     map[int]*anthropicOpenAIStreamToolBlock
	toolOrder      []int
}

func proxyOpenAIStreamAsAnthropic(w http.ResponseWriter, body io.Reader, route store.RoutedModel, fallbackInputTokens int, guardrails store.GuardrailPolicy) (tokenUsage, int64, error) {
	state := &anthropicOpenAIStreamState{
		w:              w,
		flusher:        httpFlusher(w),
		route:          route,
		id:             "msg_" + requestID(),
		inputTokens:    fallbackInputTokens,
		guardrails:     guardrails,
		textBlockIndex: -1,
		toolBlocks:     map[int]*anthropicOpenAIStreamToolBlock{},
	}
	if err := state.writeMessageStart(); err != nil {
		return tokenUsage{}, state.written, err
	}
	var usage tokenUsage
	estimatedOutputTokens := 0
	stopReason := "end_turn"
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			payload, ok := openAISSEPayload(line)
			if ok {
				if payload == "[DONE]" {
					usage = mergeEstimatedUsage(usage, fallbackInputTokens, estimatedOutputTokens)
					if err := state.finish(stopReason, usage.Output); err != nil {
						return usage, state.written, err
					}
					return usage, state.written, nil
				}
				if parsed := parseOpenAIUsage([]byte(payload)); parsed.Total > 0 || parsed.Input > 0 || parsed.Output > 0 {
					usage = parsed
				}
				if usage.Output == 0 {
					estimatedOutputTokens += estimateOpenAIStreamOutputTokens(line)
				}
				var chunk map[string]any
				if json.Unmarshal([]byte(payload), &chunk) == nil {
					if err := state.writeOpenAIChunk(chunk, &stopReason); err != nil {
						return usage, state.written, err
					}
				}
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			usage = mergeEstimatedUsage(usage, fallbackInputTokens, estimatedOutputTokens)
			if err := state.finish(stopReason, usage.Output); err != nil {
				return usage, state.written, err
			}
			return usage, state.written, nil
		}
		return usage, state.written, err
	}
}

func (s *anthropicOpenAIStreamState) writeMessageStart() error {
	return s.writeEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.id,
			"type":          "message",
			"role":          "assistant",
			"model":         s.route.Model.Route,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": s.inputTokens, "output_tokens": 0},
		},
	})
}

func (s *anthropicOpenAIStreamState) writeOpenAIChunk(chunk map[string]any, stopReason *string) error {
	choices, _ := chunk["choices"].([]any)
	for _, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		for _, text := range openAIStreamDeltaTextFragments(delta) {
			if err := s.writeTextDelta(text); err != nil {
				return err
			}
		}
		if err := s.writeToolCallDeltas(delta); err != nil {
			return err
		}
		if finish, _ := choice["finish_reason"].(string); finish != "" {
			*stopReason = anthropicStopReason(finish)
		}
	}
	return nil
}

func openAIStreamDeltaTextFragments(delta map[string]any) []string {
	fragments := make([]string, 0, 1)
	for _, key := range []string{"content", "reasoning", "reasoning_content"} {
		switch v := delta[key].(type) {
		case string:
			if v != "" {
				fragments = append(fragments, v)
			}
		case []any:
			for _, item := range v {
				block, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if text, _ := block["text"].(string); text != "" {
					fragments = append(fragments, text)
				}
			}
		}
	}
	return fragments
}

func (s *anthropicOpenAIStreamState) writeTextDelta(text string) error {
	if text == "" {
		return nil
	}
	if !s.textStarted {
		s.textBlockIndex = s.nextBlockIndex
		s.nextBlockIndex++
		s.textStarted = true
		if err := s.writeEvent("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         s.textBlockIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		}); err != nil {
			return err
		}
	}
	return s.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.textBlockIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

func (s *anthropicOpenAIStreamState) writeToolCallDeltas(delta map[string]any) error {
	rawCalls, _ := delta["tool_calls"].([]any)
	for _, item := range rawCalls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		openAIIndex := intValue(call["index"])
		block := s.toolBlocks[openAIIndex]
		if block == nil {
			block = &anthropicOpenAIStreamToolBlock{openAIIndex: openAIIndex, blockIndex: -1}
			s.toolBlocks[openAIIndex] = block
			s.toolOrder = append(s.toolOrder, openAIIndex)
		}
		if id, _ := call["id"].(string); id != "" {
			block.id = id
		}
		function, _ := call["function"].(map[string]any)
		if name, _ := function["name"].(string); name != "" {
			block.name = name
		}
		if !block.started && (block.id != "" || block.name != "") {
			if err := s.startToolBlock(block); err != nil {
				return err
			}
		}
		if args, _ := function["arguments"].(string); args != "" {
			if block.started {
				if err := s.writeToolInputDelta(block, args); err != nil {
					return err
				}
			} else {
				block.pendingJSON += args
			}
		}
	}
	return nil
}

func (s *anthropicOpenAIStreamState) startToolBlock(block *anthropicOpenAIStreamToolBlock) error {
	if block.started {
		return nil
	}
	if s.textStarted {
		if err := s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.textBlockIndex}); err != nil {
			return err
		}
		s.textStarted = false
		s.textBlockIndex = -1
	}
	block.blockIndex = s.nextBlockIndex
	s.nextBlockIndex++
	block.started = true
	if block.id == "" {
		block.id = fmt.Sprintf("toolu_%s_%d", s.id, block.openAIIndex)
	}
	if block.name == "" {
		block.name = "tool"
	}
	if err := s.writeEvent("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": block.blockIndex,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    block.id,
			"name":  block.name,
			"input": map[string]any{},
		},
	}); err != nil {
		return err
	}
	if block.pendingJSON != "" {
		pending := block.pendingJSON
		block.pendingJSON = ""
		return s.writeToolInputDelta(block, pending)
	}
	return nil
}

func (s *anthropicOpenAIStreamState) writeToolInputDelta(block *anthropicOpenAIStreamToolBlock, partialJSON string) error {
	return s.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": block.blockIndex,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": partialJSON},
	})
}

func (s *anthropicOpenAIStreamState) finish(stopReason string, outputTokens int) error {
	if s.textStarted {
		if err := s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.textBlockIndex}); err != nil {
			return err
		}
	}
	for _, openAIIndex := range s.toolOrder {
		block := s.toolBlocks[openAIIndex]
		if block == nil {
			continue
		}
		if !block.started {
			if err := s.startToolBlock(block); err != nil {
				return err
			}
		}
		if err := s.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": block.blockIndex}); err != nil {
			return err
		}
	}
	if stopReason == "" {
		stopReason = "end_turn"
	}
	if err := s.writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]int{"output_tokens": outputTokens},
	}); err != nil {
		return err
	}
	return s.writeEvent("message_stop", map[string]any{"type": "message_stop"})
}

func (s *anthropicOpenAIStreamState) writeEvent(event string, payload map[string]any) error {
	payload = applyGuardrailToStreamPayload(s.guardrails, payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	n, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, body)
	s.written += int64(n)
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return err
}
