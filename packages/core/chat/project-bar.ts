import type { ChatSession } from "../types";

/** One chip on the chat project bar. */
export interface ChatProjectChip {
  id: string;
  /** Active (non-archived) chats in this project. */
  chatCount: number;
  hasUnread: boolean;
  pinned: boolean;
}

function activityTime(s: ChatSession): number {
  return new Date(s.last_message?.created_at ?? s.updated_at).getTime();
}

/**
 * The chips above the chat list: every project with an active chat, the
 * person's pinned ones first, each group ordered by most recent chat.
 * Archived chats neither count nor surface a project, so a pinned project
 * without active chats stays off the bar. Ids that are not in `projectIds`
 * (deleted, or not loaded) are dropped.
 */
export function rankChatProjects(
  projectIds: readonly string[],
  pinnedIds: readonly string[],
  sessions: readonly ChatSession[],
): ChatProjectChip[] {
  const known = new Set(projectIds);
  const stats = new Map<string, { chatCount: number; hasUnread: boolean; lastAt: number }>();
  for (const session of sessions) {
    const id = session.project_id;
    if (!id || session.status === "archived" || !known.has(id)) continue;
    const row = stats.get(id) ?? { chatCount: 0, hasUnread: false, lastAt: 0 };
    row.chatCount += 1;
    row.hasUnread ||= session.has_unread;
    row.lastAt = Math.max(row.lastAt, activityTime(session));
    stats.set(id, row);
  }

  const pinned = new Set(pinnedIds);
  return [...stats.entries()]
    .map(([id, row]) => ({ id, ...row, pinned: pinned.has(id) }))
    .sort((a, b) => Number(b.pinned) - Number(a.pinned) || b.lastAt - a.lastAt)
    .map(({ id, chatCount, hasUnread, pinned }) => ({ id, chatCount, hasUnread, pinned }));
}

/** Sessions shown under a project filter; null keeps every session. */
export function filterChatSessionsByProject(
  sessions: ChatSession[],
  projectId: string | null,
): ChatSession[] {
  if (!projectId) return sessions;
  return sessions.filter((s) => s.project_id === projectId);
}
