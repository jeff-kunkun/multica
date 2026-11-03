package issueguard

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const SimilarThreshold = 0.40

type SimilarInput struct {
	WorkspaceID pgtype.UUID
	ProjectID   pgtype.UUID
	ParentID    pgtype.UUID
	Title       string
	Description string
	Prefix      string
	MemberID    pgtype.UUID
}

type SimilarCandidate struct {
	ID         string   `json:"id"`
	Identifier string   `json:"identifier"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Score      float64  `json:"score"`
	Why        string   `json:"why"`
	RecentDone bool     `json:"recent_done"`
	Issue      db.Issue `json:"-"`
}

// LockSimilarProject serializes even differently worded creates. Locking a
// title cannot protect a similarity check against concurrent paraphrases.
func LockSimilarProject(ctx context.Context, q *db.Queries, in SimilarInput) error {
	if !in.ProjectID.Valid {
		return nil
	}
	return q.LockIssueDuplicateKey(ctx, "issue-similar|"+util.UUIDToString(in.WorkspaceID)+"|"+util.UUIDToString(in.ProjectID))
}

func FindSimilar(ctx context.Context, q *db.Queries, in SimilarInput) ([]SimilarCandidate, error) {
	if !in.ProjectID.Valid || strings.TrimSpace(in.Title) == "" {
		return []SimilarCandidate{}, nil
	}
	visible, err := memberVisibility(ctx, q, in.WorkspaceID, in.MemberID)
	if err != nil {
		return nil, err
	}
	done, err := issuestatus.ExpandCategories(ctx, q, in.WorkspaceID, []string{issuestatus.CategoryDone})
	if err != nil {
		return nil, err
	}
	closed, err := issuestatus.ExpandCategories(ctx, q, in.WorkspaceID, []string{issuestatus.CategoryClosed})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	rows, err := q.ListSimilarIssueCandidates(ctx, db.ListSimilarIssueCandidatesParams{
		WorkspaceID: in.WorkspaceID, ProjectID: in.ProjectID,
		DoneKeys: done, ClosedKeys: closed,
		DoneAfter:   pgtype.Timestamptz{Time: now.Add(-72 * time.Hour), Valid: true},
		ClosedAfter: pgtype.Timestamptz{Time: now.Add(-2 * time.Hour), Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := []SimilarCandidate{}
	for _, row := range rows {
		if !visible(row) {
			continue
		}
		// Planned decomposition must remain possible: the parent itself and
		// siblings are related work, not accidental replacement tickets.
		if in.ParentID.Valid && (row.ID == in.ParentID || row.ParentIssueID == in.ParentID) {
			continue
		}
		score, why := Similarity(in.Title, in.Description, row.Title, row.Description.String)
		if score < SimilarThreshold {
			continue
		}
		out = append(out, SimilarCandidate{
			ID: util.UUIDToString(row.ID), Identifier: fmt.Sprintf("%s-%d", in.Prefix, row.Number),
			Title: row.Title, Status: row.Status, Score: math.Round(score*1000) / 1000,
			Why: why, RecentDone: containsKey(done, row.Status), Issue: row,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RecentDone != out[j].RecentDone {
			return !out[i].RecentDone
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if !out[i].Issue.CreatedAt.Time.Equal(out[j].Issue.CreatedAt.Time) {
			return out[i].Issue.CreatedAt.Time.Before(out[j].Issue.CreatedAt.Time)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Match the sharing facts used by issue lists, including owned agents and
// explicit shares. Resolve them in the create transaction for channel callers
// too; a hidden candidate must never leak via a create error.
func memberVisibility(ctx context.Context, q *db.Queries, ws, memberID pgtype.UUID) (func(db.Issue) bool, error) {
	if !memberID.Valid {
		return func(db.Issue) bool { return true }, nil
	}
	member, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{WorkspaceID: ws, UserID: memberID})
	if err != nil {
		return nil, err
	}
	if member.Role == "owner" {
		return func(db.Issue) bool { return true }, nil
	}
	projects, err := q.ListProjectMembershipsForUser(ctx, db.ListProjectMembershipsForUserParams{WorkspaceID: ws, MemberID: memberID})
	if err != nil {
		return nil, err
	}
	led, err := q.ListProjectIDsLedByMember(ctx, db.ListProjectIDsLedByMemberParams{WorkspaceID: ws, LeadID: memberID})
	if err != nil {
		return nil, err
	}
	agents, err := q.ListAgentIDsOwnedByMember(ctx, db.ListAgentIDsOwnedByMemberParams{WorkspaceID: ws, OwnerID: memberID})
	if err != nil {
		return nil, err
	}
	shares, err := q.ListResourceSharesForMember(ctx, db.ListResourceSharesForMemberParams{WorkspaceID: ws, MemberID: memberID})
	if err != nil {
		return nil, err
	}
	projectSet, agentSet, shared := map[pgtype.UUID]bool{}, map[pgtype.UUID]bool{}, map[string]bool{}
	for _, id := range append(projects, led...) {
		projectSet[id] = true
	}
	for _, id := range agents {
		agentSet[id] = true
	}
	for _, share := range shares {
		if share.ResourceType == "issue" {
			shared[share.ResourceID] = true
		}
	}
	isMe := func(kind string, id pgtype.UUID) bool {
		return (kind == "member" && id == memberID) || (kind == "agent" && agentSet[id])
	}
	return func(issue db.Issue) bool {
		return permission.CanSee(permission.Role(member.Role), permission.Visibility(issue.Visibility), permission.Relation{
			IsCreator:  isMe(issue.CreatorType, issue.CreatorID),
			IsAssignee: isMe(issue.AssigneeType.String, issue.AssigneeID),
			InProject:  projectSet[issue.ProjectID], SharedWith: shared[util.UUIDToString(issue.ID)],
		})
	}, nil
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

var (
	overrideLine = regexp.MustCompile(`(?m)^\s*查重放行[：:]\s*(.+)$`)
	issueRef     = regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9]*-[0-9]+\b`)
	mergeRef     = regexp.MustCompile(`![0-9]+\b|(?i)\bPR\s*#[0-9]+\b`)
	reviewURL    = regexp.MustCompile(`https?://[^\s<>()]+/(?:pull|merge_requests)/[0-9]+\b`)
	branchRef    = regexp.MustCompile("(?i)(?:分支|branch)[：: \\t]+`?([a-z0-9_][a-z0-9_./-]+)")
)

// OverrideReason only accepts a standalone line naming the reviewed tickets
// and a concrete explanation. A bare flag, prefix or identifier is not a reason.
func OverrideReason(description string, candidates []SimilarCandidate) string {
	match := overrideLine.FindStringSubmatch(description)
	if len(match) < 2 {
		return ""
	}
	reason := strings.TrimSpace(match[1])
	explanation := strings.TrimSpace(issueRef.ReplaceAllString(reason, ""))
	if len([]rune(explanation)) < 8 {
		return ""
	}
	for _, c := range candidates {
		if !hasIssueRef(reason, c.Identifier) {
			return ""
		}
	}
	return reason
}

func hasIssueRef(text, ref string) bool {
	for _, found := range issueRef.FindAllString(text, -1) {
		if strings.EqualFold(found, ref) {
			return true
		}
	}
	return false
}

// Acknowledging completed work must not hide another unacknowledged match.
func BlockingSimilar(description string, candidates []SimilarCandidate) []SimilarCandidate {
	if OverrideReason(description, candidates) != "" {
		return []SimilarCandidate{}
	}
	out := []SimilarCandidate{}
	for _, c := range candidates {
		if c.RecentDone && hasIssueRef(description, c.Identifier) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func SimilarMessage(candidates []SimilarCandidate) string {
	var b strings.Builder
	b.WriteString("发现同项目相似工作单，未创建新单：")
	for _, c := range candidates {
		fmt.Fprintf(&b, "\n%s %s（%s；%s；相似度 %.2f）", c.Identifier, c.Title, c.Status, c.Why, c.Score)
	}
	b.WriteString("\n请回原单评论接力：multica issue comment add <原单编号> --content-file <文件>。确属不同工作，在描述单独写一行「查重放行：和 <候选编号> 不同在……」（逐一列出候选，说明至少 8 字），再提交。近期完成的工作，描述点名原单后可开后续单。--allow-duplicate 不能跳过此检查。")
	return b.String()
}

func Similarity(title, description, otherTitle, otherDescription string) (float64, string) {
	a, b := references(title+"\n"+description), references(otherTitle+"\n"+otherDescription)
	for _, ref := range a {
		for _, other := range b {
			if ref == other {
				return 0.8, "同一合并请求、PR 或分支：" + ref
			}
		}
	}
	titleScore := dice(tokens(lexicalText(title)), tokens(lexicalText(otherTitle)))
	descScore := dice(tokens(lexicalText(firstRunes(description, 300))), tokens(lexicalText(firstRunes(otherDescription, 300))))
	return math.Max(titleScore, 0.5*titleScore+0.5*descScore), "标题、描述关键词相似"
}

func lexicalText(text string) string {
	text = reviewURL.ReplaceAllString(text, "")
	text = mergeRef.ReplaceAllString(text, "")
	text = branchRef.ReplaceAllString(text, "")
	return issueRef.ReplaceAllString(text, "")
}

func references(text string) []string {
	refs := reviewURL.FindAllString(text, -1)
	// Do not turn a fully qualified reference into a bare number; different
	// repositories can have the same PR/MR number in one project.
	text = reviewURL.ReplaceAllString(text, "")
	for _, ref := range mergeRef.FindAllString(text, -1) {
		refs = append(refs, strings.ToLower(strings.Join(strings.Fields(ref), "")))
	}
	for _, match := range branchRef.FindAllStringSubmatch(text, -1) {
		branch := match[1]
		switch strings.ToLower(branch) {
		case "main", "master", "kun", "develop", "dev", "test", "production", "staging":
			continue
		}
		refs = append(refs, "branch:"+branch)
	}
	return refs
}

func firstRunes(text string, n int) string {
	r := []rune(text)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func tokens(text string) map[string]struct{} {
	out := map[string]struct{}{}
	var word, han []rune
	flush := func() {
		if len(word) > 0 {
			out[string(word)] = struct{}{}
		}
		if len(han) == 1 {
			out[string(han)] = struct{}{}
		}
		for i := 1; i < len(han); i++ {
			out[string(han[i-1:i+1])] = struct{}{}
		}
		word, han = nil, nil
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) {
			if len(word) > 0 {
				flush()
			}
			han = append(han, r)
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if len(han) > 0 {
				flush()
			}
			word = append(word, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func dice(a, b map[string]struct{}) float64 {
	if len(a)+len(b) == 0 {
		return 0
	}
	common := 0
	for token := range a {
		if _, ok := b[token]; ok {
			common++
		}
	}
	return float64(2*common) / float64(len(a)+len(b))
}
