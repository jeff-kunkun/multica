/**
 * The per-parent child roll-up the surface carries (`childIssueProgressOptions`).
 * Declared structurally so this module stays free of the component layer's
 * `ChildProgress`, which is the same shape with a render-time home.
 */
export interface ParentChildRollup {
  done: number;
  total: number;
  blocked: number;
  active: number;
}

export type ParentRollupLookup = ReadonlyMap<string, ParentChildRollup>;

/**
 * What a parent card bubbles up from its sub-issues, so a project owner can
 * read a pipeline without expanding it (DENE-444):
 *
 * - `blocked` — children sitting in the `blocked` category. The loud one.
 * - `active` — children actually moving (`in_progress` / `in_review`).
 * - `stalled` — has unfinished children, none blocked, none moving. Quiet
 *   trouble: a queue nobody picked up, which otherwise looks identical to
 *   healthy work in a done/total ring.
 */
export type ParentPipelineState = "blocked" | "active" | "stalled" | "settled";

export function parentPipelineState(
  rollup: ParentChildRollup | undefined,
): ParentPipelineState {
  if (!rollup || rollup.total === 0) return "settled";
  if (rollup.blocked > 0) return "blocked";
  if (rollup.done >= rollup.total) return "settled";
  return rollup.active > 0 ? "active" : "stalled";
}
