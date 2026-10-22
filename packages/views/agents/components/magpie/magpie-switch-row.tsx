"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  isWorkspaceMagpieEnabled,
  useUpdateWorkspaceMagpie,
  workspaceMagpieOptions,
} from "@multica/core/workspace/magpie";
import { Switch } from "@multica/ui/components/ui/switch";
import { PAGE_GUTTER } from "../../../layout/page-header";
import { useT } from "../../../i18n";

/**
 * The workspace's 接入 Magpie switch, a single row at the top of the agents
 * page. Only the owner sees it, and only once a daemon has reported a magpie
 * route for one of the workspace's runtimes (the server's `available`);
 * everyone else never does. The server refuses the change from anyone else.
 */
export function MagpieSwitchRow() {
  const { t } = useT("agents");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { role } = useCurrentMember(wsId);
  const isOwner = role === "owner";
  const { data } = useQuery({ ...workspaceMagpieOptions(wsId), enabled: !!wsId && isOwner });
  const update = useUpdateWorkspaceMagpie(wsId);

  if (!isOwner || !data?.available) return null;

  const stored = isWorkspaceMagpieEnabled(workspace?.settings);
  const checked = update.isPending && update.variables !== undefined ? update.variables : stored;

  return (
    <label className={`flex min-h-11 shrink-0 cursor-pointer items-center gap-3 border-b py-2 ${PAGE_GUTTER}`}>
      <span className="min-w-0 flex-1">
        <span className="block text-body font-medium">{t(($) => $.magpie.switch_label)}</span>
        <span className="block text-caption text-muted-foreground">
          {t(($) => $.magpie.switch_description)}
        </span>
      </span>
      <Switch
        checked={checked}
        disabled={update.isPending}
        onCheckedChange={(enabled) =>
          update.mutate(enabled, {
            onError: (error) =>
              toast.error(t(($) => $.magpie.switch_failed), {
                description: error instanceof Error ? error.message : undefined,
              }),
          })
        }
      />
    </label>
  );
}
