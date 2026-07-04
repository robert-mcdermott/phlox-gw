package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func (s *Server) usage(w http.ResponseWriter, r *http.Request, user store.User) {
	summary, err := s.store.UsageForUser(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, summary)
}

func (s *Server) adminUsage(w http.ResponseWriter, r *http.Request, _ store.User) {
	summary, err := s.store.UsageAll(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, summary)
}

func (s *Server) adminUsageTimeSeries(w http.ResponseWriter, r *http.Request, _ store.User) {
	days, ok := parseDaysQuery(w, r, 30)
	if !ok {
		return
	}
	points, err := s.store.UsageTimeSeries(r.Context(), days, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, points)
}

func (s *Server) adminUsageDrilldowns(w http.ResponseWriter, r *http.Request, _ store.User) {
	days, ok := parseDaysQuery(w, r, 30)
	if !ok {
		return
	}
	drilldowns, err := s.store.UsageDrilldowns(r.Context(), days, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, drilldowns)
}

func (s *Server) adminBudgetBurnDown(w http.ResponseWriter, r *http.Request, _ store.User) {
	items, err := s.store.BudgetBurnDown(r.Context(), time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, items)
}

func parseDaysQuery(w http.ResponseWriter, r *http.Request, fallback int) (int, bool) {
	days := fallback
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			respondError(w, http.StatusBadRequest, "days must be a positive integer")
			return 0, false
		}
		days = parsed
	}
	return days, true
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request, _ store.User) {
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	items, err := s.store.ListAuditLogs(r.Context(), limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, items)
}

func (s *Server) requestLogSearch(w http.ResponseWriter, r *http.Request, _ store.User) {
	query, ok := parseRequestLogQuery(w, r, 100)
	if !ok {
		return
	}
	result, err := s.store.SearchRequestLogs(r.Context(), query)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (s *Server) requestLogCSV(w http.ResponseWriter, r *http.Request, _ store.User) {
	query, ok := parseRequestLogQuery(w, r, 100000)
	if !ok {
		return
	}
	rows, err := s.store.RequestLogExport(r.Context(), query)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filename := "phlox-gw-request-log-" + time.Now().UTC().Format("20060102") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"created_at", "request_id", "username", "department", "api_key_id", "api_key_prefix", "api_key_name", "provider_id", "provider_type", "model_route", "upstream_model_id", "protocol", "method", "endpoint", "streaming", "input_tokens", "output_tokens", "total_tokens", "cost_usd", "latency_ms", "status_code", "error", "client_ip", "user_agent"})
	for _, row := range rows {
		_ = cw.Write([]string{
			row.CreatedAt.Format(time.RFC3339Nano),
			row.RequestID,
			row.Username,
			row.Department,
			row.APIKeyID,
			row.APIKeyPrefix,
			row.APIKeyName,
			row.ProviderID,
			row.ProviderType,
			row.ModelRoute,
			row.UpstreamModelID,
			row.Protocol,
			row.Method,
			row.Endpoint,
			strconv.FormatBool(row.Streaming),
			strconv.Itoa(row.InputTokens),
			strconv.Itoa(row.OutputTokens),
			strconv.Itoa(row.TotalTokens),
			strconv.FormatFloat(row.CostUSD, 'f', 6, 64),
			strconv.FormatInt(row.LatencyMS, 10),
			strconv.Itoa(row.StatusCode),
			row.ErrorText,
			row.ClientIP,
			row.UserAgent,
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.logger.Warn("request log csv write failed", "error", err)
	}
}

func parseRequestLogQuery(w http.ResponseWriter, r *http.Request, defaultLimit int) (store.RequestLogQuery, bool) {
	values := r.URL.Query()
	query := store.RequestLogQuery{
		Search:       strings.TrimSpace(values.Get("q")),
		Username:     strings.TrimSpace(values.Get("username")),
		Department:   strings.TrimSpace(values.Get("department")),
		APIKeyID:     strings.TrimSpace(values.Get("api_key_id")),
		ProviderID:   strings.TrimSpace(values.Get("provider_id")),
		ProviderType: strings.TrimSpace(values.Get("provider_type")),
		ModelRoute:   strings.TrimSpace(values.Get("model")),
		Protocol:     strings.TrimSpace(values.Get("protocol")),
		Endpoint:     strings.TrimSpace(values.Get("endpoint")),
		Status:       strings.TrimSpace(values.Get("status")),
		Limit:        defaultLimit,
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return store.RequestLogQuery{}, false
		}
		query.Limit = limit
	}
	if raw := strings.TrimSpace(values.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			respondError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return store.RequestLogQuery{}, false
		}
		query.Offset = offset
	}
	if raw := strings.TrimSpace(values.Get("streaming")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "streaming must be true or false")
			return store.RequestLogQuery{}, false
		}
		query.Streaming = &parsed
	}
	if raw := strings.TrimSpace(values.Get("from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "from must be RFC3339")
			return store.RequestLogQuery{}, false
		}
		query.From = &parsed
	}
	if raw := strings.TrimSpace(values.Get("to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "to must be RFC3339")
			return store.RequestLogQuery{}, false
		}
		query.To = &parsed
	}
	if query.From == nil {
		if raw := strings.TrimSpace(values.Get("days")); raw != "" {
			days, err := strconv.Atoi(raw)
			if err != nil || days <= 0 {
				respondError(w, http.StatusBadRequest, "days must be a positive integer")
				return store.RequestLogQuery{}, false
			}
			from := time.Now().UTC().AddDate(0, 0, -days)
			query.From = &from
		}
	}
	return query, true
}

func (s *Server) adminUsageCSV(w http.ResponseWriter, r *http.Request, _ store.User) {
	rows, err := s.store.UsageExport(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filename := "phlox-gw-usage-" + time.Now().UTC().Format("20060102") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"created_at", "request_id", "username", "department", "api_key_id", "provider_id", "model", "protocol", "input_tokens", "output_tokens", "total_tokens", "cost_usd", "latency_ms", "status_code", "error"})
	for _, row := range rows {
		_ = cw.Write([]string{
			row.CreatedAt.Format(time.RFC3339Nano),
			row.RequestID,
			row.Username,
			row.Department,
			row.APIKeyID,
			row.ProviderID,
			row.Model,
			row.Protocol,
			strconv.Itoa(row.InputTokens),
			strconv.Itoa(row.OutputTokens),
			strconv.Itoa(row.TotalTokens),
			strconv.FormatFloat(row.CostUSD, 'f', 6, 64),
			strconv.FormatInt(row.LatencyMS, 10),
			strconv.Itoa(row.StatusCode),
			row.ErrorText,
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.logger.Warn("csv write failed", "error", err)
	}
}

func chargebackMonthFromRequest(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("month"))
	if raw == "" {
		return time.Now().UTC(), true
	}
	month, err := time.Parse("2006-01", raw)
	if err != nil {
		respondError(w, http.StatusBadRequest, "month must be formatted YYYY-MM")
		return time.Time{}, false
	}
	return month, true
}

func (s *Server) chargebackReport(w http.ResponseWriter, r *http.Request, _ store.User) {
	month, ok := chargebackMonthFromRequest(w, r)
	if !ok {
		return
	}
	report, err := s.store.ChargebackReport(r.Context(), month, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, report)
}

func (s *Server) chargebackCSV(w http.ResponseWriter, r *http.Request, _ store.User) {
	month, ok := chargebackMonthFromRequest(w, r)
	if !ok {
		return
	}
	report, err := s.store.ChargebackReport(r.Context(), month, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filename := "phlox-gw-chargeback-" + report.Month + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"month", "department", "user_id", "username", "requests", "input_tokens", "output_tokens", "total_tokens", "cost_usd", "department_budget_usd"})
	for _, dept := range report.Departments {
		budget := strconv.FormatFloat(dept.BudgetUSD, 'f', 2, 64)
		if dept.BudgetUSD <= 0 {
			budget = ""
		}
		for _, user := range dept.Users {
			_ = cw.Write([]string{
				report.Month,
				dept.Department,
				user.UserID,
				user.Username,
				strconv.FormatInt(user.Requests, 10),
				strconv.FormatInt(user.InputTokens, 10),
				strconv.FormatInt(user.OutputTokens, 10),
				strconv.FormatInt(user.TotalTokens, 10),
				strconv.FormatFloat(user.CostUSD, 'f', 6, 64),
				budget,
			})
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.logger.Warn("chargeback csv write failed", "error", err)
	}
}

func (s *Server) audit(r *http.Request, actor store.User, action, targetType, targetID, targetDisplay string, details map[string]any) {
	id, err := auth.RandomID("audit")
	if err != nil {
		s.logger.Warn("audit id failed", "error", err)
		return
	}
	detailText := "{}"
	if details != nil {
		if body, err := json.Marshal(details); err == nil {
			detailText = string(body)
		}
	}
	item := store.AuditLog{
		ID:            id,
		ActorUserID:   actor.ID,
		ActorUsername: actor.Username,
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		TargetDisplay: targetDisplay,
		Details:       limitString(detailText, 2000),
		IPAddress:     requestIP(r),
		UserAgent:     limitString(r.UserAgent(), 500),
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.store.InsertAuditLog(r.Context(), item); err != nil {
		s.logger.Warn("audit insert failed", "error", err, "action", action, "target_type", targetType, "target_id", targetID)
	}
}
