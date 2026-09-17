package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestDeleteWorkspace_SweepsTransferAttachmentStaging is the DB-level
// regression for the no-FK chore migration 491 documents: both resumable-upload
// staging tables are keyed by workspace_id and carry no foreign key, so the
// teardown is the only thing that removes them. The manifest classifies them as
// DELETE and DeleteWorkspaceLeafData must actually sweep both — a graph that
// dropped the uploads but kept their chunks would strand staged bytes for a
// workspace that no longer exists.
func TestDeleteWorkspace_SweepsTransferAttachmentStaging(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	const slug = "handler-tests-delete-transfer-staging"
	dbfx.Exec(t, `DELETE FROM workspace WHERE slug = $1`, slug)

	wsID := dbfx.Workspace(t, "Handler Test Delete Transfer Staging", slug)
	dbfx.Member(t, wsID, testUserID, "owner")

	sum := sha256.Sum256([]byte(slug))
	sha := hex.EncodeToString(sum[:])
	dbfx.InsertNoID(t, "transfer_attachment_upload", testutil.Cols{
		"workspace_id": wsID,
		"sha256":       sha,
		"source_id":    "transfer-staging-source",
		"uploader_id":  testUserID,
		"total_bytes":  3000,
		"meta":         testutil.Raw("'{}'::jsonb"),
	}, "workspace_id = $1", wsID)
	for _, offset := range []int64{0, 1000, 2000} {
		dbfx.InsertNoID(t, "transfer_attachment_upload_chunk", testutil.Cols{
			"workspace_id": wsID,
			"sha256":       sha,
			"offset_bytes": offset,
			"size_bytes":   1000,
			"data":         []byte{byte(offset)},
		}, "workspace_id = $1", wsID)
	}
	// Guard the assertion below against a fixture that staged nothing: without
	// this the counts are zero before the delete too, and the test passes for
	// the wrong reason.
	if n := dbfx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1`, wsID); n != 1 {
		t.Fatalf("fixture staged %d upload rows, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE workspace_id = $1`, wsID); n != 3 {
		t.Fatalf("fixture staged %d chunk rows, want 3", n)
	}

	req := newRequest("DELETE", "/api/workspaces/"+wsID, nil)
	req = withURLParam(req, "id", wsID)
	testutil.Call(t, testHandler.DeleteWorkspace, req).Want(http.StatusNoContent)

	if n := dbfx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1`, wsID); n != 0 {
		t.Fatalf("workspace delete left %d transfer_attachment_upload rows", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE workspace_id = $1`, wsID); n != 0 {
		t.Fatalf("workspace delete left %d transfer_attachment_upload_chunk rows", n)
	}
}
