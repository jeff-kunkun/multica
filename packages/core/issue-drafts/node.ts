import { v5 as uuidv5 } from "uuid";
import type { Issue, IssueDraftChild } from "../types";

/**
 * Identity of one alignment NODE — the group's root, or one sub-issue of it.
 *
 * Every node of a payload owns at most one issue, and which issue that is is
 * derived from the conversation rather than sent by a client:
 * `server/internal/handler/issue_draft_group.go` (`issueDraftNodeID`). The root
 * (key `""`) is the chat session itself; a sub-issue is UUIDv5 over
 * (namespace, chat_session_id + NUL + key).
 *
 * The derivation is repeated here for ONE read-only purpose: telling "this
 * payload node already produced an issue" apart from "this round adds it". The
 * server reports the group's issues with their `origin_id` (that is the node
 * id) but does not echo the node's KEY back, so a client that wants to join the
 * payload against the group has to compute the same value. Nothing here ever
 * names an identity: the confirm sends a revision, never a node id, so a client
 * still cannot point at a node belonging to someone else's alignment.
 *
 * The namespace is FROZEN — it is half the input of every node id already
 * minted, so changing it would strip every existing group of its identity and
 * make the next confirm build the whole group a second time. It must stay
 * byte-identical to `issueDraftNodeNamespace` in the Go file above; the vectors
 * in `node.test.ts` are produced by that implementation.
 */
export const ISSUE_DRAFT_NODE_NAMESPACE =
  "076522a7-f3b6-414f-afb0-41823470299e";

const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * The `origin_id` of the issue this node owns, or null when the session id is
 * not a UUID and no derivation is possible.
 *
 * The key is trimmed first: the server trims it before hashing (validation,
 * uniqueness and derivation all have to agree on what a key IS), so " c1" and
 * "c1" must not derive two different nodes here either.
 */
export function issueDraftNodeId(
  sessionId: string,
  key: string,
): string | null {
  const session = sessionId.trim().toLowerCase();
  if (!UUID_PATTERN.test(session)) return null;
  const trimmed = key.trim();
  // The root node IS the conversation: that one line is what keeps the root's
  // issue findable by the draft's own id.
  if (trimmed.length === 0) return session;
  return uuidv5(`${session}\u0000${trimmed}`, ISSUE_DRAFT_NODE_NAMESPACE);
}

/**
 * Which of these payload sub-issues already own an issue.
 *
 * `groupChildren` is the group as the server reads it back — every child of the
 * group's root, each carrying the `origin_id` it was created with. A node is
 * built when its derived id is one of them; a node that does not match is what
 * the next confirm will create.
 *
 * It joins by origin rather than by a field the user can edit, which is the
 * whole point: a node's title, stage and assignee are all editable in the
 * preview panel, and matching on any of them would call a renamed row "new" and
 * promise a create the server deliberately skips. Origin is the key the
 * confirm's own skip rule reads, so the two answers cannot disagree.
 *
 * A node whose issue LEFT the group (re-parented under another issue, detached
 * to the top level) keeps its origin but is absent from this read, so it reads
 * as new here while the server still adopts it. That is the one case where the
 * count can over-promise, and it is bounded by how rare the move is.
 */
export function issueDraftBuiltNodeKeys(
  children: readonly IssueDraftChild[],
  groupChildren: readonly Issue[],
  sessionId: string,
): Set<string> {
  const owned = new Set<string>();
  for (const child of groupChildren) {
    const origin = child.origin_id?.trim();
    if (origin) owned.add(origin.toLowerCase());
  }
  const built = new Set<string>();
  if (owned.size === 0) return built;
  for (const child of children) {
    const nodeId = issueDraftNodeId(sessionId, child.key);
    if (nodeId && owned.has(nodeId)) built.add(child.key);
  }
  return built;
}
