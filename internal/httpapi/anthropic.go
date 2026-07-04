package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func (s *Server) callAnthropicNonStreaming(parent context.Context, route store.RoutedModel, raw map[string]any, inbound http.Header, timeout time.Duration) upstreamResult {
	attemptRaw := cloneJSONMap(raw)
	attemptRaw["model"] = route.Model.ModelID
	body, err := json.Marshal(attemptRaw)
	if err != nil {
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	ctx, cancel := contextWithOptionalTimeout(parent, timeout)
	defer cancel()
	ctx, finishTrace := s.upstreamTrace(ctx, route, "anthropic", "messages")
	endpoint := anthropicMessagesEndpoint(route.Provider)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
		return upstreamResult{Route: route, Protocol: "anthropic", Status: http.StatusInternalServerError, ErrorText: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Phlox-GW/0.1")
	version := inbound.Get("anthropic-version")
	if version == "" {
		version = "2023-06-01"
	}
	req.Header.Set("anthropic-version", version)
	if beta := inbound.Get("anthropic-beta"); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	setAnthropicAuthHeader(req, route.Provider)
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
		return upstreamResult{Route: route, Protocol: "anthropic", Status: status, ErrorText: err.Error(), LatencyMS: latency}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), latencyDuration)
		return upstreamResult{Route: route, Protocol: "anthropic", Status: resp.StatusCode, Headers: resp.Header.Clone(), ErrorText: err.Error(), LatencyMS: latency}
	}
	result := upstreamResult{Route: route, Protocol: "anthropic", Status: resp.StatusCode, Headers: resp.Header.Clone(), Body: responseBody, LatencyMS: latency}
	if resp.StatusCode >= 400 {
		result.ErrorText = string(responseBody)
	}
	finishTrace(result.Status, result.ErrorText, latencyDuration)
	return result
}

func anthropicTextContentEmpty(blocks []map[string]any) bool {
	if len(blocks) == 0 {
		return true
	}
	for _, block := range blocks {
		if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
			return false
		}
	}
	return true
}

func anthropicStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

func firstPresent(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func providerErrorText(body []byte, fallback string) string {
	if len(body) == 0 {
		return fallbackString(fallback, "provider unavailable")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil {
		if errObj, ok := raw["error"].(map[string]any); ok {
			if msg, ok := errObj["message"].(string); ok && msg != "" {
				return msg
			}
		}
		if msg, ok := raw["message"].(string); ok && msg != "" {
			return msg
		}
	}
	return fallbackString(fallback, string(body))
}

func usageFromAnthropicSSELine(line []byte) tokenUsage {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return tokenUsage{}
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if payload == "" || payload == "[DONE]" {
		return tokenUsage{}
	}
	var event struct {
		Message struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return tokenUsage{}
	}
	usage := tokenUsage{
		Input:  event.Message.Usage.InputTokens,
		Output: event.Message.Usage.OutputTokens,
	}
	if event.Usage.InputTokens > 0 {
		usage.Input = event.Usage.InputTokens
	}
	if event.Usage.OutputTokens > 0 {
		usage.Output = event.Usage.OutputTokens
	}
	if event.Usage.TotalTokens > 0 {
		usage.Total = event.Usage.TotalTokens
	} else if usage.Input > 0 || usage.Output > 0 {
		usage.Total = usage.Input + usage.Output
	}
	return usage
}

func mergeAnthropicUsage(current, next tokenUsage) tokenUsage {
	if next.Input > 0 {
		current.Input = next.Input
	}
	if next.Output > 0 {
		current.Output = next.Output
	}
	if next.Total > 0 && next.Total >= current.Input+current.Output {
		current.Total = next.Total
	} else {
		current.Total = current.Input + current.Output
	}
	return current
}

func anthropicUsageBody(usage tokenUsage, streamedBytes int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"streamed_bytes": streamedBytes,
		"usage": map[string]int{
			"input_tokens":  usage.Input,
			"output_tokens": usage.Output,
		},
	})
	return body
}

func (s *Server) proxyAnthropic(w http.ResponseWriter, r *http.Request, route store.RoutedModel, body []byte) (int, []byte, string) {
	endpoint := anthropicMessagesEndpoint(route.Provider)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "anthropic", "messages")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
		anthropicError(w, http.StatusInternalServerError, err.Error())
		return http.StatusInternalServerError, nil, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Phlox-GW/0.1")
	version := r.Header.Get("anthropic-version")
	if version == "" {
		version = "2023-06-01"
	}
	req.Header.Set("anthropic-version", version)
	if beta := r.Header.Get("anthropic-beta"); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	setAnthropicAuthHeader(req, route.Provider)

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		finishTrace(http.StatusBadGateway, err.Error(), time.Since(start))
		anthropicError(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, nil, err.Error()
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

func (s *Server) proxyAnthropicStream(w http.ResponseWriter, r *http.Request, route store.RoutedModel, body []byte, guardrails store.GuardrailPolicy) (int, []byte, string) {
	endpoint := anthropicMessagesEndpoint(route.Provider)
	ctx, finishTrace := s.upstreamTrace(r.Context(), route, "anthropic", "messages.stream")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		finishTrace(http.StatusInternalServerError, err.Error(), 0)
		anthropicError(w, http.StatusInternalServerError, err.Error())
		return http.StatusInternalServerError, nil, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Phlox-GW/0.1")
	version := r.Header.Get("anthropic-version")
	if version == "" {
		version = "2023-06-01"
	}
	req.Header.Set("anthropic-version", version)
	if beta := r.Header.Get("anthropic-beta"); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	setAnthropicAuthHeader(req, route.Provider)

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		finishTrace(http.StatusBadGateway, err.Error(), time.Since(start))
		anthropicError(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, nil, err.Error()
	}
	defer resp.Body.Close()
	for k, values := range resp.Header {
		if shouldProxyHeader(k) {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			finishTrace(resp.StatusCode, err.Error(), time.Since(start))
			return resp.StatusCode, nil, err.Error()
		}
		_, _ = w.Write(responseBody)
		finishTrace(resp.StatusCode, string(responseBody), time.Since(start))
		return resp.StatusCode, responseBody, string(responseBody)
	}
	usage, n, err := translateAnthropicSSEStream(w, resp.Body, guardrails)
	body = anthropicUsageBody(usage, n)
	if err != nil {
		finishTrace(resp.StatusCode, err.Error(), time.Since(start))
		return resp.StatusCode, body, err.Error()
	}
	finishTrace(resp.StatusCode, "", time.Since(start))
	return resp.StatusCode, body, ""
}

// translateAnthropicSSEStream copies an upstream Anthropic Messages SSE
// stream to the client while tracking usage. Named distinctly from the
// proxyAnthropicStream method above (same name, different receiver, would
// otherwise be legal but confusing) since disambiguating by name alone is
// clearer for readers than relying on the receiver.
func translateAnthropicSSEStream(w http.ResponseWriter, body io.Reader, guardrails store.GuardrailPolicy) (tokenUsage, int64, error) {
	var usage tokenUsage
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
			usage = mergeAnthropicUsage(usage, usageFromAnthropicSSELine(line))
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return usage, written, nil
		}
		return usage, written, err
	}
}

func parseAnthropicUsage(body []byte) tokenUsage {
	var resp struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &resp)
	total := resp.Usage.InputTokens + resp.Usage.OutputTokens
	return tokenUsage{Input: resp.Usage.InputTokens, Output: resp.Usage.OutputTokens, Total: total}
}
