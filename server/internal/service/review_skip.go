package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Review skip (DENE-1678): an in_review ticket whose PR is already merged and
// already reviewed does not wait for the acceptance seat. Only facts the
// server holds count: the PR state it mirrors, a GitHub approval it recorded,
// or a `verdict: pass` comment from someone other than the executor.
const (
	ReviewSkipGitHubApprove   = "github_approve"
	ReviewSkipPlatformVerdict = "platform_verdict"
	// MetaKeyReviewSkip holds the reason the ticket skipped acceptance, so the
	// issue page and `issue get` say why no acceptance seat ran.
	MetaKeyReviewSkip = "review_skip"
)

// ReviewSkip is the fact that let a ticket skip acceptance.
type ReviewSkip struct {
	Kind  string
	By    string
	PRURL string
	At    time.Time
}

// Reason is the one sentence the timeline and the CLI show.
func (r ReviewSkip) Reason() string {
	by := strings.TrimSpace(r.By)
	if by == "" {
		by = "有人"
	}
	if r.Kind == ReviewSkipGitHubApprove {
		return fmt.Sprintf("PR %s 已合入，%s 在 GitHub 上批准过", r.PRURL, by)
	}
	return fmt.Sprintf("PR %s 已合入，%s 在票上给过审查通过", r.PRURL, by)
}

// ReviewSkipPR is one linked PR as the skip reads it.
type ReviewSkipPR struct {
	URL        string
	State      string
	ApprovedBy string
	ApprovedAt time.Time
}

// ReviewSkipVerdict is one comment carrying a verdict line, newest first.
type ReviewSkipVerdict struct {
	AuthorID string
	Content  string
	At       time.Time
}

// DecideReviewSkip is the rule. Every linked PR must be finished (no open or
// draft one) with at least one merged. The newest verdict from a
// non-executor decides first: a hold means somebody said no. Otherwise a
// recorded GitHub approval of a merged PR, or that newest verdict being a
// pass, lets the ticket skip. by is filled with the verdict author's id; the
// caller swaps in a name.
func DecideReviewSkip(prs []ReviewSkipPR, verdicts []ReviewSkipVerdict, executors map[string]bool) (ReviewSkip, bool) {
	merged := ""
	for _, pr := range prs {
		switch strings.ToLower(pr.State) {
		case "open", "draft":
			return ReviewSkip{}, false
		case "merged":
			if merged == "" {
				merged = pr.URL
			}
		}
	}
	if merged == "" {
		return ReviewSkip{}, false
	}
	var verdict *ReviewSkipVerdict
	for i := range verdicts {
		v := verdicts[i]
		if executors[v.AuthorID] || blockwait.Verdict(v.Content) == "" {
			continue
		}
		verdict = &v
		break
	}
	if verdict != nil && blockwait.Verdict(verdict.Content) == "hold" {
		return ReviewSkip{}, false
	}
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "merged") && strings.TrimSpace(pr.ApprovedBy) != "" {
			return ReviewSkip{Kind: ReviewSkipGitHubApprove, By: pr.ApprovedBy, PRURL: pr.URL, At: pr.ApprovedAt}, true
		}
	}
	if verdict != nil {
		return ReviewSkip{Kind: ReviewSkipPlatformVerdict, By: verdict.AuthorID, PRURL: merged, At: verdict.At}, true
	}
	return ReviewSkip{}, false
}

// FindReviewSkip reads the issue's PRs and verdict comments and applies
// DecideReviewSkip. The executor whose verdict never counts is the author of
// the evidence comment of the close that sent the ticket to review, plus any
// ids the caller names (the closing agent itself, before that close lands).
func FindReviewSkip(ctx context.Context, q *db.Queries, issue db.Issue, executors ...pgtype.UUID) (ReviewSkip, bool, error) {
	gh, err := q.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		return ReviewSkip{}, false, err
	}
	vcs, err := q.ListVCSPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		return ReviewSkip{}, false, err
	}
	prs := make([]ReviewSkipPR, 0, len(gh)+len(vcs))
	for _, pr := range gh {
		prs = append(prs, ReviewSkipPR{URL: pr.HtmlUrl, State: pr.State, ApprovedBy: pr.ApprovedBy.String, ApprovedAt: pr.ApprovedAt.Time})
	}
	for _, pr := range vcs {
		prs = append(prs, ReviewSkipPR{URL: pr.HtmlUrl, State: pr.State})
	}
	if len(prs) == 0 {
		return ReviewSkip{}, false, nil
	}
	rows, err := q.ListIssueVerdictComments(ctx, issue.ID)
	if err != nil {
		return ReviewSkip{}, false, err
	}
	verdicts := make([]ReviewSkipVerdict, 0, len(rows))
	for _, row := range rows {
		verdicts = append(verdicts, ReviewSkipVerdict{AuthorID: util.UUIDToString(row.AuthorID), Content: row.Content, At: row.CreatedAt.Time})
	}
	excluded := map[string]bool{}
	for _, id := range executors {
		if id.Valid {
			excluded[util.UUIDToString(id)] = true
		}
	}
	if closer, ok := reviewCloser(ctx, q, issue); ok {
		excluded[closer] = true
	}
	skip, ok := DecideReviewSkip(prs, verdicts, excluded)
	if !ok {
		return ReviewSkip{}, false, nil
	}
	if skip.Kind == ReviewSkipPlatformVerdict {
		skip.By = reviewAuthorName(ctx, q, rows, skip.By)
	}
	return skip, true, nil
}

// reviewCloser is the author of the last close's evidence comment.
func reviewCloser(ctx context.Context, q *db.Queries, issue db.Issue) (string, bool) {
	var meta map[string]any
	if len(issue.Metadata) == 0 || json.Unmarshal(issue.Metadata, &meta) != nil {
		return "", false
	}
	raw, _ := meta[closeprotocol.KeyEvidenceCommentID].(string)
	id, err := util.ParseUUID(raw)
	if err != nil {
		return "", false
	}
	comment, err := q.GetComment(ctx, id)
	if err != nil || !comment.AuthorID.Valid {
		return "", false
	}
	return util.UUIDToString(comment.AuthorID), true
}

func reviewAuthorName(ctx context.Context, q *db.Queries, rows []db.ListIssueVerdictCommentsRow, authorID string) string {
	for _, row := range rows {
		if util.UUIDToString(row.AuthorID) != authorID {
			continue
		}
		switch row.AuthorType {
		case "agent":
			if agent, err := q.GetAgent(ctx, row.AuthorID); err == nil && strings.TrimSpace(agent.Name) != "" {
				return agent.Name
			}
		case "member":
			if user, err := q.GetUser(ctx, row.AuthorID); err == nil && strings.TrimSpace(user.Name) != "" {
				return user.Name
			}
		}
		break
	}
	return ""
}

// RecordReviewSkip keeps the skip on the issue for the page and the CLI.
// Metadata values the clients read are primitives, so the reason and the kind
// are two string keys rather than one object.
func RecordReviewSkip(ctx context.Context, q *db.Queries, issue db.Issue, skip ReviewSkip) error {
	for key, value := range map[string]string{
		MetaKeyReviewSkip:           skip.Reason(),
		MetaKeyReviewSkip + ".kind": skip.Kind,
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
			Key: key, Value: raw, ID: issue.ID, WorkspaceID: issue.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ClearReviewSkip drops an earlier skip once the ticket is back in review.
func ClearReviewSkip(ctx context.Context, q *db.Queries, issue db.Issue) {
	for _, key := range []string{MetaKeyReviewSkip, MetaKeyReviewSkip + ".kind"} {
		_, _ = q.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{Key: key, ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	}
}
