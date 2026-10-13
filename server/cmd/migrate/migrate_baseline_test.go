package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestRunMigrationsBaselineAdoption covers the three databases the baseline
// meets: an empty one applies it, one that already ran the files it replaced
// records it without running it, and one stopped part way is refused.
func TestRunMigrationsBaselineAdoption(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		recorded []string
		wantErr  string
		wantRan  bool
	}{
		{name: "empty", wantRan: true},
		{name: "superseded", recorded: []string{"573_password_login", baselineSupersedes}},
		{name: "partial", recorded: []string{"573_password_login"}, wantErr: "has not reached " + baselineSupersedes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ctx := context.Background()
			schemaIdent := pgx.Identifier{f.schema}.Sanitize()
			if _, err := f.pool.Exec(ctx, "CREATE TABLE "+schemaIdent+".schema_migrations (version TEXT PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			for _, version := range tc.recorded {
				if _, err := f.pool.Exec(ctx, "INSERT INTO "+schemaIdent+".schema_migrations VALUES ($1)", version); err != nil {
					t.Fatal(err)
				}
			}
			baseline := filepath.Join(t.TempDir(), baselineVersion+".up.sql")
			if err := os.WriteFile(baseline, []byte("CREATE TABLE "+schemaIdent+".baseline_marker (id int);\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			opts := f.opts()
			opts.Files = []string{baseline}

			err := runMigrations(ctx, f.pool, opts)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if got := f.appliedVersions(t); len(got) != len(tc.recorded) {
					t.Fatalf("refused run recorded versions: %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := f.tableExists(t, "baseline_marker"); got != tc.wantRan {
				t.Fatalf("baseline ran = %v, want %v", got, tc.wantRan)
			}
			recorded := false
			for _, version := range f.appliedVersions(t) {
				recorded = recorded || version == baselineVersion
			}
			if !recorded {
				t.Fatalf("%s not recorded", baselineVersion)
			}
		})
	}
}
