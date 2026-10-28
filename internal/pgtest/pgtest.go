// Package pgtest gives repository and service tests a real, migrated,
// throwaway Postgres database.
//
// The server is, in order: ODYSSEY_TEST_DATABASE_URL (any server where the
// user may CREATE DATABASE), or a Postgres container started with
// testcontainers once per test binary (removed when the binary exits).
// Without either (no Docker, or `go test -short`) the tests skip, unless
// ODYSSEY_TEST_REQUIRE_DB is set: then they fail.
package pgtest

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// EnvURL names the variable with the admin connection string of an existing
// server (skips the container).
const EnvURL = "ODYSSEY_TEST_DATABASE_URL"

// EnvRequire, when set (CI), turns a missing database into a failure instead
// of a skip, so a misconfigured pipeline can't pass without running them.
const EnvRequire = "ODYSSEY_TEST_REQUIRE_DB"

// EnvImage overrides the container image (default: the compose database's).
const EnvImage = "ODYSSEY_TEST_POSTGRES_IMAGE"

const defaultImage = "postgres:16-alpine"

var server struct {
	once sync.Once
	url  string
	err  error
}

// adminURL is the connection string of the test server, starting the
// container on first use.
func adminURL(t testing.TB) string {
	t.Helper()
	if u := os.Getenv(EnvURL); u != "" {
		return u
	}
	if testing.Short() {
		unavailable(t, "-short: skipping Postgres test")
	}
	server.once.Do(func() { server.url, server.err = start() })
	if server.err != nil {
		unavailable(t, "pgtest: no "+EnvURL+" and no Postgres container: "+server.err.Error())
	}
	return server.url
}

func unavailable(t testing.TB, why string) {
	t.Helper()
	if os.Getenv(EnvRequire) != "" {
		t.Fatal(why + " (" + EnvRequire + " is set: Postgres tests must not skip)")
	}
	t.Skip(why)
}

// start runs the shared container. It is not terminated explicitly: it
// lives as long as the test binary and testcontainers' reaper removes it.
func start() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	image := os.Getenv(EnvImage)
	if image == "" {
		image = defaultImage
	}
	c, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.WithDatabase("postgres"),
		tcpostgres.BasicWaitStrategies(),
		// Throwaway data: durability off makes the many small databases fast.
		testcontainers.WithCmd("postgres", "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off", "-c", "max_connections=300"),
	)
	if err != nil {
		return "", err
	}
	return c.ConnectionString(ctx, "sslmode=disable")
}

// DB creates a fresh database, applies the given migration sets (platform
// and module sets; pass exactly what the code under test needs) and drops it
// when the test ends.
func DB(t testing.TB, sets ...migrate.Set) *sqlx.DB {
	t.Helper()
	admin := adminURL(t)
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
	db, err := sqlx.ConnectContext(ctx, "postgres", withDatabase(t, admin, name))
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
	return withDatabase(t, adminURL(t), name)
}

func withDatabase(t testing.TB, admin, name string) string {
	t.Helper()
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("pgtest: parse admin URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}
