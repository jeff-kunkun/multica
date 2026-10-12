"use client";

import { useInfiniteQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { officeDeliveriesOptions, useRetryOfficeDeliveries } from "@multica/core/bots";
import type { OfficeDelivery } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { SettingsRow } from "./settings-layout";

// Deliveries a retry can move: stuck, or waiting on the next attempt.
const RETRYABLE = new Set<OfficeDelivery["status"]>(["blocked", "pending"]);

/**
 * 送达记录 (DENE-1963): every office event pushed to your Bot, whether the Bot
 * confirmed it and whether it reached you. The server decides each state;
 * this row only shows it and lets you retry what is stuck.
 */
export function OfficeDeliveriesRow({ wsId }: { wsId: string }) {
  const { t } = useT("settings");
  const ledger = useInfiniteQuery(officeDeliveriesOptions(wsId));
  const retry = useRetryOfficeDeliveries(wsId);
  const rows = ledger.data?.pages.flatMap((page) => page.deliveries) ?? [];
  const anyRetryable = rows.some((row) => RETRYABLE.has(row.status));

  const onRetry = (eventId?: string) =>
    retry.mutate(eventId, {
      onSuccess: ({ requeued }) =>
        toast.success(t(($) => $.notifications.decisions.deliveries_retried, { count: requeued })),
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.notifications.decisions.toast_failed),
        ),
    });

  const statusLabel = (status: OfficeDelivery["status"]) =>
    t(($) => $.notifications.decisions.deliveries_status[status]);

  const detail = (row: OfficeDelivery) => {
    if (row.status === "blocked") return row.blocked_reason || statusLabel(row.status);
    if (row.last_error && (row.status === "pending" || row.status === "sending"))
      return t(($) => $.notifications.decisions.deliveries_retrying, {
        count: row.attempts,
        error: row.last_error,
      });
    return statusLabel(row.status);
  };

  return (
    <SettingsRow
      anchor="decisions-deliveries"
      label={t(($) => $.notifications.decisions.deliveries_title)}
      description={
        rows.length === 0
          ? t(($) => $.notifications.decisions.deliveries_empty)
          : t(($) => $.notifications.decisions.deliveries_hint)
      }
      size="select-wide"
    >
      {rows.length > 0 ? (
        <ul className="flex min-w-0 flex-col gap-2 text-sm">
          {anyRetryable ? (
            <li>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={retry.isPending}
                onClick={() => onRetry()}
              >
                {t(($) => $.notifications.decisions.deliveries_retry_all)}
              </Button>
            </li>
          ) : null}
          {rows.map((row) => (
            <li key={row.cursor} className="flex min-w-0 items-start gap-2">
              <div className="min-w-0 flex-1">
                <span className="block [overflow-wrap:anywhere]">{row.event.summary}</span>
                <span
                  className={`block text-caption [overflow-wrap:anywhere] ${
                    row.status === "blocked" ? "text-destructive" : "text-muted-foreground"
                  }`}
                >
                  {detail(row)}
                  {row.must_relay && row.status !== "relayed"
                    ? ` · ${t(($) => $.notifications.decisions.deliveries_must_relay)}`
                    : ""}
                </span>
              </div>
              {RETRYABLE.has(row.status) ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="shrink-0"
                  disabled={retry.isPending}
                  onClick={() => onRetry(row.event_id)}
                >
                  {t(($) => $.notifications.decisions.deliveries_retry)}
                </Button>
              ) : null}
            </li>
          ))}
          {ledger.hasNextPage ? (
            <li>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={ledger.isFetchingNextPage}
                onClick={() => void ledger.fetchNextPage()}
              >
                {t(($) => $.notifications.decisions.deliveries_more)}
              </Button>
            </li>
          ) : null}
        </ul>
      ) : null}
    </SettingsRow>
  );
}
