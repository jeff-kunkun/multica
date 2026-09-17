import type { Issue } from "@multica/core/types";

/**
 * Order a parent's children for display: staged groups ascending by stage,
 * then the unstaged group (stage === null) last. Callers render a per-group
 * stage header only when the set is actually staged.
 */
export function groupSubIssuesByStage(
  children: Issue[],
): { stage: number | null; items: Issue[] }[] {
  const byStage = new Map<number, Issue[]>();
  const unstaged: Issue[] = [];
  for (const c of children) {
    if (c.stage != null) {
      const arr = byStage.get(c.stage);
      if (arr) arr.push(c);
      else byStage.set(c.stage, [c]);
    } else {
      unstaged.push(c);
    }
  }
  const groups: { stage: number | null; items: Issue[] }[] = [...byStage.keys()]
    .sort((a, b) => a - b)
    .map((s) => ({ stage: s, items: byStage.get(s) as Issue[] }));
  if (unstaged.length > 0) groups.push({ stage: null, items: unstaged });
  return groups;
}
