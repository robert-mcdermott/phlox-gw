package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type departmentSeed struct {
	Name             string
	BudgetUSD        float64
	TargetUSD        float64
	WarnPct          float64
	UserBudgetRatios []float64
}

type userSeed struct {
	Username    string
	DisplayName string
	Department  string
	Weight      float64
}

type modelSeed struct {
	ID         string
	ModelID    string
	Route      string
	Display    string
	InputCost  float64
	OutputCost float64
}

type usageSeed struct {
	ID              string
	RequestID       string
	UserID          string
	Username        string
	Department      string
	APIKeyID        string
	APIKeyPrefix    string
	APIKeyName      string
	ProviderID      string
	ProviderType    string
	ModelRoute      string
	UpstreamModelID string
	Protocol        string
	Method          string
	Endpoint        string
	Streaming       bool
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	CostUSD         float64
	LatencyMS       int
	StatusCode      int
	ErrorText       string
	ClientIP        string
	UserAgent       string
	CreatedAt       time.Time
}

var departments = []departmentSeed{
	{Name: "Bridgeworks", BudgetUSD: 220, TargetUSD: 228, WarnPct: 85, UserBudgetRatios: []float64{1.08, 0.96, 0.82}},
	{Name: "SciComp", BudgetUSD: 180, TargetUSD: 139, WarnPct: 80, UserBudgetRatios: []float64{0.78, 0.62, 0.88}},
	{Name: "Informatics", BudgetUSD: 120, TargetUSD: 43, WarnPct: 80, UserBudgetRatios: []float64{0.38, 0.28, 0.44}},
	{Name: "SoftwareEng", BudgetUSD: 300, TargetUSD: 276, WarnPct: 85, UserBudgetRatios: []float64{0.91, 0.76, 0.98}},
	{Name: "CompBio", BudgetUSD: 160, TargetUSD: 92, WarnPct: 80, UserBudgetRatios: []float64{0.55, 0.68, 0.48}},
	{Name: "DaSL", BudgetUSD: 100, TargetUSD: 22, WarnPct: 75, UserBudgetRatios: []float64{0.2, 0.3, 0.18}},
}

var users = []userSeed{
	{Username: "alice.m", DisplayName: "Alice M.", Department: "Bridgeworks", Weight: 1.25},
	{Username: "ben.c", DisplayName: "Ben C.", Department: "Bridgeworks", Weight: 0.95},
	{Username: "carla.r", DisplayName: "Carla R.", Department: "Bridgeworks", Weight: 0.8},
	{Username: "david.k", DisplayName: "David K.", Department: "SciComp", Weight: 1.1},
	{Username: "emma.s", DisplayName: "Emma S.", Department: "SciComp", Weight: 0.9},
	{Username: "frank.l", DisplayName: "Frank L.", Department: "SciComp", Weight: 0.75},
	{Username: "grace.t", DisplayName: "Grace T.", Department: "Informatics", Weight: 1.0},
	{Username: "henry.p", DisplayName: "Henry P.", Department: "Informatics", Weight: 0.85},
	{Username: "isabel.n", DisplayName: "Isabel N.", Department: "Informatics", Weight: 0.7},
	{Username: "jack.w", DisplayName: "Jack W.", Department: "SoftwareEng", Weight: 1.35},
	{Username: "kelly.b", DisplayName: "Kelly B.", Department: "SoftwareEng", Weight: 1.05},
	{Username: "liam.h", DisplayName: "Liam H.", Department: "SoftwareEng", Weight: 0.8},
	{Username: "maya.g", DisplayName: "Maya G.", Department: "CompBio", Weight: 1.15},
	{Username: "noah.d", DisplayName: "Noah D.", Department: "CompBio", Weight: 0.9},
	{Username: "olivia.f", DisplayName: "Olivia F.", Department: "CompBio", Weight: 0.75},
	{Username: "peter.j", DisplayName: "Peter J.", Department: "DaSL", Weight: 1.0},
	{Username: "quinn.a", DisplayName: "Quinn A.", Department: "DaSL", Weight: 0.8},
	{Username: "rachel.v", DisplayName: "Rachel V.", Department: "DaSL", Weight: 0.65},
}

var models = []modelSeed{
	{
		ID:         "demo_model_bedrock_opus_4_8",
		ModelID:    "us.anthropic.claude-opus-4-8",
		Route:      "bedrock/us.anthropic.claude-opus-4-8",
		Display:    "Claude Opus 4.8 (Bedrock)",
		InputCost:  5,
		OutputCost: 25,
	},
	{
		ID:         "demo_model_bedrock_sonnet_4_6",
		ModelID:    "us.anthropic.claude-sonnet-4-6",
		Route:      "bedrock/us.anthropic.claude-sonnet-4-6",
		Display:    "Claude Sonnet 4.6 (Bedrock)",
		InputCost:  5,
		OutputCost: 15,
	},
}

func main() {
	dbPath := flag.String("db", "phlox-gw.db", "SQLite database path to seed")
	budgetMode := flag.String("budget-mode", "department-and-user", "budget mode: department-and-user or department")
	yes := flag.Bool("yes", false, "confirm mutation of the target database")
	flag.Parse()

	if !*yes {
		fmt.Fprintln(os.Stderr, "Refusing to modify the database without -yes.")
		fmt.Fprintln(os.Stderr, "Usage: go run ./scripts/seed-demo-data.go -db phlox-gw.db -budget-mode department-and-user -yes")
		os.Exit(2)
	}
	if *budgetMode != "department-and-user" && *budgetMode != "department" {
		fmt.Fprintln(os.Stderr, "Invalid -budget-mode. Use department-and-user or department.")
		os.Exit(2)
	}

	db, err := sql.Open("sqlite", *dbPath)
	must(err)
	defer db.Close()
	_, _ = db.Exec(`PRAGMA busy_timeout = 10000`)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	must(err)
	defer tx.Rollback()

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	startDay := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.UTC).AddDate(0, 0, -29)

	cleanup(ctx, tx)
	upsertProviderAndModels(ctx, tx, now)
	upsertUsersAndKeys(ctx, tx, now)
	insertDepartmentBudgets(ctx, tx, now)
	usageRows := generateUsage(now, monthStart, startDay)
	insertUsage(ctx, tx, usageRows)
	if *budgetMode == "department-and-user" {
		insertUserBudgets(ctx, tx, now, usageRows, monthStart)
	}

	must(tx.Commit())
	printSummary(db, *dbPath, *budgetMode, now, monthStart)
}

func cleanup(ctx context.Context, tx *sql.Tx) {
	statements := []string{
		`DELETE FROM usage_ledger WHERE id LIKE 'demo_%' OR request_id LIKE 'demo_%'`,
		`DELETE FROM request_log WHERE id LIKE 'demo_%' OR request_id LIKE 'demo_%'`,
		`DELETE FROM api_keys WHERE id LIKE 'demo_%'`,
		`DELETE FROM budgets WHERE id LIKE 'demo_%' OR (scope_type = 'department' AND scope_value IN ('Bridgeworks','SciComp','Informatics','SoftwareEng','CompBio','DaSL'))`,
		`DELETE FROM users WHERE id LIKE 'demo_%'`,
		`DELETE FROM models WHERE id = 'demo_model_bedrock_sonnet_4_6' OR route = 'bedrock/us.anthropic.claude-opus-4-6'`,
	}
	for _, stmt := range statements {
		_, err := tx.ExecContext(ctx, stmt)
		must(err)
	}
}

func upsertProviderAndModels(ctx context.Context, tx *sql.Tx, now time.Time) {
	ts := formatTime(now)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO providers (id, name, type, base_url, api_key, api_key_env, aws_region, enabled, health_status, consecutive_failures, last_error, created_at, updated_at)
		VALUES ('bedrock', 'AWS Bedrock', 'bedrock', '', '', '', 'us-west-2', 1, 'healthy', 0, '', ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, type = excluded.type, aws_region = excluded.aws_region, enabled = 1, updated_at = excluded.updated_at`,
		ts, ts)
	must(err)
	for _, m := range models {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO models
			(id, provider_id, model_id, route, display_name, input_cost_per_million, output_cost_per_million, context_window,
			 supports_streaming, enabled, fallback_routes, weighted_routes, retry_attempts, request_timeout_ms, health_routing_enabled, created_at, updated_at)
			VALUES (?, 'bedrock', ?, ?, ?, ?, ?, 200000, 1, 1, '', '', 1, 120000, 1, ?, ?)
			ON CONFLICT(route) DO UPDATE SET
				provider_id = 'bedrock',
				model_id = excluded.model_id,
				display_name = excluded.display_name,
				input_cost_per_million = excluded.input_cost_per_million,
				output_cost_per_million = excluded.output_cost_per_million,
				context_window = excluded.context_window,
				supports_streaming = 1,
				enabled = 1,
				updated_at = excluded.updated_at`,
			m.ID, m.ModelID, m.Route, m.Display, m.InputCost, m.OutputCost, ts, ts)
		must(err)
	}
}

func upsertUsersAndKeys(ctx context.Context, tx *sql.Tx, now time.Time) {
	ts := formatTime(now)
	for _, u := range users {
		userID := userID(u.Username)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO users (id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'user', ?, 'local', 1, ?, ?)
			ON CONFLICT(username) DO UPDATE SET display_name = excluded.display_name, department = excluded.department, is_active = 1, updated_at = excluded.updated_at`,
			userID, u.Username, strings.ReplaceAll(u.Username, ".", ".")+"@example.org", u.DisplayName, u.Department, "demo-password-hash-not-for-login", ts, ts)
		must(err)
		keyID := keyID(u.Username)
		prefix := "pgw-sk-demo-" + strings.ReplaceAll(u.Username, ".", "")
		hash := sha256.Sum256([]byte("demo-key-" + u.Username))
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api_keys (id, user_id, name, prefix, key_hash, is_active, created_at, budget_usd, rpm_limit, tpm_limit, model_allowlist)
			VALUES (?, ?, 'Demo documentation key', ?, ?, 1, ?, 0, 0, 0, '')
			ON CONFLICT(id) DO UPDATE SET user_id = excluded.user_id, name = excluded.name, prefix = excluded.prefix, is_active = 1, model_allowlist = '', budget_usd = 0, rpm_limit = 0, tpm_limit = 0`,
			keyID, userID, prefix, hex.EncodeToString(hash[:]), ts)
		must(err)
	}
}

func insertDepartmentBudgets(ctx context.Context, tx *sql.Tx, now time.Time) {
	ts := formatTime(now)
	for _, dept := range departments {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO budgets (id, scope_type, scope_value, limit_usd, warn_pct, is_active, created_at, updated_at)
			VALUES (?, 'department', ?, ?, ?, 1, ?, ?)`,
			"demo_budget_department_"+slug(dept.Name), dept.Name, dept.BudgetUSD, dept.WarnPct, ts, ts)
		must(err)
	}
}

func generateUsage(now, monthStart, startDay time.Time) []usageSeed {
	var out []usageSeed
	globalIndex := 0
	for _, dept := range departments {
		currentDays := daysBetween(maxDay(startDay, monthStart), now)
		currentWeights := make([]float64, len(currentDays)*4)
		for dayIndex, day := range currentDays {
			for slot := 0; slot < 4; slot++ {
				idx := dayIndex*4 + slot
				currentWeights[idx] = dailyWeight(dayIndex, slot, day, dept.Name)
			}
		}
		currentTotalWeight := sum(currentWeights)
		rowIndex := 0
		for dayIndex, day := range currentDays {
			for slot := 0; slot < 4; slot++ {
				weight := currentWeights[dayIndex*4+slot]
				cost := dept.TargetUSD * weight / currentTotalWeight
				out = append(out, buildUsageRow(dept.Name, day, slot, rowIndex, globalIndex, cost))
				rowIndex++
				globalIndex++
			}
		}

		previousDays := daysBetween(startDay, monthStart.AddDate(0, 0, -1))
		for dayIndex, day := range previousDays {
			for slot := 0; slot < 3; slot++ {
				cost := (dept.TargetUSD / math.Max(float64(len(currentDays)), 1)) * (0.45 + 0.12*float64((dayIndex+slot)%4))
				out = append(out, buildUsageRow(dept.Name, day, slot, rowIndex, globalIndex, cost))
				rowIndex++
				globalIndex++
			}
		}
	}
	return out
}

func buildUsageRow(department string, day time.Time, slot, rowIndex, globalIndex int, cost float64) usageSeed {
	deptUsers := usersForDepartment(department)
	u := weightedUser(deptUsers, rowIndex+slot)
	model := models[globalIndex%len(models)]
	inputTokens, outputTokens := tokensForCost(cost, model)
	status := 200
	errorText := ""
	if (rowIndex+len(department)+slot)%31 == 0 {
		status = 429
		errorText = "upstream throttled request"
	} else if (rowIndex+slot)%47 == 0 {
		status = 503
		errorText = "provider unavailable"
	}
	created := day.Add(time.Duration(8+slot*3+(rowIndex%2)) * time.Hour).Add(time.Duration((rowIndex*17)%55) * time.Minute)
	requestID := fmt.Sprintf("demo_req_%s_%s_%03d", slug(department), day.Format("20060102"), rowIndex)
	streaming := (rowIndex+slot)%3 != 0
	return usageSeed{
		ID:              "demo_usage_" + requestID,
		RequestID:       requestID,
		UserID:          userID(u.Username),
		Username:        u.Username,
		Department:      department,
		APIKeyID:        keyID(u.Username),
		APIKeyPrefix:    "pgw-sk-demo-" + strings.ReplaceAll(u.Username, ".", ""),
		APIKeyName:      "Demo documentation key",
		ProviderID:      "bedrock",
		ProviderType:    "bedrock",
		ModelRoute:      model.Route,
		UpstreamModelID: model.ModelID,
		Protocol:        "anthropic",
		Method:          "POST",
		Endpoint:        "/anthropic/v1/messages",
		Streaming:       streaming,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		TotalTokens:     inputTokens + outputTokens,
		CostUSD:         costFromTokens(inputTokens, outputTokens, model),
		LatencyMS:       820 + ((rowIndex*137 + slot*211 + len(department)*43) % 6400),
		StatusCode:      status,
		ErrorText:       errorText,
		ClientIP:        fmt.Sprintf("10.%d.%d.%d", 24+slot, len(department)+10, 40+(rowIndex%120)),
		UserAgent:       "phlox-gw-demo-data/1.0",
		CreatedAt:       created,
	}
}

func insertUsage(ctx context.Context, tx *sql.Tx, rows []usageSeed) {
	usageStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_ledger
		(id, request_id, user_id, username, department, api_key_id, provider_id, model, protocol,
		 input_tokens, output_tokens, total_tokens, cost_usd, latency_ms, status_code, error_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	must(err)
	defer usageStmt.Close()
	logStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO request_log
		(id, request_id, user_id, username, department, api_key_id, api_key_prefix, api_key_name, provider_id, provider_type,
		 model_route, upstream_model_id, protocol, method, endpoint, streaming, input_tokens, output_tokens, total_tokens, cost_usd,
		 latency_ms, status_code, error_text, client_ip, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	must(err)
	defer logStmt.Close()
	for _, row := range rows {
		ts := formatTime(row.CreatedAt)
		_, err := usageStmt.ExecContext(ctx, row.ID, row.RequestID, row.UserID, row.Username, row.Department, row.APIKeyID, row.ProviderID, row.ModelRoute, row.Protocol,
			row.InputTokens, row.OutputTokens, row.TotalTokens, row.CostUSD, row.LatencyMS, row.StatusCode, row.ErrorText, ts)
		must(err)
		_, err = logStmt.ExecContext(ctx, "demo_log_"+row.RequestID, row.RequestID, row.UserID, row.Username, row.Department, row.APIKeyID, row.APIKeyPrefix, row.APIKeyName, row.ProviderID, row.ProviderType,
			row.ModelRoute, row.UpstreamModelID, row.Protocol, row.Method, row.Endpoint, boolInt(row.Streaming), row.InputTokens, row.OutputTokens, row.TotalTokens, row.CostUSD,
			row.LatencyMS, row.StatusCode, row.ErrorText, row.ClientIP, row.UserAgent, ts)
		must(err)
	}
}

func insertUserBudgets(ctx context.Context, tx *sql.Tx, now time.Time, rows []usageSeed, monthStart time.Time) {
	spend := map[string]float64{}
	for _, row := range rows {
		if !row.CreatedAt.Before(monthStart) {
			spend[row.Username] += row.CostUSD
		}
	}
	ts := formatTime(now)
	for _, u := range users {
		ratio := userBudgetRatio(u)
		limit := roundMoney(math.Max(15, spend[u.Username]/ratio))
		_, err := tx.ExecContext(ctx, `
			INSERT INTO budgets (id, scope_type, scope_value, limit_usd, warn_pct, is_active, created_at, updated_at)
			VALUES (?, 'user', ?, ?, 85, 1, ?, ?)`,
			"demo_budget_user_"+slug(u.Username), userID(u.Username), limit, ts, ts)
		must(err)
	}
}

func printSummary(db *sql.DB, dbPath, budgetMode string, now, monthStart time.Time) {
	fmt.Printf("Seeded Phlox-GW demo data into %s\n\n", dbPath)
	query := `
		SELECT department, COUNT(*), ROUND(SUM(cost_usd), 2), SUM(total_tokens)
		FROM usage_ledger
		WHERE id LIKE 'demo_%' AND created_at >= ?
		GROUP BY department
		ORDER BY department`
	rows, err := db.Query(query, formatTime(monthStart))
	must(err)
	defer rows.Close()
	fmt.Println("Current-month demo usage by department:")
	for rows.Next() {
		var dept string
		var requests int
		var spend float64
		var tokens int64
		must(rows.Scan(&dept, &requests, &spend, &tokens))
		fmt.Printf("  %-12s %4d requests  $%7.2f  %10d tokens\n", dept, requests, spend, tokens)
	}
	must(rows.Err())

	rows, err = db.Query(`
		SELECT model, COUNT(*), ROUND(SUM(cost_usd), 2)
		FROM usage_ledger
		WHERE id LIKE 'demo_%'
		GROUP BY model
		ORDER BY model`)
	must(err)
	defer rows.Close()
	fmt.Println("\nDemo usage by model:")
	for rows.Next() {
		var model string
		var requests int
		var spend float64
		must(rows.Scan(&model, &requests, &spend))
		fmt.Printf("  %-42s %4d requests  $%7.2f\n", model, requests, spend)
	}
	must(rows.Err())

	rows, err = db.Query(`
		SELECT scope_type, COUNT(*)
		FROM budgets
		WHERE id LIKE 'demo_%'
		GROUP BY scope_type
		ORDER BY scope_type`)
	must(err)
	defer rows.Close()
	fmt.Println("\nDemo budgets:")
	for rows.Next() {
		var scopeType string
		var count int
		must(rows.Scan(&scopeType, &count))
		fmt.Printf("  %-12s %d\n", scopeType, count)
	}
	must(rows.Err())

	var usersCount, usageCount, requestCount int
	must(db.QueryRow(`SELECT COUNT(*) FROM users WHERE id LIKE 'demo_%'`).Scan(&usersCount))
	must(db.QueryRow(`SELECT COUNT(*) FROM usage_ledger WHERE id LIKE 'demo_%'`).Scan(&usageCount))
	must(db.QueryRow(`SELECT COUNT(*) FROM request_log WHERE id LIKE 'demo_%'`).Scan(&requestCount))
	fmt.Printf("\nBudget mode: %s\nDemo users: %d\nDemo usage rows: %d\nDemo request log rows: %d\nAs of: %s\n", budgetMode, usersCount, usageCount, requestCount, now.Format(time.RFC3339))
}

func usersForDepartment(dept string) []userSeed {
	var out []userSeed
	for _, u := range users {
		if u.Department == dept {
			out = append(out, u)
		}
	}
	return out
}

func weightedUser(candidates []userSeed, n int) userSeed {
	total := 0.0
	for _, u := range candidates {
		total += u.Weight
	}
	pick := math.Mod(float64((n*37)%100)/100*total, total)
	running := 0.0
	for _, u := range candidates {
		running += u.Weight
		if pick <= running {
			return u
		}
	}
	return candidates[len(candidates)-1]
}

func userBudgetRatio(u userSeed) float64 {
	deptIndex := 0
	userIndex := 0
	for i, dept := range departments {
		if dept.Name == u.Department {
			deptIndex = i
			break
		}
	}
	for _, candidate := range users {
		if candidate.Department == u.Department {
			if candidate.Username == u.Username {
				break
			}
			userIndex++
		}
	}
	ratios := departments[deptIndex].UserBudgetRatios
	return ratios[userIndex%len(ratios)]
}

func tokensForCost(cost float64, model modelSeed) (int, int) {
	output := int(math.Max(200, math.Round(cost*1_000_000/(model.OutputCost+model.InputCost*3))))
	input := int(math.Round(float64(output) * (2.4 + math.Mod(float64(output), 5)/5)))
	return input, output
}

func costFromTokens(input, output int, model modelSeed) float64 {
	return math.Round(((float64(input)*model.InputCost/1_000_000)+(float64(output)*model.OutputCost/1_000_000))*1_000_000) / 1_000_000
}

func dailyWeight(dayIndex, slot int, day time.Time, dept string) float64 {
	weekdayBoost := 1.0
	if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		weekdayBoost = 0.42
	}
	trend := 0.85 + float64(dayIndex)*0.018
	slotBoost := []float64{0.78, 1.15, 1.0, 0.92}[slot%4]
	deptBoost := 0.9 + float64((len(dept)+slot+dayIndex)%7)*0.035
	return weekdayBoost * trend * slotBoost * deptBoost
}

func daysBetween(start, end time.Time) []time.Time {
	start = dayOnly(start)
	end = dayOnly(end)
	var days []time.Time
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	return days
}

func maxDay(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func dayOnly(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func sum(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total
}

func roundMoney(v float64) float64 {
	return math.Ceil(v/5) * 5
}

func userID(username string) string {
	return "demo_user_" + slug(username)
}

func keyID(username string) string {
	return "demo_key_" + slug(username)
}

func slug(v string) string {
	v = strings.ToLower(v)
	replacer := strings.NewReplacer(".", "_", " ", "_", "-", "_")
	return replacer.Replace(v)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
