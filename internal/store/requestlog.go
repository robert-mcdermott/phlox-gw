package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type RequestLogRecord struct {
	ID              string    `json:"id"`
	RequestID       string    `json:"request_id"`
	UserID          string    `json:"user_id"`
	Username        string    `json:"username"`
	Department      string    `json:"department"`
	APIKeyID        string    `json:"api_key_id"`
	APIKeyPrefix    string    `json:"api_key_prefix"`
	APIKeyName      string    `json:"api_key_name"`
	ProviderID      string    `json:"provider_id"`
	ProviderType    string    `json:"provider_type"`
	ModelRoute      string    `json:"model_route"`
	UpstreamModelID string    `json:"upstream_model_id"`
	Protocol        string    `json:"protocol"`
	Method          string    `json:"method"`
	Endpoint        string    `json:"endpoint"`
	Streaming       bool      `json:"streaming"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	TotalTokens     int       `json:"total_tokens"`
	CostUSD         float64   `json:"cost_usd"`
	LatencyMS       int64     `json:"latency_ms"`
	StatusCode      int       `json:"status_code"`
	ErrorText       string    `json:"error_text"`
	ClientIP        string    `json:"client_ip"`
	UserAgent       string    `json:"user_agent"`
	CreatedAt       time.Time `json:"created_at"`
}

type RequestLogQuery struct {
	Search       string
	Username     string
	Department   string
	APIKeyID     string
	ProviderID   string
	ProviderType string
	ModelRoute   string
	Protocol     string
	Endpoint     string
	Status       string
	Streaming    *bool
	From         *time.Time
	To           *time.Time
	Limit        int
	Offset       int
}

type RequestLogSearchResult struct {
	Items  []RequestLogRecord `json:"items"`
	Total  int64              `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

func (s *Store) InsertRequestLog(ctx context.Context, r RequestLogRecord) error {
	now := r.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.exec(ctx, `
		INSERT INTO request_log
		(id, request_id, user_id, username, department, api_key_id, api_key_prefix, api_key_name, provider_id, provider_type,
		 model_route, upstream_model_id, protocol, method, endpoint, streaming, input_tokens, output_tokens, total_tokens, cost_usd,
		 latency_ms, status_code, error_text, client_ip, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		r.ID, r.RequestID, r.UserID, r.Username, r.Department, r.APIKeyID, r.APIKeyPrefix, r.APIKeyName, r.ProviderID, r.ProviderType,
		r.ModelRoute, r.UpstreamModelID, r.Protocol, r.Method, r.Endpoint, boolInt(r.Streaming), r.InputTokens, r.OutputTokens, r.TotalTokens, r.CostUSD,
		r.LatencyMS, r.StatusCode, limitProviderError(r.ErrorText), r.ClientIP, r.UserAgent, formatTime(now))
	return err
}

func (s *Store) SearchRequestLogs(ctx context.Context, q RequestLogQuery) (RequestLogSearchResult, error) {
	q.Limit = clampQueryLimit(q.Limit, 100, 500)
	if q.Offset < 0 {
		q.Offset = 0
	}
	where, args := requestLogWhere(q)
	var total int64
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM request_log `+where, args...).Scan(&total); err != nil {
		return RequestLogSearchResult{}, err
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, q.Limit, q.Offset)
	rows, err := s.query(ctx, `
		SELECT id, request_id, user_id, username, department, api_key_id, api_key_prefix, api_key_name, provider_id, provider_type,
		       model_route, upstream_model_id, protocol, method, endpoint, streaming, input_tokens, output_tokens, total_tokens, cost_usd,
		       latency_ms, status_code, error_text, client_ip, user_agent, created_at
		FROM request_log `+where+`
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return RequestLogSearchResult{}, err
	}
	defer rows.Close()
	items := []RequestLogRecord{}
	for rows.Next() {
		item, err := scanRequestLog(rows)
		if err != nil {
			return RequestLogSearchResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RequestLogSearchResult{}, err
	}
	return RequestLogSearchResult{Items: items, Total: total, Limit: q.Limit, Offset: q.Offset}, nil
}

func (s *Store) RequestLogExport(ctx context.Context, q RequestLogQuery) ([]RequestLogRecord, error) {
	q.Limit = clampQueryLimit(q.Limit, 100000, 100000)
	q.Offset = 0
	where, args := requestLogWhere(q)
	args = append(args, q.Limit)
	rows, err := s.query(ctx, `
		SELECT id, request_id, user_id, username, department, api_key_id, api_key_prefix, api_key_name, provider_id, provider_type,
		       model_route, upstream_model_id, protocol, method, endpoint, streaming, input_tokens, output_tokens, total_tokens, cost_usd,
		       latency_ms, status_code, error_text, client_ip, user_agent, created_at
		FROM request_log `+where+`
		ORDER BY created_at DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestLogRecord
	for rows.Next() {
		item, err := scanRequestLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func requestLogWhere(q RequestLogQuery) (string, []any) {
	var clauses []string
	var args []any
	addExact := func(column, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		clauses = append(clauses, column+" = ?")
		args = append(args, strings.TrimSpace(value))
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		clauses = append(clauses, `(lower(request_id) LIKE ? OR lower(username) LIKE ? OR lower(department) LIKE ? OR lower(api_key_id) LIKE ? OR lower(api_key_prefix) LIKE ? OR lower(api_key_name) LIKE ? OR lower(provider_id) LIKE ? OR lower(provider_type) LIKE ? OR lower(model_route) LIKE ? OR lower(upstream_model_id) LIKE ? OR lower(protocol) LIKE ? OR lower(endpoint) LIKE ? OR lower(error_text) LIKE ?)`)
		for i := 0; i < 13; i++ {
			args = append(args, like)
		}
	}
	addExact("username", q.Username)
	addExact("department", q.Department)
	addExact("api_key_id", q.APIKeyID)
	addExact("provider_id", q.ProviderID)
	addExact("provider_type", q.ProviderType)
	addExact("model_route", q.ModelRoute)
	addExact("protocol", q.Protocol)
	addExact("endpoint", q.Endpoint)
	if q.Streaming != nil {
		clauses = append(clauses, "streaming = ?")
		args = append(args, boolInt(*q.Streaming))
	}
	if q.From != nil {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, formatTime(q.From.UTC()))
	}
	if q.To != nil {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, formatTime(q.To.UTC()))
	}
	switch strings.ToLower(strings.TrimSpace(q.Status)) {
	case "", "any":
	case "success":
		clauses = append(clauses, "(status_code >= 200 AND status_code < 400 AND error_text = '')")
	case "error":
		clauses = append(clauses, "(status_code >= 400 OR error_text <> '')")
	case "4xx":
		clauses = append(clauses, "(status_code >= 400 AND status_code < 500)")
	case "5xx":
		clauses = append(clauses, "status_code >= 500")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func clampQueryLimit(value, fallback, max int) int {
	if value <= 0 {
		value = fallback
	}
	if value > max {
		value = max
	}
	return value
}

func scanRequestLog(row scanner) (RequestLogRecord, error) {
	var item RequestLogRecord
	var streaming int
	var created string
	err := row.Scan(&item.ID, &item.RequestID, &item.UserID, &item.Username, &item.Department, &item.APIKeyID, &item.APIKeyPrefix, &item.APIKeyName,
		&item.ProviderID, &item.ProviderType, &item.ModelRoute, &item.UpstreamModelID, &item.Protocol, &item.Method, &item.Endpoint, &streaming,
		&item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.CostUSD, &item.LatencyMS, &item.StatusCode, &item.ErrorText, &item.ClientIP, &item.UserAgent, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestLogRecord{}, ErrNotFound
	}
	if err != nil {
		return RequestLogRecord{}, err
	}
	item.Streaming = streaming == 1
	item.CostUSD = roundCost(item.CostUSD)
	item.CreatedAt = parseTime(created)
	return item, nil
}
