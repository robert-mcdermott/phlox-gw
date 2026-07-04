package httpapi

import (
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

const (
	playgroundMaxMessages  = 50
	playgroundMaxChars     = 64 * 1024
	playgroundDefaultToken = 1024
	playgroundMaxTokensCap = 8192
)

type playgroundChatResult struct {
	OK            bool   `json:"ok"`
	ProviderID    string `json:"provider_id"`
	ProviderType  string `json:"provider_type"`
	ModelRoute    string `json:"model_route"`
	UpstreamModel string `json:"upstream_model"`
	StatusCode    int    `json:"status_code"`
	LatencyMS     int64  `json:"latency_ms"`
	Content       string `json:"content,omitempty"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	TotalTokens   int    `json:"total_tokens"`
	Error         string `json:"error,omitempty"`
}

// playgroundChat sends an admin-authored conversation to a routed model so
// providers and models can be validated from the dashboard. Playground
// traffic bypasses API keys, budgets, rate limits, and guardrails, and is
// not recorded in the usage ledger; each call is audit-logged instead.
func (s *Server) playgroundChat(w http.ResponseWriter, r *http.Request, admin store.User) {
	var req struct {
		Route     string `json:"route"`
		System    string `json:"system"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Route) == "" {
		respondError(w, http.StatusBadRequest, "route is required")
		return
	}
	if len(req.Messages) == 0 {
		respondError(w, http.StatusBadRequest, "messages must be a non-empty array")
		return
	}
	if len(req.Messages) > playgroundMaxMessages {
		respondError(w, http.StatusBadRequest, "conversation has too many messages")
		return
	}
	system := strings.TrimSpace(req.System)
	totalChars := len(system)
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "user" && role != "assistant" {
			respondError(w, http.StatusBadRequest, "message roles must be user or assistant")
			return
		}
		if strings.TrimSpace(m.Content) == "" {
			respondError(w, http.StatusBadRequest, "message content cannot be empty")
			return
		}
		totalChars += len(m.Content)
		messages = append(messages, map[string]any{"role": role, "content": m.Content})
	}
	if totalChars > playgroundMaxChars {
		respondError(w, http.StatusBadRequest, "conversation is too large")
		return
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = playgroundDefaultToken
	}
	if maxTokens > playgroundMaxTokensCap {
		maxTokens = playgroundMaxTokensCap
	}
	route, err := s.store.ResolveModel(r.Context(), strings.TrimSpace(req.Route))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "enabled model route not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := s.runPlaygroundChat(r.Context(), route, system, messages, maxTokens)
	s.audit(r, admin, "playground.chat", "model", route.Model.ID, route.Model.Route, map[string]any{
		"ok":          result.OK,
		"status_code": result.StatusCode,
		"latency_ms":  result.LatencyMS,
		"provider_id": result.ProviderID,
		"messages":    len(messages),
	})
	respondJSON(w, http.StatusOK, result)
}

func (s *Server) runPlaygroundChat(parent context.Context, route store.RoutedModel, system string, messages []map[string]any, maxTokens int) playgroundChatResult {
	result := playgroundChatResult{
		ProviderID:    route.Provider.ID,
		ProviderType:  route.Provider.Type,
		ModelRoute:    route.Model.Route,
		UpstreamModel: route.Model.ModelID,
	}
	timeout := 60 * time.Second
	if route.Model.RequestTimeoutMS > 0 {
		timeout = time.Duration(route.Model.RequestTimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if route.Provider.Type == "bedrock" {
		return s.runBedrockPlaygroundChat(ctx, route, system, messages, maxTokens, result)
	}

	protocol := providerProtocol(route.Provider)
	var endpoint string
	var payload map[string]any
	switch protocol {
	case "openai":
		endpoint = openAIChatEndpoint(route.Provider, route.Model.ModelID)
		withSystem := messages
		if system != "" {
			withSystem = append([]map[string]any{{"role": "system", "content": system}}, messages...)
		}
		payload = map[string]any{
			"model":      route.Model.ModelID,
			"messages":   withSystem,
			"max_tokens": maxTokens,
			"stream":     false,
		}
	case "anthropic":
		endpoint = anthropicMessagesEndpoint(route.Provider)
		payload = map[string]any{
			"model":      route.Model.ModelID,
			"max_tokens": maxTokens,
			"messages":   messages,
		}
		if system != "" {
			payload["system"] = system
		}
	default:
		result.Error = "unsupported provider type"
		return result
	}
	// Reasoning-family models reject max_tokens and pinned temperature;
	// retry with adjusted parameters when the upstream names the offender.
	for attempt := 0; ; attempt++ {
		body, err := json.Marshal(payload)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			result.Error = err.Error()
			return result
		}
		req.Header.Set("Content-Type", "application/json")
		if protocol == "anthropic" {
			req.Header.Set("anthropic-version", "2023-06-01")
			setAnthropicAuthHeader(req, route.Provider)
		} else {
			setOpenAIAuthHeader(req, route.Provider)
		}
		req.Header.Set("User-Agent", "Phlox-GW/0.1")

		start := time.Now()
		resp, err := s.httpClient.Do(req)
		result.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		result.StatusCode = resp.StatusCode
		responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if protocol == "openai" && attempt < 2 {
				if adjusted, ok := openAIPayloadForUnsupportedParams(payload, resp.StatusCode, string(responseBody)); ok {
					payload = adjusted
					continue
				}
			}
			result.Error = limitString(string(responseBody), 2000)
			return result
		}
		var usage tokenUsage
		if protocol == "anthropic" {
			result.Content = anthropicResponseText(responseBody)
			usage = parseAnthropicUsage(responseBody)
		} else {
			result.Content = openAIResponseText(responseBody)
			usage = parseOpenAIUsage(responseBody)
		}
		result.InputTokens = usage.Input
		result.OutputTokens = usage.Output
		result.TotalTokens = usage.Total
		result.OK = true
		return result
	}
}

func (s *Server) runBedrockPlaygroundChat(ctx context.Context, route store.RoutedModel, system string, messages []map[string]any, maxTokens int, result playgroundChatResult) playgroundChatResult {
	raw := make([]any, 0, len(messages)+1)
	if system != "" {
		raw = append(raw, map[string]any{"role": "system", "content": system})
	}
	for _, m := range messages {
		raw = append(raw, m)
	}
	input, err := bedrockConverseInput(route.Model.ModelID, map[string]any{
		"messages":   raw,
		"max_tokens": float64(maxTokens),
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	client, err := s.bedrockClient(ctx, route.Provider)
	if err != nil {
		result.StatusCode = http.StatusBadGateway
		result.Error = err.Error()
		return result
	}
	start := time.Now()
	output, err := client.Converse(ctx, input)
	result.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		result.StatusCode = bedrockErrorStatus(err)
		result.Error = bedrockErrorMessage(err)
		return result
	}
	result.StatusCode = http.StatusOK
	result.Content = bedrockOutputText(output)
	usage := bedrockUsage(output)
	result.InputTokens = usage.Input
	result.OutputTokens = usage.Output
	result.TotalTokens = usage.Total
	result.OK = true
	return result
}

// openAIResponseText extracts the assistant text from a non-streaming OpenAI
// chat completion body, falling back to reasoning output for models that
// return their answer there.
func openAIResponseText(body []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	choices, _ := raw["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if text, err := openAIMessageText(message["content"]); err == nil && strings.TrimSpace(text) != "" {
		return text
	}
	reasoning, _ := firstPresent(message, "reasoning", "reasoning_content").(string)
	return reasoning
}

func anthropicResponseText(body []byte) string {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	var parts []string
	for _, block := range resp.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
