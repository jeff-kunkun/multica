"use client";

import { useEffect, useId, useState } from "react";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useUpdateRuntime } from "@multica/core/runtimes/mutations";
import type { AgentRuntime } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";

export function MachineCapacityControl({ runtimes }: { runtimes: AgentRuntime[] }) {
  const { t } = useT("runtimes");
  const { t: common } = useT("common");
  const userId = useAuthStore((s) => s.user?.id);
  const mutation = useUpdateRuntime(useWorkspaceId());
  const id = useId();
  const target = runtimes.find((rt) => rt.owner_id === userId) ?? runtimes[0];
  const capacity = target?.machine_capacity;
  const [draft, setDraft] = useState(String(capacity?.max_concurrent_runs ?? 12));
  useEffect(() => { setDraft(String(capacity?.max_concurrent_runs ?? 12)); }, [capacity?.max_concurrent_runs]);
  if (!target || !capacity) return null;
  const canEdit = target.owner_id === userId;
  const value = Number(draft);
  const valid = draft.trim() !== "" && Number.isInteger(value) && value >= 1 && value <= 100;
  return (
    <form className="flex flex-wrap items-center gap-2" onSubmit={(event) => {
      event.preventDefault();
      if (canEdit && valid && !mutation.isPending) mutation.mutate({ runtimeId: target.id, patch: { max_concurrent_runs: value } });
    }}>
      <div className="text-caption">
        <label htmlFor={id} className="block">{t(($) => $.machine.capacity.label)}</label>
        <span className="text-muted-foreground tabular-nums">{t(($) => $.machine.capacity.running, { active: capacity.active_runs, limit: capacity.max_concurrent_runs })}</span>
      </div>
      <Input id={id} type="number" inputMode="numeric" min={1} max={100} step={1} required
        className="h-11 w-20" value={draft} onChange={(event) => setDraft(event.target.value)}
        disabled={!canEdit || mutation.isPending} />
      {canEdit && <Button type="submit" variant="outline" className="h-11" disabled={!valid || mutation.isPending || value === capacity.max_concurrent_runs}>
        {common(($) => $.save)}
      </Button>}
      {!canEdit && <p className="w-full text-caption text-muted-foreground">{t(($) => $.machine.capacity.owner_only)}</p>}
      {mutation.isError && <p role="alert" className="w-full text-caption text-destructive">{t(($) => $.machine.capacity.failed)}</p>}
    </form>
  );
}
