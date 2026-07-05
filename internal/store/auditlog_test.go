package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditLogInsertAndList(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.InsertAuditLog(ctx, AuditLog{
		ID:            "audit_1",
		ActorUserID:   "user_admin",
		ActorUsername: "admin",
		Action:        "provider.create",
		TargetType:    "provider",
		TargetID:      "local-vllm",
		TargetDisplay: "Local vLLM",
		Details:       `{"type":"openai","enabled":true}`,
		IPAddress:     "127.0.0.1",
		UserAgent:     "test",
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertAuditLog: %v", err)
	}
	items, err := s.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one audit item, got %d", len(items))
	}
	got := items[0]
	if got.Action != "provider.create" || got.TargetID != "local-vllm" || got.ActorUsername != "admin" || got.Details == "" {
		t.Fatalf("unexpected audit item: %#v", got)
	}
}
