// Package pgtest gives repository and service tests a real, migrated,
// throwaway Postgres database. Tests are skipped unless
// ODYSSEY_TEST_DATABASE_URL points at a server where the user may CREATE
// DATABASE (`make test-db` sets it to the compose database).
package pgtest

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// EnvURL names the variable with the admin connection string.
const EnvURL = "ODYSSEY_TEST_DATABASE_URL"

// EnvRequire, when set (CI), turns a missing EnvURL into a failure instead
// of a skip, so a misconfigured pipeline can't pass without running them.
const EnvRequire = "ODYSSEY_TEST_REQUIRE_DB"

// DB creates a fresh database, applies the given migration sets (platform
// and module sets; pass exactly what the code under test needs) and drops it
// when the test ends.
func DB(t testing.TB, sets ...migrate.Set) *sqlx.DB {
	t.Helper()
	admin := os.Getenv(EnvURL)
	if admin == "" {
		if os.Getenv(EnvRequire) != "" {
			t.Fatal(EnvURL + " not set but " + EnvRequire + " is: Postgres tests must not skip")
		}
		t.Skip(EnvURL + " not set; skipping Postgres test (run make test-db)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root, err := sqlx.ConnectContext(ctx, "postgres", admin)
	if err != nil {
		t.Fatalf("pgtest: connect admin: %v", err)
	}
	name := "odyssey_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := root.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		root.Close()
		t.Fatalf("pgtest: create database: %v", err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("pgtest: parse %s: %v", EnvURL, err)
	}
	u.Path = "/" + name
	db, err := sqlx.ConnectContext(ctx, "postgres", u.String())
	if err != nil {
		t.Fatalf("pgtest: connect test database: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := root.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Logf("pgtest: drop %s: %v", name, err)
		}
		root.Close()
	})
	if err := migrate.Apply(ctx, db, sets...); err != nil {
		t.Fatalf("pgtest: migrate: %v", err)
	}
	return db
}

// URL is the connection string of database name on the test server (for
// code that opens its own connections, e.g. LISTEN).
func URL(t testing.TB, name string) string {
	t.Helper()
	u, err := url.Parse(os.Getenv(EnvURL))
	if err != nil {
		t.Fatalf("pgtest: parse %s: %v", EnvURL, err)
	}
	u.Path = "/" + name
	return u.String()
}
