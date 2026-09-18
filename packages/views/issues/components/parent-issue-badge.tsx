"use client";

import { CornerDownRight } from "lucide-react";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { useParentIssueRef } from "../surface/parent-issue-context";

/**
 * Names the parent a sub-issue belongs to, on the surfaces where sub-issues
 * sit next to top-level issues in the same column or list. Without it a board
 * column of forty cards gives no way to tell a child from a parent. (DENE-480)
 *
 * Renders nothing for a top-level issue and whenever the parent is not in the
 * surface's loaded set — a chip that can only show an id would cost the row
 * its space without telling the reader what the task belongs to.
 *
 * The click is a button, not a link: the whole card is already wrapped in the
 * issue link, so it swallows the event the way the inline pickers beside it do.
 */
export function ParentIssueBadge({
  parentIssueId,
  density = "card",
  className,
}: {
  parentIssueId: string | null | undefined;
  /** `card` gets the card's full width; `row` shares one dense list line. */
  density?: "card" | "row";
  className?: string;
}) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const router = useNavigation();
  const parent = useParentIssueRef(parentIssueId);

  if (!parent) return null;

  return (
    <button
      type="button"
      data-testid="parent-issue-badge"
      aria-label={t(($) => $.parent_issue_badge.open, {
        identifier: parent.identifier,
        title: parent.title,
      })}
      title={`${parent.identifier} · ${parent.title}`}
      onClick={(e) => {
        e.stopPropagation();
        e.preventDefault();
        router.push(paths.issueDetail(parent.id));
      }}
      onMouseDown={(e) => e.stopPropagation()}
      onPointerDown={(e) => e.stopPropagation()}
      className={cn(
        "inline-flex min-w-0 items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground hover:bg-muted hover:text-foreground",
        density === "card" ? "max-w-full" : "max-w-[180px] shrink-0",
        className,
      )}
    >
      <CornerDownRight className="size-3 shrink-0" aria-hidden />
      <span className="shrink-0 font-medium tabular-nums">{parent.identifier}</span>
      <span className="min-w-0 truncate">{parent.title}</span>
    </button>
  );
}
