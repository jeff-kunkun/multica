"use client";

import { toast } from "sonner";
import { Switch } from "@multica/ui/components/ui/switch";
import type { Workspace } from "@multica/core/types";
import { isWorkspaceMagpieEnabled, useUpdateWorkspaceMagpie } from "@multica/core/workspace/magpie";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

/**
 * The workspace's Magpie switch: 接入 Magpie (on) or 未接入 Magpie (off, the
 * default). The old routing groups keep picking seats either way; connecting
 * only adds Magpie group selection, the read-only agents mirror and real agent
 * delete. The current state is spelled out next to the switch, so the two
 * editions are never told apart by the toggle position alone.
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
          <div className="flex items-center gap-2 sm:justify-end">
            <span
              data-testid="magpie-state"
              className={checked ? "text-sm font-medium text-foreground" : "text-sm text-muted-foreground"}
            >
              {checked ? t(($) => $.agent_permissions.magpie_state_on) : t(($) => $.agent_permissions.magpie_state_off)}
            </span>
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
          </div>
        </SettingsRow>
      </SettingsCard>
    </SettingsSection>
  );
}
