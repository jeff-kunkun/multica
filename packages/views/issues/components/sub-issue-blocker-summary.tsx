"use client";

import { useMemo } from "react";
import { AlertTriangle, Circle, CircleDot } from "lucide-react";
import type { Issue } from "@multica/core/types";
import { deriveBlockerTree } from "@multica/core/issues";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

export function SubIssueBlockerSummary({ issue, children }: { issue: Issue; children: Issue[] }) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const tree = useMemo(() => deriveBlockerTree(issue, {
    childrenByParent: new Map([[issue.id, children]]),
    issueByIdentifier: new Map(children.map((child) => [child.identifier, child])),
  }), [issue, children]);
  const roots = tree.rootCauses;
  if (roots.length === 0 && tree.userActionCount === 0) return null;
  const byId = new Map(children.map((child) => [child.id, child]));
  return (
    <section data-testid="sub-issue-blocker-summary" className="mb-3 rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2">
      <div className="flex items-center gap-2 text-caption font-medium text-foreground">
        <AlertTriangle className="size-3.5 text-destructive" aria-hidden />
        <span>{t(($) => $.blocker_summary.title, { count: roots.length })}</span>
        {tree.userActionCount > 0 && <span className="text-micro text-destructive">{t(($) => $.blocker_summary.needs_you, { count: tree.userActionCount })}</span>}
      </div>
      <ul className="mt-1.5 space-y-1">
        {roots.slice(0, 5).map((root) => {
          const target = byId.get(root.id);
          return (
            <li key={root.id} className="flex min-w-0 items-center gap-1.5 text-caption">
              <CircleDot className="size-3 shrink-0 text-destructive" aria-hidden />
              {target ? <AppLink href={paths.issueDetail(target.id)} className="truncate text-muted-foreground hover:text-foreground">{target.identifier} · {target.title}</AppLink> : <span>{root.identifier}</span>}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

export function SubIssueBlockerBadge({ state, rootCause }: { state: "ROOT" | "PROPAGATED" | "CLEAR"; rootCause?: string }) {
  const { t } = useT("issues");
  if (state === "CLEAR") return null;
  const root = state === "ROOT";
  return (
    <span data-testid="sub-issue-blocker-badge" data-blocker-state={state} title={root ? t(($) => $.blocker_summary.root) : t(($) => $.blocker_summary.propagated)} className={cn("inline-flex size-4 shrink-0 items-center justify-center rounded-full", root ? "bg-destructive/10 text-destructive" : "border border-muted-foreground/50 text-muted-foreground")}>
      {root ? <CircleDot className="size-2.5" aria-hidden /> : <Circle className="size-2.5" aria-hidden />}
      <span className="sr-only">{root ? t(($) => $.blocker_summary.root) : t(($) => $.blocker_summary.propagated)}{rootCause ? ` ${rootCause}` : ""}</span>
    </span>
  );
}

export function blockerStateForIssue(parent: Issue, child: Issue, siblings: Issue[]): { state: "ROOT" | "PROPAGATED" | "CLEAR"; rootCause?: string } {
  const tree = deriveBlockerTree(parent, { childrenByParent: new Map([[parent.id, siblings]]) });
  const node = tree.nodes.get(child.id);
  return { state: node?.state ?? "CLEAR", rootCause: node?.rootCauses[0]?.identifier };
}
