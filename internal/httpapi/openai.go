package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

type tokenUsage struct {
	Input  int
	Output int
	Total  int
}

func (s *Server) callOpenAINonStreaming(parent context.Context, route store.RoutedModel, raw map[string]any, timeout time.Duration) upstreamResult {
	if route.Provider.Type == "bedrock" {
		return s.callBedrockOpenAINonStreaming(parent, route, raw, timeout)
	}
	attemptRaw := cloneJSONMap(raw)
	attemptRaw["model"] = route.Model.ModelID
	body, err := json.Marshal(attemptRaw)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "openai", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	ctx, cancel := contextWithOptionalTimeout(parent, timeout)
	defer cancel()
	ctx, finishTrace := s.upstreamTrace(ctx, route, "openai", "chat.completions")
	endpoint := openAIChatEndpoint(route.Provider, route.Model.ModelID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
		return upstreamResult{Route: route, Protocol: "openai", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	setOpenAIAuthHeader(req, route.Provider)
	req.Header.Set("User-Agent", "Phlox-GW/0.1")
	start := time.Now()
	resp, err := s.httpClient.Do(req)
	latencyDuration := time.Since(start)
	latency := latencyDuration.Milliseconds()
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		finishTrace(status, err.Error(), latencyDuration)
		return upstreamResult{Route: route, Protocol: "openai", Status: status, ErrorText: err.Error(), LatencyMS: latency}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), latencyDuration)
		return upstreamResult{Route: route, Protocol: "openai", Status: resp.StatusCode, Headers: resp.Header.Clone(), ErrorText: err.Error(), LatencyMS: latency}
	}
	result := upstreamResult{Route: route, Protocol: "openai", Status: resp.StatusCode, Headers: resp.Header.Clone(), Body: responseBody, LatencyMS: latency}
	if resp.StatusCode >= 400 {
		result.ErrorText = string(responseBody)
	}
	finishTrace(result.Status, result.ErrorText, latencyDuration)
	return result
}

// callOpenAINonStreamingWithParamRetry calls an OpenAI-protocol upstream,
// retrying with adjusted parameters when a reasoning-family model rejects
// legacy parameters (max_tokens, pinned temperature) that OpenAI clients
// often hardcode. Requests that the upstream accepts pass through unchanged.
func (s *Server) callOpenAINonStreamingWithParamRetry(parent context.Context, route store.RoutedModel, raw map[string]any, timeout time.Duration) upstreamResult {
	result := s.callOpenAINonStreaming(parent, route, raw, timeout)
	for attempt := 0; attempt < 2; attempt++ {
		adjusted, ok := openAIPayloadForUnsupportedParams(raw, result.Status, string(result.Body))
		if !ok {
			break
		}
		raw = adjusted
		result = s.callOpenAINonStreaming(parent, route, raw, timeout)
	}
	return result
}

func writeOpenAIResult(w http.ResponseWriter, result upstreamResult) {
	if result.Body == nil {
		openAIError(w, resultStatus(result), fallbackString(result.ErrorText, "provider unavailable"), "provider_error")
		return
	}
	writeUpstreamResult(w, result)
}

func writeUpstreamResult(w http.ResponseWriter, result upstreamResult) {
	for k, values := range result.Headers {
		if shouldProxyHeader(k) {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resultStatus(result))
	_, _ = w.Write(result.Body)
}

func resultStatus(result upstreamResult) int {
	if result.Status == 0 {
		return http.StatusServiceUnavailable
	}
	return result.Status
}

func fallbackString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func (s *Server) proxyOpenAI(w http.ResponseWriter, r *http.Request, route store.RoutedModel, body []byte, raw map[string]any, guardrails store.GuardrailPolicy) (int, []byte, string) {
	endpoint := openAIChatEndpoint(route.Provider, route.Model.ModelID)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "openai", "chat.completions")
	start := time.Now()
	var resp *http.Response
	// Reasoning-family models reject legacy parameters (max_tokens, pinned
	// temperature) that OpenAI clients often hardcode; when the upstream 400
	// names the offender, retry with adjusted parameters before anything is
	// written to the client. Accepted requests pass through unchanged.
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			finishTrace(http.StatusInternalServerError, err.Error(), 0)
			openAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return http.StatusInternalServerError, nil, err.Error()
		}
		req.Header.Set("Content-Type", "application/json")
		setOpenAIAuthHeader(req, route.Provider)
		req.Header.Set("User-Agent", "Phlox-GW/0.1")
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
			if adjusted, ok := openAIPayloadForUnsupportedParams(raw, resp.StatusCode, string(responseBody)); ok {
				if adjustedBody, marshalErr := json.Marshal(adjusted); marshalErr == nil {
					raw = adjusted
					body = adjustedBody
					continue
				}
			}
		}
		// No adjustment applies: proxy the upstream error response as-is.
		for k, values := range resp.Header {
			if shouldProxyHeader(k) {
				for _, v := range values {
					w.Header().Add(k, v)
				}
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(responseBody)
		finishTrace(resp.StatusCode, string(responseBody), time.Since(start))
		return resp.StatusCode, responseBody, string(responseBody)
	}
	defer resp.Body.Close()

	for k, values := range resp.Header {
		if shouldProxyHeader(k) {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)

	if stream, _ := raw["stream"].(bool); stream {
		fallbackInput := 0
		if resp.StatusCode < 400 {
			fallbackInput = estimateOpenAIInputTokens(raw)
		}
		usage, n, err := proxyOpenAIStream(w, resp.Body, fallbackInput, guardrails)
		if err != nil {
			finishTrace(resp.StatusCode, err.Error(), time.Since(start))
			return resp.StatusCode, nil, err.Error()
		}
		finishTrace(resp.StatusCode, "", time.Since(start))
		return resp.StatusCode, openAIUsageBody(usage, n), ""
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), time.Since(start))
		return resp.StatusCode, nil, err.Error()
	}
	_, _ = w.Write(responseBody)
	if resp.StatusCode >= 400 {
		finishTrace(resp.StatusCode, string(responseBody), time.Since(start))
		return resp.StatusCode, responseBody, string(responseBody)
	}
	finishTrace(resp.StatusCode, "", time.Since(start))
	return resp.StatusCode, responseBody, ""
}

func proxyOpenAIStream(w http.ResponseWriter, body io.Reader, fallbackInputTokens int, guardrails store.GuardrailPolicy) (tokenUsage, int64, error) {
	var usage tokenUsage
	estimatedOutputTokens := 0
	var written int64
	reader := bufio.NewReader(body)
	flusher, _ := w.(http.Flusher)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineToWrite := applyGuardrailToSSELine(guardrails, line)
			n, writeErr := w.Write(lineToWrite)
			written += int64(n)
			if writeErr != nil {
				return usage, written, writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
			if parsed := usageFromSSELine(line); parsed.Total > 0 || parsed.Input > 0 || parsed.Output > 0 {
				usage = parsed
			}
			if usage.Output == 0 {
				estimatedOutputTokens += estimateOpenAIStreamOutputTokens(line)
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			usage = mergeEstimatedUsage(usage, fallbackInputTokens, estimatedOutputTokens)
			return usage, written, nil
		}
		return usage, written, err
	}
}

func usageFromSSELine(line []byte) tokenUsage {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return tokenUsage{}
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if payload == "" || payload == "[DONE]" {
		return tokenUsage{}
	}
	return parseOpenAIUsage([]byte(payload))
}

func estimateOpenAIStreamOutputTokens(line []byte) int {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return 0
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if payload == "" || payload == "[DONE]" {
		return 0
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          any    `json:"content"`
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return 0
	}
	total := 0
	for _, choice := range chunk.Choices {
		total += estimateOpenAIContentTokens(choice.Delta.Content)
		total += estimateTextTokens(choice.Delta.Reasoning)
		total += estimateTextTokens(choice.Delta.ReasoningContent)
		for _, call := range choice.Delta.ToolCalls {
			total += estimateTextTokens(call.Function.Arguments)
		}
	}
	return total
}

func openAIUsageWithFallback(raw map[string]any, body []byte, status int) tokenUsage {
	usage := parseOpenAIUsage(body)
	if status >= 400 {
		return usage
	}
	if usage.Input > 0 && usage.Output > 0 && usage.Total > 0 {
		return usage
	}
	return mergeEstimatedUsage(usage, estimateOpenAIInputTokens(raw), estimateOpenAIResponseTokens(body))
}

func mergeEstimatedUsage(usage tokenUsage, estimatedInputTokens, estimatedOutputTokens int) tokenUsage {
	if usage.Input == 0 && estimatedInputTokens > 0 {
		usage.Input = estimatedInputTokens
	}
	if usage.Output == 0 && estimatedOutputTokens > 0 {
		usage.Output = estimatedOutputTokens
	}
	if usage.Total == 0 || usage.Total < usage.Input+usage.Output {
		usage.Total = usage.Input + usage.Output
	}
	return usage
}

func estimateOpenAIInputTokens(raw map[string]any) int {
	total := 0
	if messages, ok := raw["messages"].([]any); ok {
		for _, item := range messages {
			msg, ok := item.(map[string]any)
			if !ok {
				continue
			}
			total += 4
			total += estimateOpenAIContentTokens(msg["content"])
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, call := range calls {
					if body, err := json.Marshal(call); err == nil {
						total += estimateTextTokens(string(body))
					}
				}
			}
		}
	}
	if tools, ok := raw["tools"]; ok && tools != nil {
		if body, err := json.Marshal(tools); err == nil {
			total += estimateTextTokens(string(body))
		}
	}
	return total
}

func estimateOpenAIResponseTokens(body []byte) int {
	var resp struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content   any `json:"content"`
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0
	}
	total := 0
	for _, choice := range resp.Choices {
		total += estimateTextTokens(choice.Text)
		total += estimateOpenAIContentTokens(choice.Message.Content)
		for _, call := range choice.Message.ToolCalls {
			total += estimateTextTokens(call.Function.Arguments)
		}
	}
	return total
}

func estimateOpenAIContentTokens(content any) int {
	switch v := content.(type) {
	case nil:
		return 0
	case string:
		return estimateTextTokens(v)
	case []any:
		total := 0
		for _, item := range v {
			switch part := item.(type) {
			case string:
				total += estimateTextTokens(part)
			case map[string]any:
				if text, _ := part["text"].(string); text != "" {
					total += estimateTextTokens(text)
				}
			}
		}
		return total
	default:
		return 0
	}
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	chars := utf8.RuneCountInString(text)
	byChars := int(math.Ceil(float64(chars) / 4.0))
	byWords := int(math.Ceil(float64(len(strings.Fields(text))) * 1.33))
	if byWords > byChars {
		byChars = byWords
	}
	if byChars < 1 {
		return 1
	}
	return byChars
}

func openAIUsageBody(usage tokenUsage, streamedBytes int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"streamed_bytes": streamedBytes,
		"usage": map[string]int{
			"prompt_tokens":     usage.Input,
			"completion_tokens": usage.Output,
			"total_tokens":      usage.Total,
		},
	})
	return body
}

func writeOpenAIStreamData(w http.ResponseWriter, flusher http.Flusher, payload map[string]any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	n, err := fmt.Fprintf(w, "data: %s\n\n", body)
	if flusher != nil {
		flusher.Flush()
	}
	return n, err
}

func writeOpenAIStreamDone(w http.ResponseWriter, flusher http.Flusher) (int, error) {
	n, err := io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return n, err
}

func openAIStreamChunk(route store.RoutedModel, id string, created int64, choices []map[string]any, usage map[string]int) map[string]any {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   route.Model.Route,
		"choices": choices,
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	return chunk
}

func openAIStreamIncludeUsage(raw map[string]any) bool {
	options, ok := raw["stream_options"].(map[string]any)
	if !ok {
		return false
	}
	include, _ := options["include_usage"].(bool)
	return include
}

func httpFlusher(w http.ResponseWriter) http.Flusher {
	flusher, _ := w.(http.Flusher)
	return flusher
}

func openAISSEPayload(line []byte) (string, bool) {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return "", false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	return payload, payload != ""
}

func parseOpenAIUsage(body []byte) tokenUsage {
	var resp struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &resp)
	return tokenUsage{Input: resp.Usage.PromptTokens, Output: resp.Usage.CompletionTokens, Total: resp.Usage.TotalTokens}
}

// openAIPayloadForUnsupportedParams returns a copy of an OpenAI-protocol
// chat-completions payload adjusted after an upstream 400 that rejects legacy
// sampling parameters. Reasoning-family models (o-series, GPT-5) accept only
// max_completion_tokens and the default temperature; the upstream error names
// the offending parameter. ok is false when no adjustment applies.
func openAIPayloadForUnsupportedParams(payload map[string]any, status int, responseBody string) (map[string]any, bool) {
	if status != http.StatusBadRequest {
		return nil, false
	}
	adjusted := cloneJSONMap(payload)
	changed := false
	if strings.Contains(responseBody, "max_completion_tokens") {
		if v, ok := adjusted["max_tokens"]; ok {
			delete(adjusted, "max_tokens")
			adjusted["max_completion_tokens"] = v
			changed = true
		}
	}
	if strings.Contains(responseBody, "'temperature'") {
		if _, ok := adjusted["temperature"]; ok {
			delete(adjusted, "temperature")
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	return adjusted, true
}
