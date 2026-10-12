package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The activity stream (DENE-1935). A person who connected a Bot and switched a
// scope on gets what happens in their tickets and chats pushed to that Bot as
// it happens: comments, chat messages, status and assignee changes. The Bot
// decides what reaches the person; the server only tags (importance,
// categories) and never lets a secret leave. Nothing here changes how a person
// without a verified binding and a scope is treated.
//
// It rides the human-needed outbox (human_needed_event, event_type
// "activity"): delivery, backoff and the receipt endpoints are shared. What
// differs is that nobody chases it: a pushed activity is done at HTTP 200, and
// a Bot that wants to say "looked, nobody needs telling" posts `handled`.

const (
	HumanEventActivity = "activity"
	ActivitySchema     = "multica.activity/v1"

	ActivityScopeOwned    = "owned"
	ActivityScopeFollowed = "followed"
	// ActivityScopeAll (DENE-1936) is everything the person can see in the
	// workspace: every ticket they may open, plus workspace-level events (PRs,
	// runs, projects, settings, members, agents ...) their role allows.
	ActivityScopeAll = "owner_visible_all"

	ImportanceCritical = "critical"
	ImportanceHigh     = "high"
	ImportanceNormal   = "normal"
	ImportanceLow      = "low"

	CategoryNeedsHuman = "needs_human"
	CategoryRisk       = "risk"
	CategoryDecision   = "decision"
	CategoryResult     = "result"
	CategoryProgress   = "progress"
	CategoryCompletion = "completion"
	CategoryFailure    = "failure"
	CategoryInfo       = "info"

	ActivityCommentCreated  = "comment.created"
	ActivityCommentEdited   = "comment.edited"
	ActivityChatMessage     = "chat.message"
	ActivityStatusChanged   = "status.changed"
	ActivityAssigneeChanged = "assignee.changed"

	// activityRetention is how long an undelivered activity row is kept; a Bot
	// that was down longer than this catches up from the ticket, not the stream.
	activityRetention = 72 * time.Hour
	// activityLowBacklog caps how many low-importance rows may wait for one
	// Bot. Past it new low rows are dropped so a dead webhook cannot make the
	// stream grow without bound; normal and high are never dropped.
	activityLowBacklog = 2000
	activityExcerptMax = 1000
)

// ErrInvalidActivityScope is returned for a scope name the server does not know.
var ErrInvalidActivityScope = errors.New("activity scope must be owned, followed and/or owner_visible_all")

// NormalizeActivityScope validates and orders a scope list. Empty is valid: it
// switches the stream off.
func NormalizeActivityScope(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, raw := range in {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "" {
			continue
		}
		if s != ActivityScopeOwned && s != ActivityScopeFollowed && s != ActivityScopeAll {
			return nil, fmt.Errorf("%w: %q", ErrInvalidActivityScope, raw)
		}
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for _, s := range []string{ActivityScopeOwned, ActivityScopeFollowed, ActivityScopeAll} {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// ---- tagging ----

// ActivityFacts is what tagging needs to know about one thing that happened.
type ActivityFacts struct {
	Type       string // ActivityCommentCreated ...
	ActorType  string // member | agent | system
	Text       string // the comment or message body, "" for status changes
	FromStatus string
	ToStatus   string
}

var (
	riskWords = []string{
		"风险", "失败", "报错", "出错", "错误", "故障", "阻塞", "卡住", "回滚", "崩溃", "超时", "丢失", "泄露", "漏洞", "未归类", "没通过", "不通过",
		"risk", "fail", "error", "blocked", "rollback", "crash", "timeout", "outage", "leak", "vulnerab", "regress",
	}
	sensitiveWords = []string{
		"生产", "线上", "付款", "付费", "支付", "充值", "扣费", "计费", "发布", "上线", "部署", "合并到", "删除", "不可逆",
		"production", "deploy", "release", "payment", "billing", "charge", "refund", "delete", "irreversible",
	}
	decisionWords = []string{
		"要你做", "需要你", "请你", "请确认", "请拍板", "拍板", "你来定", "你决定", "要你决定", "等你", "待你", "请回复", "请批准", "请审批",
		"need you", "your call", "please confirm", "please approve", "waiting on you", "needs your",
	}
	resultWords = []string{
		"完成", "已合并", "已发布", "已上线", "已交付", "交付", "验收通过", "结论", "总结", "报告", "已修复", "通过了",
		"done", "merged", "shipped", "delivered", "completed", "fixed", "conclusion", "summary",
	}
)

func containsAny(text string, words []string) bool {
	lower := strings.ToLower(text)
	for _, w := range words {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// ClassifyActivity tags one event. Rules lean high: a Bot filters a noisy
// "high" for free, but a missed risk is the failure this stream exists to
// prevent. critical is not produced here — that is what human_needed events
// are for, and they keep their own lane.
func ClassifyActivity(f ActivityFacts) (string, []string) {
	cats := map[string]bool{}
	importance := ImportanceNormal
	raise := func(level string) {
		if activityRank(level) > activityRank(importance) {
			importance = level
		}
	}
	switch f.Type {
	case ActivityStatusChanged:
		switch f.ToStatus {
		case "blocked":
			importance = ImportanceHigh
			cats[CategoryRisk] = true
		case "done":
			cats[CategoryCompletion], cats[CategoryResult] = true, true
		case "cancelled":
			cats[CategoryCompletion], cats[CategoryResult] = true, true
		case "in_review":
			cats[CategoryResult], cats[CategoryProgress] = true, true
		case "in_progress":
			importance = ImportanceLow
			cats[CategoryProgress] = true
		case "todo", "backlog":
			importance = ImportanceLow
			cats[CategoryInfo] = true
		default:
			cats[CategoryProgress] = true
		}
		return importance, sortedCategories(cats)
	case ActivityAssigneeChanged:
		return ImportanceLow, []string{CategoryInfo}
	}

	// Comments and chat messages: read the words.
	text := f.Text
	cats[CategoryProgress] = true
	if containsAny(text, resultWords) {
		cats[CategoryResult], cats[CategoryCompletion] = true, true
		delete(cats, CategoryProgress)
	}
	if containsAny(text, riskWords) {
		raise(ImportanceHigh)
		cats[CategoryRisk] = true
		delete(cats, CategoryProgress)
	}
	if containsAny(text, sensitiveWords) {
		raise(ImportanceHigh)
		cats[CategoryRisk], cats[CategoryDecision] = true, true
	}
	if containsAny(text, decisionWords) {
		raise(ImportanceHigh)
		cats[CategoryDecision] = true
		delete(cats, CategoryProgress)
	}
	if strings.Contains(text, "失败") || strings.Contains(strings.ToLower(text), "failed") {
		cats[CategoryFailure] = true
	}
	// A plain system notice that matched nothing is information, not progress.
	if f.ActorType == "system" && len(cats) == 1 && cats[CategoryProgress] {
		delete(cats, CategoryProgress)
		cats[CategoryInfo] = true
	}
	if len(cats) == 0 {
		cats[CategoryInfo] = true
	}
	return importance, sortedCategories(cats)
}

func activityRank(level string) int {
	switch level {
	case ImportanceCritical:
		return 3
	case ImportanceHigh:
		return 2
	case ImportanceNormal:
		return 1
	}
	return 0
}

// ActivityAtLeast reports whether level is min or above ("" means no floor).
func ActivityAtLeast(level, min string) bool {
	return min == "" || activityRank(level) >= activityRank(min)
}

func sortedCategories(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// ---- keeping secrets in ----

const maskedSecret = "[已打码]"

var (
	secretPEM    = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`)
	secretPrefix = regexp.MustCompile(`\b(?:(?:mul|mat|msk|mpat|mbt)_[A-Za-z0-9_\-]{8,}|sk-[A-Za-z0-9_\-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9\-]{10,}|AIza[0-9A-Za-z_\-]{30,}|eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,})`)
	secretBearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._\-~+/=]{8,}`)
	// A credential field is "name, separator, value". A quoted value is masked
	// whole, whatever it holds (spaces, one character, a newline, an escaped
	// quote or backslash as in a JSON string); an unquoted one runs to the next
	// separator and is masked from its first character: "ab" is a password like
	// any other. The name may itself sit between escaped quotes (JSON inside a
	// JSON string).
	secretAssignQuoted = regexp.MustCompile(`(?i)(password|passwd|pwd|passphrase|secret|token|api[_\- ]?key|apikey|access[_\- ]?key|private[_\- ]?key|credential|authorization|webhook[_\- ]?key|密码|口令|密钥|令牌|钥匙|凭据)(\\*["']?\s*[:=：]\s*)(\\*"(?:[^"\\]|\\[\s\S])*"|\\*'(?:[^'\\]|\\[\s\S])*'|“[^”]*”|「[^」]*」|‘[^’]*’)`)
	secretAssign       = regexp.MustCompile(`(?i)(password|passwd|pwd|passphrase|secret|token|api[_\- ]?key|apikey|access[_\- ]?key|private[_\- ]?key|credential|authorization|webhook[_\- ]?key|密码|口令|密钥|令牌|钥匙|凭据)(\\*["']?\s*[:=：]\s*(?:\\*["'“「‘])?)((?:\\[\s\S]|[^\s"',;，；”」’\\])+)`)
	secretURLPwd       = regexp.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`)
	secretLong         = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{32,}`)
	gitSHA             = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// MaskSecrets replaces anything in text that looks like a credential. It errs
// toward masking: a Bot can ask the person for a value, but cannot take back
// one that already left. A git commit id and ordinary paths are kept.
func MaskSecrets(text string) string {
	if text == "" {
		return text
	}
	text = secretPEM.ReplaceAllString(text, maskedSecret)
	text = secretPrefix.ReplaceAllString(text, maskedSecret)
	text = secretBearer.ReplaceAllString(text, "$1 "+maskedSecret)
	text = secretAssignQuoted.ReplaceAllStringFunc(text, maskQuotedValue)
	text = secretAssign.ReplaceAllString(text, "$1$2"+maskedSecret)
	text = secretURLPwd.ReplaceAllString(text, "${1}"+maskedSecret+"@")
	text = secretLong.ReplaceAllStringFunc(text, func(m string) string {
		if looksLikeSecretRun(m) {
			return maskedSecret
		}
		return m
	})
	return text
}

// maskQuotedValue masks the quoted value of a credential field and keeps the
// quote marks, so masked JSON stays valid JSON.
func maskQuotedValue(m string) string {
	sub := secretAssignQuoted.FindStringSubmatch(m)
	if sub == nil {
		return m
	}
	lead := sub[3][:len(sub[3])-len(strings.TrimLeft(sub[3], `\`))]
	quoted := []rune(strings.TrimLeft(sub[3], `\`))
	if len(quoted) <= 2 {
		return m // an empty value hides nothing
	}
	return sub[1] + sub[2] + lead + string(quoted[0]) + maskedSecret + string(quoted[len(quoted)-1])
}

// looksLikeSecretRun decides about one long run of token characters: a path
// or an identifier made of words is fine, a run of mixed letters and digits is
// not.
func looksLikeSecretRun(m string) bool {
	if gitSHA.MatchString(m) {
		return false
	}
	longest, cur := 0, 0
	hasDigit, hasLetter, hasUpper, hasLower := false, false, false, false
	best := struct{ digit, letter bool }{}
	runDigit, runLetter := false, false
	flush := func() {
		if cur > longest {
			longest = cur
			best.digit, best.letter = runDigit, runLetter
		}
		cur, runDigit, runLetter = 0, false, false
	}
	for _, r := range m {
		switch {
		case r >= '0' && r <= '9':
			cur++
			runDigit, hasDigit = true, true
		case r >= 'a' && r <= 'z':
			cur++
			runLetter, hasLetter, hasLower = true, true, true
		case r >= 'A' && r <= 'Z':
			cur++
			runLetter, hasLetter, hasUpper = true, true, true
		default:
			flush()
		}
	}
	flush()
	if longest >= 24 && best.digit && best.letter {
		return true
	}
	return len(m) >= 40 && hasDigit && hasLetter && hasUpper && hasLower
}

// ScrubPayload is the last gate before the network. The builders mask the free
// text they know about; this walks the finished body as it will be sent
// (summary, author, titles, options, the human_needed block, whatever a later
// change adds) and masks every string in it, so protection does not depend on
// each builder remembering. The body goes through JSON first, which flattens
// typed values (option structs, slices of strings) into what the Bot receives.
func ScrubPayload(fields map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	scrubValue(out)
	return out, nil
}

// ScrubFields masks every string in v in place and returns it.
func ScrubFields(v any) any { return scrubValue(v) }

// scrubString masks one string of a body. What it keeps verbatim it keeps by
// its shape, never by the key it sits under: a key name says nothing about
// who wrote the value (an option id is whatever the asker typed). A real
// UUID or timestamp cannot hold a credential; a link the server built is
// masked piece by piece, so its path is not read as one long token and a
// secret smuggled into it is still caught.
func scrubString(s string) string {
	switch {
	case s == "":
		return s
	case uuidShape.MatchString(s):
		return s
	case isRFC3339(s):
		return s
	case isPlainLink(s):
		return maskLink(s)
	}
	return MaskSecrets(s)
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isRFC3339(s string) bool {
	if len(s) < 20 || len(s) > 40 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}

// isPlainLink is an http(s) address with no spaces and no user:password part.
func isPlainLink(s string) bool {
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.User == nil && u.Host != ""
}

// maskLink masks each "/"-separated piece on its own.
func maskLink(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = MaskSecrets(p)
	}
	return strings.Join(parts, "/")
}

func scrubValue(v any) any {
	switch t := v.(type) {
	case string:
		return scrubString(t)
	case map[string]any:
		for k, val := range t {
			t[k] = scrubValue(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = scrubValue(val)
		}
		return t
	}
	return v
}

// ActivityExcerpt is the text a Bot reads: mentions flattened, secrets masked,
// bounded.
func ActivityExcerpt(text string) string {
	text = strings.TrimSpace(util.MentionRe.ReplaceAllString(text, "@$1"))
	return truncateRunesForHuman(MaskSecrets(text), activityExcerptMax)
}

// ---- routing ----

// ActivityBody is the stream-specific part stored with the event.
type ActivityBody struct {
	ActivityType string   `json:"activity_type"`
	Importance   string   `json:"importance"`
	Categories   []string `json:"categories"`
	Excerpt      string   `json:"excerpt"`
	SourceID     string   `json:"source_id,omitempty"`
	FromStatus   string   `json:"from_status,omitempty"`
	ToStatus     string   `json:"to_status,omitempty"`
	OccurredAt   string   `json:"occurred_at"`
	// The same fact on the office event stream (DENE-1963) carries the same
	// key, so a Bot that gets both shows it once.
	BusinessKey string `json:"business_key,omitempty"`

	// Workspace-level events (DENE-1936) only. Names and ids, never values.
	Workspace *WorkspaceEventBody `json:"workspace,omitempty"`
}

// ActivityInput is one thing that happened, as a producer saw it.
type ActivityInput struct {
	WorkspaceID   pgtype.UUID
	IssueID       pgtype.UUID // exactly one of IssueID / ChatSessionID
	ChatSessionID pgtype.UUID
	CommentID     pgtype.UUID
	Type          string
	DedupeKey     string
	BusinessKey   string // see ActivityBody.BusinessKey
	SourceID      string // comment or message id
	ActorType     string
	ActorID       pgtype.UUID
	ViaBotID      string
	Text          string
	FromStatus    string
	ToStatus      string
	OccurredAt    time.Time
}

// RouteActivity writes the event for every person whose stream covers the
// source. It returns how many Bots were handed the event. It does not look at
// the person's current visibility: the worker re-checks that at send time, so a
// permission change lands on the next push.
func RouteActivity(ctx context.Context, q *db.Queries, in ActivityInput) (int, error) {
	if !in.WorkspaceID.Valid || strings.TrimSpace(in.DedupeKey) == "" || in.Type == "" {
		return 0, nil
	}
	var bindings []db.MemberBotBinding
	var err error
	switch {
	case in.IssueID.Valid:
		bindings, err = q.ListActivityBindingsForIssue(ctx, db.ListActivityBindingsForIssueParams{WorkspaceID: in.WorkspaceID, ID: in.IssueID})
	case in.ChatSessionID.Valid:
		bindings, err = q.ListActivityBindingsForChat(ctx, db.ListActivityBindingsForChatParams{WorkspaceID: in.WorkspaceID, ID: in.ChatSessionID})
	}
	if err != nil || len(bindings) == 0 {
		return 0, err
	}
	importance, categories := ClassifyActivity(ActivityFacts{
		Type: in.Type, ActorType: in.ActorType, Text: in.Text, FromStatus: in.FromStatus, ToStatus: in.ToStatus,
	})
	occurred := in.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	body, err := json.Marshal(ActivityBody{
		ActivityType: in.Type, Importance: importance, Categories: categories,
		Excerpt: activityText(in), SourceID: in.SourceID,
		FromStatus: in.FromStatus, ToStatus: in.ToStatus,
		OccurredAt:  occurred.UTC().Format(time.RFC3339Nano),
		BusinessKey: in.BusinessKey,
	})
	if err != nil {
		return 0, err
	}
	callerType := in.ActorType
	if callerType == "" {
		callerType = "system"
	}
	sent := 0
	for _, b := range bindings {
		if in.ActorType == "member" && in.ActorID.Valid && in.ActorID == b.UserID {
			continue // their own words are not news to them
		}
		if in.ViaBotID != "" && in.ViaBotID == util.UUIDToString(b.BotID) {
			continue // the person's own Bot wrote it; echoing it back would loop
		}
		channel, chErr := q.GetHumanChannel(ctx, db.GetHumanChannelParams{WorkspaceID: b.WorkspaceID, UserID: b.UserID})
		if chErr != nil || !HumanChannelUsable(channel) {
			continue
		}
		if in.CommentID.Valid {
			asked, askErr := q.HasHumanNeededForComment(ctx, db.HasHumanNeededForCommentParams{BotID: b.BotID, SourceCommentID: in.CommentID})
			if askErr == nil && asked {
				continue // already pushed as "you are needed"; once is enough
			}
		}
		if importance == ImportanceLow {
			backlog, countErr := q.CountQueuedActivityForBot(ctx, b.BotID)
			if countErr == nil && backlog >= activityLowBacklog {
				slog.Warn("activity stream: dropping low-importance event, Bot backlog full", "bot_id", util.UUIDToString(b.BotID))
				continue
			}
		}
		_, createErr := q.CreateHumanNeededEvent(ctx, db.CreateHumanNeededEventParams{
			WorkspaceID:     b.WorkspaceID,
			BotID:           b.BotID,
			BindingID:       b.ID,
			TargetUserID:    b.UserID,
			EventType:       HumanEventActivity,
			Kind:            in.Type,
			DedupeKey:       in.DedupeKey,
			IssueID:         in.IssueID,
			ChatSessionID:   in.ChatSessionID,
			SourceCommentID: in.CommentID,
			CallerType:      callerType,
			CallerID:        in.ActorID,
			Options:         []byte("[]"),
			Urgency:         HumanUrgencyNormal,
			ExpiresAt:       pgtype.Timestamptz{Time: time.Now().Add(activityRetention), Valid: true},
			Activity:        body,
		})
		if errors.Is(createErr, pgx.ErrNoRows) {
			continue // already written for this Bot
		}
		if createErr != nil {
			slog.Warn("activity stream: write failed", "error", createErr, "type", in.Type)
			continue
		}
		sent++
	}
	return sent, nil
}

// activityText is the excerpt for an event: the words for a comment or a chat
// message, a one-line description for a state change.
func activityText(in ActivityInput) string {
	switch in.Type {
	case ActivityStatusChanged:
		return fmt.Sprintf("状态从 %s 变为 %s", in.FromStatus, in.ToStatus)
	case ActivityAssigneeChanged:
		return ActivityExcerpt(in.Text)
	}
	return ActivityExcerpt(in.Text)
}
