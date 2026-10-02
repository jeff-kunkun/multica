import { useState } from "react";
import { RotateCcw, ShieldCheck, Timer } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import type { Issue } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useQueryClient } from "@tanstack/react-query";
import { issueKeys } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";

/** Shared ticket-level affordances for the server-owned stall protocol. */
export function IssueStallActionBanner({ issue }: { issue: Issue }) {
  const action = typeof issue.metadata?.["stall.action"] === "string" ? issue.metadata["stall.action"] : "";
  const reason = typeof issue.metadata?.["stall.reason"] === "string" ? issue.metadata["stall.reason"] : "";
  const [pending, setPending] = useState(false);
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  if (!action || (action !== "announced" && action !== "parent_completed" && action !== "cancelled")) return null;
  const run = async (fn: () => Promise<unknown>) => {
    setPending(true);
    try { await fn(); await qc.invalidateQueries({ queryKey: issueKeys.detail(wsId, issue.id) }); }
    finally { setPending(false); }
  };
  const announced = action === "announced";
  return (
    <div className="mb-3 flex items-start gap-3 rounded-lg border border-amber-300/60 bg-amber-50/60 p-3 text-caption dark:bg-amber-950/20">
      {announced ? <Timer className="mt-0.5 size-4 shrink-0 text-amber-600" /> : <ShieldCheck className="mt-0.5 size-4 shrink-0 text-emerald-600" />}
      <div className="min-w-0 flex-1">
        <div className="font-medium">{announced ? "这张票正在 24 小时停滞公示" : "这张票由系统自动处理"}</div>
        {reason && <div className="mt-1 text-muted-foreground">{reason}</div>}
        <div className="mt-2 flex gap-2">
          {announced ? (
            <Button size="sm" variant="outline" disabled={pending} onClick={() => void run(() => api.keepIssueStall(issue.id))}><ShieldCheck className="mr-1 size-3.5" />保留</Button>
          ) : (
            <Button size="sm" variant="outline" disabled={pending} onClick={() => void run(() => api.undoIssueStall(issue.id))}><RotateCcw className="mr-1 size-3.5" />撤销处理</Button>
          )}
        </div>
      </div>
    </div>
  );
}
