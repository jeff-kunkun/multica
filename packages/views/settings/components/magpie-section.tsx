"use client";

import { toast } from "sonner";
import { Switch } from "@multica/ui/components/ui/switch";
import type { Workspace } from "@multica/core/types";
import { isWorkspaceMagpieEnabled, useUpdateWorkspaceMagpie } from "@multica/core/workspace/magpie";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

/**
 * The workspace's Magpie switch. One deployment serves office workspaces (on)
 * and colleague workspaces that run as official Multica (off, the default).
 */
export function MagpieSection({ workspace, canManage }: { workspace: Workspace; canManage: boolean }) {
  const { t } = useT("settings");
  const update = useUpdateWorkspaceMagpie(workspace.id);
  const stored = isWorkspaceMagpieEnabled(workspace.settings);
  const checked = update.isPending && update.variables !== undefined ? update.variables : stored;

  return (
    <SettingsSection title={t(($) => $.workspace.magpie_title)}>
      <SettingsCard>
        <SettingsRow
          anchor="magpie"
          label={t(($) => $.workspace.magpie_label)}
          description={
            canManage
              ? t(($) => $.workspace.magpie_description)
              : t(($) => $.workspace.magpie_owner_only)
          }
        >
          <Switch
            checked={checked}
            disabled={!canManage || update.isPending}
            aria-label={t(($) => $.workspace.magpie_label)}
            onCheckedChange={(enabled) =>
              update.mutate(enabled, {
                onSuccess: () =>
                  toast.success(t(($) => $.auto_save.toast_saved), { id: "settings-auto-save" }),
                onError: (error) =>
                  toast.error(
                    error instanceof Error ? error.message : t(($) => $.auto_save.failed),
                  ),
              })
            }
          />
        </SettingsRow>
      </SettingsCard>
    </SettingsSection>
  );
}
