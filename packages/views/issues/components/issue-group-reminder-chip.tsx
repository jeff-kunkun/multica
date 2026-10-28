"use client";

import { memo } from "react";
import { Megaphone } from "lucide-react";
import { useFeatureEnabled } from "@multica/core/config";
import { OFFICE_WATCHDOG_GROUP_REMINDER_FLAG } from "@multica/core/feature-flags";
import type { WatchdogReminder } from "@multica/core/types";
import { useT, useTimeAgo } from "../../i18n";

/**
 * Says the watchdog already told the project group that the assignee is
 * running but silent in this issue (DENE-2667). Hidden unless this client
 * was built with the office flag, and on servers that do not send the field.
 */
export const IssueGroupReminderChip = memo(function IssueGroupReminderChip({
  reminder,
}: {
  reminder?: WatchdogReminder;
}) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const enabled = useFeatureEnabled(OFFICE_WATCHDOG_GROUP_REMINDER_FLAG);
  if (!enabled || !reminder?.group_reminded_at) return null;
  const label = t(($) => $.watchdog_reminder.label);
  const detail = t(($) => $.watchdog_reminder.detail, {
    count: reminder.group_reminder_count,
    time: timeAgo(reminder.group_reminded_at),
  });
  return (
    <span
      role="status"
      title={detail}
      aria-label={`${label} · ${detail}`}
      className="inline-flex h-7 max-w-72 min-w-0 items-center gap-1.5 rounded-full border border-border px-2.5 text-caption text-muted-foreground"
    >
      <Megaphone className="size-3.5 shrink-0" aria-hidden="true" />
      <span className="min-w-0 truncate max-sm:hidden">{label}</span>
    </span>
  );
});
