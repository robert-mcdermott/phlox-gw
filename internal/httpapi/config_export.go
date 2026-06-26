package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

const (
	adminConfigExportKind       = "phlox-gw.admin_config_export"
	adminConfigExportVersion    = 1
	adminConfigExportSchema     = 1
	adminConfigSigningKeyName   = "phlox-gw-signing-key.json"
	adminConfigSigningAlgorithm = "ed25519"
)

type signedAdminConfigExport struct {
	Version     int                      `json:"version"`
	Kind        string                   `json:"kind"`
	GeneratedAt time.Time                `json:"generated_at"`
	Signature   adminConfigSignature     `json:"signature"`
	Payload     adminConfigExportPayload `json:"payload"`
}

type adminConfigSignedContent struct {
	Version     int                      `json:"version"`
	Kind        string                   `json:"kind"`
	GeneratedAt time.Time                `json:"generated_at"`
	Payload     adminConfigExportPayload `json:"payload"`
}

type adminConfigSignature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Value     string `json:"value"`
}

type adminConfigExportPayload struct {
	SchemaVersion int                          `json:"schema_version"`
	Providers     []adminProviderConfigExport  `json:"providers"`
	Models        []adminModelConfigExport     `json:"models"`
	Budgets       []adminBudgetConfigExport    `json:"budgets"`
	RateLimits    []adminRateLimitConfigExport `json:"rate_limits"`
	Guardrails    adminGuardrailConfigExport   `json:"guardrails"`
	Excluded      []string                     `json:"excluded"`
	Notes         []string                     `json:"notes"`
}

type adminProviderConfigExport struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	BaseURL      string `json:"base_url"`
	APIKeyEnv    string `json:"api_key_env,omitempty"`
	AWSRegion    string `json:"aws_region,omitempty"`
	Enabled      bool   `json:"enabled"`
	SecretSource string `json:"secret_source"`
}

type adminModelConfigExport struct {
	ID                   string  `json:"id"`
	ProviderID           string  `json:"provider_id"`
	ModelID              string  `json:"model_id"`
	Route                string  `json:"route"`
	DisplayName          string  `json:"display_name"`
	InputCostPerMillion  float64 `json:"input_cost_per_million"`
	OutputCostPerMillion float64 `json:"output_cost_per_million"`
	ContextWindow        int     `json:"context_window"`
	SupportsStreaming    bool    `json:"supports_streaming"`
	Enabled              bool    `json:"enabled"`
	FallbackRoutes       string  `json:"fallback_routes,omitempty"`
	WeightedRoutes       string  `json:"weighted_routes,omitempty"`
	RetryAttempts        int     `json:"retry_attempts"`
	RequestTimeoutMS     int     `json:"request_timeout_ms"`
	HealthRoutingEnabled bool    `json:"health_routing_enabled"`
}

type adminBudgetConfigExport struct {
	ID         string  `json:"id"`
	ScopeType  string  `json:"scope_type"`
	ScopeValue string  `json:"scope_value"`
	LimitUSD   float64 `json:"limit_usd"`
	WarnPct    float64 `json:"warn_pct"`
	IsActive   bool    `json:"is_active"`
}

type adminRateLimitConfigExport struct {
	ID         string `json:"id"`
	ScopeType  string `json:"scope_type"`
	ScopeValue string `json:"scope_value"`
	RPMLimit   int    `json:"rpm_limit"`
	TPMLimit   int    `json:"tpm_limit"`
	IsActive   bool   `json:"is_active"`
}

type adminGuardrailConfigExport struct {
	ID                 string                              `json:"id"`
	Enabled            bool                                `json:"enabled"`
	InputAction        string                              `json:"input_action"`
	OutputAction       string                              `json:"output_action"`
	DetectEmail        bool                                `json:"detect_email"`
	DetectPhone        bool                                `json:"detect_phone"`
	DetectSSN          bool                                `json:"detect_ssn"`
	DetectCreditCard   bool                                `json:"detect_credit_card"`
	DetectAPIKey       bool                                `json:"detect_api_key"`
	CustomPatterns     []adminGuardrailCustomPatternExport `json:"custom_patterns"`
	RedactionText      string                              `json:"redaction_text"`
	StreamingBlockMode string                              `json:"streaming_block_mode"`
}

type adminGuardrailCustomPatternExport struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Pattern       string `json:"pattern"`
	Action        string `json:"action"`
	RedactionText string `json:"redaction_text"`
	Enabled       bool   `json:"enabled"`
}

type adminConfigSigningKeyFile struct {
	KeyID      string `json:"key_id"`
	Algorithm  string `json:"algorithm"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
	CreatedAt  string `json:"created_at"`
}

type adminConfigSigningKey struct {
	KeyID      string
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

func (b signedAdminConfigExport) signedContent() adminConfigSignedContent {
	return adminConfigSignedContent{
		Version:     b.Version,
		Kind:        b.Kind,
		GeneratedAt: b.GeneratedAt,
		Payload:     b.Payload,
	}
}

func (s *Server) adminConfigExport(w http.ResponseWriter, r *http.Request, admin store.User) {
	bundle, err := s.buildSignedAdminConfigExport(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, admin, "config.export", "configuration", "admin_config", "Signed admin configuration export", map[string]any{
		"providers": len(bundle.Payload.Providers),
		"models":    len(bundle.Payload.Models),
		"budgets":   len(bundle.Payload.Budgets),
		"limits":    len(bundle.Payload.RateLimits),
		"key_id":    bundle.Signature.KeyID,
	})
	w.Header().Set("Content-Disposition", `attachment; filename="phlox-gw-admin-config-`+time.Now().UTC().Format("20060102T150405Z")+`.json"`)
	respondJSON(w, http.StatusOK, bundle)
}

func (s *Server) buildSignedAdminConfigExport(ctx context.Context) (signedAdminConfigExport, error) {
	payload, err := s.adminConfigExportPayload(ctx)
	if err != nil {
		return signedAdminConfigExport{}, err
	}
	now := time.Now().UTC()
	bundle := signedAdminConfigExport{
		Version:     adminConfigExportVersion,
		Kind:        adminConfigExportKind,
		GeneratedAt: now,
		Payload:     payload,
	}
	key, err := s.adminConfigSigningKey()
	if err != nil {
		return signedAdminConfigExport{}, err
	}
	body, err := json.Marshal(bundle.signedContent())
	if err != nil {
		return signedAdminConfigExport{}, err
	}
	signature := ed25519.Sign(key.PrivateKey, body)
	bundle.Signature = adminConfigSignature{
		Algorithm: adminConfigSigningAlgorithm,
		KeyID:     key.KeyID,
		PublicKey: base64.StdEncoding.EncodeToString(key.PublicKey),
		Value:     base64.StdEncoding.EncodeToString(signature),
	}
	return bundle, nil
}

func (s *Server) adminConfigExportPayload(ctx context.Context) (adminConfigExportPayload, error) {
	providers, err := s.store.ListProviders(ctx)
	if err != nil {
		return adminConfigExportPayload{}, err
	}
	models, err := s.store.ListModels(ctx, true)
	if err != nil {
		return adminConfigExportPayload{}, err
	}
	budgets, err := s.store.ListBudgets(ctx)
	if err != nil {
		return adminConfigExportPayload{}, err
	}
	rateLimits, err := s.store.ListRateLimits(ctx)
	if err != nil {
		return adminConfigExportPayload{}, err
	}
	guardrails, err := s.store.GetGuardrailPolicy(ctx)
	if err != nil {
		return adminConfigExportPayload{}, err
	}
	return adminConfigExportPayload{
		SchemaVersion: adminConfigExportSchema,
		Providers:     exportProviders(providers),
		Models:        exportModels(models),
		Budgets:       exportBudgets(budgets),
		RateLimits:    exportRateLimits(rateLimits),
		Guardrails:    exportGuardrails(guardrails),
		Excluded: []string{
			"provider direct API key values",
			"users and password hashes",
			"user-minted API keys and key hashes",
			"sessions",
			"usage ledger rows",
			"request metadata logs",
			"audit log rows",
			"provider runtime health state",
			"runtime environment secrets such as OIDC client secret and session secret",
		},
		Notes: []string{
			"Provider api_key_env values are exported so target environments can provide secrets through environment variables.",
			"Providers that used a direct stored API key are marked direct-redacted and need their secret re-entered after import or restore.",
			"The signature covers version, kind, generated_at, and payload.",
		},
	}, nil
}

func exportProviders(providers []store.Provider) []adminProviderConfigExport {
	out := make([]adminProviderConfigExport, 0, len(providers))
	for _, p := range providers {
		out = append(out, adminProviderConfigExport{
			ID:           p.ID,
			Name:         p.Name,
			Type:         p.Type,
			BaseURL:      p.BaseURL,
			APIKeyEnv:    p.APIKeyEnv,
			AWSRegion:    p.AWSRegion,
			Enabled:      p.Enabled,
			SecretSource: providerSecretSource(p),
		})
	}
	return out
}

func providerSecretSource(p store.Provider) string {
	if p.Type == "bedrock" {
		return "aws-credential-chain"
	}
	if strings.TrimSpace(p.APIKeyEnv) != "" {
		return "environment"
	}
	if p.APIKey != "" {
		return "direct-redacted"
	}
	return "none"
}

func exportModels(models []store.Model) []adminModelConfigExport {
	out := make([]adminModelConfigExport, 0, len(models))
	for _, m := range models {
		out = append(out, adminModelConfigExport{
			ID:                   m.ID,
			ProviderID:           m.ProviderID,
			ModelID:              m.ModelID,
			Route:                m.Route,
			DisplayName:          m.DisplayName,
			InputCostPerMillion:  m.InputCostPerMillion,
			OutputCostPerMillion: m.OutputCostPerMillion,
			ContextWindow:        m.ContextWindow,
			SupportsStreaming:    m.SupportsStreaming,
			Enabled:              m.Enabled,
			FallbackRoutes:       m.FallbackRoutes,
			WeightedRoutes:       m.WeightedRoutes,
			RetryAttempts:        m.RetryAttempts,
			RequestTimeoutMS:     m.RequestTimeoutMS,
			HealthRoutingEnabled: m.HealthRoutingEnabled,
		})
	}
	return out
}

func exportBudgets(budgets []store.Budget) []adminBudgetConfigExport {
	out := make([]adminBudgetConfigExport, 0, len(budgets))
	for _, b := range budgets {
		out = append(out, adminBudgetConfigExport{
			ID:         b.ID,
			ScopeType:  b.ScopeType,
			ScopeValue: b.ScopeValue,
			LimitUSD:   b.LimitUSD,
			WarnPct:    b.WarnPct,
			IsActive:   b.IsActive,
		})
	}
	return out
}

func exportRateLimits(limits []store.RateLimit) []adminRateLimitConfigExport {
	out := make([]adminRateLimitConfigExport, 0, len(limits))
	for _, limit := range limits {
		out = append(out, adminRateLimitConfigExport{
			ID:         limit.ID,
			ScopeType:  limit.ScopeType,
			ScopeValue: limit.ScopeValue,
			RPMLimit:   limit.RPMLimit,
			TPMLimit:   limit.TPMLimit,
			IsActive:   limit.IsActive,
		})
	}
	return out
}

func exportGuardrails(policy store.GuardrailPolicy) adminGuardrailConfigExport {
	patterns := make([]adminGuardrailCustomPatternExport, 0, len(policy.CustomPatterns))
	for _, pattern := range policy.CustomPatterns {
		patterns = append(patterns, adminGuardrailCustomPatternExport{
			ID:            pattern.ID,
			Name:          pattern.Name,
			Pattern:       pattern.Pattern,
			Action:        pattern.Action,
			RedactionText: pattern.RedactionText,
			Enabled:       pattern.Enabled,
		})
	}
	return adminGuardrailConfigExport{
		ID:                 policy.ID,
		Enabled:            policy.Enabled,
		InputAction:        policy.InputAction,
		OutputAction:       policy.OutputAction,
		DetectEmail:        policy.DetectEmail,
		DetectPhone:        policy.DetectPhone,
		DetectSSN:          policy.DetectSSN,
		DetectCreditCard:   policy.DetectCreditCard,
		DetectAPIKey:       policy.DetectAPIKey,
		CustomPatterns:     patterns,
		RedactionText:      policy.RedactionText,
		StreamingBlockMode: policy.StreamingBlockMode,
	}
}

func (s *Server) adminConfigSigningKey() (adminConfigSigningKey, error) {
	path := s.adminConfigSigningKeyPath()
	if body, err := os.ReadFile(path); err == nil {
		return parseAdminConfigSigningKey(body)
	} else if !errors.Is(err, os.ErrNotExist) {
		return adminConfigSigningKey{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return adminConfigSigningKey{}, err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return adminConfigSigningKey{}, err
	}
	keyID := adminConfigKeyID(publicKey)
	file := adminConfigSigningKeyFile{
		KeyID:      keyID,
		Algorithm:  adminConfigSigningAlgorithm,
		PublicKey:  base64.StdEncoding.EncodeToString(publicKey),
		PrivateKey: base64.StdEncoding.EncodeToString(privateKey),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return adminConfigSigningKey{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return adminConfigSigningKey{}, readErr
		}
		return parseAdminConfigSigningKey(body)
	}
	if err != nil {
		return adminConfigSigningKey{}, err
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return adminConfigSigningKey{}, err
	}
	return adminConfigSigningKey{KeyID: keyID, PublicKey: publicKey, PrivateKey: ed25519.PrivateKey(privateKey)}, nil
}

func (s *Server) adminConfigSigningKeyPath() string {
	if path := strings.TrimSpace(s.cfg.ConfigSigningKeyFile); path != "" {
		return path
	}
	dir := strings.TrimSpace(s.cfg.DataDir)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, adminConfigSigningKeyName)
}

func parseAdminConfigSigningKey(body []byte) (adminConfigSigningKey, error) {
	var file adminConfigSigningKeyFile
	if err := json.Unmarshal(body, &file); err != nil {
		return adminConfigSigningKey{}, err
	}
	if file.Algorithm != "" && file.Algorithm != adminConfigSigningAlgorithm {
		return adminConfigSigningKey{}, errors.New("unsupported admin config signing key algorithm")
	}
	privateKey, err := base64.StdEncoding.DecodeString(file.PrivateKey)
	if err != nil {
		return adminConfigSigningKey{}, err
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return adminConfigSigningKey{}, errors.New("invalid admin config signing private key")
	}
	publicKey := ed25519.PrivateKey(privateKey).Public().(ed25519.PublicKey)
	if file.PublicKey != "" {
		storedPublicKey, err := base64.StdEncoding.DecodeString(file.PublicKey)
		if err != nil {
			return adminConfigSigningKey{}, err
		}
		if !ed25519.PublicKey(storedPublicKey).Equal(publicKey) {
			return adminConfigSigningKey{}, errors.New("admin config signing key public/private mismatch")
		}
	}
	keyID := file.KeyID
	if keyID == "" {
		keyID = adminConfigKeyID(publicKey)
	}
	return adminConfigSigningKey{KeyID: keyID, PublicKey: publicKey, PrivateKey: privateKey}, nil
}

func adminConfigKeyID(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(sum[:12])
}
