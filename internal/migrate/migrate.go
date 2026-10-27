// Package migrate applies embedded, ordered *.up.sql migrations per owner
// (the platform and each module). Each owner has its own Postgres schema and
// its own namespace in the ledger, so modules can be added or removed without
// renumbering anyone else. Applied migrations are immutable: a changed
// checksum fails boot.
package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/jmoiron/sqlx"
)

// Set is one owner's migrations: Owner is also its Postgres schema name.
type Set struct {
	Owner string
	Files fs.FS // *.up.sql at the root; nil means no migrations
}

// lockKey is the advisory lock shared by Apply and Lock.
const lockKey = 511020771

// Lock runs fn while holding the same advisory lock Apply takes, so boot-time
// work that must not interleave across replicas (installing the framework
// packs) is serialized with migrations and with itself. fn runs on its own
// pool connections; the lock lives on a dedicated transaction that ends when
// fn returns.
func Lock(ctx context.Context, db *sqlx.DB, fn func(ctx context.Context) error) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return errx.Wrap(err, "begin lock", errx.TypeInternal)
	}
	defer tx.Rollback() //nolint:errcheck // releases the lock
	if err = noTimeouts(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		return errx.Wrap(err, "acquire lock", errx.TypeInternal)
	}
	return fn(ctx)
}

// noTimeouts lifts the session's lock_timeout and statement_timeout for this
// transaction only (SET LOCAL). The application connects with short timeouts
// (bootstrap sessionTimeouts), but waiting for another replica's migration on
// the advisory lock and running long DDL are legitimate here and must not be
// cut short. It must run before the lock is requested.
func noTimeouts(ctx context.Context, tx *sqlx.Tx) error {
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = 0; SET LOCAL statement_timeout = 0`); err != nil {
		return errx.Wrap(err, "lift session timeouts", errx.TypeInternal)
	}
	return nil
}

var ownerName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Apply runs pending migrations of every set, in order, in one transaction
// under an advisory lock so concurrent replicas never migrate twice.
func Apply(ctx context.Context, db *sqlx.DB, sets ...Set) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return errx.Wrap(err, "begin migrations", errx.TypeInternal)
	}
	defer tx.Rollback()
	if err = noTimeouts(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		return errx.Wrap(err, "lock migrations", errx.TypeInternal)
	}
	if _, err = tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS platform;
		CREATE TABLE IF NOT EXISTS platform.schema_migrations (
			owner text NOT NULL, name text NOT NULL, checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (owner, name))`); err != nil {
		return errx.Wrap(err, "create migration ledger", errx.TypeInternal)
	}
	for _, set := range sets {
		if err := applySet(ctx, tx, set); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return errx.Wrap(err, "commit migrations", errx.TypeInternal)
	}
	return nil
}

func applySet(ctx context.Context, tx *sqlx.Tx, set Set) error {
	if !ownerName.MatchString(set.Owner) {
		return errx.Internal("invalid migration owner: " + set.Owner)
	}
	if set.Files == nil {
		return nil
	}
	names, err := fs.Glob(set.Files, "*.up.sql")
	if err != nil {
		return errx.Wrap(err, "list migrations of "+set.Owner, errx.TypeInternal)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+set.Owner); err != nil {
		return errx.Wrap(err, "create schema "+set.Owner, errx.TypeInternal)
	}
	for _, name := range names {
		content, err := fs.ReadFile(set.Files, name)
		if err != nil {
			return errx.Wrap(err, "read migration "+set.Owner+"/"+name, errx.TypeInternal)
		}
		sum := sha256.Sum256(content)
		checksum := hex.EncodeToString(sum[:])
		var stored string
		err = tx.GetContext(ctx, &stored,
			`SELECT checksum FROM platform.schema_migrations WHERE owner = $1 AND name = $2`, set.Owner, name)
		if err == nil {
			if stored != checksum {
				return errx.Conflict("applied migration was modified: " + set.Owner + "/" + name)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return errx.Wrap(err, "read migration state", errx.TypeInternal)
		}
		if strings.TrimSpace(string(content)) != "" {
			if _, err = tx.ExecContext(ctx, string(content)); err != nil {
				return errx.Wrap(err, "apply migration "+set.Owner+"/"+name, errx.TypeInternal)
			}
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO platform.schema_migrations (owner, name, checksum) VALUES ($1, $2, $3)`,
			set.Owner, name, checksum); err != nil {
			return errx.Wrap(err, "record migration "+set.Owner+"/"+name, errx.TypeInternal)
		}
	}
	return nil
}
