# Board parent groups: one parent's sub-issues together, one color each (DENE-2760)

On the board, the sub-issues of one parent sit together in every column under
a colored header, and a tab row above the board narrows it to one parent.
Display only: no issue, status, position or permission changes.

## How to turn it on

- Desktop: a build with `office_parent_groups` in `VITE_MULTICA_FEATURE_FLAGS`.
  Web: `office_parent_groups` in `NEXT_PUBLIC_MULTICA_FEATURE_FLAGS`.
- Builds without the flag show the board unchanged.
- Server, daemon and CLI need nothing new: grouping reads the existing
  `parent_issue_id` of the issues the board already loaded.

## What the flag changes

- Column order: each parent's sub-issues move up to the slot of that parent's
  first sub-issue in the column; cards without a parent keep their order and
  place. The board applies this to its drag order too, so a dropped card's new
  position is computed from the neighbours on screen.
- Group header, before each run: a tinted chip with the parent's color, number,
  title without a leading `Project: ` tag, and the count in this column
  (「2734 看板防卡死 · 3 张」). Clicking it folds or unfolds that parent in
  that column only; the fold is remembered per board in the view store
  (`boardCollapsedParentGroups`, keys `<column>|<parent>`). The arrow next to
  it opens the parent. A parent the board did not load is fetched once by id;
  if that fails the chip reads 「父任务」.
- Cards in a group get a 3px left bar in the parent's color and drop their own
  parent chip, since the header names the parent. Cards without a parent look
  as before.
- Colors: 8 validated categorical colors with light and dark variants
  (`packages/views/issues/utils/parent-groups.ts`). A parent's slot comes from
  a hash of its id; when two parents on the board hash to one slot the newer
  one takes the next free slot, so up to 8 parents always differ and an
  appearing parent never repaints the others. Past 8, colors repeat.
- Tab row: 「全部」 plus one tab per parent on the board, newest first, each
  with its total across columns. A tab narrows every column, and the column
  counts, to that parent; pressing it again or 「全部」 returns. The choice is
  not remembered across reloads. No tab row when no card has a parent.
- Paged columns: the board loads long columns a page at a time, and the tabs
  only see loaded cards. So the tab row appears only once every column has its
  last page, no column is still fetching, and no more columns are waiting;
  until then it stays hidden and a tab picked earlier is ignored, rather than
  listing some parents and showing short counts. Once the board has settled
  like this, a background refresh no longer hides the row or drops the picked
  tab (DENE-2762); the row still steps aside if a column gains more pages, is
  loading from empty, or fails. Grouping, colors and header counts still apply
  to the loaded cards, and a header grows as the column loads more.

## Not in this change

- List view, the sub-issue accordion and swimlanes are unchanged.
- Mobile: no native screen; mobile web at 390px uses the same board, and the
  tab row scrolls sideways.

## Rollback

Remove `office_parent_groups` from the build flags, or revert the merge commit.
No migration, no new route; the only stored data is the fold list in the
browser's view-store snapshot, which older builds ignore.
