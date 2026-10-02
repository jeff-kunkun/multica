"use client";

import { Target } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { issueGoalOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import type { IssueGoalCheck } from "@multica/core/types";

function passed(check: IssueGoalCheck) {
  return check.passed === true || check.status === "passed" || check.status === "done" || check.status === "achieved";
}

/** Compact target marker shared by list and board cards. */
export function GoalProgressBadge({ issueId }: { issueId: string }) {
  const wsId = useWorkspaceId();
  const { data: goal } = useQuery(issueGoalOptions(wsId, issueId));
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
