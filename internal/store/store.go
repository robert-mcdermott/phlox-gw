package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

type Store struct {
	db      *sql.DB
	dialect sqlDialect
}

type OpenOptions struct {
	Driver               string
	Path                 string
	URL                  string
	MaxOpenConns         int
	MaxIdleConns         int
	ConnMaxLifetime      time.Duration
	MigrationLockTimeout time.Duration
}

type sqlDialect string

const (
	dialectSQLite   sqlDialect = "sqlite"
	dialectPostgres sqlDialect = "postgres"
)

func Open(path string) (*Store, error) {
	return OpenWithOptions(OpenOptions{Driver: string(dialectSQLite), Path: path})
}

func OpenWithOptions(opts OpenOptions) (*Store, error) {
	driver := normalizeDriver(opts.Driver)
	var sqlDriver, dsn string
	var dialect sqlDialect
	switch driver {
	case "", "sqlite", "sqlite3":
		if strings.TrimSpace(opts.Path) == "" {
			return nil, errors.New("sqlite database path is required")
		}
		sqlDriver = "sqlite"
		dsn = opts.Path
		dialect = dialectSQLite
	case "postgres", "postgresql", "pgx":
		if strings.TrimSpace(opts.URL) == "" {
			return nil, errors.New("postgres database URL is required")
		}
		sqlDriver = "pgx"
		dsn = opts.URL
		dialect = dialectPostgres
	default:
		return nil, fmt.Errorf("unsupported database driver %q", opts.Driver)
	}

	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, err
	}
	switch dialect {
	case dialectSQLite:
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(0)
	case dialectPostgres:
		maxOpen := opts.MaxOpenConns
		if maxOpen <= 0 {
			maxOpen = 25
		}
		maxIdle := opts.MaxIdleConns
		if maxIdle <= 0 {
			maxIdle = maxOpen
		}
		lifetime := opts.ConnMaxLifetime
		if lifetime <= 0 {
			lifetime = 30 * time.Minute
		}
		db.SetMaxOpenConns(maxOpen)
		db.SetMaxIdleConns(maxIdle)
		db.SetConnMaxLifetime(lifetime)
	}

	s := &Store{db: db, dialect: dialect}
	if err := s.init(context.Background(), opts.MigrationLockTimeout); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Driver() string {
	return string(s.dialect)
}

func normalizeDriver(driver string) string {
	return strings.ToLower(strings.TrimSpace(driver))
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.rebind(query), args...)
}

func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.rebind(query), args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.rebind(query), args...)
}

func (s *Store) txExec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, s.rebind(query), args...)
}

func (s *Store) txQueryRow(ctx context.Context, tx *sql.Tx, query string, args ...any) *sql.Row {
	return tx.QueryRowContext(ctx, s.rebind(query), args...)
}

func (s *Store) rebind(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	return rebindPostgres(query)
}

func rebindPostgres(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(query); i++ {
		ch := query[i]
		next := byte(0)
		if i+1 < len(query) {
			next = query[i+1]
		}

		if inLineComment {
			b.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			b.WriteByte(ch)
			if ch == '*' && next == '/' {
				b.WriteByte(next)
				i++
				inBlockComment = false
			}
			continue
		}
		if inSingle {
			b.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' {
					b.WriteByte(next)
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			b.WriteByte(ch)
			if ch == '"' {
				if next == '"' {
					b.WriteByte(next)
					i++
					continue
				}
				inDouble = false
			}
			continue
		}

		switch {
		case ch == '-' && next == '-':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inLineComment = true
		case ch == '/' && next == '*':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inBlockComment = true
		case ch == '\'':
			b.WriteByte(ch)
			inSingle = true
		case ch == '"':
			b.WriteByte(ch)
			inDouble = true
		case ch == '?':
			b.WriteByte('$')
			b.WriteString(fmt.Sprint(arg))
			arg++
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

type scanner interface {
	Scan(dest ...any) error
}
