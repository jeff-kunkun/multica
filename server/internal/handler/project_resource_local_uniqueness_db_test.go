package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The two identity rules, against the database that enforces them
// (DENE-617 invariants 10 and 11). The application check in
// findLocalDirectoryConflictReason exists to produce a readable message; these
// tests prove the constraint holds even for a writer that never calls it,
// which is the whole reason the rules are indexes and not just Go code.

func insertLocalDirectory(t *testing.T, fx *testutil.Fixture, projectID, ref string) (string, error) {
	t.Helper()
	var id string
	err := testPool.QueryRow(context.Background(),
		`INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref)
		 VALUES ($1, $2, 'local_directory', $3::jsonb) RETURNING id`,
		projectID, testWorkspaceID, ref,
	).Scan(&id)
	if err == nil {
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM project_resource WHERE id = $1`, id)
		})
	}
	return id, err
}

// Invariant 10: one row per directory per machine. Identity is the resolved
// real_path, so a symlink and its target are one directory — and a row written
// before real_path existed falls back to local_path, which is the value the
// index COALESCEs to as well.
func TestDatabaseRefusesTheSameDirectoryTwice(t *testing.T) {
	requireDB(t)
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	project := fx.Project(t, "DENE-617 identity")

	if _, err := insertLocalDirectory(t, fx, project,
		`{"local_path":"/Users/me/code/app","daemon_id":"d1","real_path":"/Users/me/code/app"}`); err != nil {
		t.Fatalf("first directory rejected: %v", err)
	}

	// Same directory, different spelling of local_path and a different label.
	// Neither is identity, so the row is still the same directory.
	_, err := insertLocalDirectory(t, fx, project,
		`{"local_path":"/Users/me/code/app/","daemon_id":"d1","real_path":"/Users/me/code/app","label":"renamed"}`)
	if err == nil {
		t.Fatal("the database accepted the same directory twice")
	}
	if !strings.Contains(err.Error(), "idx_project_resource_local_directory_identity") {
		t.Fatalf("rejected by %v, want the identity index", err)
	}

	// A different directory on the same machine is now perfectly legal — that
	// is the rule this change relaxed.
	if _, err := insertLocalDirectory(t, fx, project,
		`{"local_path":"/Users/me/notes","daemon_id":"d1","real_path":"/Users/me/notes"}`); err != nil {
		t.Fatalf("a second, different directory on the same machine was rejected: %v", err)
	}

	// And the same path on ANOTHER machine is a different directory: two
	// laptops can each carry /Users/me/code/app for one project.
	if _, err := insertLocalDirectory(t, fx, project,
		`{"local_path":"/Users/me/code/app","daemon_id":"d2","real_path":"/Users/me/code/app"}`); err != nil {
		t.Fatalf("the same path on a second machine was rejected: %v", err)
	}
}

// A row with no real_path — every row written before this change — is
// identified by its local_path, so the rule keeps its old meaning for old data.
func TestDatabaseIdentifiesLegacyRowsByLocalPath(t *testing.T) {
	requireDB(t)
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	project := fx.Project(t, "DENE-617 legacy identity")

	if _, err := insertLocalDirectory(t, fx, project, `{"local_path":"/Users/me/legacy","daemon_id":"d1"}`); err != nil {
		t.Fatalf("legacy row rejected: %v", err)
	}
	if _, err := insertLocalDirectory(t, fx, project,
		`{"local_path":"/Users/me/legacy","daemon_id":"d1","label":"again"}`); err == nil {
		t.Fatal("a legacy row was added twice; its local_path is its identity")
	}
}

// Invariant 11: one row per repository per machine, and only when the
// repository can be identified. Four plain folders carry no repo_key and must
// all remain legal — that is what the partial predicate buys.
func TestDatabaseRefusesASecondCheckoutOfOneRepositoryButAllowsPlainFolders(t *testing.T) {
	requireDB(t)
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	project := fx.Project(t, "DENE-617 repo identity")

	if _, err := insertLocalDirectory(t, fx, project,
		`{"local_path":"/Users/me/code/app","daemon_id":"d1","real_path":"/Users/me/code/app","repo_key":"github.com/o/app"}`); err != nil {
		t.Fatalf("first checkout rejected: %v", err)
	}
	_, err := insertLocalDirectory(t, fx, project,
		`{"local_path":"/Users/me/code/app-copy","daemon_id":"d1","real_path":"/Users/me/code/app-copy","repo_key":"github.com/o/app"}`)
	if err == nil {
		t.Fatal("a second checkout of the same repository was accepted on one machine")
	}
	if !strings.Contains(err.Error(), "idx_project_resource_local_directory_repo") {
		t.Fatalf("rejected by %v, want the repository index", err)
	}

	// Four unrelated plain folders: no repo_key, so nothing to collide on.
	for _, path := range []string{"/Users/me/a", "/Users/me/b", "/Users/me/c", "/Users/me/d"} {
		ref := `{"local_path":"` + path + `","daemon_id":"d1","real_path":"` + path + `"}`
		if _, err := insertLocalDirectory(t, fx, project, ref); err != nil {
			t.Fatalf("plain folder %s was rejected: %v", path, err)
		}
	}

	// The same repository on a second machine is the ordinary two-laptop case.
	if _, err := insertLocalDirectory(t, fx, project,
		`{"local_path":"/Users/me/code/app","daemon_id":"d2","real_path":"/Users/me/code/app","repo_key":"github.com/o/app"}`); err != nil {
		t.Fatalf("the same repository on a second machine was rejected: %v", err)
	}
}

// The rules are per project: two projects may each point at the same directory
// and the same repository, which is how one checkout serves several projects.
func TestTheIdentityRulesAreScopedToOneProject(t *testing.T) {
	requireDB(t)
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	ref := `{"local_path":"/Users/me/code/shared","daemon_id":"d1","real_path":"/Users/me/code/shared","repo_key":"github.com/o/shared"}`
	for _, title := range []string{"DENE-617 scope A", "DENE-617 scope B"} {
		if _, err := insertLocalDirectory(t, fx, fx.Project(t, title), ref); err != nil {
			t.Fatalf("%s: rejected: %v", title, err)
		}
	}
}
