package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dbstartup"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/migrations"
)

// preMigrationHook runs work that must happen before a specific migration is
// applied in the direction whose hook map selected it. Hooks are idempotent and
// must not depend on the migration loop's session-pinned advisory lock
// — they run on the pool, not on the loop's pinned conn, so they can
// safely acquire other session-level locks (e.g. advisory lock 4246
// for the task_usage hourly rollup).
//
// Returning an error aborts the migration run. The corresponding migration is
// not added to (up) or removed from (down) schema_migrations, so the next run
// retries the hook + migration.
type preMigrationHook func(ctx context.Context, pool *pgxpool.Pool) error

// concurrentIndexCleanups maps a migration version to the index it builds with
// CREATE INDEX CONCURRENTLY. Every entry gets an invalid-index cleanup hook, so
// an interrupted build cannot be mistaken for success on retry: IF NOT EXISTS
// would otherwise skip an INVALID leftover and record the migration as applied.
// TestEveryConcurrentUpBuildHasCleanup fails the build when a migration that
// builds an index concurrently is missing here.
var concurrentIndexCleanups = map[string]string{
	"591_bot_webhook_receiver_name_index": "bot_webhook_receiver_bot_name",
}

// concurrentDownIndexCleanups is the rollback counterpart: every down migration
// that rebuilds an index concurrently.
var concurrentDownIndexCleanups = map[string]string{}

var preMigrationHooks = cleanupHooks(concurrentIndexCleanups)

var preRollbackHooks = cleanupHooks(concurrentDownIndexCleanups)

func cleanupHooks(cleanups map[string]string) map[string]preMigrationHook {
	hooks := make(map[string]preMigrationHook, len(cleanups))
	for version, index := range cleanups {
		hooks[version] = cleanupInvalidConcurrentIndexHook(index)
	}
	return hooks
}

func hooksForDirection(direction string) map[string]preMigrationHook {
	switch direction {
	case "up":
		return preMigrationHooks
	case "down":
		return preRollbackHooks
	default:
		return nil
	}
}

// cleanupInvalidConcurrentIndexHook removes an INVALID index left by an
// interrupted or failed CREATE INDEX CONCURRENTLY before the migration retries.
// Without this guard, CREATE INDEX ... IF NOT EXISTS would treat the leftover
// relation as success and allow a later migration to drop the still-valid old
// index. Non-index relations fail closed instead of being dropped implicitly.
func cleanupInvalidConcurrentIndexHook(indexRegclass string) preMigrationHook {
	return func(ctx context.Context, pool *pgxpool.Pool) error {
		var schemaName, relationName string
		var isIndex, isValid bool
		err := pool.QueryRow(ctx, `
			SELECT n.nspname, c.relname, c.relkind = 'i', COALESCE(i.indisvalid, FALSE)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_index i ON i.indexrelid = c.oid
			WHERE c.oid = to_regclass($1)
		`, indexRegclass).Scan(&schemaName, &relationName, &isIndex, &isValid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect concurrent index %q: %w", indexRegclass, err)
		}
		if !isIndex {
			return fmt.Errorf("relation %q exists but is not an index", indexRegclass)
		}
		if isValid {
			return nil
		}

		qualifiedName := pgx.Identifier{schemaName, relationName}.Sanitize()
		if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+qualifiedName); err != nil {
			return fmt.Errorf("drop invalid concurrent index %s: %w", qualifiedName, err)
		}
		slog.Warn("removed invalid index before migration retry", "index", qualifiedName)
		return nil
	}
}

// migrationAdvisoryLockKey is the int64 identifier used with Postgres
// pg_advisory_lock to serialize the migration loop across concurrent
// runners (multi-replica backend Deployment, scale-up, or a manual
// `migrate up` overlapping with pod startup). The exact value is
// arbitrary — it just needs to be stable across every process that runs
// migrations against the same database. See GitHub multica-ai/multica#3647.
const migrationAdvisoryLockKey int64 = 7244554146635925501

// defaultSchemaMigrationsTable is the unqualified name of the bookkeeping
// table that tracks which migrations have been applied. Tests override
// this so a concurrent-race harness can run against the same shared
// Postgres without colliding with the production table.
const defaultSchemaMigrationsTable = "schema_migrations"

// runOptions carries everything runMigrations needs that is not the
// pool itself. Tests use it to inject a hermetic migrations directory,
// a unique per-test bookkeeping table, and a unique advisory-lock key
// that doesn't collide with any other migration runner sharing the same
// Postgres instance.
type runOptions struct {
	// Direction is "up" or "down".
	Direction string
	// Files is the ordered list of .sql files to apply. Production callers
	// pass migrations.Files(direction); tests pass a curated set written
	// to a t.TempDir().
	Files []string
	// SchemaMigrationsTable is the bookkeeping table to read/write.
	// May be schema-qualified (e.g. "migrate_test_xyz.schema_migrations").
	// Empty means defaultSchemaMigrationsTable.
	SchemaMigrationsTable string
	// AdvisoryLockKey is the int64 used with pg_advisory_lock. Zero means
	// migrationAdvisoryLockKey. Tests pass a unique key per run so
	// concurrent test workers do not block on the production migration
	// runner if it happens to share the database.
	AdvisoryLockKey int64
	// Hooks maps migration version → pre-migration hook. The hook
	// receives the pool (not the loop's pinned conn) so it can take
	// its own session-level locks. nil or missing entries mean "no
	// hook" and the migration runs straight through. Production main()
	// passes the direction-specific hook map; tests leave this nil unless they
	// exercise a hook.
	Hooks map[string]preMigrationHook
}

func main() {
	logger.Init()

	if len(os.Args) < 2 {
		fmt.Println("Usage: go run ./cmd/migrate <up|down>")
		os.Exit(1)
	}

	direction := os.Args[1]
	if direction != "up" && direction != "down" {
		fmt.Println("Usage: go run ./cmd/migrate <up|down>")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	startupSettings := dbstartup.SettingsFromEnv()
	poolConfig, err := dbstartup.ParsePoolConfig(dbURL, startupSettings.ConnectTimeout)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		os.Exit(1)
	}
	poolConfig.ConnConfig.OnNotice = logMigrationNotice
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	files, err := migrations.Files(direction)
	if err != nil {
		slog.Error("failed to find migration files", "error", err)
		os.Exit(1)
	}

	options := runOptions{
		Direction: direction,
		Files:     files,
		Hooks:     hooksForDirection(direction),
	}
	startupCtx, stopStartup := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopStartup()
	retryOptions := startupSettings.RetryOptions()
	retryOptions.ShouldRetry = dbstartup.IsTransientDatabaseError
	retryOptions.OnRetry = func(event dbstartup.RetryEvent) {
		slog.Warn("database unavailable before migrations; retrying",
			"attempt", event.Attempt,
			"retry_in", event.Delay,
			"error", event.Err,
		)
	}
	if err := dbstartup.Retry(startupCtx, retryOptions, pool.Ping); err != nil {
		slog.Error("unable to ping database", "error", err)
		os.Exit(1)
	}

	migrationErr := runMigrations(startupCtx, pool, options)
	if migrationErr != nil && startupSettings.StartupTimeout > 0 && dbstartup.IsTransientDatabaseError(migrationErr) {
		slog.Warn("migration interrupted by database unavailability; retrying", "error", migrationErr)
		migrationRetryOptions := startupSettings.RetryOptions()
		migrationRetryOptions.ShouldRetry = dbstartup.IsTransientDatabaseError
		migrationRetryOptions.AllowOperationPastTimeout = true
		migrationRetryOptions.OnRetry = func(event dbstartup.RetryEvent) {
			slog.Warn("database unavailable during migration retry",
				"attempt", event.Attempt,
				"retry_in", event.Delay,
				"error", event.Err,
			)
		}
		migrationErr = dbstartup.Retry(startupCtx, migrationRetryOptions, func(attemptCtx context.Context) error {
			if err := pool.Ping(attemptCtx); err != nil {
				return fmt.Errorf("ping database: %w", err)
			}
			return runMigrations(attemptCtx, pool, options)
		})
	}
	if migrationErr != nil {
		slog.Error("migration run failed", "error", migrationErr)
		os.Exit(1)
	}

	fmt.Println("Done.")
}

// migrationReportPrefix marks a notice a migration raises on purpose to say
// what it changed, e.g.
//
//	RAISE NOTICE 'migration report: renamed % row(s)', n;
//
// Only these reach the migration log. The server's own notices cannot be told
// apart from a deliberate RAISE by condition code — "does not exist, skipping"
// and the rename notice of ADD CONSTRAINT ... USING INDEX both carry SQLSTATE
// 00000 — so the marker is explicit.
const migrationReportPrefix = "migration report: "

// migrationReport returns the text of a notice raised with
// migrationReportPrefix, and false for every other notice.
func migrationReport(notice *pgconn.Notice) (string, bool) {
	return strings.CutPrefix(notice.Message, migrationReportPrefix)
}

// logMigrationNotice forwards migration reports to the migration log. pgx
// drops server notices unless a handler is set, so without it the per-row
// report a data-repair migration prints (e.g. 476) never reached whoever ran
// the deploy.
func logMigrationNotice(_ *pgconn.PgConn, notice *pgconn.Notice) {
	if report, ok := migrationReport(notice); ok {
		slog.Info("migration report", "message", report)
	}
}

// runMigrations applies (direction="up") or rolls back (direction="down")
// the given file list against the supplied pool, serialized through a
// Postgres session-level advisory lock so multiple concurrent runners
// (multi-replica startup, scale-up, manual migrate overlap) take turns
// instead of racing each other.
//
// It is safe to invoke concurrently from multiple goroutines or
// processes against the same database with the same options: every
// caller blocks on pg_advisory_lock, and once it is their turn the
// already-applied EXISTS check turns each finished migration into a
// no-op skip. See GitHub multica-ai/multica#3647 / MUL-2923.
func runMigrations(ctx context.Context, pool *pgxpool.Pool, opts runOptions) error {
	switch opts.Direction {
	case "up", "down":
		// ok
	default:
		return fmt.Errorf("invalid direction %q (want \"up\" or \"down\")", opts.Direction)
	}

	table := opts.SchemaMigrationsTable
	if table == "" {
		table = defaultSchemaMigrationsTable
	}
	tableIdent, err := quoteQualifiedIdentifier(table)
	if err != nil {
		return fmt.Errorf("invalid schema migrations table %q: %w", table, err)
	}
	lockKey := opts.AdvisoryLockKey
	if lockKey == 0 {
		lockKey = migrationAdvisoryLockKey
	}

	// pg_advisory_lock is scoped to a single session, so we must pin one
	// *pgxpool.Conn for the whole run — calling pool.Exec would attach the
	// lock to a random connection that pgxpool could hand back out before
	// the loop finishes, making the lock effectively a no-op. We use the
	// blocking pg_advisory_lock (not pg_try_*) so a late-arriving runner
	// queues behind the current one instead of crash-looping; once it
	// acquires the lock the EXISTS checks below turn finished migrations
	// into no-op skips.
	//
	// We deliberately do NOT wrap the loop in a single transaction: the
	// repo already ships migrations using CREATE INDEX CONCURRENTLY,
	// which Postgres rejects inside a transaction block.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	// Best-effort explicit unlock on the success path. On error returns
	// the defer still runs; on os.Exit error paths in main() it does not,
	// but session-level advisory locks are released automatically when
	// the connection closes at process exit, so the next runner is never
	// permanently blocked.
	defer func() {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			slog.Warn("failed to release migration advisory lock", "error", err)
		}
	}()

	// Create migrations tracking table.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`, tableIdent)); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	existsSQL := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE version = $1)", tableIdent)
	insertSQL := fmt.Sprintf("INSERT INTO %s (version) VALUES ($1)", tableIdent)
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE version = $1", tableIdent)

	if opts.Direction == "up" {
		if err := adoptBaseline(ctx, conn, tableIdent, opts.Files); err != nil {
			return err
		}
	}

	for _, file := range opts.Files {
		version := migrations.ExtractVersion(file)

		var exists bool
		if err := conn.QueryRow(ctx, existsSQL, version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %q: %w", version, err)
		}

		if opts.Direction == "up" {
			if exists {
				fmt.Printf("  skip  %s (already applied)\n", version)
				continue
			}
		} else {
			if !exists {
				fmt.Printf("  skip  %s (not applied)\n", version)
				continue
			}
		}

		sql, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", file, err)
		}

		// Run any pre-migration hook before the SQL file. Hooks
		// receive the *pgxpool.Pool (not the loop's pinned conn), so
		// they can acquire other session-level locks without
		// colliding with migrationAdvisoryLockKey. Hook failures
		// abort the run before schema_migrations is updated, so the
		// same version retries cleanly on the next invocation.
		if hook, ok := opts.Hooks[version]; ok && hook != nil {
			slog.Info("running pre-migration hook", "version", version, "direction", opts.Direction)
			if err := hook(ctx, pool); err != nil {
				return fmt.Errorf("pre-migration hook for %q (%s): %w", version, opts.Direction, err)
			}
		}

		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %q: %w", file, err)
		}

		if opts.Direction == "up" {
			_, err = conn.Exec(ctx, insertSQL, version)
		} else {
			_, err = conn.Exec(ctx, deleteSQL, version)
		}
		if err != nil {
			return fmt.Errorf("record migration %q: %w", version, err)
		}

		fmt.Printf("  %s  %s\n", opts.Direction, version)
	}

	return nil
}

// quoteQualifiedIdentifier safely quotes either an unqualified table
// name ("foo") or a schema-qualified name ("schema.foo") for embedding
// into a SQL statement. Postgres does not let parametrized queries
// supply identifiers, so we have to interpolate, but pgx.Identifier
// does the right escaping (double-quotes, embedded-quote handling).
//
// The accepted shape is exactly one or two dot-separated components.
// Names containing more than one dot are rejected outright rather than
// silently sanitized into a "schema"."b.c" reference, which is valid
// SQL but almost certainly not what the caller meant.
func quoteQualifiedIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty identifier")
	}
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("identifier %q has more than one dot; only schema.table is supported", name)
	}
	for _, p := range parts {
		if p == "" {
			return "", fmt.Errorf("empty component in %q", name)
		}
	}
	return pgx.Identifier(parts).Sanitize(), nil
}

// baselineVersion is the single migration that holds the whole schema as of
// baselineSupersedes. Migrations 001_init..585 were compressed into it
// (DENE-2018); a fresh database applies only the baseline.
const (
	baselineVersion    = "001_baseline"
	baselineSupersedes = "585_issue_origin_meeting_validate"
)

// adoptBaseline records the baseline as applied, without running it, on a
// database that already ran every migration the baseline replaces. A database
// that ran only some of them is refused: the baseline cannot finish a partial
// upgrade, so it must first be brought to baselineSupersedes with a build from
// before the compression, or be recreated empty.
func adoptBaseline(ctx context.Context, conn *pgxpool.Conn, tableIdent string, files []string) error {
	hasBaseline := false
	for _, file := range files {
		if migrations.ExtractVersion(file) == baselineVersion {
			hasBaseline = true
			break
		}
	}
	if !hasBaseline {
		return nil
	}

	var applied, superseded, anyRow bool
	if err := conn.QueryRow(ctx, fmt.Sprintf(`
		SELECT
			EXISTS (SELECT 1 FROM %[1]s WHERE version = $1),
			EXISTS (SELECT 1 FROM %[1]s WHERE version = $2),
			EXISTS (SELECT 1 FROM %[1]s)
	`, tableIdent), baselineVersion, baselineSupersedes).Scan(&applied, &superseded, &anyRow); err != nil {
		return fmt.Errorf("check baseline: %w", err)
	}
	switch {
	case applied || !anyRow:
		return nil
	case superseded:
		if _, err := conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s (version) VALUES ($1)", tableIdent), baselineVersion); err != nil {
			return fmt.Errorf("record baseline: %w", err)
		}
		fmt.Printf("  adopt %s (database already at %s)\n", baselineVersion, baselineSupersedes)
		return nil
	default:
		return fmt.Errorf("database predates %s but has not reached %s; upgrade it with a build from before the baseline first, or start from an empty database", baselineVersion, baselineSupersedes)
	}
}
