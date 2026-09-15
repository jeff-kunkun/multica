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
