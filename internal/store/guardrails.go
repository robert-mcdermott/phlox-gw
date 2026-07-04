package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type GuardrailPolicy struct {
	ID                 string                   `json:"id"`
	Enabled            bool                     `json:"enabled"`
	InputAction        string                   `json:"input_action"`
	OutputAction       string                   `json:"output_action"`
	DetectEmail        bool                     `json:"detect_email"`
	DetectPhone        bool                     `json:"detect_phone"`
	DetectSSN          bool                     `json:"detect_ssn"`
	DetectCreditCard   bool                     `json:"detect_credit_card"`
	DetectAPIKey       bool                     `json:"detect_api_key"`
	CustomPatterns     []GuardrailCustomPattern `json:"custom_patterns"`
	RedactionText      string                   `json:"redaction_text"`
	StreamingBlockMode string                   `json:"streaming_block_mode"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

type GuardrailCustomPattern struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Pattern       string `json:"pattern"`
	Action        string `json:"action"`
	RedactionText string `json:"redaction_text"`
	Enabled       bool   `json:"enabled"`
}

func DefaultGuardrailPolicy() GuardrailPolicy {
	now := time.Now().UTC()
	return GuardrailPolicy{
		ID:                 "default",
		Enabled:            false,
		InputAction:        "redact",
		OutputAction:       "redact",
		DetectEmail:        true,
		DetectPhone:        true,
		DetectSSN:          true,
		DetectCreditCard:   true,
		DetectAPIKey:       true,
		RedactionText:      "[REDACTED]",
		StreamingBlockMode: "reject",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func (s *Store) GetGuardrailPolicy(ctx context.Context) (GuardrailPolicy, error) {
	row := s.queryRow(ctx, `
		SELECT id, enabled, input_action, output_action, detect_email, detect_phone, detect_ssn, detect_credit_card, detect_api_key,
		       custom_patterns, redaction_text, streaming_block_mode, created_at, updated_at
		FROM guardrail_policies
		WHERE id = 'default'`)
	p, err := scanGuardrailPolicy(row)
	if errors.Is(err, ErrNotFound) {
		return DefaultGuardrailPolicy(), nil
	}
	if err != nil {
		return GuardrailPolicy{}, err
	}
	return p, nil
}

func (s *Store) UpdateGuardrailPolicy(ctx context.Context, p GuardrailPolicy) (GuardrailPolicy, error) {
	p = normalizeGuardrailPolicy(p)
	now := time.Now().UTC()
	existing, err := s.GetGuardrailPolicy(ctx)
	if err != nil {
		return GuardrailPolicy{}, err
	}
	created := existing.CreatedAt
	if created.IsZero() {
		created = now
	}
	_, err = s.exec(ctx, `
		INSERT INTO guardrail_policies
		(id, enabled, input_action, output_action, detect_email, detect_phone, detect_ssn, detect_credit_card, detect_api_key,
		 custom_patterns, redaction_text, streaming_block_mode, created_at, updated_at)
		VALUES ('default', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled = excluded.enabled,
			input_action = excluded.input_action,
			output_action = excluded.output_action,
			detect_email = excluded.detect_email,
			detect_phone = excluded.detect_phone,
			detect_ssn = excluded.detect_ssn,
			detect_credit_card = excluded.detect_credit_card,
			detect_api_key = excluded.detect_api_key,
			custom_patterns = excluded.custom_patterns,
			redaction_text = excluded.redaction_text,
			streaming_block_mode = excluded.streaming_block_mode,
			updated_at = excluded.updated_at`,
		boolInt(p.Enabled), p.InputAction, p.OutputAction, boolInt(p.DetectEmail), boolInt(p.DetectPhone), boolInt(p.DetectSSN),
		boolInt(p.DetectCreditCard), boolInt(p.DetectAPIKey), encodeGuardrailCustomPatterns(p.CustomPatterns), p.RedactionText, p.StreamingBlockMode, formatTime(created), formatTime(now))
	if err != nil {
		return GuardrailPolicy{}, err
	}
	return s.GetGuardrailPolicy(ctx)
}

func scanGuardrailPolicy(row scanner) (GuardrailPolicy, error) {
	var p GuardrailPolicy
	var enabled, email, phone, ssn, creditCard, apiKey int
	var customPatterns, created, updated string
	err := row.Scan(&p.ID, &enabled, &p.InputAction, &p.OutputAction, &email, &phone, &ssn, &creditCard, &apiKey, &customPatterns, &p.RedactionText, &p.StreamingBlockMode, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return GuardrailPolicy{}, ErrNotFound
	}
	if err != nil {
		return GuardrailPolicy{}, err
	}
	p.Enabled = enabled == 1
	p.DetectEmail = email == 1
	p.DetectPhone = phone == 1
	p.DetectSSN = ssn == 1
	p.DetectCreditCard = creditCard == 1
	p.DetectAPIKey = apiKey == 1
	p.CustomPatterns = decodeGuardrailCustomPatterns(customPatterns)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return normalizeGuardrailPolicy(p), nil
}

func normalizeGuardrailPolicy(p GuardrailPolicy) GuardrailPolicy {
	p.ID = "default"
	switch p.InputAction {
	case "off", "redact", "block":
	default:
		p.InputAction = "redact"
	}
	switch p.OutputAction {
	case "off", "redact", "block":
	default:
		p.OutputAction = "redact"
	}
	if strings.TrimSpace(p.RedactionText) == "" {
		p.RedactionText = "[REDACTED]"
	} else {
		p.RedactionText = strings.TrimSpace(p.RedactionText)
	}
	if p.StreamingBlockMode != "reject" {
		p.StreamingBlockMode = "reject"
	}
	p.CustomPatterns = normalizeGuardrailCustomPatterns(p.CustomPatterns, p.RedactionText)
	return p
}

func normalizeGuardrailCustomPatterns(patterns []GuardrailCustomPattern, defaultRedaction string) []GuardrailCustomPattern {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]GuardrailCustomPattern, 0, len(patterns))
	for i, pattern := range patterns {
		pattern.ID = strings.TrimSpace(pattern.ID)
		pattern.Name = strings.TrimSpace(pattern.Name)
		pattern.Pattern = strings.TrimSpace(pattern.Pattern)
		pattern.Action = strings.TrimSpace(strings.ToLower(pattern.Action))
		pattern.RedactionText = strings.TrimSpace(pattern.RedactionText)
		if pattern.Pattern == "" {
			continue
		}
		if pattern.ID == "" {
			pattern.ID = fmt.Sprintf("custom-%d", i+1)
		}
		if pattern.Name == "" {
			pattern.Name = pattern.ID
		}
		if pattern.Action != "block" {
			pattern.Action = "redact"
		}
		if pattern.RedactionText == "" {
			pattern.RedactionText = defaultRedaction
		}
		out = append(out, pattern)
		if len(out) >= 100 {
			break
		}
	}
	return out
}

func encodeGuardrailCustomPatterns(patterns []GuardrailCustomPattern) string {
	if len(patterns) == 0 {
		return "[]"
	}
	body, err := json.Marshal(patterns)
	if err != nil {
		return "[]"
	}
	return string(body)
}

func decodeGuardrailCustomPatterns(value string) []GuardrailCustomPattern {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var patterns []GuardrailCustomPattern
	if err := json.Unmarshal([]byte(value), &patterns); err != nil {
		return nil
	}
	return patterns
}
