"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useRequiredWorkspaceSlug } from "@multica/core/paths";
import { notificationPreferenceOptions } from "@multica/core/notification-preferences/queries";
import { useUpdateNotificationPreferences } from "@multica/core/notification-preferences/mutations";
import { Switch } from "@multica/ui/components/ui/switch";
import { Button } from "@multica/ui/components/ui/button";
import { SettingsRow } from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { toast } from "sonner";

export function RuntimeAlertSetting() {
  const wsId = useWorkspaceId();
  const slug = useRequiredWorkspaceSlug();
  const { t } = useT("runtimes");
  const query = useQuery(notificationPreferenceOptions(wsId, slug));
  const mutation = useUpdateNotificationPreferences();
  return (
    <SettingsRow label={t(($) => $.alerts.label)} description={t(($) => $.alerts.description)} className="px-0">
      {query.isError ? (
        <Button variant="ghost" onClick={() => query.refetch()}>{t(($) => $.alerts.retry)}</Button>
      ) : (
        <label className="flex min-h-11 min-w-11 cursor-pointer items-center justify-end">
          <Switch
            aria-label={t(($) => $.alerts.label)}
            checked={query.data?.preferences.runtime_alerts !== "muted"}
            disabled={query.isPending || mutation.isPending}
            onCheckedChange={(checked) => mutation.mutate(
              { ...query.data?.preferences, runtime_alerts: checked ? "all" : "muted" },
              { onError: () => toast.error(t(($) => $.alerts.save_failed)) },
            )}
          />
        </label>
      )}
    </SettingsRow>
  );
}
