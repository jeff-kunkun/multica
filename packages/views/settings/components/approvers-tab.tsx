"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  planApproverListOptions,
  useAddPlanApprover,
  useRemovePlanApprover,
} from "@multica/core/plans";
import { memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsTab } from "./settings-layout";

/**
 * Who may approve plans. Owners always can; the owner grants the right to
 * other people. Bot keys and agents never approve; only members are offered.
 */
export function ApproversTab() {
  const { t } = useT("plans");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const isOwner = useCurrentMember(wsId).role === "owner";
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: approvers = [] } = useQuery(planApproverListOptions(wsId, !!wsId));
  const add = useAddPlanApprover(wsId);
  const remove = useRemovePlanApprover(wsId);
  const [pick, setPick] = useState<string | null>(null);

  const owners = members.filter((m) => m.role === "owner");
  const granted = new Set(approvers.map((a) => a.user_id));
  const candidates = members.filter(
    (m) => m.role !== "owner" && !granted.has(m.user_id),
  );
  const items = candidates.map((m) => ({ value: m.user_id, label: m.name }));
  const fail = () => toast.error(t(($) => $.approvers.failed));

  return (
    <SettingsTab
      title={t(($) => $.approvers.title)}
      description={t(($) => $.approvers.description)}
      scope="workspace"
    >
      <SettingsCard>
        <SettingsRow label={t(($) => $.approvers.owners)}>
          <span className="block truncate text-body text-muted-foreground">
            {owners.map((m) => m.name).join("、")}
          </span>
        </SettingsRow>
        {approvers.map((approver) => (
          <SettingsRow
            key={approver.user_id}
            label={
              <span className="flex min-w-0 items-center gap-2">
                <ActorAvatar actorType="member" actorId={approver.user_id} size="xs" />
                <span className="truncate">{approver.name}</span>
              </span>
            }
          >
            {isOwner ? (
              <Button
                variant="ghost"
                size="sm"
                disabled={remove.isPending}
                onClick={() => remove.mutate(approver.user_id, { onError: fail })}
              >
                {t(($) => $.approvers.remove)}
              </Button>
            ) : null}
          </SettingsRow>
        ))}
        {isOwner ? (
          <SettingsRow label={t(($) => $.approvers.add)} size="select">
            <div className="flex gap-2">
              <Select items={items} value={pick} onValueChange={(value) => setPick(value as string | null)}>
                <SelectTrigger className="min-w-0 flex-1" aria-label={t(($) => $.approvers.add_placeholder)}>
                  <SelectValue placeholder={t(($) => $.approvers.add_placeholder)} />
                </SelectTrigger>
                <SelectContent>
                  {items.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                size="sm"
                disabled={!pick || add.isPending}
                onClick={() =>
                  pick && add.mutate(pick, { onSuccess: () => setPick(null), onError: fail })
                }
              >
                {t(($) => $.approvers.add)}
              </Button>
            </div>
          </SettingsRow>
        ) : (
          <SettingsRow label={t(($) => $.approvers.add)} description={t(($) => $.approvers.owner_only)}>
            <span />
          </SettingsRow>
        )}
      </SettingsCard>
    </SettingsTab>
  );
}
