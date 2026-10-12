package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// The owner's go-ahead for the four dangerous Bot actions (DENE-1936). It is a
// server-held object, not a sentence the Bot writes: the first blocked call
// opens a question card addressed to the owner, only the owner's own answer in
// the app (or `multica ask answer`) approves it, and the approval is spent by
// exactly one repeat of exactly that call. A Bot cannot answer the card, so it
// cannot approve itself.

const (
	// OwnerConfirmationKind is the agent_ask kind of the card. The ask answering
	// code treats it specially: Bot-carried answers are refused.
	OwnerConfirmationKind = "owner_confirmation"
	// OwnerConfirmationTTL is how long a request lives, approved or not.
	OwnerConfirmationTTL = 30 * time.Minute

	OwnerConfirmApprove = "approve"
	OwnerConfirmDeny    = "deny"

	OwnerConfirmPending  = "pending"
	OwnerConfirmApproved = "approved"
	OwnerConfirmDenied   = "denied"
)

// OwnerConfirmationRequest is one blocked call as the interceptor saw it.
type OwnerConfirmationRequest struct {
	WorkspaceID pgtype.UUID
	BotID       pgtype.UUID
	BotName     string
	OwnerUserID pgtype.UUID
	Action      string
	Method      string
	Route       string // sanitized pattern, never the raw path
	Fields      []string
	ScopeHash   string
}

// OwnerConfirmation is the request as the Bot is told about it.
type OwnerConfirmation struct {
	ID        pgtype.UUID
	AskID     pgtype.UUID
	Status    string // pending | approved | denied
	ExpiresAt time.Time
	Created   bool // this call opened it (and so told the owner)
}

// OwnerConfirmer finds or opens the request for a blocked call. It is the
// handler's, because opening one also delivers the card to the owner (inbox and
// their Bot) the way every other question card is delivered.
type OwnerConfirmer interface {
	EnsureOwnerConfirmation(ctx context.Context, req OwnerConfirmationRequest) (OwnerConfirmation, error)
}

// OwnerConfirmationScopeHash binds an approval to one operation: the method, the
// exact path (so the exact object) and the names of the fields it writes. Values
// are not part of it and not kept anywhere.
func OwnerConfirmationScopeHash(method, path string, fields []string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(method) + "\n" + path + "\n" + strings.Join(SafeFieldNames(fields), ",")))
	return hex.EncodeToString(sum[:])
}

var dangerLabels = map[string]string{
	DangerDeleteWorkspace: "删除工作区",
	DangerChangeMembers:   "改成员或角色",
	DangerResetKeys:       "重置密钥",
	DangerChangeBilling:   "改额度或付费",
}

// DangerLabel is the plain name of a dangerous action.
func DangerLabel(action string) string {
	if l := dangerLabels[action]; l != "" {
		return l
	}
	return action
}

// OwnerConfirmationTitle is the card's headline, in the audit's own wording.
func OwnerConfirmationTitle(botName, action string) string {
	name := strings.TrimSpace(MaskSecrets(botName))
	if name == "" {
		name = "未命名"
	}
	return "Bot〈" + name + "〉想" + DangerLabel(action) + "，要你点头"
}

// OwnerConfirmationQuestion is the one question on the card. It names the call
// (route and field names, no values) and where the answer has to be given.
func OwnerConfirmationQuestion(req OwnerConfirmationRequest) string {
	var b strings.Builder
	b.WriteString("这一步默认不允许：" + DangerLabel(req.Action) + "。")
	b.WriteString("请求：" + strings.ToUpper(req.Method) + " " + req.Route)
	if f := SafeFieldNames(req.Fields); len(f) > 0 {
		b.WriteString("（字段：" + strings.Join(f, "、") + "）")
	}
	b.WriteString("。只有你本人在 Multica 里点这张卡、或用 multica ask answer 才算数；Bot 不能替你答，点头只管这一次，" + "30 分钟内有效。")
	return b.String()
}
