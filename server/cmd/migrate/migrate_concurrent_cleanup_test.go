package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sync/errgroup"
)

var concurrentIndexNamePattern = regexp.MustCompile(
	`(?i)CREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_]+)`)

// stripSQLLineComments drops `--` comment lines so prose that mentions SQL is
// not mistaken for SQL.
func stripSQLLineComments(body []byte) []byte {
	var kept [][]byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("--")) {
			continue
		}
		kept = append(kept, line)
	}
	return bytes.Join(kept, []byte("\n"))
}

// migrationSQL is what the audits below read out of one migration file, with
// its comment lines dropped first: the name of every index it builds
// concurrently, in file order.
type migrationSQL struct {
	builds []string
}

// migrationCorpus is every migration file, keyed by file name
// ("001_baseline.up.sql"), read and parsed once per package run.
var migrationCorpus = sync.OnceValues(func() (map[string]migrationSQL, error) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		return nil, err
	}
	parsed := make([]migrationSQL, len(paths))
	var g errgroup.Group
	g.SetLimit(16)
	for i, path := range paths {
		g.Go(func() error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			body := stripSQLLineComments(raw)
			var builds []string
			for _, match := range concurrentIndexNamePattern.FindAllSubmatch(body, -1) {
				builds = append(builds, string(match[1]))
			}
			parsed[i] = migrationSQL{builds: builds}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	corpus := make(map[string]migrationSQL, len(paths))
	for i, path := range paths {
		corpus[filepath.Base(path)] = parsed[i]
	}
	return corpus, nil
})

// migrationsInDirection returns the corpus and, sorted, the names of its files
// for one direction.
func migrationsInDirection(t *testing.T, direction string) (map[string]migrationSQL, []string) {
	t.Helper()
	corpus, err := migrationCorpus()
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	suffix := "." + direction + ".sql"
	var names []string
	for name := range corpus {
		if strings.HasSuffix(name, suffix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return corpus, names
}

// TestConcurrentIndexCleanupsMatchTheirMigrations guards the mapping that wires
// invalid-index cleanup hooks to migrations. A hook that names an index no
// migration creates is a silent no-op: the retry then treats the INVALID
// leftover as success and the index stays unusable. Nothing at runtime would
// report that, so the names are checked against the migration files here.
func TestConcurrentIndexCleanupsMatchTheirMigrations(t *testing.T) {
	t.Parallel()
	assertConcurrentIndexCleanupsMatchTheirMigrations(
		t,
		concurrentIndexCleanups,
		preMigrationHooks,
		"up",
	)
	assertConcurrentIndexCleanupsMatchTheirMigrations(
		t,
		concurrentDownIndexCleanups,
		preRollbackHooks,
		"down",
	)
}

// TestEveryConcurrentDownBuildHasCleanup works in the opposite direction from
// TestConcurrentIndexCleanupsMatchTheirMigrations: every rollback migration
// that builds an index concurrently must be registered. This prevents a new or
// existing down migration from silently missing retry cleanup.
func TestEveryConcurrentDownBuildHasCleanup(t *testing.T) {
	t.Parallel()
	assertEveryConcurrentBuildHasCleanup(t, "down", concurrentDownIndexCleanups)
}

// TestEveryConcurrentUpBuildHasCleanup is the up-direction counterpart. An
// unregistered build is invisible until a real interrupted migration turns into
// either a permanently INVALID index recorded as success (`IF NOT EXISTS`) or a
// wedged migrator (bare `CREATE`), so the check belongs here rather than in
// review.
func TestEveryConcurrentUpBuildHasCleanup(t *testing.T) {
	t.Parallel()
	assertEveryConcurrentBuildHasCleanup(t, "up", concurrentIndexCleanups)
}

func assertEveryConcurrentBuildHasCleanup(t *testing.T, direction string, cleanups map[string]string) {
	t.Helper()
	suffix := "." + direction + ".sql"
	corpus, names := migrationsInDirection(t, direction)
	if len(names) == 0 {
		t.Fatalf("no %s migrations found", direction)
	}

	for _, name := range names {
		builds := corpus[name].builds
		if len(builds) == 0 {
			continue
		}
		version := strings.TrimSuffix(name, suffix)
		if len(builds) != 1 {
			t.Errorf("%s: has %d concurrent index builds; cleanup registration supports exactly one", version, len(builds))
			continue
		}
		indexName := builds[0]
		registered, ok := cleanups[version]
		if !ok {
			t.Errorf("%s: builds %q concurrently on %s but has no %s cleanup", version, indexName, direction, direction)
			continue
		}
		if registered != indexName {
			t.Errorf("%s: %s cleanup registers %q, migration builds %q", version, direction, registered, indexName)
		}
	}
}

func assertConcurrentIndexCleanupsMatchTheirMigrations(
	t *testing.T,
	cleanups map[string]string,
	hooks map[string]preMigrationHook,
	direction string,
) {
	t.Helper()
	corpus, _ := migrationsInDirection(t, direction)
	for version, indexName := range cleanups {
		migration, ok := corpus[version+"."+direction+".sql"]
		if !ok {
			t.Errorf("%s: has a cleanup hook but no %s migration file", version, direction)
			continue
		}
		// The comment headers on these migrations mention CREATE INDEX
		// CONCURRENTLY in prose, so the corpus matches statements only.
		if len(migration.builds) == 0 {
			t.Errorf("%s: has a cleanup hook but builds no index concurrently", version)
			continue
		}
		if got := migration.builds[0]; got != indexName {
			t.Errorf("%s: hook cleans %q but the migration builds %q", version, indexName, got)
		}
		if hooks[version] == nil {
			t.Errorf("%s: no pre-migration hook registered", version)
		}
	}
}
