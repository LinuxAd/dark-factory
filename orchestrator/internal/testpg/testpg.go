// Package testpg gives integration tests a fresh, empty Postgres database.
//
// Tests that need Postgres call New. It reads GATEWAY_TEST_DATABASE_URL, a DSN
// for a server where the user may CREATE DATABASE; `make test` starts a
// throwaway postgres container and sets it. Without it the test is skipped.
package testpg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnvVar names the admin DSN used to create per-test databases.
const EnvVar = "GATEWAY_TEST_DATABASE_URL"

// New creates a database unique to t, drops it when t ends and returns its DSN.
func New(t testing.TB) string {
	t.Helper()
	admin := os.Getenv(EnvVar)
	if admin == "" {
		t.Skipf("integration test skipped: %s is not set (run `make -C orchestrator test`, which starts Postgres in Docker)", EnvVar)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "t_" + hex.EncodeToString(b)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvVar, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop database %s: %v", name, err)
		}
	})

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvVar, err)
	}
	u.Path = "/" + name
	return u.String()
}
