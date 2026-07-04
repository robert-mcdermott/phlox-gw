package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) InsertAuditLog(ctx context.Context, item AuditLog) error {
	now := item.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.exec(ctx, `
		INSERT INTO audit_log
		(id, actor_user_id, actor_username, action, target_type, target_id, target_display, details, ip_address, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.ActorUserID, item.ActorUsername, item.Action, item.TargetType, item.TargetID, item.TargetDisplay, item.Details, item.IPAddress, item.UserAgent, formatTime(now))
	return err
}

func (s *Store) ListAuditLogs(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.query(ctx, `
		SELECT id, actor_user_id, actor_username, action, target_type, target_id, target_display, details, ip_address, user_agent, created_at
		FROM audit_log
		ORDER BY created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		item, err := scanAuditLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanAuditLog(row scanner) (AuditLog, error) {
	var item AuditLog
	var created string
	err := row.Scan(&item.ID, &item.ActorUserID, &item.ActorUsername, &item.Action, &item.TargetType, &item.TargetID, &item.TargetDisplay, &item.Details, &item.IPAddress, &item.UserAgent, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditLog{}, ErrNotFound
	}
	if err != nil {
		return AuditLog{}, err
	}
	item.CreatedAt = parseTime(created)
	return item, nil
}
