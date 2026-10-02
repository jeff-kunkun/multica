"use client";

import { Target } from "lucide-react";
import type { Issue } from "@multica/core/types";

/** Compact target marker shared by list and board cards. Reads the
 *  `goal_progress` summary the list API batches in, so rendering a list never
 *  fans out one goal request per row. */
export function GoalProgressBadge({ issue }: { issue?: Issue }) {
  const summary = issue?.goal_progress;
  if (!summary) return null;
  return (
    <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-brand/10 px-1.5 py-0.5 text-micro font-medium text-brand" aria-label={`Goal ${summary.done}/${summary.total}`}>
      <Target className="size-3" aria-hidden="true" />
      {summary.done}/{summary.total}
    </span>
  );
}
