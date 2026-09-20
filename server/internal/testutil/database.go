package testutil

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDatabaseURL returns the database DB-backed tests connect to.
//
// TEST_DATABASE_URL wins over DATABASE_URL so a runner can hand the suite a
// database of its own without changing what the application under test
// resolves. `scripts/test-db.sh` sets both to a database it created for this
// run and drops again when the run ends, which is what keeps one run's rows out
// of the next run's assertions.
//
// There is deliberately no localhost fallback. A hardcoded default cannot be
// right — this checkout's database name is allocated per worktree, never the
// `multica` a constant would guess — and it hides the difference between "the
// suite ran against the configured database" and "the suite found nothing and
// skipped". Callers turn an empty result into a skip through SkipDatabase.
func TestDatabaseURL() string {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	return os.Getenv("DATABASE_URL")
}

// RequireTestDatabase reports whether this run promised a database.
//
// `scripts/test-db.sh` sets MULTICA_REQUIRE_TEST_DB=1 because it created the
// database the suite is about to use: anything that cannot reach it now is a
// real problem, not a machine that happens to lack Postgres.
func RequireTestDatabase() bool {
	return os.Getenv("MULTICA_REQUIRE_TEST_DB") == "1"
}

// SkipDatabase reports that the test database is unreachable and stops the
// calling test — as a failure when this run required a database, otherwise as
// a skip, which is what lets a laptop without Postgres still run the rest of
// the tree.
//
// Every DB-backed package needs exactly this decision, and each one used to
// spell it out (and the URL above) for itself. Keep them here so a run cannot
// be green in one package and loud in another.
func SkipDatabase(t testing.TB, err error) {
	t.Helper()
	if RequireTestDatabase() {
		t.Fatalf("database required (MULTICA_REQUIRE_TEST_DB=1) but unreachable: %v", err)
	}
	t.Skipf("skipping: database not reachable: %v", err)
}

// OpenTestDatabase connects to the test database and stops the calling test if
// it is unreachable. The pool is closed with the test.
//
// It is the one place a DB-backed suite should get its pool from: the URL, the
// connect, and the skip-or-fail decision are all shared state, and a suite that
// resolved them differently from its neighbours is how a leftover-row failure
// becomes a package-shaped mystery.
func OpenTestDatabase(ctx context.Context, t testing.TB) *pgxpool.Pool {
	t.Helper()

	url := TestDatabaseURL()
	if url == "" {
		SkipDatabase(t, errNoTestDatabase)
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		SkipDatabase(t, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		SkipDatabase(t, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// errNoTestDatabase reports the one failure OpenTestDatabase invents rather
// than observes: no database was configured at all.
var errNoTestDatabase = errTestDatabaseNotConfigured{}

type errTestDatabaseNotConfigured struct{}

func (errTestDatabaseNotConfigured) Error() string {
	return "neither TEST_DATABASE_URL nor DATABASE_URL is set; run tests through `make test` (scripts/test-go.sh) or set one of them"
}
