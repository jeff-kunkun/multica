package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func projectMemberEmail(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf("pm-%s-%s@multica.test", strings.ReplaceAll(t.Name(), "/", "-"), suffix)
}

func seedProjectMemberUser(t *testing.T, name, suffix, role string) string {
	t.Helper()
	userID := dbfx.User(t, name, projectMemberEmail(t, suffix))
	dbfx.Member(t, testWorkspaceID, userID, role)
	return userID
}

func projectMemberAddReq(t *testing.T, actorID, projectID, memberID string) *http.Request {
	t.Helper()
	req := newRequestAs(actorID, "POST", "/api/projects/"+projectID+"/members", map[string]any{
		"member_id": memberID,
	})
	return testutil.WithURLParams(req, "id", projectID)
}

func projectMemberListReq(t *testing.T, actorID, projectID string) *http.Request {
	t.Helper()
	req := newRequestAs(actorID, "GET", "/api/projects/"+projectID+"/members", nil)
	return testutil.WithURLParams(req, "id", projectID)
}

func projectMemberRemoveReq(t *testing.T, actorID, projectID, memberID string) *http.Request {
	t.Helper()
	req := newRequestAs(actorID, "DELETE", "/api/projects/"+projectID+"/members/"+memberID, nil)
	return testutil.WithURLParams(req, "id", projectID, "memberId", memberID)
}

func TestProjectMemberAddListRemove(t *testing.T) {
	projectID := dbfx.Project(t, "project member lifecycle")
	memberID := seedProjectMemberUser(t, "Lifecycle Member", "lifecycle", "member")

	added := testutil.Decode[ProjectMemberResponse](t, testHandler.AddProjectMember, projectMemberAddReq(t, testUserID, projectID, memberID), http.StatusCreated)
	if added.MemberID != memberID {
		t.Fatalf("added member_id = %s, want %s", added.MemberID, memberID)
	}
	if added.Name != "Lifecycle Member" {
		t.Fatalf("added name = %q, want Lifecycle Member", added.Name)
	}

	listed := testutil.Decode[[]ProjectMemberResponse](t, testHandler.ListProjectMembers, projectMemberListReq(t, testUserID, projectID), http.StatusOK)
	if len(listed) != 1 || listed[0].MemberID != memberID {
		t.Fatalf("list after add = %+v, want one row for %s", listed, memberID)
	}

	testutil.Call(t, testHandler.RemoveProjectMember, projectMemberRemoveReq(t, testUserID, projectID, memberID)).Want(http.StatusNoContent)

	listed = testutil.Decode[[]ProjectMemberResponse](t, testHandler.ListProjectMembers, projectMemberListReq(t, testUserID, projectID), http.StatusOK)
	if len(listed) != 0 {
		t.Fatalf("list after remove = %+v, want empty", listed)
	}
}

func TestProjectMemberAddIdempotent(t *testing.T) {
	projectID := dbfx.Project(t, "project member idempotent")
	memberID := seedProjectMemberUser(t, "Idempotent Member", "idempotent", "member")

	first := testutil.Decode[ProjectMemberResponse](t, testHandler.AddProjectMember, projectMemberAddReq(t, testUserID, projectID, memberID), http.StatusCreated)
	second := testutil.Decode[ProjectMemberResponse](t, testHandler.AddProjectMember, projectMemberAddReq(t, testUserID, projectID, memberID), http.StatusCreated)
	if first.ID != second.ID {
		t.Fatalf("idempotent add returned different ids %s vs %s", first.ID, second.ID)
	}

	listed := testutil.Decode[[]ProjectMemberResponse](t, testHandler.ListProjectMembers, projectMemberListReq(t, testUserID, projectID), http.StatusOK)
	if len(listed) != 1 {
		t.Fatalf("list after duplicate add = %+v, want one row", listed)
	}
}

func TestProjectMemberAddRejectsForeignMember(t *testing.T) {
	projectID := dbfx.Project(t, "project member foreign")
	foreignID := dbfx.User(t, "Foreign User", projectMemberEmail(t, "foreign"))

	testutil.Call(t, testHandler.AddProjectMember, projectMemberAddReq(t, testUserID, projectID, foreignID)).Want(http.StatusBadRequest)
}

func TestProjectMemberAddForbiddenForPlainMember(t *testing.T) {
	projectID := dbfx.Project(t, "project member forbidden")
	plainID := seedProjectMemberUser(t, "Plain Member", "plain", "member")
	targetID := seedProjectMemberUser(t, "Target Member", "target", "member")

	testutil.Call(t, testHandler.AddProjectMember, projectMemberAddReq(t, plainID, projectID, targetID)).Want(http.StatusForbidden)
}

func TestProjectMemberAddAllowsProjectLead(t *testing.T) {
	leadID := seedProjectMemberUser(t, "Lead Member", "lead", "member")
	targetID := seedProjectMemberUser(t, "Lead Target", "lead-target", "member")
	projectID := dbfx.Project(t, "project member lead write", testutil.Cols{
		"lead_type": "member",
		"lead_id":   leadID,
	})

	added := testutil.Decode[ProjectMemberResponse](t, testHandler.AddProjectMember, projectMemberAddReq(t, leadID, projectID, targetID), http.StatusCreated)
	if added.MemberID != targetID {
		t.Fatalf("lead add member_id = %s, want %s", added.MemberID, targetID)
	}
}

func TestDeleteProjectClearsProjectMembers(t *testing.T) {
	projectID := dbfx.Project(t, "project member delete project")
	memberID := seedProjectMemberUser(t, "Delete Project Member", "delete-project", "member")
	dbfx.Insert(t, "project_member", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"project_id":   projectID,
		"member_id":    memberID,
	})

	req := newRequest("DELETE", "/api/projects/"+projectID, nil)
	req = testutil.WithURLParams(req, "id", projectID)
	testutil.Call(t, testHandler.DeleteProject, req).Want(http.StatusNoContent)

	count := dbfx.Count(t, `SELECT COUNT(*) FROM project_member WHERE project_id = $1`, projectID)
	if count != 0 {
		t.Fatalf("project_member rows after project delete = %d, want 0", count)
	}
}

func TestRevokeMemberClearsProjectMembers(t *testing.T) {
	projectID := dbfx.Project(t, "project member revoke")
	userID := seedProjectMemberUser(t, "Revoke Member", "revoke", "member")
	dbfx.Insert(t, "project_member", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"project_id":   projectID,
		"member_id":    userID,
	})

	var memberRowID string
	dbfx.QueryRow(t, `SELECT id FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, userID).Scan(&memberRowID)

	if _, err := testHandler.revokeAndRemoveMember(
		context.Background(),
		parseUUID(testWorkspaceID),
		parseUUID(userID),
		parseUUID(memberRowID),
		parseUUID(testUserID),
	); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	count := dbfx.Count(t, `SELECT COUNT(*) FROM project_member WHERE member_id = $1`, userID)
	if count != 0 {
		t.Fatalf("project_member rows after member revoke = %d, want 0", count)
	}
}
