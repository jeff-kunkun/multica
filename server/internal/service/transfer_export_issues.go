package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Size of one `GET /api/issues` page. The endpoint silently clamps anything
// above 100 (contract §8.4), so asking for exactly 100 keeps the client and
// the server in agreement instead of relying on the clamp.
const transferIssuePageSize = 100

// Server-side cap of the comments list (`commentHardCap` in
// internal/handler/comment.go). A `since` page answers with at most this many
// rows; a shorter page means the walk reached the end of the stream.
const transferCommentPageSize = 2000

// transferCommentEpoch is the `since` cursor that turns the comments read into
// the chronological streaming path from the very first request. The default
// (cursor-less) path returns the NEWEST 2000 comments and offers no cursor, so
// it can neither be walked forward nor sampled consistently with a full export.
const transferCommentEpoch = "1970-01-01T00:00:00Z"

// transferIssueExport is everything the V3 `issues` group contributes to a
// bundle: the issue rows, the comments grouped by issue, the relation rows
// (labels + reactions), the attachment metadata/bodies and the secret records.
type transferIssueExport struct {
	IssueRows       []TransferIssueRow
	CommentsByIssue map[string][]TransferCommentRow
	Relations       []TransferRelationRow
	Attachments     []TransferAttachmentRow
	Blobs           map[string][]byte
	Secrets         []SecretOmitted
	Estimate        *TransferEstimate
	Gaps            []TransferExportGap
}

// exportIssues reads the four issue-scoped read interfaces (plus the issue
// detail read, the only place issue reactions are served) and turns them into
// bundle rows. It fills refs.Issues with the WHOLE package index: the import
// endpoint refuses a shard whose refs do not name every issue in the bundle,
// so this map is built once here and shipped on every shard.
func exportIssues(ctx context.Context, src TransferSourceClient, opts TransferExportOpts, refs *TransferRefs) *transferIssueExport {
	out := &transferIssueExport{
		CommentsByIssue: map[string][]TransferCommentRow{},
		Blobs:           map[string][]byte{},
	}
	inc := includeSet(opts.Include)
	exportAttachmentBodies := inc["attachments"] || len(opts.Include) == 0

	issues, trunc, err := fetchAllTransferIssues(ctx, src)
	if err != nil {
		out.Gaps = append(out.Gaps, transferReadGap(TransferIncludeIssues, err))
		return out
	}
	appendTransferGap(&out.Gaps, TransferIncludeIssues, trunc)

	if refs.Issues == nil {
		refs.Issues = map[string]TransferIssueRef{}
	}
	if opts.Estimate {
		out.Estimate = estimateTransferIssues(ctx, src, issues, refs)
		return out
	}

	// Resume: an issue already checkpointed is not read again, and its rows
	// come back from the partial directory instead of the source. Any issue not
	// in the completed set is re-walked from scratch (§8.5).
	resume := loadTransferPartial(opts.PartialDir, opts.OutPath, opts.WorkspaceRef)
	resumeIssues := map[string]TransferIssueRow{}
	resumeComments := map[string][]TransferCommentRow{}
	if resume != nil {
		rows, comments, atts, relations, blobs := loadPartialIssues(opts.PartialDir, resume.CompletedIssueIDs)
		for _, row := range rows {
			resumeIssues[row.SourceID] = row
		}
		resumeComments = comments
		out.Attachments = append(out.Attachments, atts...)
		out.Relations = append(out.Relations, relations...)
		for sha, body := range blobs {
			out.Blobs[sha] = body
		}
	}

	for _, raw := range issues {
		issueID := strField(raw, "id")
		if issueID == "" {
			out.Gaps = append(out.Gaps, TransferExportGap{Group: TransferIncludeIssues, Reason: gapReasonListShapeUnknown})
			continue
		}
		if saved, ok := resumeIssues[issueID]; ok {
			out.IssueRows = append(out.IssueRows, saved)
			out.CommentsByIssue[issueID] = resumeComments[issueID]
			refs.Issues[issueID] = TransferIssueRef{Number: saved.Number, Identifier: strField(raw, "identifier")}
			continue
		}
		row, rowSecrets := transferIssueRow(raw, refs)
		out.Secrets = append(out.Secrets, rowSecrets...)
		refs.Issues[issueID] = TransferIssueRef{Number: row.Number, Identifier: strField(raw, "identifier")}

		comments, commentSecrets, commentAtts, commentRelations, commentBlobs, missing, err := exportIssueComments(ctx, src, issueID, exportAttachmentBodies)
		if err != nil {
			out.Gaps = append(out.Gaps, transferReadGap(TransferIncludeIssues, err))
			return out
		}
		appendTransferGap(&out.Gaps, TransferIncludeIssues, missing)
		out.Secrets = append(out.Secrets, commentSecrets...)

		attachments, attSecrets, attBlobs, attGap, err := exportIssueAttachments(ctx, src, issueID, exportAttachmentBodies)
		if err != nil {
			out.Gaps = append(out.Gaps, transferReadGap(TransferIncludeIssues, err))
			return out
		}
		out.Secrets = append(out.Secrets, attSecrets...)

		labels, labelGap, err := exportIssueLabels(ctx, src, issueID)
		if err != nil {
			out.Gaps = append(out.Gaps, transferReadGap(TransferIncludeIssues, err))
			return out
		}
		reactions, reactionGap, err := exportIssueReactions(ctx, src, issueID)
		if err != nil {
			out.Gaps = append(out.Gaps, transferReadGap(TransferIncludeIssues, err))
			return out
		}
		for _, gap := range []*TransferListTruncation{attGap, labelGap, reactionGap} {
			appendTransferGap(&out.Gaps, TransferIncludeIssues, gap)
		}

		issueAtts := append(append([]TransferAttachmentRow{}, commentAtts...), attachments...)
		issueRelations := append(append(append([]TransferRelationRow{}, commentRelations...), labels...), reactions...)
		issueBlobs := map[string][]byte{}
		for sha, body := range commentBlobs {
			issueBlobs[sha] = body
		}
		for sha, body := range attBlobs {
			issueBlobs[sha] = body
		}

		out.IssueRows = append(out.IssueRows, row)
		out.CommentsByIssue[issueID] = comments
		out.Attachments = append(out.Attachments, issueAtts...)
		out.Relations = append(out.Relations, issueRelations...)
		for sha, body := range issueBlobs {
			out.Blobs[sha] = body
		}

		if opts.PartialDir != "" {
			if resume == nil {
				resume = newTransferPartialState(opts)
			}
			watermark := TransferIssueWatermark{}
			if n := len(comments); n > 0 {
				watermark.LastCreatedAt = comments[n-1].CreatedAt
			}
			for _, c := range comments {
				watermark.CommentIDs = append(watermark.CommentIDs, c.SourceID)
			}
			_ = persistCompletedIssue(opts.PartialDir, resume, row, comments, issueAtts, issueRelations, issueBlobs, watermark)
		}
	}
	return out
}

// transferIssueListEnvelope is the shape `GET /api/issues` answers with. A
// still-open server could add `has_more`; when it is present the walk trusts it
// over the page size, and when it is absent the page size is the only signal.
type transferIssueListEnvelope struct {
	Issues  []map[string]any `json:"issues"`
	Total   int              `json:"total"`
	HasMore *bool            `json:"has_more"`
}

// fetchAllTransferIssues walks the issue list with `sort=created_at&direction=asc`.
//
// Why this exact cursor: the default sort is `position` (drag order) and
// `last_activity_at` is rewritten by every comment, so either one reorders rows
// while the walk is running and duplicates or skips them at a page boundary
// (contract §8.4). The endpoint appends `created_at DESC, id DESC` as a unique
// tiebreak, so ascending reads are stable for rows that already exist.
//
// `total` is deliberately ignored: it is an independent COUNT that moves when
// the source creates issues during the export, so it can neither end the walk
// nor prove completeness. Completeness comes from the id set.
func fetchAllTransferIssues(ctx context.Context, src TransferSourceClient) ([]map[string]any, *TransferListTruncation, error) {
	var all []map[string]any
	seen := map[string]bool{}
	offset := 0
	for {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(transferIssuePageSize))
		q.Set("offset", strconv.Itoa(offset))
		q.Set("sort", "created_at")
		q.Set("direction", "asc")
		var envelope transferIssueListEnvelope
		if err := src.GetJSON(ctx, "/api/issues?"+q.Encode(), &envelope); err != nil {
			return all, nil, err
		}
		page := envelope.Issues
		added := 0
		for _, raw := range page {
			id := strField(raw, "id")
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			all = append(all, raw)
			added++
		}
		switch {
		case len(page) == 0:
			return all, nil, nil
		case added == 0:
			// A whole page of rows already seen: this cursor is not advancing,
			// so stopping is the only alternative to reading forever. The id
			// set above is what keeps this from hiding a duplicate.
			return all, &TransferListTruncation{Reason: gapReasonListHasMore, Limit: len(page)}, nil
		case envelope.HasMore != nil:
			if !*envelope.HasMore {
				return all, nil, nil
			}
		case len(page) < transferIssuePageSize:
			return all, nil, nil
		}
		offset += len(page)
	}
}

// getTransferCommentPage reads one `since` page of an issue's comments. The
// caller always passes a cursor: the cursor-less path answers with the newest
// 2000 rows and cannot be walked forward.
func getTransferCommentPage(ctx context.Context, src TransferSourceClient, issueID, since string) ([]map[string]any, error) {
	q := url.Values{}
	if since != "" {
		q.Set("since", since)
	}
	// `fold` is deliberately absent: folding a resolved thread drops its middle
	// replies, which is exactly the "discussion is missing" failure this export
	// must not reproduce (contract §8.4). It is also mutually exclusive with
	// `since`, so this path could not send it even by mistake.
	path := "/api/issues/" + url.PathEscape(issueID) + "/comments?" + q.Encode()
	var page []map[string]any
	if err := src.GetJSON(ctx, path, &page); err != nil {
		return nil, err
	}
	return page, nil
}

// walkTransferComments pages an issue's comments in ascending created_at order.
//
// The cursor is `last created_at of the page minus one microsecond`, not the
// timestamp itself. The server predicate is `created_at > $3` with no id
// tiebreak (pkg/db/queries/comment.sql: ListCommentsSinceForIssue), so reusing
// the last row's timestamp verbatim skips every comment that shares that
// microsecond with a row that fell past the page boundary — and comments
// written in one task run routinely collide there. Backing the cursor off by
// one microsecond re-reads the tie group, and the seen-id set drops the rows
// already exported, so the walk advances without losing a tie. One microsecond
// (not one nanosecond) is the right step because PostgreSQL stores timestamptz
// at microsecond resolution and would round a finer cursor up onto the tie.
//
// maxPages == 1 is the `--estimate` sample; it returns a truncation the caller
// ignores because a sample is not a read failure.
func walkTransferComments(ctx context.Context, src TransferSourceClient, issueID string, maxPages int) ([]map[string]any, *TransferListTruncation, error) {
	var all []map[string]any
	seen := map[string]bool{}
	since := transferCommentEpoch
	for page := 0; maxPages <= 0 || page < maxPages; page++ {
		rows, err := getTransferCommentPage(ctx, src, issueID, since)
		if err != nil {
			return all, nil, err
		}
		if len(rows) == 0 {
			return all, nil, nil
		}
		lastCreated := ""
		for _, raw := range rows {
			if c := strField(raw, "created_at"); c != "" {
				lastCreated = c
			}
			id := strField(raw, "id")
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			all = append(all, raw)
		}
		if maxPages == 1 {
			return all, &TransferListTruncation{Reason: gapReasonCommentWindowSampled, Limit: transferCommentPageSize}, nil
		}
		if len(rows) < transferCommentPageSize {
			return all, nil, nil
		}
		next, ok := transferPreviousMicrosecond(lastCreated)
		if !ok || next == since {
			// A full page that cannot move the cursor means the server has more
			// rows at this instant than one page can carry. The rows read so
			// far still ship, but the gap must be named.
			return all, &TransferListTruncation{Reason: gapReasonCommentWindowTruncated, Limit: transferCommentPageSize}, nil
		}
		since = next
	}
	return all, nil, nil
}

// transferPreviousMicrosecond backs a page's last created_at off by one
// microsecond so the next `created_at > cursor` read still sees rows tied on
// that timestamp.
func transferPreviousMicrosecond(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", false
	}
	return t.Add(-time.Microsecond).UTC().Format(time.RFC3339Nano), true
}

// exportIssueComments turns one issue's comment stream into bundle rows, the
// comment-held attachments and reactions, and the secret records for both.
func exportIssueComments(ctx context.Context, src TransferSourceClient, issueID string, exportAttachmentBodies bool) (
	[]TransferCommentRow, []SecretOmitted, []TransferAttachmentRow, []TransferRelationRow, map[string][]byte, *TransferListTruncation, error,
) {
	raws, trunc, err := walkTransferComments(ctx, src, issueID, 0)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	var rows []TransferCommentRow
	var secrets []SecretOmitted
	var atts []TransferAttachmentRow
	var relations []TransferRelationRow
	blobs := map[string][]byte{}
	for _, raw := range raws {
		id := strField(raw, "id")
		if id == "" {
			continue
		}
		content := strField(raw, "content")
		if scanned := ScanTransferContent(content); scanned.Changed {
			content = scanned.Text
			secrets = append(secrets, secretOmittedFromHits("comment", id, "content", scanned.Hits))
		}
		row := TransferCommentRow{
			SourceID:       id,
			IssueID:        issueID,
			AuthorType:     strField(raw, "author_type"),
			AuthorID:       strField(raw, "author_id"),
			Content:        content,
			Type:           strField(raw, "type"),
			CreatedAt:      strField(raw, "created_at"),
			UpdatedAt:      strField(raw, "updated_at"),
			ParentID:       strPtrField(raw, "parent_id"),
			ResolvedAt:     strPtrField(raw, "resolved_at"),
			ResolvedByType: strPtrField(raw, "resolved_by_type"),
			ResolvedByID:   strPtrField(raw, "resolved_by_id"),
			// Tombstones ship: their only job is to keep their replies on a
			// direct parent pointer instead of flattening the thread (§1.5).
			DeletedAt: strPtrField(raw, "deleted_at"),
		}
		if attsRaw, ok := raw["attachments"].([]any); ok {
			for _, a := range attsRaw {
				m, _ := a.(map[string]any)
				aid := strField(m, "id")
				row.AttachmentIDs = append(row.AttachmentIDs, aid)
				if !exportAttachmentBodies {
					continue
				}
				att, blob, sec := exportOneIssueAttachment(ctx, src, issueID, id, m)
				atts = append(atts, att)
				secrets = append(secrets, sec...)
				if blob != nil && att.SHA256 != "" {
					blobs[att.SHA256] = blob
				}
			}
		}
		if reactions, ok := raw["reactions"].([]any); ok {
			for _, r := range reactions {
				m, _ := r.(map[string]any)
				relations = append(relations, TransferRelationRow{
					Kind:      TransferRelationCommentReaction,
					CommentID: id,
					ActorType: strField(m, "actor_type"),
					ActorID:   strField(m, "actor_id"),
					Emoji:     strField(m, "emoji"),
					CreatedAt: strField(m, "created_at"),
				})
			}
		}
		rows = append(rows, row)
	}
	return rows, secrets, atts, relations, blobs, trunc, nil
}

// exportIssueAttachments reads the attachments mounted directly on the issue.
// Comment attachments arrive inline on the comment rows, so they are not read
// again here (ListAttachmentsByIssue selects `issue_id = $1` only).
func exportIssueAttachments(ctx context.Context, src TransferSourceClient, issueID string, exportAttachmentBodies bool) ([]TransferAttachmentRow, []SecretOmitted, map[string][]byte, *TransferListTruncation, error) {
	var raws []map[string]any
	trunc, err := getList(ctx, src, "/api/issues/"+url.PathEscape(issueID)+"/attachments", &raws)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if !exportAttachmentBodies {
		return nil, nil, nil, trunc, nil
	}
	var atts []TransferAttachmentRow
	var secrets []SecretOmitted
	blobs := map[string][]byte{}
	for _, raw := range raws {
		att, blob, sec := exportOneIssueAttachment(ctx, src, issueID, "", raw)
		atts = append(atts, att)
		secrets = append(secrets, sec...)
		if blob != nil && att.SHA256 != "" {
			blobs[att.SHA256] = blob
		}
	}
	return atts, secrets, blobs, trunc, nil
}

// exportIssueLabels reads one issue's labels. The label definition itself is
// carried by the V1 config group; the relation is keyed by the V1 identity
// `(resource_type, lower(name))` so the import can reconnect it.
func exportIssueLabels(ctx context.Context, src TransferSourceClient, issueID string) ([]TransferRelationRow, *TransferListTruncation, error) {
	var raws []map[string]any
	trunc, err := getList(ctx, src, "/api/issues/"+url.PathEscape(issueID)+"/labels", &raws)
	if err != nil {
		return nil, nil, err
	}
	var rows []TransferRelationRow
	for _, raw := range raws {
		name := strField(raw, "name")
		if name == "" {
			continue
		}
		rows = append(rows, TransferRelationRow{
			Kind:              TransferRelationIssueLabel,
			IssueID:           issueID,
			LabelResourceType: strField(raw, "resource_type"),
			LabelName:         name,
		})
	}
	return rows, trunc, nil
}

// exportIssueReactions reads the issue's own reactions from the issue detail
// response. It is the only read interface that serves them: the list response
// never fills `reactions`, and the reactions route registers POST/DELETE only.
func exportIssueReactions(ctx context.Context, src TransferSourceClient, issueID string) ([]TransferRelationRow, *TransferListTruncation, error) {
	var detail struct {
		Reactions []struct {
			ActorType string `json:"actor_type"`
			ActorID   string `json:"actor_id"`
			Emoji     string `json:"emoji"`
			CreatedAt string `json:"created_at"`
		} `json:"reactions"`
	}
	if err := src.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueID), &detail); err != nil {
		return nil, nil, err
	}
	var rows []TransferRelationRow
	for _, r := range detail.Reactions {
		rows = append(rows, TransferRelationRow{
			Kind:      TransferRelationIssueReaction,
			IssueID:   issueID,
			ActorType: r.ActorType,
			ActorID:   r.ActorID,
			Emoji:     r.Emoji,
			CreatedAt: r.CreatedAt,
		})
	}
	return rows, nil, nil
}

// exportOneIssueAttachment mirrors the chat attachment path (V2 §3.4): the
// filename is scanned, the body is exported only for images and text-like
// files, text bodies that hit a secret pattern lose their whole body, and every
// other content type stays metadata-only.
func exportOneIssueAttachment(ctx context.Context, src TransferSourceClient, issueID, commentID string, raw map[string]any) (TransferAttachmentRow, []byte, []SecretOmitted) {
	id := strField(raw, "id")
	origFilename := strField(raw, "filename")
	ct := strField(raw, "content_type")
	size := int64(floatField(raw, "size_bytes"))
	filename := origFilename
	var secrets []SecretOmitted
	if scannedName := ScanTransferContent(origFilename); scannedName.Changed {
		filename = scannedName.Text
		secrets = append(secrets, secretOmittedFromHits("attachment", id, "filename", scannedName.Hits))
	}
	row := TransferAttachmentRow{
		SourceID:    id,
		Filename:    filename,
		ContentType: ct,
		SizeBytes:   size,
		CreatedAt:   strField(raw, "created_at"),
	}
	if issueID != "" {
		row.IssueID = &issueID
	}
	if commentID != "" {
		row.CommentID = &commentID
	}
	exportBody, reason := shouldExportAttachmentBody(ct, origFilename, size)
	if !exportBody {
		row.BodyOmittedReason = &reason
		return row, nil, secrets
	}
	body, err := src.GetBytes(ctx, "/api/attachments/"+url.PathEscape(id)+"/download")
	if err != nil {
		r := "attachment_body_not_exported"
		row.BodyOmittedReason = &r
		return row, nil, secrets
	}
	if isTextAttachment(ct, origFilename) {
		scanned := ScanTransferContent(string(body))
		if scanned.Changed {
			r := "content_pattern"
			row.BodyOmittedReason = &r
			return row, nil, append(secrets, secretOmittedFromHits("attachment", id, "body", scanned.Hits))
		}
	}
	sum := sha256.Sum256(body)
	row.SHA256 = hex.EncodeToString(sum[:])
	row.Body = "attachments/blobs/" + row.SHA256
	return row, body, secrets
}

// transferIssueRow projects one list row onto the bundle's issue row, scanning
// the three text fields the secret table covers and running the V1 fallback key
// cleaning over the metadata and properties bags (contract §10.1 / §10.2).
func transferIssueRow(raw map[string]any, refs *TransferRefs) (TransferIssueRow, []SecretOmitted) {
	var secrets []SecretOmitted
	id := strField(raw, "id")

	title := strField(raw, "title")
	if scanned := ScanTransferContent(title); scanned.Changed {
		title = scanned.Text
		secrets = append(secrets, secretOmittedFromHits("issue", id, "title", scanned.Hits))
	}
	description := strPtrField(raw, "description")
	if description != nil {
		if scanned := ScanTransferContent(*description); scanned.Changed {
			v := scanned.Text
			description = &v
			secrets = append(secrets, secretOmittedFromHits("issue", id, "description", scanned.Hits))
		}
	}

	status := strField(raw, "status")
	category := strField(raw, "status_category")
	if category == "" {
		category = refs.IssueStatuses[status]
	}

	row := TransferIssueRow{
		SourceID:       id,
		Number:         int32(floatField(raw, "number")),
		Title:          title,
		Description:    description,
		Status:         status,
		StatusCategory: category,
		Priority:       strField(raw, "priority"),
		AssigneeType:   strPtrField(raw, "assignee_type"),
		AssigneeID:     strPtrField(raw, "assignee_id"),
		CreatorType:    strField(raw, "creator_type"),
		CreatorID:      strField(raw, "creator_id"),
		ParentIssueID:  strPtrField(raw, "parent_issue_id"),
		ProjectID:      strPtrField(raw, "project_id"),
		Position:       floatField(raw, "position"),
		Stage:          int32PtrField(raw, "stage"),
		StartDate:      strPtrField(raw, "start_date"),
		DueDate:        strPtrField(raw, "due_date"),
		CreatedAt:      strField(raw, "created_at"),
		UpdatedAt:      strField(raw, "updated_at"),
		// last_activity_at is still nullable on the wire. Falling back to
		// updated_at keeps the list ordering key populated instead of importing
		// a NULL the target would sort to the bottom.
		LastActivityAt: firstNonEmpty(strField(raw, "last_activity_at"), strField(raw, "updated_at")),
		Metadata:       scrubTransferJSONBag(rawField(raw, "metadata"), "issue", id, "metadata", &secrets),
		Properties:     scrubTransferJSONBag(rawField(raw, "properties"), "issue", id, "properties", &secrets),
	}
	return row, secrets
}

// scrubTransferJSONBag runs the V1 fallback key-name cleaning over one JSONB
// value bag. A key that names secret material nulls that whole value and is
// recorded: fragment substitution would corrupt a structured value (contract
// §10.2).
func scrubTransferJSONBag(raw json.RawMessage, entity, sourceID, field string, secrets *[]SecretOmitted) json.RawMessage {
	if len(raw) == 0 || isJSONNull(raw) {
		return raw
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return raw
	}
	scrubTransferJSONValue(tree, "", func(path string) {
		*secrets = append(*secrets, SecretOmitted{
			Entity:   entity,
			SourceID: sourceID,
			Field:    joinJSONPath(field, path),
			Reason:   secretReasonDenylist,
		})
	})
	out, err := json.Marshal(tree)
	if err != nil {
		return raw
	}
	return out
}

func scrubTransferJSONValue(v any, path string, note func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if secretKeyName(k) {
				if child != nil {
					t[k] = nil
					note(k)
				}
				continue
			}
			scrubTransferJSONValue(child, k, note)
		}
	case []any:
		for i, child := range t {
			scrubTransferJSONValue(child, fmt.Sprintf("%s[%d]", path, i), note)
		}
	}
}

// estimateTransferIssues is the §8.3 issue half of `--estimate`: one comments
// sample per issue (through the same paging code the real export uses), the
// description bytes, and the metadata of the attachments whose bodies would
// ship.
func estimateTransferIssues(ctx context.Context, src TransferSourceClient, issues []map[string]any, refs *TransferRefs) *TransferEstimate {
	est := &TransferEstimate{Issues: len(issues)}
	bodyBytes := int64(0)
	for _, raw := range issues {
		id := strField(raw, "id")
		if id == "" {
			continue
		}
		if n := floatField(raw, "number"); n != 0 {
			refs.Issues[id] = TransferIssueRef{Number: int32(n), Identifier: strField(raw, "identifier")}
		}
		rowBytes := int64(800 + len(strField(raw, "description")))
		sample, _, err := walkTransferComments(ctx, src, id, 1)
		if err != nil {
			continue
		}
		est.IssueComments += len(sample)
		for _, c := range sample {
			rowBytes += 500 + int64(len(strField(c, "content")))
			if atts, ok := c["attachments"].([]any); ok {
				bodyBytes += transferEstimateAttachmentBytes(atts)
			}
		}
		est.EstimatedBytes += rowBytes
		var attRaws []map[string]any
		if _, err := getList(ctx, src, "/api/issues/"+url.PathEscape(id)+"/attachments", &attRaws); err == nil {
			bodyBytes += transferEstimateAttachmentBytesAny(attRaws)
		}
	}
	est.EstimatedBytes += bodyBytes
	return est
}

func transferEstimateAttachmentBytes(atts []any) int64 {
	var total int64
	for _, a := range atts {
		m, _ := a.(map[string]any)
		total += transferEstimateOneAttachment(m)
	}
	return total
}

func transferEstimateAttachmentBytesAny(atts []map[string]any) int64 {
	var total int64
	for _, m := range atts {
		total += transferEstimateOneAttachment(m)
	}
	return total
}

// transferEstimateOneAttachment counts a body only when the export would ship
// it: the size ceiling and the content-type gate are the same ones the real
// walk applies, so a bundle full of binaries is not estimated as if every byte
// travelled.
func transferEstimateOneAttachment(m map[string]any) int64 {
	size := int64(floatField(m, "size_bytes"))
	if export, _ := shouldExportAttachmentBody(strField(m, "content_type"), strField(m, "filename"), size); !export {
		return 0
	}
	return size
}

// TransferIssueNumberWatermark returns a workspace's highest issue number. It
// is what `transfer import --renumber` offsets its numbers by.
//
// The contract (§2.3) names `workspace.issue_counter` as the offset, and this
// is deliberately not that value: no read interface serves `issue_counter`.
// `WorkspaceResponse` does not serialize it and the router registers no
// counter route, so the only watermark a client can observe is the largest
// `number` it can list. The two agree whenever the top issues still exist, and
// where they differ the counter is larger — which makes MAX(number) a lower
// bound. Renumbered numbers are still strictly above every number already in
// the target, and the finalize bump raises `issue_counter` to the imported
// maximum, so the offset can only collide with a number the target allocates
// later if its counter already sat above MAX(number) (deleted top issues) and
// the source bundle happens to contain that exact number. That surfaces as a
// unique-constraint error on the later create, never as a silent overwrite.
func TransferIssueNumberWatermark(ctx context.Context, src TransferSourceClient) (int32, error) {
	issues, _, err := fetchAllTransferIssues(ctx, src)
	if err != nil {
		return 0, err
	}
	var max int32
	for _, raw := range issues {
		if n := int32(floatField(raw, "number")); n > max {
			max = n
		}
	}
	return max, nil
}

// shardTransferIssues groups the rows into issue shards. The rule (contract
// §8.2) is that one issue's comments never straddle a shard, so each shard
// pairs `issues-000N.jsonl` with the `comments-000N.jsonl` holding exactly
// those issues' comments. A thread bigger than the shard ceiling is split, and
// each piece still carries its issue row, because the import is idempotent by
// deterministic id.
func shardTransferIssues(issues []TransferIssueRow, comments map[string][]TransferCommentRow) ([][]TransferIssueRow, [][]TransferCommentRow) {
	var issueShards [][]TransferIssueRow
	var commentShards [][]TransferCommentRow
	var curIssues []TransferIssueRow
	var curComments []TransferCommentRow
	curBytes := 0
	flush := func() {
		if len(curIssues) == 0 && len(curComments) == 0 {
			return
		}
		issueShards = append(issueShards, curIssues)
		commentShards = append(commentShards, curComments)
		curIssues, curComments, curBytes = nil, nil, 0
	}
	for _, issue := range issues {
		issueRaw, _ := json.Marshal(issue)
		issueBytes := len(issueRaw) + 1
		for i, chunk := range splitTransferCommentChunks(comments[issue.SourceID]) {
			// One issue row per shard: a split thread starts a fresh shard
			// instead of repeating its parent row inside the same file.
			if i > 0 {
				flush()
			}
			chunkRaw, _ := json.Marshal(chunk)
			need := issueBytes + len(chunkRaw)
			if (len(curComments)+len(chunk) > TransferMessageShardMaxRows || curBytes+need > TransferMessageShardMaxBytes) &&
				(len(curIssues) > 0 || len(curComments) > 0) {
				flush()
			}
			curIssues = append(curIssues, issue)
			curComments = append(curComments, chunk...)
			curBytes += need
		}
	}
	flush()
	return issueShards, commentShards
}

// splitTransferCommentChunks breaks one issue's comments so every chunk fits
// the shard byte ceiling. The row ceiling is deliberately not enforced here:
// a single long thread is allowed to break it as long as the bytes fit (§8.2).
func splitTransferCommentChunks(comments []TransferCommentRow) [][]TransferCommentRow {
	if len(comments) == 0 {
		return [][]TransferCommentRow{{}}
	}
	var chunks [][]TransferCommentRow
	start := 0
	size := 0
	for i, c := range comments {
		raw, _ := json.Marshal(c)
		need := len(raw) + 1
		if i > start && size+need > TransferMessageShardMaxBytes {
			chunks = append(chunks, comments[start:i])
			start, size = i, 0
		}
		size += need
	}
	chunks = append(chunks, comments[start:])
	return chunks
}

// int32PtrField reads a nullable numeric column. An absent or null key is nil
// (issue.stage is nullable); a present zero is a real value.
func int32PtrField(m map[string]any, k string) *int32 {
	if m == nil {
		return nil
	}
	v, ok := m[k]
	if !ok || v == nil {
		return nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	out := int32(f)
	return &out
}

// ---------------------------------------------------------------------------
// Checkpointing (§8.5)
// ---------------------------------------------------------------------------

// loadPartialIssues reads the checkpointed issue rows selected by `completed`.
// Every dependency of those rows that lives in a separate file — comments,
// attachments, relations, blobs — is restored alongside them, so a resumed
// bundle is byte-for-byte the bundle a fresh walk would have produced.
func loadPartialIssues(dir string, completed []string) ([]TransferIssueRow, map[string][]TransferCommentRow, []TransferAttachmentRow, []TransferRelationRow, map[string][]byte) {
	want := map[string]bool{}
	for _, id := range completed {
		if id != "" {
			want[id] = true
		}
	}
	issueRows := []TransferIssueRow{}
	commentsByIssue := map[string][]TransferCommentRow{}
	commentToIssue := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(dir, "issues", "issues-*.jsonl"))
	sort.Strings(matches)
	for _, ip := range matches {
		rows, err := decodeTransferJSONL[TransferIssueRow](ip)
		if err != nil {
			continue
		}
		commentsPath := filepath.Join(filepath.Dir(ip), strings.Replace(filepath.Base(ip), "issues-", "comments-", 1))
		comments, _ := decodeTransferJSONL[TransferCommentRow](commentsPath)
		byIssue := map[string][]TransferCommentRow{}
		for _, c := range comments {
			byIssue[c.IssueID] = append(byIssue[c.IssueID], c)
			commentToIssue[c.SourceID] = c.IssueID
		}
		for _, row := range rows {
			if !want[row.SourceID] {
				continue
			}
			issueRows = append(issueRows, row)
			commentsByIssue[row.SourceID] = byIssue[row.SourceID]
		}
	}
	var atts []TransferAttachmentRow
	if all, err := decodeTransferJSONL[TransferAttachmentRow](filepath.Join(dir, "attachments", "index.jsonl")); err == nil {
		for _, a := range all {
			if a.IssueID != nil && want[*a.IssueID] {
				atts = append(atts, a)
				continue
			}
			if a.CommentID != nil && want[commentToIssue[*a.CommentID]] {
				atts = append(atts, a)
			}
		}
	}
	var relations []TransferRelationRow
	if all, err := decodeTransferJSONL[TransferRelationRow](filepath.Join(dir, "issues", "relations.jsonl")); err == nil {
		for _, r := range all {
			if r.CommentID != "" {
				if want[commentToIssue[r.CommentID]] {
					relations = append(relations, r)
				}
				continue
			}
			if want[r.IssueID] {
				relations = append(relations, r)
			}
		}
	}
	blobs := map[string][]byte{}
	entries, _ := os.ReadDir(filepath.Join(dir, "attachments", "blobs"))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, "attachments", "blobs", e.Name())); err == nil {
			blobs[e.Name()] = b
		}
	}
	return issueRows, commentsByIssue, atts, relations, blobs
}

// persistCompletedIssue checkpoints one finished issue: its row, its comments,
// its attachments, its relations and the comment watermark the state records.
func persistCompletedIssue(dir string, state *transferPartialState, issue TransferIssueRow, comments []TransferCommentRow, atts []TransferAttachmentRow, relations []TransferRelationRow, blobs map[string][]byte, watermark TransferIssueWatermark) error {
	if dir == "" || state == nil {
		return nil
	}
	issuesDir := filepath.Join(dir, "issues")
	if err := os.MkdirAll(issuesDir, 0o700); err != nil {
		return err
	}
	n := nextPartialIssueIndex(dir)
	if err := os.WriteFile(filepath.Join(issuesDir, fmt.Sprintf("issues-%04d.jsonl", n)), encodeTransferJSONL([]TransferIssueRow{issue}), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(issuesDir, fmt.Sprintf("comments-%04d.jsonl", n)), encodeTransferJSONL(comments), 0o600); err != nil {
		return err
	}
	if len(relations) > 0 {
		f, err := os.OpenFile(filepath.Join(issuesDir, "relations.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(encodeTransferJSONL(relations)); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	if err := persistPartialAttachments(dir, atts, blobs); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range state.CompletedIssueIDs {
		seen[id] = true
	}
	if !seen[issue.SourceID] {
		state.CompletedIssueIDs = append(state.CompletedIssueIDs, issue.SourceID)
	}
	if state.IssueCommentWatermark == nil {
		state.IssueCommentWatermark = map[string]TransferIssueWatermark{}
	}
	state.IssueCommentWatermark[issue.SourceID] = watermark
	for sha := range blobs {
		if sha == "" {
			continue
		}
		found := false
		for _, existing := range state.DownloadedBlobSHA256 {
			if existing == sha {
				found = true
				break
			}
		}
		if !found {
			state.DownloadedBlobSHA256 = append(state.DownloadedBlobSHA256, sha)
		}
	}
	return writeTransferPartialState(dir, state)
}

func nextPartialIssueIndex(dir string) int {
	matches, _ := filepath.Glob(filepath.Join(dir, "issues", "issues-*.jsonl"))
	return len(matches) + 1
}
