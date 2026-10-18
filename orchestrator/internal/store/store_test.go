package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/testpg"
)

func open(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, testpg.New(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestMigrateEmptyDatabase(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, table := range []string{"agent_calls", "gateway_tokens"} {
		var ok bool
		if err := s.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&ok); err != nil || !ok {
			t.Errorf("table %s missing (err %v)", table, err)
		}
	}
	// A second run is a no-op.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestTokenRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	hash := make([]byte, 32)
	hash[0] = 1
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	if err := s.CreateToken(ctx, hash, Token{RunID: "r1", Stage: "smoke", ExpiresAt: exp}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	got, err := s.LookupToken(ctx, hash)
	if err != nil {
		t.Fatalf("LookupToken: %v", err)
	}
	if got.RunID != "r1" || got.TaskID != "" || got.Stage != "smoke" || !got.ExpiresAt.Equal(exp) || got.RevokedAt != nil {
		t.Errorf("got %+v", got)
	}
	other := make([]byte, 32)
	if _, err := s.LookupToken(ctx, other); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("unknown hash: err = %v, want ErrTokenNotFound", err)
	}
}

func TestInsertAgentCall(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cost := "0.037200"
	id, err := s.InsertAgentCall(ctx, AgentCall{RunID: "r", TaskID: "t", Stage: "s", Model: "m",
		InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4, ServerToolRequests: 5,
		CostUSD: &cost, LatencyMS: 6, HTTPStatus: 200})
	if err != nil {
		t.Fatalf("InsertAgentCall: %v", err)
	}
	var gotCost *string
	var task *string
	if err := s.pool.QueryRow(ctx, `SELECT cost_usd::text, task_id FROM agent_calls WHERE id=$1`, id).Scan(&gotCost, &task); err != nil {
		t.Fatal(err)
	}
	if gotCost == nil || *gotCost != cost || task == nil || *task != "t" {
		t.Errorf("cost %v task %v", gotCost, task)
	}
	id2, err := s.InsertAgentCall(ctx, AgentCall{RunID: "r", Stage: "s", LatencyMS: 1, HTTPStatus: 499})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT cost_usd::text, task_id FROM agent_calls WHERE id=$1`, id2).Scan(&gotCost, &task); err != nil {
		t.Fatal(err)
	}
	if gotCost != nil || task != nil {
		t.Errorf("want NULL cost and task, got %v %v", gotCost, task)
	}
}
