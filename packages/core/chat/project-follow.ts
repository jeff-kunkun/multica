import { sameProjectIds } from "./project-context";

/**
 * Inputs to the floating chat's "follow the project I am looking at" rule
 * (DENE-603). Everything here is read from the surface that hosts the chat —
 * route, chat store, project cache — so the rule itself stays pure.
 */
export interface ProjectFollowInput {
  /** The project the main UI is currently on, resolved from the route. Null on
   *  a route that names no project (settings, inbox without an issue, …). */
  routeProjectId: string | null;
  /** The chat draft's current project set. */
  selectedProjectIds: string[];
  /** The user picked a project inside the chat: stop following the route. */
  locked: boolean;
  /** An existing session is open. Its project set is server-owned and the
   *  route must never rewrite it. */
  hasSession: boolean;
  /** The project list has resolved. Following before it does would bind an id
   *  the composer cannot render yet. */
  projectsLoaded: boolean;
  /** The route's project is in the loaded project list. The composer prunes
   *  ids the list does not know, so following one (stale list, project created
   *  elsewhere) would be pruned and re-followed in a loop until the list
   *  refetches. */
  routeProjectKnown: boolean;
}

/**
 * The project set the chat draft should follow to, or `null` for "leave it
 * alone".
 *
 * Three things stop the follow, in order of authority:
 *
 *  1. An open session — its set lives on the server (DENE-522) and belongs to
 *     the conversation, not to whatever page the user wandered onto since.
 *  2. A manual pick inside the chat (`locked`) — acceptance criterion 4: a
 *     route change must not overwrite what the user just chose, mid-compose.
 *  3. A route that names no project. Leaving a project page is not the same
 *     as detaching its context: clearing here would wipe the persisted draft
 *     set every time the user opened Settings. Only another project replaces
 *     a project.
 */
export function planFollowedProjectIds(input: ProjectFollowInput): string[] | null {
  const { routeProjectId, selectedProjectIds, locked, hasSession, projectsLoaded, routeProjectKnown } =
    input;
  if (hasSession) return null;
  if (locked) return null;
  if (!projectsLoaded) return null;
  if (!routeProjectId) return null;
  if (!routeProjectKnown) return null;
  const next = [routeProjectId];
  return sameProjectIds(next, selectedProjectIds) ? null : next;
}
