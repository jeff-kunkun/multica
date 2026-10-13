import type { Workspace } from "../types";
import { paths } from "./paths";

/**
 * Where a signed-in user lands:
 *   workspace[0] → /<first.slug>/issues
 *   no workspace → /workspaces/new
 *
 * Callers that need invitation-aware routing (callback / login) handle
 * pending invites themselves before calling this resolver — this resolver
 * only deals with the post-invite-check destination.
 */
export function resolvePostAuthDestination(workspaces: Workspace[]): string {
  const first = workspaces[0];
  if (first) {
    return paths.workspace(first.slug).issues();
  }
  return paths.newWorkspace();
}
