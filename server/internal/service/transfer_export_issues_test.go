package service

import (
	"fmt"
	"strings"
	"testing"
)

// The shard rule (§8.2) is that one issue's comments never straddle a shard and
// `issues-000N.jsonl` holds exactly the issues whose comments are in
// `comments-000N.jsonl`. The import pairs the two files by index, so breaking
// the pairing silently attaches comments to the wrong shard.
func TestShardTransferIssues_PairsShardsAndNeverSplitsAThread(t *testing.T) {
	issues := []TransferIssueRow{
		{SourceID: "issue-1", Number: 1},
		{SourceID: "issue-2", Number: 2},
	}
	comments := map[string][]TransferCommentRow{
		"issue-1": transferCommentRowsForTest("issue-1", 3000),
		"issue-2": transferCommentRowsForTest("issue-2", 3000),
	}
	issueShards, commentShards := shardTransferIssues(issues, comments)

	if len(issueShards) != len(commentShards) {
		t.Fatalf("%d issue shards but %d comment shards", len(issueShards), len(commentShards))
	}
	if len(issueShards) < 2 {
		t.Fatalf("6000 comment rows produced %d shard(s); the 5000-row ceiling must split them", len(issueShards))
	}
	for i := range issueShards {
		// Every comment in this shard must belong to an issue the same shard
		// declares.
		inShard := map[string]bool{}
		for _, issue := range issueShards[i] {
			inShard[issue.SourceID] = true
		}
		counts := map[string]int{}
		for _, c := range commentShards[i] {
			if !inShard[c.IssueID] {
				t.Fatalf("shard %d holds a comment of issue %s but does not declare it", i+1, c.IssueID)
			}
			counts[c.IssueID]++
		}
		for id, n := range counts {
			if n != len(comments[id]) {
				t.Fatalf("shard %d holds %d of issue %s's %d comments: a thread was split across shards", i+1, n, id, len(comments[id]))
			}
		}
	}
}

// A thread bigger than the shard byte ceiling splits, and every piece still
// carries its issue row so the import can write the comment rows.
func TestShardTransferIssues_OversizedThreadSplitsWithItsIssueRow(t *testing.T) {
	big := strings.Repeat("x", 6<<20)
	issues := []TransferIssueRow{{SourceID: "issue-1", Number: 1}}
	comments := map[string][]TransferCommentRow{
		"issue-1": {
			{SourceID: "c-1", IssueID: "issue-1", Content: big},
			{SourceID: "c-2", IssueID: "issue-1", Content: big},
			{SourceID: "c-3", IssueID: "issue-1", Content: big},
		},
	}
	issueShards, commentShards := shardTransferIssues(issues, comments)
	if len(issueShards) < 2 {
		t.Fatalf("an 18 MiB thread produced %d shard(s); the 16 MiB ceiling must split it", len(issueShards))
	}
	total := 0
	for i := range issueShards {
		if len(issueShards[i]) != 1 || issueShards[i][0].SourceID != "issue-1" {
			t.Fatalf("shard %d does not carry the thread's issue row: %v", i+1, issueShards[i])
		}
		total += len(commentShards[i])
	}
	if total != 3 {
		t.Fatalf("split lost comments: %d of 3", total)
	}
}

// An issue with no comments still has to reach a shard; otherwise it never
// arrives at all.
func TestShardTransferIssues_KeepsCommentlessIssues(t *testing.T) {
	issueShards, commentShards := shardTransferIssues(
		[]TransferIssueRow{{SourceID: "issue-1", Number: 1}}, map[string][]TransferCommentRow{})
	if len(issueShards) != 1 || len(issueShards[0]) != 1 {
		t.Fatalf("issue shards=%v", issueShards)
	}
	if len(commentShards) != 1 || len(commentShards[0]) != 0 {
		t.Fatalf("comment shards=%v", commentShards)
	}
}

// The outer version follows the bundle's content, not the CLI's version: a
// bundle without the issues group stays readable by an un-upgraded target.
func TestTransferBundleSchemaVersionForContent(t *testing.T) {
	for _, tc := range []struct {
		include []string
		want    int
	}{
		{nil, TransferBundleSchemaVersionV1},
		{[]string{"config", "conversations", "attachments"}, TransferBundleSchemaVersionV1},
		{[]string{"config", "issues"}, TransferBundleSchemaVersionV2},
	} {
		if got := TransferBundleSchemaVersionForContent(tc.include); got != tc.want {
			t.Errorf("include %v -> schema_version %d, want %d", tc.include, got, tc.want)
		}
	}
}

// The `since` cursor has to back off past the page's last timestamp: the server
// predicate is a strict `created_at > cursor`, so reusing the timestamp itself
// skips every comment tied with the row that fell past the boundary.
func TestTransferPreviousMicrosecond_BacksOffOneMicrosecond(t *testing.T) {
	got, ok := transferPreviousMicrosecond("2026-09-16T10:33:18.000004Z")
	if !ok || got != "2026-09-16T10:33:18.000003Z" {
		t.Fatalf("cursor=%q ok=%v, want the timestamp one microsecond earlier", got, ok)
	}
	if _, ok := transferPreviousMicrosecond(""); ok {
		t.Fatal("an empty timestamp must not produce a cursor")
	}
	if _, ok := transferPreviousMicrosecond("not-a-time"); ok {
		t.Fatal("a malformed timestamp must not produce a cursor")
	}
}

func transferCommentRowsForTest(issueID string, n int) []TransferCommentRow {
	rows := make([]TransferCommentRow, n)
	for i := range rows {
		rows[i] = TransferCommentRow{
			SourceID:  fmt.Sprintf("%s-c-%04d", issueID, i),
			IssueID:   issueID,
			Content:   "hello",
			CreatedAt: "2026-09-01T00:00:00Z",
		}
	}
	return rows
}
