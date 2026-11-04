package issuewatchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
)

// Owner reminders come in three kinds, all on one chain (DENE-2967,
// DENE-2981): a status reminder wakes the responsible owner as soon as an
// employee delivers (in_review), blocks or finishes (done) an issue; a
// progress reminder (progress_wake.go) reports a running issue; the
// 30-minute stall reminder is the fallback. They share the outbox, the
// claim-time fresh read and the Mac transport.
const (
	ownerStalledEvent = "owner_stalled"
	ownerStatusEvent  = "owner_status"

	reminderKindStall  = "stall"
	reminderKindStatus = "status"

	// statusWakeWindow bounds which status entries a scan still wakes for, so
	// turning the feature on does not wake owners for every issue that has sat
	// in review for days; those stay with the stall fallback.
	statusWakeWindow = 10 * time.Minute
	// statusWakeSettle holds a fresh status reminder briefly, so a delivery
	// comment posted right after the status flip is the one the owner reads.
	statusWakeSettle = 10 * time.Second

	summaryMaxRunes  = 280
	maxEvidenceLinks = 5
	linkMaxRunes     = 300
	maxAttachments   = 5
)

var ownerReminderEvents = []string{ownerStalledEvent, ownerStatusEvent, ownerProgressEvent}

// statusWakeCause is the cause a status reminder carries for each status that
// wakes the owner at once.
var statusWakeCause = map[string]string{"in_review": "delivered", "blocked": "blocked", "done": "done"}

// WakesOnStatus reports whether entering status wakes the owner at once.
func WakesOnStatus(status string) bool {
	_, ok := statusWakeCause[status]
	return ok
}

// statusWakeEligible: the issue sits in a waking status and does not wait on Kun.
func statusWakeEligible(issue Issue) bool {
	return WakesOnStatus(issue.Status) && !waitsOnKun(issue)
}

// statusEntry is when an issue entered its current status: the latest
// status-change event, provided it moved the issue to that status. EventID 0
// means the issue was created in it (or the log has no matching move). Actor
// is who moved it: a member move is Kun's or a manager's own, never an
// employee's progress, so it wakes nobody.
type statusEntry struct {
	EventID int64
	At      time.Time
	Actor   string
}

func currentStatusEntry(ctx context.Context, store QueryStore, issue Issue) (statusEntry, error) {
	var (
		entry  statusEntry
		status string
	)
	err := store.QueryRow(ctx, `
SELECT id, occurred_at, actor_type, COALESCE(changes->0->>'new', '')
FROM office_event
WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND event_type = 'issue.status_changed'
ORDER BY id DESC
LIMIT 1`, issue.WorkspaceID, issue.ID).Scan(&entry.EventID, &entry.At, &entry.Actor, &status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != issue.Status) {
		return statusEntry{At: issue.CreatedAt}, nil
	}
	return entry, err
}

// WakeOnStatus is the event path: called right after an issue's status
// changes. The scan repeats the same check, so a missed event costs at most
// one tick.
func (w *Watchdog) WakeOnStatus(ctx context.Context, issueID string) error {
	if !w.cfg.Enabled {
		return nil
	}
	issue, err := w.loadIssue(ctx, w.cfg.WorkspaceID, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return w.enqueueStatusWake(ctx, issue, w.now())
}

// recentStatusEntries lists issues that may have entered a waking status
// within the window. A status move bumps updated_at, so this cheap issue read
// covers every entry the window can still wake for.
func (w *Watchdog) recentStatusEntries(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := w.pool.Query(ctx, `
SELECT id::text FROM issue
WHERE workspace_id = $1::uuid AND status IN ('in_review', 'blocked', 'done') AND updated_at >= $2
ORDER BY updated_at ASC
LIMIT 500`, w.cfg.WorkspaceID, now.Add(-statusWakeWindow))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// enqueueStatusWake records one owner reminder per status entry. The event
// key is the status-change event, so the event path, every later scan and a
// second server all land on the same row.
func (w *Watchdog) enqueueStatusWake(ctx context.Context, issue Issue, now time.Time) error {
	cause, ok := statusWakeCause[issue.Status]
	if !ok || !statusWakeEligible(issue) {
		return nil
	}
	entry, err := currentStatusEntry(ctx, w.pool, issue)
	if err != nil {
		return err
	}
	if entry.EventID == 0 || entry.Actor == "member" || now.Sub(entry.At) > statusWakeWindow {
		return nil
	}
	manager, registration := w.cfg.Owners.Resolve(issue.ManagerName, issue.ProjectID)
	key := fmt.Sprintf("owner-status:%d", entry.EventID)
	event := Reminder{
		Kind: reminderKindStatus, EventID: issue.ID + ":" + key, IssueID: issue.ID,
		IssueNumber: issue.Identifier, Title: issue.Title, Status: issue.Status, Manager: manager,
		ProjectName: issue.ProjectName, Cause: cause, StatusEntry: entry.EventID, EnteredAt: entry.At,
		OccurredAt: now, RegistrationNeeded: registration,
		Reason: statusWakeReason(issue.Status, registration),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	available := entry.At.Add(statusWakeSettle)
	if available.Before(now) {
		available = now
	}
	_, err = w.pool.Exec(ctx, `
INSERT INTO office_issue_watchdog_attempt
(workspace_id, issue_id, event_key, event_type, target, manager_name, payload, available_at)
VALUES ($1::uuid,$2::uuid,$3,$4,'manager',$5,$6::jsonb,$7)
ON CONFLICT (workspace_id,issue_id,event_key) DO NOTHING`,
		issue.WorkspaceID, issue.ID, key, ownerStatusEvent, manager, payload, available)
	return err
}

func statusWakeReason(status string, registration bool) string {
	reason := "单子已交付（审核中），请负责人验收：通过就关单，不通过就在原单写清要改什么；在项目群报一句结果。"
	switch status {
	case "blocked":
		reason = "单子已阻塞，请负责人在原单解阻或安排下一步，在项目群报一句卡在哪。"
	case "done":
		reason = "员工已做完并关单，请负责人在项目群报一句做成了什么。"
	}
	if registration {
		reason += " 项目归属缺失，请扫帚补登记。"
	}
	return reason
}

// entryAlreadyWoken reports whether the owner was, or may have been, woken
// for this status entry. Only a proven non-delivery (not_sent after the
// retry, or dropped before sending) leaves the stall fallback free to fire.
func entryAlreadyWoken(ctx context.Context, store QueryStore, issue Issue, entry statusEntry) (bool, error) {
	var woken bool
	err := store.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM office_issue_watchdog_attempt
  WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND event_type = ANY($3)
    AND payload->>'status' = $4 AND payload->>'status_entry' = $5
    AND NOT (outcome = 'failed' AND delivery_result IN ('not_sent', 'resolved_before_delivery', 'stale'))
)`, issue.WorkspaceID, issue.ID, ownerReminderEvents, issue.Status, fmt.Sprint(entry.EventID)).Scan(&woken)
	return woken, err
}

// delivery is the latest substantive comment the owner should read: the
// assignee's own when there is one, otherwise anyone's.
type delivery struct {
	CommentID   string
	Author      string
	At          time.Time
	Summary     string
	Links       []string
	Attachments []attachmentRef
}

type attachmentRef struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
}

// latestDelivery is the comment the owner reads: for a status reminder the
// assignee's newest comment, else anyone's; for a progress reminder the newest
// agent comment, the one that set the reminder off.
func latestDelivery(ctx context.Context, store QueryStore, issue Issue, progress bool) (delivery, bool, error) {
	var (
		d       delivery
		content string
	)
	err := store.QueryRow(ctx, `
SELECT c.id::text, c.content, c.created_at, COALESCE(a.name, u.name, '')
FROM comment c
LEFT JOIN agent a ON c.author_type = 'agent' AND a.id = c.author_id
LEFT JOIN "user" u ON c.author_type = 'member' AND u.id = c.author_id
WHERE c.workspace_id = $1::uuid AND c.issue_id = $2::uuid
  AND c.author_type IN ('member', 'agent') AND c.type = 'comment'
  AND c.content NOT LIKE '%' || $3 || '%'
  AND regexp_replace(lower(trim(c.content)), '[[:space:][:punct:]，。！？、；：…]+', '', 'g') <> '收到'
  AND (NOT $6 OR c.author_type = 'agent')
ORDER BY (NOT $6 AND c.author_type = $4 AND c.author_id::text = $5) DESC, c.created_at DESC, c.id DESC
LIMIT 1`, issue.WorkspaceID, issue.ID, watchdogCommentMarker, issue.AssigneeType, issue.AssigneeID, progress,
	).Scan(&d.CommentID, &content, &d.At, &d.Author)
	if errors.Is(err, pgx.ErrNoRows) {
		return delivery{}, false, nil
	}
	if err != nil {
		return delivery{}, false, err
	}
	d.Summary = summarize(content)
	d.Links = evidenceLinks(content)
	rows, err := store.Query(ctx, `
SELECT id::text, filename FROM attachment
WHERE workspace_id = $1::uuid AND comment_id = $2::uuid
ORDER BY created_at, id
LIMIT $3`, issue.WorkspaceID, d.CommentID, maxAttachments)
	if err != nil {
		return delivery{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref attachmentRef
		if err := rows.Scan(&ref.ID, &ref.Filename); err != nil {
			return delivery{}, false, err
		}
		d.Attachments = append(d.Attachments, ref)
	}
	return d, true, rows.Err()
}

var (
	whitespaceRun = regexp.MustCompile(`\s+`)
	// A link may carry the mask the sanitizer left in it.
	linkPattern = regexp.MustCompile("https?://(?:[^\\s<>()\\[\\]{}\"'`]|\\[已打码\\])+")
	// Rules the shared sanitizer does not cover: a bearer value, a credential
	// field whose name merely contains the word (token_value, Cookie) and may
	// sit in quotes, and token-shaped values by prefix.
	bearerValue  = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	namedSecret  = regexp.MustCompile(`(?i)\b([\w-]*(?:token|secret|password|passwd|api[_-]?key|cookie)[\w-]*)(["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s"',;}&]+)`)
	shapedSecret = regexp.MustCompile(`(?i)\b(?:mul|mcp|sk|pk|rk|ghp|gho|ghs|ghu|github_pat|xox[abprs])[-_][A-Za-z0-9_-]{12,}`)
	// Credential-named parameters in a link's query or fragment (signed and
	// session links) that the field rules do not name.
	// A parameter name in a link's query or fragment, up to its "=".
	linkParam = regexp.MustCompile(`[?&#;]([\w.-]+)=`)
	// A parameter is a credential when a whole part of its name is one of
	// these: access_token and X-Amz-Signature are, author and assigned are not.
	secretParamWords = map[string]bool{"auth": true, "authorization": true, "sig": true, "signature": true,
		"session": true, "sessionid": true, "sid": true, "token": true, "secret": true, "pass": true,
		"passwd": true, "password": true, "pwd": true, "key": true, "apikey": true, "cookie": true,
		"credential": true, "credentials": true, "code": true, "jwt": true}
	paramNameParts = regexp.MustCompile(`[_.-]+`)
	camelBoundary  = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	// Concatenated names end in one of these: JSESSIONID, PHPSESSID, accesstoken.
	secretParamEndings = []string{"token", "secret", "password", "passwd", "sessid", "sessionid", "signature", "apikey", "credential", "cookie"}
	// Evidence the shared long-run rule would otherwise swallow: full commit
	// hashes and UUIDs. Set aside only for the shared pass; the stricter rules
	// above still see them, so "Bearer <uuid>" or "sk-<hex>" stay masked.
	evidenceID = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{40}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`)
)

const (
	secretMark = "[已打码]"
	// pathSlash stands in for "/" inside a link's path during the shared pass,
	// so its long-run rule judges each path segment, not host plus path.
	pathSlash = "\uE002"
)

// maskSecrets hides credential-looking values in what an owner reminder
// forwards. The shared Bot sanitizer sees the whole text, so a credential
// field is masked whole even when it holds a link; inside links it judges one
// path segment at a time, so repository, run and commit links stay usable.
// The stricter local rules then run over the restored text.
func maskSecrets(s string) string {
	s = linkPattern.ReplaceAllStringFunc(s, func(link string) string {
		head := strings.Index(link, "://") + 3
		path := strings.IndexByte(link[head:], '/')
		if path < 0 {
			return link
		}
		path += head
		end := len(link)
		if i := strings.IndexAny(link[path:], "?#"); i >= 0 {
			end = path + i // query and fragment values stay whole for the long-value rule
		}
		return link[:path] + strings.ReplaceAll(link[path:end], "/", pathSlash) + link[end:]
	})
	var kept []string
	s = evidenceID.ReplaceAllStringFunc(s, func(id string) string {
		kept = append(kept, id)
		return fmt.Sprintf("\uE000%d\uE001", len(kept)-1)
	})
	s = service.MaskSecrets(s)
	for i, id := range kept {
		s = strings.Replace(s, fmt.Sprintf("\uE000%d\uE001", i), id, 1)
	}
	s = strings.ReplaceAll(s, pathSlash, "/")
	s = bearerValue.ReplaceAllString(s, "${1}"+secretMark)
	s = namedSecret.ReplaceAllStringFunc(s, func(m string) string {
		sub := namedSecret.FindStringSubmatch(m)
		if strings.Contains(sub[3], secretMark) {
			return m
		}
		return sub[1] + sub[2] + secretMark
	})
	s = textToken.ReplaceAllStringFunc(s, maskParamCredential)
	return shapedSecret.ReplaceAllString(s, secretMark)
}

// textToken is any run of non-space text. Parameters are judged per token, not
// per recognised link, so relative links, upper-case schemes and links with
// brackets are covered too.
var textToken = regexp.MustCompile(`\S+`)

// maskParamCredential masks a token from the value of its first
// credential-named parameter (after ?, &, # or ;) to the token's end: whatever
// separators that value holds, nothing after a credential leaves. Earlier,
// ordinary parameters stay as cited.
func maskParamCredential(token string) string {
	for _, loc := range linkParam.FindAllStringSubmatchIndex(token, -1) {
		if credentialParam(token[loc[2]:loc[3]]) {
			value := loc[1]
			if token[value:] == secretMark {
				return token
			}
			return token[:value] + secretMark
		}
	}
	return token
}

func credentialParam(name string) bool {
	split := strings.ToLower(camelBoundary.ReplaceAllString(name, "${1}_${2}"))
	for _, part := range append(paramNameParts.Split(split, -1), strings.ToLower(name)) {
		if secretParamWords[part] {
			return true
		}
		for _, ending := range secretParamEndings {
			if strings.HasSuffix(part, ending) {
				return true
			}
		}
	}
	return false
}

func summarize(content string) string {
	text := strings.TrimSpace(whitespaceRun.ReplaceAllString(content, " "))
	return clipRunes(maskSecrets(text), summaryMaxRunes)
}

// evidenceLinks are the links the delivery cites, taken from the masked text.
func evidenceLinks(content string) []string {
	var links []string
	seen := map[string]bool{}
	for _, raw := range linkPattern.FindAllString(maskSecrets(content), -1) {
		link := strings.TrimRight(raw, ".,;:!?。，；：！？、）")
		// net/url refuses the mask inside userinfo; validate the shape without it.
		if _, err := url.Parse(strings.ReplaceAll(link, secretMark, "x")); err != nil || seen[link] {
			continue
		}
		seen[link] = true
		links = append(links, clipRunes(link, linkMaxRunes))
		if len(links) == maxEvidenceLinks {
			break
		}
	}
	return links
}

// issueURL is the issue's web address when the operator configured the web
// origin; the Mac transport fills its own when this is empty.
func (w *Watchdog) issueURL(issue Issue) string {
	if w.cfg.AppURL == "" || issue.WorkspaceSlug == "" {
		return ""
	}
	return w.cfg.AppURL + "/" + url.PathEscape(issue.WorkspaceSlug) + "/issues/" + issue.ID
}

// refreshReminder replaces everything an owner reads with what is true at
// claim time: status, title, owner and the latest delivery comment.
func (w *Watchdog) refreshReminder(ctx context.Context, store QueryStore, event *Reminder, issue Issue, now time.Time) error {
	event.Title, event.Status, event.ProjectName = issue.Title, issue.Status, issue.ProjectName
	event.Manager, event.RegistrationNeeded = w.cfg.Owners.Resolve(issue.ManagerName, issue.ProjectID)
	event.CheckedAt = now
	event.IssueURL = w.issueURL(issue)
	d, ok, err := latestDelivery(ctx, store, issue, event.Kind == reminderKindProgress)
	if err != nil {
		return err
	}
	event.Comment = nil
	if ok {
		event.Comment = &ReminderComment{ID: d.CommentID, Author: d.Author, At: d.At,
			Summary: d.Summary, Links: d.Links, Attachments: d.Attachments}
	}
	return nil
}
