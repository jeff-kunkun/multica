package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// An alignment is where the reference material is usually made: a screenshot
// someone dropped into the conversation, an HTML prototype the carrier
// uploaded. These tests pin what has to happen to it at the one moment the
// conversation becomes work — the files must end up owned by the group's ROOT,
// exactly once, while the sub-issues merely link them.
//
// Database-backed, because every statement here is about what the attachment
// rows say after the confirm committed: an attachment bound to a chat message
// is deleted with that message (the FK cascades), so "the issue owns the file"
// is the difference between a description link that keeps working and one that
// rots.

// draftSessionAttachment uploads one file into a conversation the way the real
// path does — a chat message, plus the attachment row bound to it — and returns
// the attachment id and the URL a description would embed.
func draftSessionAttachment(t *testing.T, sessionID, uploaderID, filename string) (string, string) {
	t.Helper()
	messageID := dbfx.Insert(t, "chat_message", testutil.Cols{
		"chat_session_id": sessionID,
		"role":            "user",
		"content":         "here is the " + filename,
	})
	url := "/uploads/" + filename
	attachmentID := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"chat_session_id": sessionID,
		"chat_message_id": messageID,
		"uploader_type":   "member",
		"uploader_id":     uploaderID,
		"filename":        filename,
		"url":             url,
		"content_type":    "image/png",
		"size_bytes":      2048,
	})
	return attachmentID, url
}

// attachmentMarkdown is what a description carries when it references a file:
// the ordinary markdown link, never a second copy of the bytes.
func attachmentMarkdown(attachmentID, filename string) string {
	return fmt.Sprintf("![%s](/api/attachments/%s/download)", filename, attachmentID)
}

// attachmentOwner reads an attachment's issue owner as text, spelling the NULL
// case as the empty string so a failed expectation reports "nobody" instead of
// an error out of Scan.
func attachmentOwner(t *testing.T, attachmentID string) string {
	t.Helper()
	var owner string
	dbfx.QueryRow(t, `SELECT COALESCE(issue_id::text, '') FROM attachment WHERE id = $1`, attachmentID).
		Scan(&owner)
	return owner
}

// The headline: confirming an alignment hands its files to the root issue.
func TestFinalizeIssueDraftCarriesSessionAttachmentsToRoot(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	prototypeID, prototypeURL := draftSessionAttachment(t, session.SessionID, testUserID, "prototype.png")

	// The carrier's own upload is bound to its REPLY, which is the shape
	// BindChatAttachmentsToMessage leaves behind: chat_message_id only, no
	// chat_session_id. A read of the session column alone would drop it.
	replyID := dbfx.Insert(t, "chat_message", testutil.Cols{
		"chat_session_id": session.SessionID,
		"role":            "assistant",
		"content":         "prototype attached",
	})
	mockID := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"chat_message_id": replyID,
		"uploader_type":   "agent",
		"uploader_id":     session.AgentID,
		"filename":        "mock.html",
		"url":             "/uploads/mock.html",
		"content_type":    "text/html",
		"size_bytes":      4096,
	})

	// The sub-issue references the prototype instead of uploading it again.
	child := draftChild("c1", "build the screen", "todo")
	child["description"] = "match this:\n\n" + attachmentMarkdown(prototypeID, "prototype.png")
	payload := draftGroupPayload("prototype round", child)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", payload)

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if len(finalized.Issues) != 2 {
		t.Fatalf("confirm produced %d issues, want the root plus one child", len(finalized.Issues))
	}
	rootID := finalized.IssueID
	childID := finalized.Issues[1].ID

	// Both files — the user's upload and the carrier's — belong to the root.
	for _, attachmentID := range []string{prototypeID, mockID} {
		if got := attachmentOwner(t, attachmentID); got != rootID {
			t.Fatalf("attachment %s is owned by %q, want the group's root %s", attachmentID, got, rootID)
		}
	}

	// The child references the prototype; it does not own a copy of it. This is
	// the whole reason the carry is root-only.
	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM attachment WHERE workspace_id = $1 AND issue_id = $2
	`, testWorkspaceID, childID); got != 0 {
		t.Fatalf("the sub-issue owns %d attachments; it must only link them", got)
	}
	// One row per file: embedding the link in a description did not upload a
	// second copy alongside the one the conversation already produced.
	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM attachment WHERE workspace_id = $1 AND url = $2
	`, testWorkspaceID, prototypeURL); got != 1 {
		t.Fatalf("%d attachment rows share %s, want exactly the one that was uploaded", got, prototypeURL)
	}

	var childDescription string
	dbfx.QueryRow(t, `SELECT description FROM issue WHERE id = $1`, childID).Scan(&childDescription)
	if want := attachmentMarkdown(prototypeID, "prototype.png"); !strings.Contains(childDescription, want) {
		t.Fatalf("the sub-issue lost the markdown link to the prototype:\n%s", childDescription)
	}

	// The carry is idempotent: a repeat confirm adopts the group and finds
	// nothing left to hand over.
	var again FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&again)
	if got := attachmentOwner(t, prototypeID); got != rootID {
		t.Fatalf("the repeat confirm moved the prototype to %q", got)
	}
}

// Nothing uploaded means nothing about the confirm changes — and, just as
// important, it means no file belonging to some OTHER conversation is swept up
// in it.
func TestFinalizeIssueDraftWithoutAttachmentsIsUnchanged(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	// An unowned file in a different conversation: exactly what a session-blind
	// query would pick up.
	foreignID, _ := draftSessionAttachment(t,
		dbfx.ChatSession(t, handlerTestAgentID(t)), testUserID, "someone-elses.png")

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready",
		draftGroupPayload("no files here", draftChild("c1", "one", "todo"), draftChild("c2", "two", "todo")))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if len(finalized.Issues) != 3 {
		t.Fatalf("the confirm produced %d issues, want the root plus two children", len(finalized.Issues))
	}
	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM attachment a
		JOIN issue i ON i.id = a.issue_id
		WHERE a.workspace_id = $1 AND i.origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("%d attachments were bound to an alignment that uploaded nothing", got)
	}
	if got := attachmentOwner(t, foreignID); got != "" {
		t.Fatalf("an attachment from another conversation was claimed by %q", got)
	}
}
