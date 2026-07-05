package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"display_name"`
	Department   string     `json:"department"`
	Role         string     `json:"role"`
	PasswordHash string     `json:"-"`
	AuthProvider string     `json:"auth_provider"`
	IsActive     bool       `json:"is_active"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	row := s.queryRow(ctx, `SELECT id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at, last_login_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) GetUserByID(ctx context.Context, id string) (User, error) {
	row := s.queryRow(ctx, `SELECT id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at, last_login_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) TouchLogin(ctx context.Context, userID string, t time.Time) error {
	_, err := s.exec(ctx, `UPDATE users SET last_login_at = ?, updated_at = ? WHERE id = ?`, formatTime(t), formatTime(t), userID)
	return err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.query(ctx, `SELECT id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at, last_login_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, u User) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `
		INSERT INTO users (id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.Email, u.DisplayName, u.Department, u.Role, u.PasswordHash, valueOr(u.AuthProvider, "local"), boolInt(u.IsActive), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateUser(ctx context.Context, u User) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE users
		SET email = ?, display_name = ?, department = ?, role = ?, is_active = ?, updated_at = ?
		WHERE id = ?`,
		u.Email, u.DisplayName, u.Department, u.Role, boolInt(u.IsActive), formatTime(now), u.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateFederatedUser(ctx context.Context, u User) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE users
		SET email = ?, display_name = ?, department = ?, role = ?, auth_provider = ?, updated_at = ?
		WHERE id = ?`,
		u.Email, u.DisplayName, u.Department, u.Role, valueOr(u.AuthProvider, "oidc"), formatTime(now), u.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetUserPassword(ctx context.Context, userID, passwordHash string) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, formatTime(now), userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.txExec(ctx, tx, `DELETE FROM budgets WHERE scope_type = 'user' AND scope_value = ?`, userID); err != nil {
		return err
	}
	if _, err := s.txExec(ctx, tx, `DELETE FROM rate_limits WHERE scope_type = 'user' AND scope_value = ?`, userID); err != nil {
		return err
	}
	res, err := s.txExec(ctx, tx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func scanUser(row scanner) (User, error) {
	var u User
	var active int
	var created, updated string
	var last sql.NullString
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Department, &u.Role, &u.PasswordHash, &u.AuthProvider, &active, &created, &updated, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.IsActive = active == 1
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	if last.Valid {
		t := parseTime(last.String)
		u.LastLoginAt = &t
	}
	return u, nil
}
