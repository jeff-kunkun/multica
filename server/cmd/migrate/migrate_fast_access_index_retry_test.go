package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Exercise the real migration SQL after a canceled concurrent build leaves an
// invalid index. A plain IF NOT EXISTS retry must not count as a repair.
func TestFastAccessIndexesRecoverAfterInterruptedBuild(t *testing.T) {
	for _, tc := range []struct{ version, index, table, column string }{
		{"674_fast_access_pending_transaction_index", "fast_access_pending_transaction", "fast_access_pending", "transaction_id"},
		{"675_fast_access_journal_retention_index", "fast_access_journal_retention", "fast_access_journal", "changed_at"},
		{"676_fast_access_cursor_expiry_index", "fast_access_cursor_expiry", "fast_access_cursor", "expires_at"},
		{"677_fast_access_batch_expiry_index", "fast_access_batch_expiry", "fast_access_batch", "expires_at"},
	} {
		t.Run(tc.version, func(t *testing.T) { testFastAccessIndexRetry(t, tc.version, tc.index, tc.table, tc.column) })
	}
}

func testFastAccessIndexRetry(t *testing.T, version, indexName, table, column string) {
	t.Parallel()
	pool := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	suffix := fmt.Sprintf("%d_%d", time.Now().UnixNano(), rand.Uint32())
	schema := "migrate_fast_access_" + suffix
	schemaIdent := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schemaIdent); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaIdent+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})

	columnType := "TIMESTAMPTZ"
	if column == "transaction_id" {
		columnType = "BIGINT"
	}
	tableName := pgx.Identifier{schema, table}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE TABLE "+tableName+" ("+pgx.Identifier{column}.Sanitize()+" "+columnType+")"); err != nil {
		t.Fatalf("create task table: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", version+".up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	createIndex := strings.Replace(string(raw), "ON "+table+"(", "ON "+tableName+"(", 1)

	// Interrupt the build the way a real one gets interrupted: an open
	// transaction that has written to the table owns an xid the concurrent
	// build must wait for, and statement_timeout cancels the wait.
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker conn: %v", err)
	}
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		blocker.Release()
		t.Fatalf("begin blocker tx: %v", err)
	}
	if _, err := blockerTx.Exec(ctx, "INSERT INTO "+tableName+" DEFAULT VALUES"); err != nil {
		blocker.Release()
		t.Fatalf("blocker insert: %v", err)
	}

	builder, err := pool.Acquire(ctx)
	if err != nil {
		blocker.Release()
		t.Fatalf("acquire builder conn: %v", err)
	}
	if _, err := builder.Exec(ctx, "SET statement_timeout = '2s'"); err != nil {
		builder.Release()
		blocker.Release()
		t.Fatalf("set statement_timeout: %v", err)
	}
	_, buildErr := builder.Exec(ctx, createIndex)
	// pgxpool does not reset session state, so clear the fuse before the
	// connection goes back to the pool.
	if _, err := builder.Exec(ctx, "SET statement_timeout = DEFAULT"); err != nil {
		t.Logf("reset statement_timeout: %v", err)
	}
	builder.Release()
	if buildErr == nil {
		blocker.Release()
		t.Fatal("interrupted build unexpectedly succeeded")
	}
	_ = blockerTx.Rollback(ctx)
	blocker.Release()

	assertIndexValidity(t, pool, schema, indexName, false)
	t.Logf("%s: index INVALID after failed build / retry without cleanup", indexName)

	migrationPath := filepath.Join(t.TempDir(), version+".up.sql")
	if err := os.WriteFile(migrationPath, []byte(createIndex), 0o600); err != nil {
		t.Fatalf("write retry migration: %v", err)
	}
	opts := runOptions{
		Direction:             "up",
		Files:                 []string{migrationPath},
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
	}

	// Without the hook the retry is a silent no-op: IF NOT EXISTS sees the
	// invalid relation, reports success, and the migration is recorded.
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatalf("retry without hook: %v", err)
	}
	assertIndexValidity(t, pool, schema, indexName, false)
	t.Logf("%s: index INVALID after failed build / retry without cleanup", indexName)

	// With the production hook — schema-qualified so it resolves inside the
	// test schema — the leftover is dropped and the index is rebuilt.
	if preMigrationHooks[version] == nil {
		t.Fatalf("production hook is not registered for %s", version)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM "+pgx.Identifier{schema, "schema_migrations"}.Sanitize()+" WHERE version = $1", version); err != nil {
		t.Fatalf("reset recorded version: %v", err)
	}
	opts.Hooks = map[string]preMigrationHook{
		version: cleanupInvalidConcurrentIndexHook(pgx.Identifier{schema, concurrentIndexCleanups[version]}.Sanitize()),
	}
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatalf("retry migration with invalid-index cleanup: %v", err)
	}
	assertIndexValidity(t, pool, schema, indexName, true)
	t.Logf("%s: index VALID after cleanup and retry", indexName)
}
