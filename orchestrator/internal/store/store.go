// Package store is the gateway's Postgres access: migrations, gateway
// tokens and agent_calls rows.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/LinuxAd/dark-factory/memory"
)

// ErrTokenNotFound is returned by LookupToken for an unknown hash.
var ErrTokenNotFound = errors.New("token not found")

// Store wraps a pgx pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to dsn and pings once. An empty dsn uses the libpq PG*
// environment variables.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies every pending migration from memory/migrations. Goose
// holds a Postgres session advisory lock while it runs, so concurrent
// gateways apply each migration exactly once.
func (s *Store) Migrate(ctx context.Context) error {
	fsys, err := fs.Sub(memory.Migrations, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("create migration lock: %w", err)
	}
	db := stdlib.OpenDBFromPool(s.pool)
	defer func() { _ = db.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// Token is what a gateway token maps to. TaskID "" means no task.
type Token struct {
	RunID     string
	TaskID    string
	Stage     string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// CreateToken stores the SHA-256 of a token.
func (s *Store) CreateToken(ctx context.Context, sha256 []byte, t Token) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO gateway_tokens (token_sha256, run_id, task_id, stage, expires_at)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5)`,
		sha256, t.RunID, t.TaskID, t.Stage, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert gateway token: %w", err)
	}
	return nil
}

// LookupToken returns the token with this hash, whatever its expiry or
// revocation; the caller decides validity.
func (s *Store) LookupToken(ctx context.Context, sha256 []byte) (Token, error) {
	var t Token
	err := s.pool.QueryRow(ctx,
		`SELECT run_id, COALESCE(task_id, ''), stage, expires_at, revoked_at
		 FROM gateway_tokens WHERE token_sha256 = $1`, sha256).
		Scan(&t.RunID, &t.TaskID, &t.Stage, &t.ExpiresAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("look up gateway token: %w", err)
	}
	return t, nil
}

// AgentCall is one agent_calls row. CostUSD nil stores NULL (unpriced model).
type AgentCall struct {
	RunID              string
	TaskID             string
	Stage              string
	Model              string
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	ServerToolRequests int64
	CostUSD            *string // decimal text, e.g. "0.012345"
	LatencyMS          int64
	HTTPStatus         int
}

// InsertAgentCall writes one row and returns its id.
func (s *Store) InsertAgentCall(ctx context.Context, c AgentCall) (int64, error) {
	var cost sql.NullString
	if c.CostUSD != nil {
		cost = sql.NullString{String: *c.CostUSD, Valid: true}
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO agent_calls (run_id, task_id, stage, model, input_tokens, output_tokens,
		   cache_read_tokens, cache_write_tokens, server_tool_requests, cost_usd, latency_ms, http_status)
		 VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8, $9, $10::numeric, $11, $12)
		 RETURNING id`,
		c.RunID, c.TaskID, c.Stage, c.Model, c.InputTokens, c.OutputTokens,
		c.CacheReadTokens, c.CacheWriteTokens, c.ServerToolRequests, cost, c.LatencyMS, c.HTTPStatus).
		Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert agent call: %w", err)
	}
	return id, nil
}
