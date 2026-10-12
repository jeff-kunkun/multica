"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { RoutingShadowResult } from "@multica/core/workspace/routing-shadow";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

export function RoutingShadowSection({ wsId, canRun }: { wsId: string; canRun: boolean }) {
  const { t } = useT("settings");
  const [offset, setOffset] = useState(0);
  const compare = useMutation({
    mutationFn: async () => {
      const page = await api.listRoutingShadowTickets(wsId, offset);
      const results: RoutingShadowResult[] = [];
      for (const ticket of page.items.slice(0, 20)) {
        results.push(await api.compareRoutingShadow(wsId, ticket.id));
      }
      return { results, next: page.items.length > 20 ? offset + 20 : page.next_offset };
    },
    onSuccess: (result) => setOffset(result.next),
  });
  return (
    <SettingsSection title={t(($) => $.routing.shadow_title)} description={t(($) => $.routing.shadow_description)}>
      <SettingsCard>
        <SettingsRow label={t(($) => $.routing.shadow_title)} description={!canRun ? t(($) => $.routing.shadow_locked) : undefined}>
          <Button className="min-h-11" disabled={!canRun || offset < 0 || compare.isPending} onClick={() => compare.mutate()}>
            {compare.isPending ? t(($) => $.routing.shadow_progress) : t(($) => $.routing.shadow_run)}
          </Button>
        </SettingsRow>
        {compare.error ? <p className="px-4 py-3 text-sm text-destructive">{compare.error.message}</p> : null}
        {compare.data?.results.length === 0 ? <p className="px-4 py-3 text-sm">{t(($) => $.routing.shadow_empty)}</p> : null}
        {compare.data?.results.map((result) => (
          <SettingsRow key={result.issue_id} size="text" label={result.title} description={result.error || result.proposed?.reason}>
            <div className="break-words text-sm">
              <p>{t(($) => $.routing.shadow_reference)}: {result.reference_tier_label ?? result.reference_tier}</p>
              <p>{t(($) => $.routing.shadow_actual)}: {result.actual_seat ?? "—"}</p>
              <p>{t(($) => $.routing.shadow_result)}: {result.selected?.Name ?? result.proposed?.tier ?? "—"}</p>
              {result.proposed ? <p>{Math.round(result.proposed.confidence * 100)}%</p> : null}
            </div>
          </SettingsRow>
        ))}
      </SettingsCard>
    </SettingsSection>
  );
}
