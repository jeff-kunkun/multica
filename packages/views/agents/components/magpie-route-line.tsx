"use client";

import { Fragment } from "react";
import type { MagpieRoute } from "@multica/core/runtimes";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * A runtime's magpie route, read-only: the group chip, then the models in
 * order with the primary in bold. Routes are edited in magpie, not here.
 * Callers render nothing when there is no route to show.
 */
export function MagpieRouteLine({
  route,
  className,
}: {
  route: MagpieRoute;
  className?: string;
}) {
  const { t } = useT("agents");
  const routed = route.group !== "";
  return (
    <div
      title={t(($) => $.magpie.edit_hint)}
      className={cn(
        "flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-caption text-muted-foreground",
        className,
      )}
    >
      <span className="inline-flex shrink-0 items-center gap-1 rounded-sm bg-muted px-1.5">
        <span
          aria-hidden="true"
          className={cn(
            "size-1.5 rounded-full",
            routed ? "bg-success" : "bg-muted-foreground/40",
          )}
        />
        {routed ? route.group : t(($) => $.magpie.unrouted)}
      </span>
      {routed ? (
        route.models.map((model, i) => (
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
        ))
      ) : (
        <span>{t(($) => $.magpie.cli_models)}</span>
      )}
    </div>
  );
}
