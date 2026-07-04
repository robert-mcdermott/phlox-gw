package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestAdminActionCreatesAuditLog(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := st.EnsureSeedData(hash); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
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

	loginResp := jsonRequest(t, handler, http.MethodPost, "/api/auth/login", "", map[string]any{"username": "admin", "password": "admin"})
	var login struct {
		Token string `json:"token"`
	}
	decodeRecorder(t, loginResp, &login)
	if login.Token == "" {
		t.Fatal("missing login token")
	}

	username := "audit_user"
	createResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/users", login.Token, map[string]any{
		"username":     username,
		"password":     "Passw0rd!",
		"email":        "audit_user@localhost",
		"display_name": "Audit User",
		"department":   "Audit",
		"role":         "user",
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create user status = %d body = %s", createResp.Code, createResp.Body.String())
	}

	resp := jsonRequest(t, handler, http.MethodGet, "/api/admin/audit-log?limit=20", login.Token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("audit status = %d body = %s", resp.Code, resp.Body.String())
	}
	var items []store.AuditLog
	decodeRecorder(t, resp, &items)
	found := false
	for _, item := range items {
		if item.Action == "user.create" && item.TargetDisplay == username && item.ActorUsername == "admin" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("did not find user.create audit event in %#v", items)
	}
}

func TestAdminChargebackReportAndCSV(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	admin := store.User{ID: "user_admin", Username: "admin", Role: "admin", PasswordHash: "unused", AuthProvider: "local", IsActive: true}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	now := time.Now().UTC()
	// Middle of the previous month, immune to end-of-month normalization.
	previous := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -15)
	records := []store.UsageRecord{
		{ID: "u1", RequestID: "r1", UserID: "user_alice", Username: "alice", Department: "Engineering", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CostUSD: 1.25, CreatedAt: now},
		{ID: "u2", RequestID: "r2", UserID: "user_bob", Username: "bob", Department: "Engineering", InputTokens: 20, OutputTokens: 10, TotalTokens: 30, CostUSD: 0.75, CreatedAt: now},
		{ID: "u3", RequestID: "r3", UserID: "user_alice", Username: "alice", Department: "Engineering", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 9.00, CreatedAt: previous},
	}
	for _, record := range records {
		if err := st.InsertUsage(ctx, record); err != nil {
			t.Fatalf("InsertUsage: %v", err)
		}
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
	token := sessionToken(t, admin)

	// Default month is the current month.
	resp := jsonRequest(t, handler, http.MethodGet, "/api/admin/chargeback", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("chargeback status = %d body = %s", resp.Code, resp.Body.String())
	}
	var report store.MonthlyChargebackReport
	decodeRecorder(t, resp, &report)
	if report.Month != now.Format("2006-01") || report.Requests != 2 || report.CostUSD != 2.0 {
		t.Fatalf("unexpected current report: %#v", report)
	}
	if len(report.AvailableMonths) != 2 {
		t.Fatalf("unexpected available months: %#v", report.AvailableMonths)
	}

	// Previous months remain queryable for chargebacks.
	prevResp := jsonRequest(t, handler, http.MethodGet, "/api/admin/chargeback?month="+previous.Format("2006-01"), token, nil)
	if prevResp.Code != http.StatusOK {
		t.Fatalf("previous month status = %d body = %s", prevResp.Code, prevResp.Body.String())
	}
	var prevReport store.MonthlyChargebackReport
	decodeRecorder(t, prevResp, &prevReport)
	if prevReport.Requests != 1 || prevReport.CostUSD != 9.0 {
		t.Fatalf("unexpected previous report: %#v", prevReport)
	}

	badResp := jsonRequest(t, handler, http.MethodGet, "/api/admin/chargeback?month=junk", token, nil)
	if badResp.Code != http.StatusBadRequest {
		t.Fatalf("bad month status = %d body = %s", badResp.Code, badResp.Body.String())
	}

	csvResp := jsonRequest(t, handler, http.MethodGet, "/api/admin/chargeback/export.csv?month="+now.Format("2006-01"), token, nil)
	if csvResp.Code != http.StatusOK {
		t.Fatalf("csv status = %d body = %s", csvResp.Code, csvResp.Body.String())
	}
	if got := csvResp.Header().Get("Content-Disposition"); !strings.Contains(got, "phlox-gw-chargeback-"+now.Format("2006-01")+".csv") {
		t.Fatalf("unexpected content disposition: %q", got)
	}
	lines := strings.Split(strings.TrimSpace(csvResp.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header plus two rows, got %d lines: %s", len(lines), csvResp.Body.String())
	}
	if !strings.HasPrefix(lines[0], "month,department,user_id,username,requests") {
		t.Fatalf("unexpected csv header: %s", lines[0])
	}
	if !strings.Contains(lines[1], "Engineering,user_alice,alice,1,") {
		t.Fatalf("expected alice as highest spender first: %s", lines[1])
	}
}

func TestAdminRequestLogSearchAndCSV(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSeedData("admin-hash"); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
	}
	admin, err := st.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if err := st.InsertRequestLog(ctx, store.RequestLogRecord{
		ID:              "reqlog_admin_api",
		RequestID:       "req_search_1",
		UserID:          admin.ID,
		Username:        admin.Username,
		Department:      admin.Department,
		APIKeyID:        "key_search",
		APIKeyPrefix:    "pgw-sk-test",
		APIKeyName:      "Search key",
		ProviderID:      "openai",
		ProviderType:    "openai",
		ModelRoute:      "openai/gpt-4o-mini",
		UpstreamModelID: "gpt-4o-mini",
		Protocol:        "openai",
		Method:          "POST",
		Endpoint:        "/v1/chat/completions",
		StatusCode:      429,
		ErrorText:       "rate limited",
		ClientIP:        "203.0.113.9",
		UserAgent:       "test-agent",
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertRequestLog: %v", err)
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
	token := sessionToken(t, admin)
	resp := jsonRequest(t, handler, http.MethodGet, "/api/admin/request-log?q=rate&status=error&limit=10", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("request log status = %d body = %s", resp.Code, resp.Body.String())
	}
	var result store.RequestLogSearchResult
	decodeRecorder(t, resp, &result)
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].RequestID != "req_search_1" {
		t.Fatalf("unexpected request log result: %#v", result)
	}
	csvResp := jsonRequest(t, handler, http.MethodGet, "/api/admin/request-log/export.csv?q=rate", token, nil)
	if csvResp.Code != http.StatusOK {
		t.Fatalf("request log csv status = %d body = %s", csvResp.Code, csvResp.Body.String())
	}
	if got := csvResp.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("csv content type = %q", got)
	}
	if body := csvResp.Body.String(); !strings.Contains(body, "req_search_1") || strings.Contains(body, "prompt") {
		t.Fatalf("unexpected csv body: %s", body)
	}
}
