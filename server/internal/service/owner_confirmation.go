package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// What the owner checks before approving, already masked: the body's values
	// ("role=admin"), the query, and who or what the path points at.
	Values []string
	Query  string
	Object string
	// PathIDs are the ids in the raw path, so the card can name the object.
	PathIDs []pgtype.UUID
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

// OwnerConfirmationScopeHash binds an approval to one operation and everything
// that decides its result: the method, the exact path (so the exact object), the
// query and the body with its values. Adding Mallory as admin is not the call
// the owner approved when they approved Alice as member, so it does not spend
// that approval. Only the digest is kept; the values themselves are not.
// body must be CanonicalJSON's output (nil for no body).
func OwnerConfirmationScopeHash(method, path, query string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(strings.ToUpper(method) + "\n" + path + "\n" + CanonicalQuery(query) + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// CanonicalJSON re-encodes a JSON document so the same content always gives the
// same bytes (keys sorted, whitespace dropped, numbers kept as written). ok is
// false when the input is not one JSON document, or when an object names a key
// twice (in any letter case), which a handler could read either way.
func CanonicalJSON(raw []byte) ([]byte, bool) {
	if !unambiguousKeys(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		return nil, false // more than one document
	}
	out, err := json.Marshal(v)
	return out, err == nil
}

// unambiguousKeys reports whether no object in the document, at any depth,
// names the same key twice. Go decodes into structs matching keys without
// regard to case, last one winning, so {"Role":"admin","role":"member"} and its
// reordering mean different things to a handler yet decode to one sorted map:
// such a body cannot be bound to one approval and is refused (DENE-1936 R1).
// Keys are compared under the same case folding the decoder uses.
func unambiguousKeys(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	// One set of folded keys per open object; nil for an open array.
	var open []map[string]bool
	expectKey := func() bool { return len(open) > 0 && open[len(open)-1] != nil }
	inKeyPos := true // the next token in the innermost object is a key
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return len(open) == 0
		}
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				open = append(open, map[string]bool{})
				inKeyPos = true
				continue
			case '[':
				open = append(open, nil)
			case '}', ']':
				open = open[:len(open)-1]
			}
		case string:
			if expectKey() && inKeyPos {
				k := foldKey(t)
				if open[len(open)-1][k] {
					return false
				}
				open[len(open)-1][k] = true
				inKeyPos = false
				continue
			}
		}
		// A value just ended (scalar, or a closed object/array): the next token
		// of an enclosing object is a key again.
		if t, ok := tok.(json.Delim); ok && t == '[' {
			continue
		}
		inKeyPos = true
	}
}

// foldKey maps every rune to the smallest rune of its case-folding orbit, so
// foldKey(a) == foldKey(b) exactly when strings.EqualFold(a, b), the rule Go's
// JSON decoder uses to match keys to struct fields.
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < m {
				m = f
			}
		}
		b.WriteRune(m)
	}
	return b.String()
}

// CanonicalQuery orders a raw query string so parameter order does not matter.
func CanonicalQuery(raw string) string {
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}
	return vals.Encode()
}

const confirmationValueRunes = 80

// OwnerConfirmationValues renders a body's top-level values for the card, one
// "name=value" per field, sorted, each run through MaskSecrets as a whole so a
// credential field shows as masked. Fields whose name is itself data are left
// out, like everywhere else.
func OwnerConfirmationValues(body []byte) []string {
	var obj map[string]json.RawMessage
	if len(body) == 0 || json.Unmarshal(body, &obj) != nil {
		return nil
	}
	names := SafeFieldNames(keysOf(obj))
	out := make([]string, 0, len(names))
	for _, k := range names {
		v := string(obj[k])
		var str string
		if json.Unmarshal(obj[k], &str) == nil {
			v = str
		}
		out = append(out, MaskSecrets(k+"="+clipRunes(v, confirmationValueRunes)))
	}
	return out
}

// OwnerConfirmationQueryText is the query as the card shows it, masked.
func OwnerConfirmationQueryText(raw string) string {
	q := CanonicalQuery(raw)
	if q == "" {
		return ""
	}
	if u, err := url.QueryUnescape(q); err == nil {
		q = u
	}
	return MaskSecrets(clipRunes(strings.ReplaceAll(q, "&", "、"), confirmationValueRunes*2))
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
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
// and what it would do (who or what, which values, masked) so the owner knows
// what they are approving, and where the answer has to be given.
func OwnerConfirmationQuestion(req OwnerConfirmationRequest) string {
	var b strings.Builder
	b.WriteString("这一步默认不允许：" + DangerLabel(req.Action) + "。")
	b.WriteString("请求：" + strings.ToUpper(req.Method) + " " + req.Route)
	if o := strings.TrimSpace(MaskSecrets(req.Object)); o != "" {
		b.WriteString("。对象：" + o)
	}
	if len(req.Values) > 0 {
		b.WriteString("。内容：" + strings.Join(req.Values, "、"))
	} else if f := SafeFieldNames(req.Fields); len(f) > 0 {
		b.WriteString("（字段：" + strings.Join(f, "、") + "）")
	}
	if req.Query != "" {
		b.WriteString("。参数：" + req.Query)
	}
	b.WriteString("。只有你本人在 Multica 里点这张卡、或用 multica ask answer 才算数；Bot 不能替你答，点头只管这一次、只管上面这些内容，内容改了要重新问你，" + "30 分钟内有效。")
	return b.String()
}
