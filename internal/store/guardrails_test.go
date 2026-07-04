package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestGuardrailPolicyDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	policy, err := s.GetGuardrailPolicy(ctx)
	if err != nil {
		t.Fatalf("GetGuardrailPolicy default: %v", err)
	}
	if policy.ID != "default" || policy.Enabled || policy.InputAction != "redact" || policy.OutputAction != "redact" || !policy.DetectEmail || policy.RedactionText == "" {
		t.Fatalf("unexpected default guardrail policy: %#v", policy)
	}
	updated, err := s.UpdateGuardrailPolicy(ctx, GuardrailPolicy{
		Enabled:          true,
		InputAction:      "block",
		OutputAction:     "redact",
		DetectEmail:      true,
		DetectPhone:      false,
		DetectSSN:        true,
		DetectCreditCard: true,
		DetectAPIKey:     true,
		CustomPatterns: []GuardrailCustomPattern{{
			ID:            "employee-id",
			Name:          "Employee ID",
			Pattern:       `EMP-[0-9]+`,
			Action:        "redact",
			RedactionText: "[EMPLOYEE_ID]",
			Enabled:       true,
		}},
		RedactionText: "[PRIVATE]",
	})
	if err != nil {
		t.Fatalf("UpdateGuardrailPolicy: %v", err)
	}
	if !updated.Enabled || updated.InputAction != "block" || updated.OutputAction != "redact" || updated.DetectPhone || updated.RedactionText != "[PRIVATE]" || updated.StreamingBlockMode != "reject" {
		t.Fatalf("unexpected updated guardrail policy: %#v", updated)
	}
	if len(updated.CustomPatterns) != 1 || updated.CustomPatterns[0].Pattern != `EMP-[0-9]+` || updated.CustomPatterns[0].RedactionText != "[EMPLOYEE_ID]" {
		t.Fatalf("unexpected updated guardrail policy: %#v", updated)
	}
}
