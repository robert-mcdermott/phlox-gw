package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Budget struct {
	ID         string    `json:"id"`
	ScopeType  string    `json:"scope_type"`
	ScopeValue string    `json:"scope_value"`
	LimitUSD   float64   `json:"limit_usd"`
	WarnPct    float64   `json:"warn_pct"`
	IsActive   bool      `json:"is_active"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type BudgetStatus struct {
	Blocked bool             `json:"blocked"`
	Warning bool             `json:"warning"`
	Reason  string           `json:"reason"`
	Items   []BudgetLineItem `json:"items"`
}

type BudgetLineItem struct {
	Budget   Budget  `json:"budget"`
	SpendUSD float64 `json:"spend_usd"`
	Ratio    float64 `json:"ratio"`
	Blocked  bool    `json:"blocked"`
	Warning  bool    `json:"warning"`
}

func (s *Store) ListBudgets(ctx context.Context) ([]Budget, error) {
	rows, err := s.query(ctx, `SELECT id, scope_type, scope_value, limit_usd, warn_pct, is_active, created_at, updated_at FROM budgets ORDER BY scope_type, scope_value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var budgets []Budget
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return nil, err
		}
		budgets = append(budgets, b)
	}
	return budgets, rows.Err()
}

func (s *Store) CreateBudget(ctx context.Context, b Budget) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `
		INSERT INTO budgets (id, scope_type, scope_value, limit_usd, warn_pct, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ScopeType, b.ScopeValue, b.LimitUSD, b.WarnPct, boolInt(b.IsActive), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) DeleteBudget(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM budgets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateBudget(ctx context.Context, b Budget) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE budgets
		SET scope_type = ?, scope_value = ?, limit_usd = ?, warn_pct = ?, is_active = ?, updated_at = ?
		WHERE id = ?`,
		b.ScopeType, b.ScopeValue, b.LimitUSD, b.WarnPct, boolInt(b.IsActive), formatTime(now), b.ID)
	if err != nil {
		if isUniqueErr(err) {
			return ErrConflict
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) BudgetStatus(ctx context.Context, u User, pricedOnly bool) (BudgetStatus, error) {
	if !pricedOnly {
		return BudgetStatus{}, nil
	}
	rows, err := s.query(ctx, `
		SELECT id, scope_type, scope_value, limit_usd, warn_pct, is_active, created_at, updated_at
		FROM budgets
		WHERE is_active = 1 AND (
			(scope_type = 'user' AND scope_value = ?) OR
			(scope_type = 'department' AND scope_value = ?)
		)`, u.ID, u.Department)
	if err != nil {
		return BudgetStatus{}, err
	}
	var budgets []Budget
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			rows.Close()
			return BudgetStatus{}, err
		}
		budgets = append(budgets, b)
	}
	if err := rows.Close(); err != nil {
		return BudgetStatus{}, err
	}
	if err := rows.Err(); err != nil {
		return BudgetStatus{}, err
	}

	var status BudgetStatus
	start, end := monthBounds(time.Now().UTC())
	for _, b := range budgets {
		spend, err := s.spendForBudget(ctx, b, start, end)
		if err != nil {
			return BudgetStatus{}, err
		}
		ratio := 0.0
		if b.LimitUSD > 0 {
			ratio = spend / b.LimitUSD
		}
		item := BudgetLineItem{
			Budget:   b,
			SpendUSD: roundCost(spend),
			Ratio:    ratio,
			Blocked:  b.LimitUSD > 0 && spend >= b.LimitUSD,
			Warning:  b.LimitUSD > 0 && spend >= b.LimitUSD*(b.WarnPct/100),
		}
		if item.Blocked {
			status.Blocked = true
			status.Reason = fmt.Sprintf("%s budget %s is at or over limit", b.ScopeType, b.ScopeValue)
		}
		if item.Warning {
			status.Warning = true
		}
		status.Items = append(status.Items, item)
	}
	return status, nil
}

func (s *Store) spendForBudget(ctx context.Context, b Budget, start, end time.Time) (float64, error) {
	var field string
	switch b.ScopeType {
	case "user":
		field = "user_id"
	case "department":
		field = "department"
	default:
		return 0, nil
	}
	var spend float64
	err := s.queryRow(ctx, `SELECT COALESCE(SUM(cost_usd), 0) FROM usage_ledger WHERE `+field+` = ? AND created_at >= ? AND created_at < ?`,
		b.ScopeValue, formatTime(start), formatTime(end)).Scan(&spend)
	return spend, err
}

func scanBudget(row scanner) (Budget, error) {
	var b Budget
	var active int
	var created, updated string
	err := row.Scan(&b.ID, &b.ScopeType, &b.ScopeValue, &b.LimitUSD, &b.WarnPct, &active, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, ErrNotFound
	}
	if err != nil {
		return Budget{}, err
	}
	b.IsActive = active == 1
	b.CreatedAt = parseTime(created)
	b.UpdatedAt = parseTime(updated)
	return b, nil
}
