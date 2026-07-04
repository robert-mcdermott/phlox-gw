package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Model struct {
	ID                   string    `json:"id"`
	ProviderID           string    `json:"provider_id"`
	ModelID              string    `json:"model_id"`
	Route                string    `json:"route"`
	DisplayName          string    `json:"display_name"`
	InputCostPerMillion  float64   `json:"input_cost_per_million"`
	OutputCostPerMillion float64   `json:"output_cost_per_million"`
	ContextWindow        int       `json:"context_window"`
	SupportsStreaming    bool      `json:"supports_streaming"`
	Enabled              bool      `json:"enabled"`
	FallbackRoutes       string    `json:"fallback_routes"`
	WeightedRoutes       string    `json:"weighted_routes"`
	RetryAttempts        int       `json:"retry_attempts"`
	RequestTimeoutMS     int       `json:"request_timeout_ms"`
	HealthRoutingEnabled bool      `json:"health_routing_enabled"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type RoutedModel struct {
	Model    Model    `json:"model"`
	Provider Provider `json:"provider"`
}

func (s *Store) ListModels(ctx context.Context, includeDisabled bool) ([]Model, error) {
	query := `SELECT ` + modelColumns + ` FROM models`
	if !includeDisabled {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY provider_id, model_id`
	rows, err := s.query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

func (s *Store) CreateModel(ctx context.Context, m Model) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `
		INSERT INTO models
		(id, provider_id, model_id, route, display_name, input_cost_per_million, output_cost_per_million, context_window,
		 supports_streaming, enabled, fallback_routes, weighted_routes, retry_attempts, request_timeout_ms, health_routing_enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ProviderID, m.ModelID, m.Route, m.DisplayName, m.InputCostPerMillion, m.OutputCostPerMillion, m.ContextWindow,
		boolInt(m.SupportsStreaming), boolInt(m.Enabled), normalizeList(m.FallbackRoutes), normalizeList(m.WeightedRoutes), clampNonNegative(m.RetryAttempts), clampNonNegative(m.RequestTimeoutMS), boolInt(m.HealthRoutingEnabled), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateModel(ctx context.Context, m Model) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE models
		SET provider_id = ?, model_id = ?, route = ?, display_name = ?, input_cost_per_million = ?, output_cost_per_million = ?,
		    context_window = ?, supports_streaming = ?, enabled = ?, fallback_routes = ?, weighted_routes = ?, retry_attempts = ?, request_timeout_ms = ?,
		    health_routing_enabled = ?, updated_at = ?
		WHERE id = ?`,
		m.ProviderID, m.ModelID, m.Route, m.DisplayName, m.InputCostPerMillion, m.OutputCostPerMillion, m.ContextWindow,
		boolInt(m.SupportsStreaming), boolInt(m.Enabled), normalizeList(m.FallbackRoutes), normalizeList(m.WeightedRoutes), clampNonNegative(m.RetryAttempts), clampNonNegative(m.RequestTimeoutMS),
		boolInt(m.HealthRoutingEnabled), formatTime(now), m.ID)
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

func (s *Store) DeleteModel(ctx context.Context, id string) error {
	route, err := s.ResolveModelByID(ctx, id, false)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.txExec(ctx, tx, `DELETE FROM rate_limits WHERE scope_type = 'model' AND scope_value IN (?, ?, ?)`, route.Model.Route, route.Model.ModelID, route.Model.ID); err != nil {
		return err
	}
	res, err := s.txExec(ctx, tx, `DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) ResolveModelByID(ctx context.Context, id string, requireEnabled bool) (RoutedModel, error) {
	query := routedModelQuery + ` WHERE m.id = ?`
	if requireEnabled {
		query += ` AND m.enabled = 1 AND p.enabled = 1`
	}
	return scanRoutedModel(s.queryRow(ctx, query, id))
}

func (s *Store) ResolveModel(ctx context.Context, requested string) (RoutedModel, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return RoutedModel{}, ErrNotFound
	}
	var row *sql.Row
	if strings.Contains(requested, "/") {
		row = s.queryRow(ctx, routedModelQuery+` WHERE m.route = ? AND m.enabled = 1 AND p.enabled = 1`, requested)
	} else {
		rows, err := s.query(ctx, routedModelQuery+` WHERE m.model_id = ? AND m.enabled = 1 AND p.enabled = 1`, requested)
		if err != nil {
			return RoutedModel{}, err
		}
		defer rows.Close()
		var matches []RoutedModel
		for rows.Next() {
			rm, err := scanRoutedModel(rows)
			if err != nil {
				return RoutedModel{}, err
			}
			matches = append(matches, rm)
		}
		if err := rows.Err(); err != nil {
			return RoutedModel{}, err
		}
		if len(matches) != 1 {
			return RoutedModel{}, ErrNotFound
		}
		return matches[0], nil
	}
	return scanRoutedModel(row)
}

func (s *Store) ResolveModelCandidates(ctx context.Context, requested string) ([]RoutedModel, error) {
	primary, err := s.ResolveModel(ctx, requested)
	if err != nil {
		return nil, err
	}
	candidates := []RoutedModel{primary}
	seen := map[string]bool{primary.Model.Route: true, primary.Model.ID: true}
	for _, fallback := range splitNormalizedList(primary.Model.FallbackRoutes) {
		if seen[fallback] {
			continue
		}
		route, err := s.ResolveModel(ctx, fallback)
		if err != nil {
			continue
		}
		if seen[route.Model.Route] || seen[route.Model.ID] {
			continue
		}
		seen[route.Model.Route] = true
		seen[route.Model.ID] = true
		candidates = append(candidates, route)
	}
	return candidates, nil
}

const modelColumns = `id, provider_id, model_id, route, display_name, input_cost_per_million, output_cost_per_million, context_window, supports_streaming, enabled, fallback_routes, weighted_routes, retry_attempts, request_timeout_ms, health_routing_enabled, created_at, updated_at`

const modelColumnsAliased = `m.id, m.provider_id, m.model_id, m.route, m.display_name, m.input_cost_per_million, m.output_cost_per_million, m.context_window, m.supports_streaming, m.enabled, m.fallback_routes, m.weighted_routes, m.retry_attempts, m.request_timeout_ms, m.health_routing_enabled, m.created_at, m.updated_at`

func scanModel(row scanner) (Model, error) {
	var m Model
	var streaming, enabled, healthRouting int
	var created, updated string
	err := row.Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.Route, &m.DisplayName, &m.InputCostPerMillion, &m.OutputCostPerMillion, &m.ContextWindow,
		&streaming, &enabled, &m.FallbackRoutes, &m.WeightedRoutes, &m.RetryAttempts, &m.RequestTimeoutMS, &healthRouting, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	if err != nil {
		return Model{}, err
	}
	m.SupportsStreaming = streaming == 1
	m.Enabled = enabled == 1
	m.HealthRoutingEnabled = healthRouting == 1
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	return m, nil
}

const routedModelQuery = `
	SELECT ` + modelColumnsAliased + `,
	       ` + providerColumnsAliased + `
	FROM models m
	JOIN providers p ON p.id = m.provider_id`

func scanRoutedModel(row scanner) (RoutedModel, error) {
	var m Model
	var p Provider
	var mStreaming, mEnabled, mHealthRouting, pEnabled int
	var pLastCheck, pCircuitOpen sql.NullString
	var mCreated, mUpdated, pCreated, pUpdated string
	err := row.Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.Route, &m.DisplayName, &m.InputCostPerMillion, &m.OutputCostPerMillion, &m.ContextWindow,
		&mStreaming, &mEnabled, &m.FallbackRoutes, &m.WeightedRoutes, &m.RetryAttempts, &m.RequestTimeoutMS, &mHealthRouting, &mCreated, &mUpdated,
		&p.ID, &p.Name, &p.Type, &p.BaseURL, &p.APIKey, &p.APIKeyEnv, &p.AzureAPIVersion, &p.AWSRegion, &p.AWSAuthMethod, &p.AWSAccessKeyID, &p.AWSSecretAccessKey, &p.AWSSessionToken, &p.BedrockAPIKey, &pEnabled, &p.HealthStatus, &p.ConsecutiveFailures, &pLastCheck, &p.LastError, &pCircuitOpen, &pCreated, &pUpdated)
	if errors.Is(err, sql.ErrNoRows) {
		return RoutedModel{}, ErrNotFound
	}
	if err != nil {
		return RoutedModel{}, err
	}
	m.SupportsStreaming = mStreaming == 1
	m.Enabled = mEnabled == 1
	m.HealthRoutingEnabled = mHealthRouting == 1
	m.CreatedAt = parseTime(mCreated)
	m.UpdatedAt = parseTime(mUpdated)
	p.Enabled = pEnabled == 1
	if p.HealthStatus == "" {
		p.HealthStatus = "unknown"
	}
	if pLastCheck.Valid {
		t := parseTime(pLastCheck.String)
		p.LastHealthCheckAt = &t
	}
	if pCircuitOpen.Valid {
		t := parseTime(pCircuitOpen.String)
		p.CircuitOpenUntil = &t
	}
	p.CreatedAt = parseTime(pCreated)
	p.UpdatedAt = parseTime(pUpdated)
	return RoutedModel{Model: m, Provider: p}, nil
}
