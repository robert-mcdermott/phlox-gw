package httpapi

import (
	"errors"
	"net/http"
	urlpkg "net/url"
	"os"
	"strings"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func (s *Server) providers(w http.ResponseWriter, r *http.Request, _ store.User) {
	providers, err := s.store.ListProviders(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range providers {
		if providers[i].APIKey != "" {
			providers[i].APIKeyEnv = providers[i].APIKeyEnv + secretMarker(providers[i].APIKeyEnv)
		}
		providers[i].HasAWSSecret = providers[i].AWSSecretAccessKey != ""
		providers[i].HasBedrockAPIKey = providers[i].BedrockAPIKey != ""
	}
	respondJSON(w, http.StatusOK, providers)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request, admin store.User) {
	p, secrets, ok := s.providerFromRequest(w, r, "")
	if !ok {
		return
	}
	if !secrets.APIKey {
		p.APIKey = ""
	}
	if err := s.store.CreateProvider(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "provider id already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "provider.create", "provider", p.ID, p.Name, providerAuditDetails(p, secrets))
	respondJSON(w, http.StatusCreated, p)
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, admin store.User) {
	p, secrets, ok := s.providerFromRequest(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.store.UpdateProvider(r.Context(), p, secrets); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "provider not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "provider.update", "provider", p.ID, p.Name, providerAuditDetails(p, secrets))
	respondJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request, admin store.User) {
	id := r.PathValue("id")
	if err := s.store.DeleteProvider(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondError(w, http.StatusNotFound, "provider not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "provider.delete", "provider", id, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) providerFromRequest(w http.ResponseWriter, r *http.Request, pathID string) (store.Provider, store.ProviderSecretUpdate, bool) {
	var req struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		Type               string `json:"type"`
		BaseURL            string `json:"base_url"`
		APIKey             string `json:"api_key"`
		APIKeyEnv          string `json:"api_key_env"`
		AzureAPIVersion    string `json:"azure_api_version"`
		AWSRegion          string `json:"aws_region"`
		AWSAuthMethod      string `json:"aws_auth_method"`
		AWSAccessKeyID     string `json:"aws_access_key_id"`
		AWSSecretAccessKey string `json:"aws_secret_access_key"`
		AWSSessionToken    string `json:"aws_session_token"`
		BedrockAPIKey      string `json:"bedrock_api_key"`
		Enabled            bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	isCreate := pathID == ""
	id := strings.TrimSpace(req.ID)
	if pathID != "" {
		id = pathID
	}
	if id == "" || strings.TrimSpace(req.Name) == "" {
		respondError(w, http.StatusBadRequest, "provider id and name are required")
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	switch req.Type {
	case "openai", "anthropic", "azure-openai", "azure-anthropic", "google", "bedrock":
	default:
		respondError(w, http.StatusBadRequest, "provider type must be openai, anthropic, azure-openai, azure-anthropic, google, or bedrock")
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	if req.Type == "google" && strings.TrimSpace(req.BaseURL) == "" {
		// The Gemini API has a fixed public endpoint; a blank base URL means
		// the standard OpenAI-compatible surface.
		req.BaseURL = defaultGoogleBaseURL
	}
	if req.Type != "bedrock" && strings.TrimSpace(req.BaseURL) == "" {
		respondError(w, http.StatusBadRequest, "base_url is required for non-Bedrock providers")
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	p := store.Provider{
		ID:                 id,
		Name:               strings.TrimSpace(req.Name),
		Type:               req.Type,
		BaseURL:            strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		APIKey:             req.APIKey,
		APIKeyEnv:          strings.TrimSpace(req.APIKeyEnv),
		AzureAPIVersion:    strings.TrimSpace(req.AzureAPIVersion),
		AWSRegion:          strings.TrimSpace(req.AWSRegion),
		AWSAuthMethod:      strings.TrimSpace(req.AWSAuthMethod),
		AWSAccessKeyID:     strings.TrimSpace(req.AWSAccessKeyID),
		AWSSecretAccessKey: strings.TrimSpace(req.AWSSecretAccessKey),
		AWSSessionToken:    strings.TrimSpace(req.AWSSessionToken),
		BedrockAPIKey:      strings.TrimSpace(req.BedrockAPIKey),
		Enabled:            req.Enabled,
	}
	if p.Type != "azure-openai" {
		// api-version only applies to Azure OpenAI deployment endpoints.
		p.AzureAPIVersion = ""
	}
	secrets := store.ProviderSecretUpdate{APIKey: strings.TrimSpace(req.APIKey) != ""}
	if p.Type != "bedrock" {
		// Wipe Bedrock-only settings so stale credentials do not linger
		// after a provider changes type.
		p.AWSRegion = ""
		p.AWSAuthMethod = ""
		p.AWSAccessKeyID = ""
		p.AWSSecretAccessKey = ""
		p.AWSSessionToken = ""
		p.BedrockAPIKey = ""
		secrets.AWSCredentials = true
		secrets.BedrockAPIKey = true
		return p, secrets, true
	}
	// Bedrock providers authenticate via AWS, not a base URL or bearer env var.
	p.BaseURL = ""
	p.APIKey = ""
	p.APIKeyEnv = ""
	secrets.APIKey = true
	if p.AWSAuthMethod == "" {
		p.AWSAuthMethod = "chain"
	}
	switch p.AWSAuthMethod {
	case "chain":
		p.AWSAccessKeyID = ""
		p.AWSSecretAccessKey = ""
		p.AWSSessionToken = ""
		p.BedrockAPIKey = ""
		secrets.AWSCredentials = true
		secrets.BedrockAPIKey = true
	case "keys":
		if p.AWSAccessKeyID == "" {
			respondError(w, http.StatusBadRequest, "aws_access_key_id is required for access-key auth")
			return store.Provider{}, store.ProviderSecretUpdate{}, false
		}
		if isCreate && p.AWSSecretAccessKey == "" {
			respondError(w, http.StatusBadRequest, "aws_secret_access_key is required for access-key auth")
			return store.Provider{}, store.ProviderSecretUpdate{}, false
		}
		secrets.AWSCredentials = p.AWSSecretAccessKey != ""
		p.BedrockAPIKey = ""
		secrets.BedrockAPIKey = true
	case "api_key":
		if isCreate && p.BedrockAPIKey == "" {
			respondError(w, http.StatusBadRequest, "bedrock_api_key is required for API-key auth")
			return store.Provider{}, store.ProviderSecretUpdate{}, false
		}
		secrets.BedrockAPIKey = p.BedrockAPIKey != ""
		p.AWSAccessKeyID = ""
		p.AWSSecretAccessKey = ""
		p.AWSSessionToken = ""
		secrets.AWSCredentials = true
	default:
		respondError(w, http.StatusBadRequest, "aws_auth_method must be chain, keys, or api_key")
		return store.Provider{}, store.ProviderSecretUpdate{}, false
	}
	return p, secrets, true
}

func providerAuditDetails(p store.Provider, secrets store.ProviderSecretUpdate) map[string]any {
	return map[string]any{
		"id":                      p.ID,
		"name":                    p.Name,
		"type":                    p.Type,
		"base_url":                p.BaseURL,
		"api_key_env":             p.APIKeyEnv,
		"azure_api_version":       p.AzureAPIVersion,
		"direct_secret_updated":   secrets.APIKey,
		"aws_region":              p.AWSRegion,
		"aws_auth_method":         p.AWSAuthMethod,
		"aws_credentials_updated": secrets.AWSCredentials,
		"bedrock_api_key_updated": secrets.BedrockAPIKey,
		"enabled":                 p.Enabled,
	}
}

const defaultAzureAPIVersion = "2024-10-21"

const defaultGoogleBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

func providerProtocol(p store.Provider) string {
	switch p.Type {
	case "azure-openai", "google":
		return "openai"
	case "azure-anthropic":
		return "anthropic"
	default:
		return p.Type
	}
}

func ensureStreamUsageOption(p store.Provider, raw map[string]any) {
	if p.Type != "google" {
		return
	}
	if stream, _ := raw["stream"].(bool); !stream {
		return
	}
	if _, ok := raw["stream_options"]; ok {
		return
	}
	raw["stream_options"] = map[string]any{"include_usage": true}
}

func azureAPIVersion(p store.Provider) string {
	if v := strings.TrimSpace(p.AzureAPIVersion); v != "" {
		return v
	}
	return defaultAzureAPIVersion
}

func openAIChatEndpoint(p store.Provider, upstreamModel string) string {
	base := strings.TrimRight(p.BaseURL, "/")
	if p.Type == "azure-openai" {
		return base + "/openai/deployments/" + urlpkg.PathEscape(upstreamModel) + "/chat/completions?api-version=" + urlpkg.QueryEscape(azureAPIVersion(p))
	}
	return base + "/chat/completions"
}

func anthropicMessagesEndpoint(p store.Provider) string {
	return strings.TrimRight(p.BaseURL, "/") + "/v1/messages"
}

func setOpenAIAuthHeader(req *http.Request, p store.Provider) {
	apiKey := providerAPIKey(p)
	if apiKey == "" {
		return
	}
	if p.Type == "azure-openai" {
		req.Header.Set("api-key", apiKey)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

func setAnthropicAuthHeader(req *http.Request, p store.Provider) {
	apiKey := providerAPIKey(p)
	if apiKey == "" {
		return
	}
	req.Header.Set("x-api-key", apiKey)
	if p.Type == "azure-anthropic" {
		// Azure AI Foundry accepts the Anthropic-style x-api-key header, but
		// some gateway configurations only honor Azure's api-key header, so
		// send both.
		req.Header.Set("api-key", apiKey)
	}
}

func providerAPIKey(p store.Provider) string {
	if p.APIKeyEnv != "" {
		if value := os.Getenv(p.APIKeyEnv); value != "" {
			return value
		}
	}
	return p.APIKey
}
