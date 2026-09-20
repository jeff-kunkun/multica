package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Pinned-first (DENE-500) is a caller-scoped keyset dimension, not a head
// block spliced in front of the rows. These tests pin the two properties that
// choice is for: the row set is exactly the unpinned row set in a different
// order — so counts cannot drift — and walking the cursor chain returns every
// matching issue exactly once.

// pinSeed is one seeded project plus its issue ids in natural (position ASC)
// order, so an assertion can name "the 55th issue" without re-deriving it.
type pinSeed struct {
	projectID string
	ids       []string
}

// seedPinProject creates a project of `count` issues with distinct positions,
// titles, priorities, last-activity timestamps and start dates, so every sort
// field under test has a total order.
func seedPinProject(t *testing.T, count int) pinSeed {
	t.Helper()
	projectID := dbfx.Project(t, "Pin table "+uuid.NewString()[:8])
	ids := make([]string, 0, count)
	base := time.Now().UTC().Add(-time.Duration(count) * time.Hour)
	for n := 1; n <= count; n++ {
		cols := testutil.Cols{
			"project_id":       projectID,
			"position":         float64(n),
			"priority":         []string{"urgent", "high", "medium", "low", "none"}[(n-1)%5],
			"last_activity_at": base.Add(time.Duration(n) * time.Minute),
		}
		// Some issues have no start or due date, so the nulls-last arm of the
		// cursor and the ORDER BY are exercised rather than assumed.
		if n%3 != 0 {
			cols["start_date"] = base.AddDate(0, 0, n)
		}
		if n%4 != 0 {
			cols["due_date"] = base.AddDate(0, 0, 40-n)
		}
		ids = append(ids, dbfx.Issue(t, fmt.Sprintf("pin seed %03d", n), cols))
	}
	return pinSeed{projectID: projectID, ids: ids}
}

func pinIssueRow(t *testing.T, userID, issueID string) {
	t.Helper()
	dbfx.Insert(t, "pinned_item", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"user_id":      userID,
		"item_type":    "issue",
		"item_id":      issueID,
		"position":     float64(1),
	})
}

func listPinRows(t *testing.T, request issueTableRowsRequest) issueTableRowsResponse {
	t.Helper()
	var response issueTableRowsResponse
	testutil.Call(t, testHandler.ListIssueTableRows, newRequest("POST", "/api/issues/table/rows", request)).
		Want(http.StatusOK).JSON(&response)
	return response
}

// walkPinCursors follows the whole cursor chain and returns the ids in the
// order the server served them, failing on any id that arrives twice.
func walkPinCursors(t *testing.T, query issueTableQuerySpec, limit int) []string {
	t.Helper()
	var order []string
	seen := map[string]struct{}{}
	cursor := (*string)(nil)
	for page := 0; ; page++ {
		if page > 400 {
			t.Fatalf("cursor did not terminate after %d pages", page)
		}
		response := listPinRows(t, issueTableRowsRequest{
			Query: query,
			Group: issueTableGroupSpec{Kind: "none"},
			Page:  issueTablePageRequest{Limit: limit, Cursor: cursor},
		})
		for _, row := range response.Rows {
			if _, duplicate := seen[row.Issue.ID]; duplicate {
				t.Fatalf("issue %s repeated across pages", row.Issue.ID)
			}
			seen[row.Issue.ID] = struct{}{}
			order = append(order, row.Issue.ID)
		}
		if response.NextCursor == nil {
			return order
		}
		cursor = response.NextCursor
	}
}

func assertSameIDSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d rows, want %d", label, len(got), len(want))
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, id := range got {
		gotSet[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := gotSet[id]; !ok {
			t.Fatalf("%s: issue %s missing from the paginated set", label, id)
		}
	}
}

// TestIssueTablePinnedFirstSurfacesLateRowOnPageOne is the headline case: the
// pinned issue is the 55th in natural order — page 2 at the default page size —
// and pinned-first has to lift it to the first row of page 1, and keep it there
// after "load more".
func TestIssueTablePinnedFirstSurfacesLateRowOnPageOne(t *testing.T) {
	seed := seedPinProject(t, 60)
	lift := seed.ids[54]
	pinIssueRow(t, testUserID, lift)

	query := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc", PinnedFirst: true},
	}
	first := listPinRows(t, issueTableRowsRequest{
		Query: query,
		Group: issueTableGroupSpec{Kind: "none"},
		Page:  issueTablePageRequest{Limit: 50},
	})
	if len(first.Rows) != 50 {
		t.Fatalf("first page rows = %d, want 50", len(first.Rows))
	}
	if first.Rows[0].Issue.ID != lift {
		t.Fatalf("first page row 0 = %s, want the pinned issue %s", first.Rows[0].Issue.ID, lift)
	}
	if !first.Rows[0].IsPinned {
		t.Fatalf("row 0 must report is_pinned=true")
	}
	for _, row := range first.Rows[1:] {
		if row.IsPinned {
			t.Fatalf("row %s reports is_pinned=true but is not pinned", row.Issue.ID)
		}
	}
	if first.Total != 60 {
		t.Fatalf("total = %d, want 60 — the pinned row must not be counted twice", first.Total)
	}
	if first.NextCursor == nil {
		t.Fatalf("first page must offer a continuation")
	}

	second := listPinRows(t, issueTableRowsRequest{
		Query: query,
		Group: issueTableGroupSpec{Kind: "none"},
		Page:  issueTablePageRequest{Limit: 50, Cursor: first.NextCursor},
	})
	for _, row := range second.Rows {
		if row.Issue.ID == lift {
			t.Fatalf("the pinned row came back on page 2 instead of staying first")
		}
	}

	order := walkPinCursors(t, query, 50)
	assertSameIDSet(t, "pinned-first page 1", order, seed.ids)
	if order[0] != lift {
		t.Fatalf("first row of the full walk = %s, want the pinned issue %s", order[0], lift)
	}
}

// TestIssueTablePinnedFirstKeysetIsCompleteAcrossSorts walks the cursor chain
// for every sort field, at three pin counts around one page of rows. Each walk
// must return exactly the 60 seeded issues, once each: a pin block that is
// re-sorted, duplicated or skipped shows up here as a missing or repeated id.
func TestIssueTablePinnedFirstKeysetIsCompleteAcrossSorts(t *testing.T) {
	seed := seedPinProject(t, 60)
	sorts := []struct {
		name string
		sort issueTableSortRequest
	}{
		{"position", issueTableSortRequest{Field: "position", Direction: "asc"}},
		{"title", issueTableSortRequest{Field: "title", Direction: "asc"}},
		{"start_date_nulls_last", issueTableSortRequest{Field: "start_date", Direction: "asc"}},
		{"due_date_nulls_last_desc", issueTableSortRequest{Field: "due_date", Direction: "desc"}},
		{"last_activity", issueTableSortRequest{Field: "last_activity", Direction: "desc"}},
		{"priority", issueTableSortRequest{Field: "priority", Direction: "asc"}},
		{"created_at", issueTableSortRequest{Field: "created_at", Direction: "desc"}},
	}
	// Fewer than a page, exactly one page, more than one page.
	for _, pinCount := range []int{3, 10, 25} {
		for _, sortCase := range sorts {
			name := fmt.Sprintf("pins=%d/%s", pinCount, sortCase.name)
			t.Run(name, func(t *testing.T) {
				// Pins are per (workspace, user) and shared across subtests, so
				// each subtest names its own pin set and clears it again.
				pinned := make([]string, 0, pinCount)
				for n := 0; n < pinCount; n++ {
					// Spread across both pages of a 10-row page size.
					issueID := seed.ids[(n*7)%len(seed.ids)]
					pinIssueRow(t, testUserID, issueID)
					pinned = append(pinned, issueID)
				}

				sortRequest := sortCase.sort
				sortRequest.PinnedFirst = true
				query := issueTableQuerySpec{
					Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
					Sort:  sortRequest,
				}
				order := walkPinCursors(t, query, 10)
				assertSameIDSet(t, name, order, seed.ids)

				// Every pinned issue leads the walk, in the active sort order
				// rather than in sidebar pin order.
				head := order[:pinCount]
				for index, id := range pinned {
					if !containsString(head, id) {
						t.Fatalf("%s: pinned issue %s not in the leading block %v", name, id, head)
					}
					_ = index
				}
			})
		}
	}
}

// TestIssueTablePinnedFirstFilteredPinIsAbsent proves the two arms share one
// membership predicate: a pinned issue the active filter excludes is simply
// not in the result set, and the remaining order is the unpinned order.
func TestIssueTablePinnedFirstFilteredPinIsAbsent(t *testing.T) {
	seed := seedPinProject(t, 12)
	excluded := seed.ids[0]
	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, excluded)
	pinIssueRow(t, testUserID, excluded)

	base := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc"},
	}
	filtered := base
	filtered.Filters = issueTableFiltersRequest{Statuses: []string{"todo"}}
	pinnedFiltered := filtered
	pinnedFiltered.Sort.PinnedFirst = true

	plainOrder := walkPinCursors(t, filtered, 5)
	pinnedOrder := walkPinCursors(t, pinnedFiltered, 5)
	if containsString(pinnedOrder, excluded) {
		t.Fatalf("a pinned issue excluded by the filter must not be returned")
	}
	if strings.Join(plainOrder, ",") != strings.Join(pinnedOrder, ",") {
		t.Fatalf("order changed with pins but no matching pinned row:\nplain=%v\npinned=%v", plainOrder, pinnedOrder)
	}
}

// TestIssueTablePinnedFirstIgnoresOtherUsersPins: pins are per-user, so a
// colleague's pin must not reorder my table.
func TestIssueTablePinnedFirstIgnoresOtherUsersPins(t *testing.T) {
	seed := seedPinProject(t, 12)
	otherUser := dbfx.User(t, "Other pinner", "other-pinner-"+uuid.NewString()[:8]+"@multica.ai")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	pinIssueRow(t, otherUser, seed.ids[9])

	base := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc"},
	}
	pinnedFirst := base
	pinnedFirst.Sort.PinnedFirst = true

	plain := walkPinCursors(t, base, 5)
	pinned := walkPinCursors(t, pinnedFirst, 5)
	if strings.Join(plain, ",") != strings.Join(pinned, ",") {
		t.Fatalf("another user's pin changed my order:\nplain=%v\npinned=%v", plain, pinned)
	}
	if pinned[0] != seed.ids[0] {
		t.Fatalf("first row = %s, want the naturally first issue %s", pinned[0], seed.ids[0])
	}
}

// TestIssueTablePinnedFirstUnpinRestoresNaturalOrder pins a late row, unpins
// it, and requires the resulting order to be identical to a query that never
// asked for pinned-first — the row must fall back to its manual position, not
// to the front.
func TestIssueTablePinnedFirstUnpinRestoresNaturalOrder(t *testing.T) {
	seed := seedPinProject(t, 12)
	lifted := seed.ids[10]
	pinIssueRow(t, testUserID, lifted)

	pinnedFirst := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc", PinnedFirst: true},
	}
	natural := pinnedFirst
	natural.Sort.PinnedFirst = false

	if before := walkPinCursors(t, pinnedFirst, 5); before[0] != lifted {
		t.Fatalf("pinned row did not lead before unpinning: %v", before)
	}
	dbfx.Exec(t, `DELETE FROM pinned_item WHERE workspace_id = $1 AND user_id = $2 AND item_type = 'issue' AND item_id = $3`,
		testWorkspaceID, testUserID, lifted)

	after := walkPinCursors(t, pinnedFirst, 5)
	want := walkPinCursors(t, natural, 5)
	if strings.Join(after, ",") != strings.Join(want, ",") {
		t.Fatalf("unpinned order differs from the never-pinned order:\nafter=%v\nwant=%v", after, want)
	}
	if after[0] != seed.ids[0] {
		t.Fatalf("first row after unpinning = %s, want %s", after[0], seed.ids[0])
	}
}

// TestIssueTablePinnedFirstCountsDoNotDrift compares total, the status facet
// and the grouped headers with and without pinned-first. The pinned block
// reorders rows only, so all three must be byte-identical.
func TestIssueTablePinnedFirstCountsDoNotDrift(t *testing.T) {
	seed := seedPinProject(t, 12)
	pinIssueRow(t, testUserID, seed.ids[7])
	pinIssueRow(t, testUserID, seed.ids[2])

	base := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc"},
	}
	pinned := base
	pinned.Sort.PinnedFirst = true

	plainRows := listPinRows(t, issueTableRowsRequest{
		Query: base, Group: issueTableGroupSpec{Kind: "none"},
		Page: issueTablePageRequest{Limit: 50},
	})
	pinnedRows := listPinRows(t, issueTableRowsRequest{
		Query: pinned, Group: issueTableGroupSpec{Kind: "none"},
		Page: issueTablePageRequest{Limit: 50},
	})
	if plainRows.Total != pinnedRows.Total {
		t.Fatalf("total drifted: plain=%d pinned=%d", plainRows.Total, pinnedRows.Total)
	}

	facetRequest := func(query issueTableQuerySpec) issueTableFacetsRequest {
		return issueTableFacetsRequest{
			Query:        query,
			Facets:       []issueTableFacetSpec{{Kind: "status"}},
			IncludeTotal: boolPtr(true),
		}
	}
	var plainFacets, pinnedFacets issueTableFacetsResponse
	testutil.Call(t, testHandler.ListIssueTableFacets, newRequest("POST", "/api/issues/table/facets", facetRequest(base))).
		Want(http.StatusOK).JSON(&plainFacets)
	testutil.Call(t, testHandler.ListIssueTableFacets, newRequest("POST", "/api/issues/table/facets", facetRequest(pinned))).
		Want(http.StatusOK).JSON(&pinnedFacets)
	if plainFacets.Total != pinnedFacets.Total {
		t.Fatalf("facet total drifted: plain=%d pinned=%d", plainFacets.Total, pinnedFacets.Total)
	}
	if encodeJSON(t, plainFacets.Facets) != encodeJSON(t, pinnedFacets.Facets) {
		t.Fatalf("status facet drifted:\nplain=%s\npinned=%s",
			encodeJSON(t, plainFacets.Facets), encodeJSON(t, pinnedFacets.Facets))
	}

	groupRequest := func(query issueTableQuerySpec) issueTableGroupsRequest {
		return issueTableGroupsRequest{
			Query: query,
			Group: issueTableGroupSpec{Kind: "status"},
			Page:  issueTablePageRequest{Limit: 50},
		}
	}
	var plainGroups, pinnedGroups issueTableGroupsResponse
	testutil.Call(t, testHandler.ListIssueTableGroups, newRequest("POST", "/api/issues/table/groups", groupRequest(base))).
		Want(http.StatusOK).JSON(&plainGroups)
	testutil.Call(t, testHandler.ListIssueTableGroups, newRequest("POST", "/api/issues/table/groups", groupRequest(pinned))).
		Want(http.StatusOK).JSON(&pinnedGroups)
	if encodeJSON(t, plainGroups.Groups) != encodeJSON(t, pinnedGroups.Groups) {
		t.Fatalf("group headers drifted:\nplain=%s\npinned=%s",
			encodeJSON(t, plainGroups.Groups), encodeJSON(t, pinnedGroups.Groups))
	}
}

func boolPtr(value bool) *bool { return &value }

func encodeJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(encoded)
}

// TestIssueTablePinnedFirstStaysInsideItsBranch pins a row in the group it
// actually renders in. The pinned row must lead its own status branch and must
// not be lifted into another branch: group headers and the rows under them have
// to keep agreeing.
func TestIssueTablePinnedFirstStaysInsideItsBranch(t *testing.T) {
	seed := seedPinProject(t, 6)
	late := seed.ids[5]
	dbfx.Exec(t, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, late)
	pinIssueRow(t, testUserID, late)

	query := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc", PinnedFirst: true},
	}
	page := listPinRows(t, issueTableRowsRequest{
		Query:    query,
		Group:    issueTableGroupSpec{Kind: "status"},
		GroupKey: strPtr("status:in_progress"),
		Page:     issueTablePageRequest{Limit: 50},
	})
	if len(page.Rows) == 0 || page.Rows[0].Issue.ID != late {
		t.Fatalf("pinned row must lead its own status branch, got %v", rowIDs(page.Rows))
	}
	for _, row := range page.Rows {
		if row.Issue.Status != "in_progress" {
			t.Fatalf("branch returned a row from another status: %s=%s", row.Issue.ID, row.Issue.Status)
		}
	}

	todoPage := listPinRows(t, issueTableRowsRequest{
		Query:    query,
		Group:    issueTableGroupSpec{Kind: "status"},
		GroupKey: strPtr("status:todo"),
		Page:     issueTablePageRequest{Limit: 50},
	})
	if len(todoPage.Rows) != 5 {
		t.Fatalf("todo branch rows = %d, want 5", len(todoPage.Rows))
	}
	for _, row := range todoPage.Rows {
		if row.Issue.ID == late {
			t.Fatalf("pinned row was lifted into a branch it does not belong to")
		}
	}
}

func rowIDs(rows []issueTableRowResponse) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Issue.ID)
	}
	return ids
}

// TestIssueTablePinnedFirstHierarchyChildBranch: a pinned sub-issue leads its
// parent's child list and is never pulled up to the parent's own level.
func TestIssueTablePinnedFirstHierarchyChildBranch(t *testing.T) {
	seed := seedPinProject(t, 1)
	parent := seed.ids[0]
	children := []string{
		dbfx.Issue(t, "pin hierarchy child a", testutil.Cols{"project_id": seed.projectID, "parent_issue_id": parent, "position": float64(2)}),
		dbfx.Issue(t, "pin hierarchy child b", testutil.Cols{"project_id": seed.projectID, "parent_issue_id": parent, "position": float64(3)}),
		dbfx.Issue(t, "pin hierarchy child c", testutil.Cols{"project_id": seed.projectID, "parent_issue_id": parent, "position": float64(4)}),
	}
	pinIssueRow(t, testUserID, children[2])

	query := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc", PinnedFirst: true},
	}
	childrenPage := listPinRows(t, issueTableRowsRequest{
		Query:     query,
		Group:     issueTableGroupSpec{Kind: "none"},
		Hierarchy: issueTableHierarchyRequest{Enabled: true},
		ParentID:  &parent,
		Page:      issueTablePageRequest{Limit: 50},
	})
	if len(childrenPage.Rows) != 3 || childrenPage.Rows[0].Issue.ID != children[2] {
		t.Fatalf("pinned child must lead its parent's child list, got %v", rowIDs(childrenPage.Rows))
	}
	if !childrenPage.Rows[0].IsPinned {
		t.Fatalf("pinned child must report is_pinned=true")
	}

	rootsPage := listPinRows(t, issueTableRowsRequest{
		Query:     query,
		Group:     issueTableGroupSpec{Kind: "none"},
		Hierarchy: issueTableHierarchyRequest{Enabled: true},
		Page:      issueTablePageRequest{Limit: 50},
	})
	for _, row := range rootsPage.Rows {
		if row.Issue.ID == children[2] {
			t.Fatalf("pinned child was lifted out of its parent's list")
		}
	}
}

// TestIssueTablePinnedFirstCursorRejectsChangedPinSet: the pinned set is part
// of the cursor position. Pinning mid-scroll has to be refused, not answered
// with a shifted window that repeats or drops rows.
func TestIssueTablePinnedFirstCursorRejectsChangedPinSet(t *testing.T) {
	seed := seedPinProject(t, 12)
	pinIssueRow(t, testUserID, seed.ids[1])

	query := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc", PinnedFirst: true},
	}
	first := listPinRows(t, issueTableRowsRequest{
		Query: query, Group: issueTableGroupSpec{Kind: "none"},
		Page: issueTablePageRequest{Limit: 3},
	})
	if first.NextCursor == nil {
		t.Fatalf("expected a continuation cursor")
	}
	pinIssueRow(t, testUserID, seed.ids[9])

	var conflict map[string]any
	testutil.Call(t, testHandler.ListIssueTableRows, newRequest("POST", "/api/issues/table/rows", issueTableRowsRequest{
		Query: query, Group: issueTableGroupSpec{Kind: "none"},
		Page: issueTablePageRequest{Limit: 3, Cursor: first.NextCursor},
	})).Want(http.StatusConflict).JSON(&conflict)
	if conflict["error"] != "cursor_query_mismatch" {
		t.Fatalf("conflict body = %v, want cursor_query_mismatch", conflict)
	}
}

// TestIssueTablePinnedFirstSkippedForMachineCredential: pins are a per-human
// preference. A task-token caller gets the pre-feature query — natural order
// and no row badge — even if it asks for pinned-first.
func TestIssueTablePinnedFirstSkippedForMachineCredential(t *testing.T) {
	seed := seedPinProject(t, 6)
	pinIssueRow(t, testUserID, seed.ids[5])

	query := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc", PinnedFirst: true},
	}
	request := newRequest("POST", "/api/issues/table/rows", issueTableRowsRequest{
		Query: query, Group: issueTableGroupSpec{Kind: "none"},
		Page: issueTablePageRequest{Limit: 50},
	})
	request.Header.Set("X-Actor-Source", "task_token")

	var response issueTableRowsResponse
	testutil.Call(t, testHandler.ListIssueTableRows, request).Want(http.StatusOK).JSON(&response)
	if response.Rows[0].Issue.ID != seed.ids[0] {
		t.Fatalf("machine credential got pinned-first order: %v", rowIDs(response.Rows))
	}
	for _, row := range response.Rows {
		if row.IsPinned {
			t.Fatalf("machine credential response must not carry a pin badge")
		}
	}
}

// TestIssueTablePinnedFirstQueryShape pins the shape the plan is built on: the
// page is a two-arm UNION ALL that materializes pin_rank per arm, and the OUTER
// FROM page re-sorts by (pin_rank, sort key). Dropping the outer pin_rank — the
// plan's most silent failure mode — makes this fail.
func TestIssueTablePinnedFirstQueryShape(t *testing.T) {
	seed := seedPinProject(t, 3)
	pinIssueRow(t, testUserID, seed.ids[2])

	labelCalls := 0
	rowQuerySQL := ""
	handler := *testHandler
	handler.TxStarter = issueTableEnrichmentFailTxStarter{
		inner:       testHandler.TxStarter,
		labelCalls:  &labelCalls,
		rowQuerySQL: &rowQuerySQL,
	}

	query := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "project", ProjectID: seed.projectID},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc", PinnedFirst: true},
	}
	handler.ListIssueTableRows(httptest.NewRecorder(), newRequest("POST", "/api/issues/table/rows", issueTableRowsRequest{
		Query: query, Group: issueTableGroupSpec{Kind: "none"},
		Page: issueTablePageRequest{Limit: 50},
	}))

	if !strings.Contains(rowQuerySQL, "0 AS pin_rank") ||
		!strings.Contains(rowQuerySQL, "1 AS pin_rank") ||
		!strings.Contains(rowQuerySQL, "UNION ALL") {
		t.Fatalf("pinned page must be a two-arm UNION ALL materializing pin_rank per arm:\n%s", rowQuerySQL)
	}
	if !strings.Contains(rowQuerySQL, "NOT EXISTS (SELECT 1 FROM pinned_item") {
		t.Fatalf("plain arm must exclude pinned rows:\n%s", rowQuerySQL)
	}
	if !strings.Contains(rowQuerySQL, "ORDER BY pin_rank,") &&
		!strings.Contains(rowQuerySQL, "ORDER BY i.pin_rank,") {
		t.Fatalf("outer ORDER BY must rank pin_rank first:\n%s", rowQuerySQL)
	}
	if !strings.Contains(rowQuerySQL, "i.pin_rank") {
		t.Fatalf("outer select must expose the materialized pin_rank:\n%s", rowQuerySQL)
	}
}

// TestIssueTablePinnedFirstFingerprintIsUnchangedWhenOff is the rollout
// guarantee: a request that does not ask for pinned-first hashes exactly as it
// did before this feature, so cursors minted by an older server keep working.
func TestIssueTablePinnedFirstFingerprintIsUnchangedWhenOff(t *testing.T) {
	spec := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "workspace"},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc"},
	}
	off, err := canonicalIssueTableFingerprint(testWorkspaceID, spec)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	spec.Sort.PinnedFirst = true
	on, err := canonicalIssueTableFingerprint(testWorkspaceID, spec)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if off == on {
		t.Fatalf("pinned_first must be part of the query fingerprint")
	}
	// A request that never asks for pinned-first must serialize to the exact
	// pre-feature bytes, which is what keeps old cursors valid.
	spec.Sort.PinnedFirst = false
	encoded, err := json.Marshal(spec.Sort)
	if err != nil {
		t.Fatalf("marshal sort: %v", err)
	}
	if string(encoded) != `{"field":"position","direction":"asc"}` {
		t.Fatalf("sort spec wire shape changed: %s", encoded)
	}
	spec.Sort.PinnedFirst = true
	encoded, err = json.Marshal(spec.Sort)
	if err != nil {
		t.Fatalf("marshal sort: %v", err)
	}
	if !strings.Contains(string(encoded), `"pinned_first":true`) {
		t.Fatalf("sort spec must carry pinned_first when asked for: %s", encoded)
	}
}
