import type {
  LocalDirectoryResourceRef,
  ProjectResource,
} from "@multica/core/types";

// Which local directory a run writes is decided by order: the first
// local_directory on the claiming machine is the working directory, and every
// other one reaches the agent read-only (DENE-617). That makes ordering a
// product decision the user has to be able to make, not a detail — so this is
// the rule for moving one, kept pure and tested on its own.
//
// Canonical tests: local-directory-order.test.ts. The component suite covers
// the wiring (which buttons appear, what they call), not this matrix.

/** One row's new `position`, ready for the update endpoint. */
export interface ResourcePositionPatch {
  resourceId: string;
  position: number;
}

export type MoveDirection = "up" | "down";

function localDaemonOf(resource: ProjectResource): string | null {
  if (resource.resource_type !== "local_directory") return null;
  const ref = resource.resource_ref as Partial<LocalDirectoryResourceRef>;
  const daemonId = typeof ref?.daemon_id === "string" ? ref.daemon_id : "";
  return daemonId === "" ? null : daemonId;
}

/**
 * Indices, in list order, of the local directories bound to one machine.
 *
 * Only these are neighbours of each other: a github_repo row between two of
 * them changes nothing about which directory a run writes, and neither does a
 * directory on a different machine.
 */
export function localDirectoryIndexes(
  resources: readonly ProjectResource[],
  daemonId: string | null,
): number[] {
  if (!daemonId) return [];
  const out: number[] = [];
  resources.forEach((resource, index) => {
    if (localDaemonOf(resource) === daemonId) out.push(index);
  });
  return out;
}

/** Whether this row can move in that direction — what the buttons disable on. */
export function canMoveLocalDirectory(
  resources: readonly ProjectResource[],
  resourceId: string,
  daemonId: string | null,
  direction: MoveDirection,
): boolean {
  return moveLocalDirectory(resources, resourceId, daemonId, direction).length > 0;
}

/**
 * Move one local directory past its nearest sibling on the same machine.
 *
 * Returns the rows whose `position` changed, and an EMPTY list when the move
 * is not available (already first or last, not this machine's, unknown row) —
 * so a caller that ignores the check still cannot send a no-op write.
 *
 * Positions are renumbered across the whole list rather than swapped between
 * the two rows. Swapping assumes the stored values are distinct, and they are
 * not guaranteed to be: rows created without an explicit position can tie, and
 * the list then falls back to created_at. Renumbering states the resulting
 * order outright instead of depending on what the old numbers happened to be.
 */
export function moveLocalDirectory(
  resources: readonly ProjectResource[],
  resourceId: string,
  daemonId: string | null,
  direction: MoveDirection,
): ResourcePositionPatch[] {
  const group = localDirectoryIndexes(resources, daemonId);
  const from = resources.findIndex((r) => r.id === resourceId);
  if (from < 0) return [];
  const slot = group.indexOf(from);
  if (slot < 0) return [];
  const neighbour = direction === "up" ? group[slot - 1] : group[slot + 1];
  if (neighbour === undefined) return [];

  const next = resources.slice();
  const [moved] = next.splice(from, 1);
  if (!moved) return [];
  // Splicing the row out shifts everything after it down by one, so inserting
  // at the neighbour's ORIGINAL index lands before it when moving up and after
  // it when moving down — which is exactly the swap, without special-casing
  // the direction.
  next.splice(neighbour, 0, moved);

  const patches: ResourcePositionPatch[] = [];
  next.forEach((resource, index) => {
    if (resource.position !== index) {
      patches.push({ resourceId: resource.id, position: index });
    }
  });
  return patches;
}
