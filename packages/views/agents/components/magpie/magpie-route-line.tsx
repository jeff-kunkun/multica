"use client";

import { Fragment } from "react";
import type { MagpieRoute } from "@multica/core/runtimes";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";

/**
 * A magpie agent's route, read-only: the group chip, then the models in
 * order with the primary in bold. Routes are edited in magpie, not here.
 * Callers render it only for routes with a group.
 */
export function MagpieRouteLine({
  route,
  className,
}: {
  route: MagpieRoute;
  className?: string;
}) {
  const { t } = useT("agents");
  return (
    <div
      title={t(($) => $.magpie.edit_hint)}
      className={cn(
        "flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-caption text-muted-foreground",
        className,
      )}
    >
      <span className="inline-flex shrink-0 items-center gap-1 rounded-sm bg-muted px-1.5">
        <span aria-hidden="true" className="size-1.5 rounded-full bg-success" />
        {route.group}
      </span>
      {route.models.map((model, i) => (
        <Fragment key={model.id}>
          {i > 0 && (
            <span aria-hidden="true" className="text-faint-foreground">
              →
            </span>
          )}
          <span
            className={cn(
              "whitespace-nowrap",
              i === 0 && "font-semibold text-foreground",
            )}
          >
            {model.name}
          </span>
        </Fragment>
      ))}
    </div>
  );
}
