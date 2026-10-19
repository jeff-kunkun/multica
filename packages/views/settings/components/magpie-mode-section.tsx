"use client";

import { toast } from "sonner";
import { Switch } from "@multica/ui/components/ui/switch";
import type { Workspace } from "@multica/core/types";
import { isWorkspaceMagpieEnabled, useUpdateWorkspaceMagpie } from "@multica/core/workspace/magpie";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

/**
 * The workspace's magpie mode switch. Off by default; the old routing groups
 * keep picking seats either way, the switch only adds magpie group selection,
 * the read-only agents mirror and real agent delete.
 */
export function MagpieModeSection({ workspace, canManage }: { workspace: Workspace; canManage: boolean }) {
  const { t } = useT("settings");
  const update = useUpdateWorkspaceMagpie(workspace.id);
  const stored = isWorkspaceMagpieEnabled(workspace.settings);
  const checked = update.isPending && update.variables !== undefined ? update.variables : stored;

  return (
    <SettingsSection anchor="magpie" title={t(($) => $.agent_permissions.magpie_title)}>
      <SettingsCard>
        <SettingsRow
          anchor="magpie-mode"
          label={t(($) => $.agent_permissions.magpie_label)}
          description={t(($) => $.agent_permissions.magpie_description)}
          size="text"
        >
          <Switch
            checked={checked}
            disabled={!canManage || update.isPending}
            aria-label={t(($) => $.agent_permissions.magpie_label)}
            onCheckedChange={(enabled) =>
              update.mutate(enabled, {
                onSuccess: () => toast.success(t(($) => $.agent_permissions.saved), { id: "settings-auto-save" }),
                onError: (error) =>
                  toast.error(error instanceof Error ? error.message : t(($) => $.agent_permissions.save_failed)),
              })
            }
          />
        </SettingsRow>
      </SettingsCard>
    </SettingsSection>
  );
}
