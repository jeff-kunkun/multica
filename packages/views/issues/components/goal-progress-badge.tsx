"use client";

import { Target } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { issueGoalOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import type { Issue, IssueGoalCheck } from "@multica/core/types";

function passed(check: IssueGoalCheck) {
  return check.passed === true || check.status === "passed" || check.status === "done" || check.status === "achieved";
}

/** Compact target marker shared by list and board cards. */
export function GoalProgressBadge({ issueId, issue }: { issueId?: string; issue?: Issue }) {
  const wsId = useWorkspaceId();
  const summary = issue?.goal_progress;
  const { data: goal } = useQuery({
    ...issueGoalOptions(wsId, issueId ?? ""),
    enabled: !summary && !!issueId,
  });
  if (summary) {
    return (
      <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-brand/10 px-1.5 py-0.5 text-micro font-medium text-brand" aria-label={`Goal ${summary.done}/${summary.total}`}>
        <Target className="size-3" aria-hidden="true" />
        {summary.done}/{summary.total}
      </span>
    );
  }
  if (!goal) return null;
  const done = goal.checks.filter(passed).length;
  const checks = goal.checks.map((check) => check.description ?? check.title ?? "").filter(Boolean);
  return (
    <span
      className="inline-flex shrink-0 items-center gap-1 rounded-full bg-brand/10 px-1.5 py-0.5 text-micro font-medium text-brand"
      title={checks.length ? checks.join(" · ") : "Goal completion line"}
      aria-label={`Goal ${done}/${goal.checks.length}`}
    >
      <Target className="size-3" aria-hidden="true" />
      {done}/{goal.checks.length}
    </span>
  );
}
