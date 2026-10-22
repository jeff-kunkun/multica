// Server projections the one-row-per-issue inbox reads (DENE-882).

/**
 * GET /api/inbox/unread-issues — one row per issue the current user has
 * unread inbox rows on (DENE-901). `held_count` of them hang on an open call
 * and stay unread until the call is answered or closed.
 */
export interface UnreadInboxIssue {
  issue_id: string;
  identifier: string;
  title: string;
  status: string;
  parent_issue_id: string | null;
  unread_count: number;
  held_count: number;
  latest_at: string;
}

export interface InboxBoardOwnerPayload {
  type: string;
  id: string;
}

/** One row of GET /api/inbox/board (DENE-975); `children` nest the same shape. */
export interface InboxBoardRowPayload {
  issue_id: string;
  identifier: string;
  title: string;
  /** Current lifecycle key, used to make board status actions reversible. */
  status?: string;
  parent_issue_id: string | null;
  lane: "running" | "blocked" | "todo" | "fresh" | "done";
  kind: string;
  from: InboxBoardOwnerPayload | null;
  from_name: string;
  next: InboxBoardOwnerPayload | null;
  next_name: string;
  at: string;
  unread: number;
  children: InboxBoardRowPayload[] | null;
}

/**
 * GET /api/inbox/board — the inbox lanes built server-side (DENE-975).
 * `as_of` is the server clock at the read; pass it back as `unread_since` to
 * replay this visit's unread markers after marking everything read.
 */
export interface InboxBoardResponse {
  running: InboxBoardRowPayload[];
  /** Absent from servers older than DENE-1409. */
  blocked?: InboxBoardRowPayload[];
  todo: InboxBoardRowPayload[];
  fresh: InboxBoardRowPayload[];
  done: InboxBoardRowPayload[];
  viewer_id: string;
  as_of: string;
  unread_since: string | null;
  tz: string;
  day_start: string;
  /** Unread rows a mark-all-read would clear (open calls excluded). */
  unread_markable: number;
}
