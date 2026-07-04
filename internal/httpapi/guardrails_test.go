package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestGuardrailPreviewCustomPatternAndInvalidRegex(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin := store.User{ID: "admin_guardrail_preview", Username: "admin", Role: "admin", PasswordHash: "unused", AuthProvider: "local", IsActive: true}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	policy := store.DefaultGuardrailPolicy()
	policy.Enabled = true
	policy.InputAction = "redact"
	policy.DetectEmail = false
	policy.DetectPhone = false
	policy.DetectSSN = false
	policy.DetectCreditCard = false
	policy.DetectAPIKey = false
	policy.CustomPatterns = []store.GuardrailCustomPattern{{
		ID:            "employee-id",
		Name:          "Employee ID",
		Pattern:       `EMP-[0-9]+`,
		Action:        "redact",
		RedactionText: "[EMPLOYEE_ID]",
		Enabled:       true,
	}}
	resp := jsonRequest(t, handler, http.MethodPost, "/api/admin/guardrails/test", sessionToken(t, admin), map[string]any{
		"phase":  "input",
		"text":   "Employee EMP-12345",
		"policy": policy,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var preview guardrailPreviewResponse
	decodeRecorder(t, resp, &preview)
	if !preview.Redacted || preview.Blocked || preview.Output != "Employee [EMPLOYEE_ID]" || len(preview.Findings) != 1 || preview.Findings[0] != "custom:Employee ID" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	policy.CustomPatterns[0].Pattern = `[`
	invalid := jsonRequest(t, handler, http.MethodPost, "/api/admin/guardrails/test", sessionToken(t, admin), map[string]any{
		"phase":  "input",
		"text":   "Employee EMP-12345",
		"policy": policy,
	})
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "invalid regex") {
		t.Fatalf("expected invalid regex error, status = %d body = %s", invalid.Code, invalid.Body.String())
	}
}
