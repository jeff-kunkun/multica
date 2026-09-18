"use client";

import { Pin } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";

/**
 * The in-row marker for an issue the table query ranked into its pinned block.
 *
 * Passive by design: it reports the ordering the server already applied and
 * carries no toggle of its own. The switch is the row's context menu /
 * `togglePin`, so there is exactly one writer and no second piece of pin state
 * to keep in sync. It is not interactive, so it stays out of the tab order,
 * but it still names itself for assistive tech — an icon nobody can read is not
 * an indicator. (DENE-500)
 */
export function PinnedRowBadge({
  label,
  className,
}: {
  label: string;
  className?: string;
}) {
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      data-testid="pinned-row-badge"
      className={cn(
        "inline-flex shrink-0 items-center text-muted-foreground",
        className,
      )}
    >
      <Pin aria-hidden="true" className="size-3 rotate-45" />
    </span>
  );
}
