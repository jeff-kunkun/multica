package service

import (
	"github.com/google/uuid"
)

// Transfer UUID namespaces. These constants are part of the on-disk contract:
// changing any of them after release would make a re-import of the same bundle
// insert duplicate chat rows instead of hitting ON CONFLICT.
var (
	NSTransferChatSession = uuid.MustParse("a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a1")
	NSTransferChatMessage = uuid.MustParse("a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a2")
	NSTransferAttachment  = uuid.MustParse("a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a3")
	// V3 task transfer. Issue attachments share NSTransferAttachment above:
	// they land in the same table, and two namespaces writing one table would
	// let the same source attachment exist twice.
	NSTransferIssue    = uuid.MustParse("a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a4")
	NSTransferComment  = uuid.MustParse("a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a5")
	NSTransferReaction = uuid.MustParse("a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a6")
)

func TransferChatSessionID(targetWorkspaceID, sourceID string) uuid.UUID {
	return uuid.NewSHA1(NSTransferChatSession, []byte(targetWorkspaceID+"/"+sourceID))
}

func TransferChatMessageID(targetWorkspaceID, sourceID string) uuid.UUID {
	return uuid.NewSHA1(NSTransferChatMessage, []byte(targetWorkspaceID+"/"+sourceID))
}

func TransferAttachmentID(targetWorkspaceID, sourceID string) uuid.UUID {
	return uuid.NewSHA1(NSTransferAttachment, []byte(targetWorkspaceID+"/"+sourceID))
}

func TransferIssueID(targetWorkspaceID, sourceID string) uuid.UUID {
	return uuid.NewSHA1(NSTransferIssue, []byte(targetWorkspaceID+"/"+sourceID))
}

func TransferCommentID(targetWorkspaceID, sourceID string) uuid.UUID {
	return uuid.NewSHA1(NSTransferComment, []byte(targetWorkspaceID+"/"+sourceID))
}

// TransferReactionID covers comment_reaction and issue_reaction: the source id
// is already unique per table (a comment id and an issue id never collide), so
// one namespace keeps the derivation uniform without risking a clash.
func TransferReactionID(targetWorkspaceID, sourceID string) uuid.UUID {
	return uuid.NewSHA1(NSTransferReaction, []byte(targetWorkspaceID+"/"+sourceID))
}
