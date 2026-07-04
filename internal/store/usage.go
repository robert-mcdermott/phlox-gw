package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

type BudgetBurnDownItem struct {
	Budget               Budget  `json:"budget"`
	SpendUSD             float64 `json:"spend_usd"`
	RemainingUSD         float64 `json:"remaining_usd"`
	Ratio                float64 `json:"ratio"`
	DailyAverageUSD      float64 `json:"daily_average_usd"`
	ProjectedMonthEndUSD float64 `json:"projected_month_end_usd"`
	ProjectedRatio       float64 `json:"projected_ratio"`
	DaysElapsed          int     `json:"days_elapsed"`
	DaysRemaining        int     `json:"days_remaining"`
	Blocked              bool    `json:"blocked"`
	Warning              bool    `json:"warning"`
}

type UsageRecord struct {
	ID           string
	RequestID    string
	UserID       string
	Username     string
	Department   string
	APIKeyID     string
	ProviderID   string
	Model        string
	Protocol     string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	LatencyMS    int64
	StatusCode   int
	ErrorText    string
	CreatedAt    time.Time
}

type UsageSummary struct {
	InputTokens  int64                 `json:"input_tokens"`
	OutputTokens int64                 `json:"output_tokens"`
	TotalTokens  int64                 `json:"total_tokens"`
	CostUSD      float64               `json:"cost_usd"`
	Requests     int64                 `json:"requests"`
	ByModel      []UsageSummaryByModel `json:"by_model"`
}

type UsageSummaryByModel struct {
	Model        string  `json:"model"`
	ProviderID   string  `json:"provider_id"`
	Department   string  `json:"department,omitempty"`
	Username     string  `json:"username,omitempty"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type UsageTimeSeriesPoint struct {
	Date         string  `json:"date"`
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

type UsageDrilldowns struct {
	Providers []UsageDrilldownRow `json:"providers"`
	Models    []UsageDrilldownRow `json:"models"`
}

type UsageDrilldownRow struct {
	ProviderID   string     `json:"provider_id"`
	Model        string     `json:"model,omitempty"`
	Requests     int64      `json:"requests"`
	Errors       int64      `json:"errors"`
	ErrorRate    float64    `json:"error_rate"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	TotalTokens  int64      `json:"total_tokens"`
	CostUSD      float64    `json:"cost_usd"`
	AvgLatencyMS float64    `json:"avg_latency_ms"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
}

type UsageExportRow struct {
	CreatedAt    time.Time `json:"created_at"`
	RequestID    string    `json:"request_id"`
	Username     string    `json:"username"`
	Department   string    `json:"department"`
	APIKeyID     string    `json:"api_key_id"`
	ProviderID   string    `json:"provider_id"`
	Model        string    `json:"model"`
	Protocol     string    `json:"protocol"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	LatencyMS    int64     `json:"latency_ms"`
	StatusCode   int       `json:"status_code"`
	ErrorText    string    `json:"error_text"`
}

func (s *Store) InsertUsage(ctx context.Context, r UsageRecord) error {
	now := r.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.exec(ctx, `
		INSERT INTO usage_ledger
		(id, request_id, user_id, username, department, api_key_id, provider_id, model, protocol, input_tokens, output_tokens, total_tokens, cost_usd, latency_ms, status_code, error_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		r.ID, r.RequestID, r.UserID, r.Username, r.Department, r.APIKeyID, r.ProviderID, r.Model, r.Protocol,
		r.InputTokens, r.OutputTokens, r.TotalTokens, r.CostUSD, r.LatencyMS, r.StatusCode, r.ErrorText, formatTime(now))
	return err
}

func (s *Store) UsageForUser(ctx context.Context, userID string) (UsageSummary, error) {
	return s.usageSummary(ctx, `WHERE user_id = ?`, userID)
}

func (s *Store) UsageAll(ctx context.Context) (UsageSummary, error) {
	return s.usageSummary(ctx, ``)
}

func (s *Store) BudgetBurnDown(ctx context.Context, now time.Time) ([]BudgetBurnDownItem, error) {
	start, end := monthBounds(now.UTC())
	budgets, err := s.ListBudgets(ctx)
	if err != nil {
		return nil, err
	}
	elapsed := now.UTC().Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}
	total := end.Sub(start)
	elapsedRatio := 0.0
	if total > 0 {
		elapsedRatio = elapsed.Seconds() / total.Seconds()
	}
	if elapsedRatio <= 0 {
		elapsedRatio = 1 / total.Seconds()
	}
	daysElapsed := int(math.Ceil(elapsed.Hours() / 24))
	if daysElapsed < 1 {
		daysElapsed = 1
	}
	daysRemaining := int(math.Ceil(end.Sub(now.UTC()).Hours() / 24))
	if daysRemaining < 0 {
		daysRemaining = 0
	}
	items := make([]BudgetBurnDownItem, 0, len(budgets))
	for _, budget := range budgets {
		spend, err := s.spendForBudget(ctx, budget, start, end)
		if err != nil {
			return nil, err
		}
		spend = roundCost(spend)
		remaining := roundCost(math.Max(budget.LimitUSD-spend, 0))
		ratio := 0.0
		if budget.LimitUSD > 0 {
			ratio = spend / budget.LimitUSD
		}
		projected := roundCost(spend / elapsedRatio)
		projectedRatio := 0.0
		if budget.LimitUSD > 0 {
			projectedRatio = projected / budget.LimitUSD
		}
		warnRatio := budget.WarnPct / 100
		items = append(items, BudgetBurnDownItem{
			Budget:               budget,
			SpendUSD:             spend,
			RemainingUSD:         remaining,
			Ratio:                ratio,
			DailyAverageUSD:      roundCost(spend / float64(daysElapsed)),
			ProjectedMonthEndUSD: projected,
			ProjectedRatio:       projectedRatio,
			DaysElapsed:          daysElapsed,
			DaysRemaining:        daysRemaining,
			Blocked:              budget.IsActive && budget.LimitUSD > 0 && spend >= budget.LimitUSD,
			Warning:              budget.IsActive && budget.LimitUSD > 0 && spend >= budget.LimitUSD*warnRatio,
		})
	}
	return items, nil
}

type ChargebackUserRow struct {
	UserID       string  `json:"user_id"`
	Username     string  `json:"username"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type ChargebackDepartment struct {
	Department   string              `json:"department"`
	Requests     int64               `json:"requests"`
	InputTokens  int64               `json:"input_tokens"`
	OutputTokens int64               `json:"output_tokens"`
	TotalTokens  int64               `json:"total_tokens"`
	CostUSD      float64             `json:"cost_usd"`
	BudgetUSD    float64             `json:"budget_usd"`
	Users        []ChargebackUserRow `json:"users"`
}

type MonthlyChargebackReport struct {
	Month           string                 `json:"month"`
	PeriodStart     time.Time              `json:"period_start"`
	PeriodEnd       time.Time              `json:"period_end"`
	GeneratedAt     time.Time              `json:"generated_at"`
	Requests        int64                  `json:"requests"`
	InputTokens     int64                  `json:"input_tokens"`
	OutputTokens    int64                  `json:"output_tokens"`
	TotalTokens     int64                  `json:"total_tokens"`
	CostUSD         float64                `json:"cost_usd"`
	Departments     []ChargebackDepartment `json:"departments"`
	AvailableMonths []string               `json:"available_months"`
}

// ChargebackReport aggregates the usage ledger for one calendar month by
// department and user. Department and username come from the ledger rows, so
// the report reflects attribution at the time of use even if users have since
// moved departments or been deleted.
func (s *Store) ChargebackReport(ctx context.Context, month time.Time, now time.Time) (MonthlyChargebackReport, error) {
	start, end := monthBounds(month.UTC())
	report := MonthlyChargebackReport{
		Month:       start.Format("2006-01"),
		PeriodStart: start,
		PeriodEnd:   end,
		GeneratedAt: now.UTC(),
	}
	rows, err := s.query(ctx, `
		SELECT department, user_id, MAX(username) AS username,
		       COUNT(*) AS requests,
		       COALESCE(SUM(input_tokens), 0) AS input_tokens,
		       COALESCE(SUM(output_tokens), 0) AS output_tokens,
		       COALESCE(SUM(total_tokens), 0) AS total_tokens,
		       COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM usage_ledger
		WHERE created_at >= ? AND created_at < ?
		GROUP BY department, user_id
		ORDER BY department`, formatTime(start), formatTime(end))
	if err != nil {
		return MonthlyChargebackReport{}, err
	}
	defer rows.Close()
	byDept := make(map[string]*ChargebackDepartment)
	var deptOrder []string
	for rows.Next() {
		var department string
		var user ChargebackUserRow
		if err := rows.Scan(&department, &user.UserID, &user.Username, &user.Requests, &user.InputTokens, &user.OutputTokens, &user.TotalTokens, &user.CostUSD); err != nil {
			return MonthlyChargebackReport{}, err
		}
		user.CostUSD = roundCost(user.CostUSD)
		dept, ok := byDept[department]
		if !ok {
			dept = &ChargebackDepartment{Department: department}
			byDept[department] = dept
			deptOrder = append(deptOrder, department)
		}
		dept.Requests += user.Requests
		dept.InputTokens += user.InputTokens
		dept.OutputTokens += user.OutputTokens
		dept.TotalTokens += user.TotalTokens
		dept.CostUSD = roundCost(dept.CostUSD + user.CostUSD)
		dept.Users = append(dept.Users, user)
		report.Requests += user.Requests
		report.InputTokens += user.InputTokens
		report.OutputTokens += user.OutputTokens
		report.TotalTokens += user.TotalTokens
		report.CostUSD = roundCost(report.CostUSD + user.CostUSD)
	}
	if err := rows.Err(); err != nil {
		return MonthlyChargebackReport{}, err
	}

	budgets, err := s.ListBudgets(ctx)
	if err != nil {
		return MonthlyChargebackReport{}, err
	}
	for _, budget := range budgets {
		if budget.ScopeType == "department" && budget.IsActive {
			if dept, ok := byDept[budget.ScopeValue]; ok {
				dept.BudgetUSD = budget.LimitUSD
			}
		}
	}

	report.Departments = make([]ChargebackDepartment, 0, len(deptOrder))
	for _, name := range deptOrder {
		dept := byDept[name]
		sort.SliceStable(dept.Users, func(i, j int) bool { return dept.Users[i].CostUSD > dept.Users[j].CostUSD })
		report.Departments = append(report.Departments, *dept)
	}
	sort.SliceStable(report.Departments, func(i, j int) bool { return report.Departments[i].CostUSD > report.Departments[j].CostUSD })

	monthRows, err := s.query(ctx, `SELECT DISTINCT substr(created_at, 1, 7) AS month FROM usage_ledger ORDER BY month DESC`)
	if err != nil {
		return MonthlyChargebackReport{}, err
	}
	defer monthRows.Close()
	for monthRows.Next() {
		var m string
		if err := monthRows.Scan(&m); err != nil {
			return MonthlyChargebackReport{}, err
		}
		report.AvailableMonths = append(report.AvailableMonths, m)
	}
	return report, monthRows.Err()
}

func (s *Store) UsageTimeSeries(ctx context.Context, days int, now time.Time) ([]UsageTimeSeriesPoint, error) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	endDay := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	startDay := endDay.AddDate(0, 0, -(days - 1))
	points := make([]UsageTimeSeriesPoint, days)
	byDate := make(map[string]*UsageTimeSeriesPoint, days)
	for i := range points {
		day := startDay.AddDate(0, 0, i).Format("2006-01-02")
		points[i].Date = day
		byDate[day] = &points[i]
	}

	rows, err := s.query(ctx, `
		SELECT substr(created_at, 1, 10) AS day,
		       COUNT(*) AS requests,
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_text <> '' THEN 1 ELSE 0 END), 0) AS errors,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(cost_usd), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM usage_ledger
		WHERE created_at >= ?
		GROUP BY day
		ORDER BY day`, formatTime(startDay))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item UsageTimeSeriesPoint
		if err := rows.Scan(&item.Date, &item.Requests, &item.Errors, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.CostUSD, &item.AvgLatencyMS); err != nil {
			return nil, err
		}
		if point, ok := byDate[item.Date]; ok {
			item.CostUSD = roundCost(item.CostUSD)
			item.AvgLatencyMS = math.Round(item.AvgLatencyMS)
			*point = item
		}
	}
	return points, rows.Err()
}

func (s *Store) UsageDrilldowns(ctx context.Context, days int, now time.Time) (UsageDrilldowns, error) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	start := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	providers, err := s.usageDrilldownRows(ctx, "provider", start)
	if err != nil {
		return UsageDrilldowns{}, err
	}
	models, err := s.usageDrilldownRows(ctx, "model", start)
	if err != nil {
		return UsageDrilldowns{}, err
	}
	return UsageDrilldowns{Providers: providers, Models: models}, nil
}

func (s *Store) usageDrilldownRows(ctx context.Context, dimension string, start time.Time) ([]UsageDrilldownRow, error) {
	var query string
	switch dimension {
	case "provider":
		query = `
			SELECT provider_id, '' AS model,
			       COUNT(*) AS requests,
			       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_text <> '' THEN 1 ELSE 0 END), 0) AS errors,
			       COALESCE(SUM(input_tokens), 0),
			       COALESCE(SUM(output_tokens), 0),
			       COALESCE(SUM(total_tokens), 0),
			       COALESCE(SUM(cost_usd), 0) AS cost,
			       COALESCE(AVG(latency_ms), 0),
			       MAX(created_at)
			FROM usage_ledger
			WHERE created_at >= ?
			GROUP BY provider_id
			ORDER BY cost DESC, requests DESC
			LIMIT 100`
	case "model":
		query = `
			SELECT provider_id, model,
			       COUNT(*) AS requests,
			       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_text <> '' THEN 1 ELSE 0 END), 0) AS errors,
			       COALESCE(SUM(input_tokens), 0),
			       COALESCE(SUM(output_tokens), 0),
			       COALESCE(SUM(total_tokens), 0),
			       COALESCE(SUM(cost_usd), 0) AS cost,
			       COALESCE(AVG(latency_ms), 0),
			       MAX(created_at)
			FROM usage_ledger
			WHERE created_at >= ?
			GROUP BY provider_id, model
			ORDER BY cost DESC, requests DESC
			LIMIT 100`
	default:
		return nil, fmt.Errorf("unsupported drilldown dimension %q", dimension)
	}
	rows, err := s.query(ctx, query, formatTime(start))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDrilldownRow
	for rows.Next() {
		var item UsageDrilldownRow
		var lastUsed string
		if err := rows.Scan(&item.ProviderID, &item.Model, &item.Requests, &item.Errors, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.CostUSD, &item.AvgLatencyMS, &lastUsed); err != nil {
			return nil, err
		}
		if item.Requests > 0 {
			item.ErrorRate = float64(item.Errors) / float64(item.Requests)
		}
		item.CostUSD = roundCost(item.CostUSD)
		item.AvgLatencyMS = math.Round(item.AvgLatencyMS)
		if lastUsed != "" {
			t := parseTime(lastUsed)
			item.LastUsedAt = &t
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UsageExport(ctx context.Context) ([]UsageExportRow, error) {
	rows, err := s.query(ctx, `
		SELECT created_at, request_id, username, department, api_key_id, provider_id, model, protocol,
		       input_tokens, output_tokens, total_tokens, cost_usd, latency_ms, status_code, error_text
		FROM usage_ledger
		ORDER BY created_at DESC
		LIMIT 100000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageExportRow
	for rows.Next() {
		var row UsageExportRow
		var created string
		if err := rows.Scan(&created, &row.RequestID, &row.Username, &row.Department, &row.APIKeyID, &row.ProviderID, &row.Model, &row.Protocol,
			&row.InputTokens, &row.OutputTokens, &row.TotalTokens, &row.CostUSD, &row.LatencyMS, &row.StatusCode, &row.ErrorText); err != nil {
			return nil, err
		}
		row.CreatedAt = parseTime(created)
		row.CostUSD = roundCost(row.CostUSD)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) usageSummary(ctx context.Context, where string, args ...any) (UsageSummary, error) {
	var summary UsageSummary
	row := s.queryRow(ctx, `SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0), COUNT(*) FROM usage_ledger `+where, args...)
	if err := row.Scan(&summary.InputTokens, &summary.OutputTokens, &summary.TotalTokens, &summary.CostUSD, &summary.Requests); err != nil {
		return UsageSummary{}, err
	}
	summary.CostUSD = roundCost(summary.CostUSD)

	query := `SELECT model, provider_id, department, username, COUNT(*) AS requests, COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0) AS cost
		FROM usage_ledger ` + where + ` GROUP BY model, provider_id, department, username ORDER BY cost DESC, requests DESC LIMIT 100`
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return UsageSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item UsageSummaryByModel
		if err := rows.Scan(&item.Model, &item.ProviderID, &item.Department, &item.Username, &item.Requests, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.CostUSD); err != nil {
			return UsageSummary{}, err
		}
		item.CostUSD = roundCost(item.CostUSD)
		summary.ByModel = append(summary.ByModel, item)
	}
	return summary, rows.Err()
}

func Cost(inputTokens, outputTokens int, m Model) float64 {
	cost := (float64(inputTokens) * m.InputCostPerMillion / 1_000_000) + (float64(outputTokens) * m.OutputCostPerMillion / 1_000_000)
	return roundCost(cost)
}

func IsPriced(m Model) bool {
	return m.InputCostPerMillion > 0 || m.OutputCostPerMillion > 0
}

func monthBounds(t time.Time) (time.Time, time.Time) {
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

func roundCost(v float64) float64 {
	return math.Round(v*1_000_000) / 1_000_000
}
