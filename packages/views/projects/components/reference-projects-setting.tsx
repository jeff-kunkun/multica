"use client";

import { useQuery } from "@tanstack/react-query";
import type { ChatLinkedProjectRef } from "@multica/core/types";
import { linkedProjectOptionsOptions, linkedProjectSettingsOptions, useSetLinkedProjectSettings } from "@multica/core/workspace-links";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuCheckboxItem } from "@multica/ui/components/ui/dropdown-menu";
import { useT } from "../../i18n";
import { linkedKey, linkedRows } from "./linked-project-rows";

export function ReferenceProjectsSetting({ kind, id, canWrite }: { kind: "projects" | "autopilots"; id: string; canWrite: boolean }) {
  const wsId = useWorkspaceId();
  const { t } = useT("projects");
  const settings = useQuery(linkedProjectSettingsOptions(wsId, kind, id));
  const options = useQuery(linkedProjectOptionsOptions(wsId));
  const save = useSetLinkedProjectSettings(wsId, kind, id);
  const selected = settings.data?.effective_linked_projects ?? [];
  const rows = linkedRows(options.data ?? [], selected);
  const disabled = !canWrite || save.isPending || !settings.data;
  const refs = selected.map(({ link_id, project_id }) => ({ link_id, project_id }));
  function toggle(ref: ChatLinkedProjectRef) {
    const key = linkedKey(ref);
    save.mutate(refs.some((item) => linkedKey(item) === key)
      ? refs.filter((item) => linkedKey(item) !== key)
      : [...refs, ref]);
  }
  return (
    <section className="min-w-0 space-y-2 px-2">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-body font-medium">{t(($) => $.references.title)}</h3>
          <p className="text-caption text-muted-foreground">{t(($) => $.references.description)}</p>
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="outline" className="min-h-11" />} disabled={disabled || options.isPending || options.isError}>{t(($) => $.references.choose)}</DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="max-h-72 w-72 max-w-[calc(100vw-2rem)] overflow-y-auto">
            {rows.map((row) => <DropdownMenuCheckboxItem disabled={disabled} key={row.key} className="min-h-11" checked={selected.some((item) => linkedKey(item) === row.key)} onCheckedChange={() => toggle(row.ref)}>
              <span className="min-w-0 flex-1 break-words">{row.title}</span>
              <span className="ml-2 max-w-24 truncate text-caption text-muted-foreground">{row.available ? row.sourceName : t(($) => $.references.unavailable)}</span>
            </DropdownMenuCheckboxItem>)}
            {rows.length === 0 && <p className="px-2 py-3 text-caption text-muted-foreground">{t(($) => $.references.empty)}</p>}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {kind === "autopilots" && <label className="flex min-h-11 items-center gap-2 text-body"><input type="checkbox" disabled={disabled} checked={settings.data?.inherited ?? false} onChange={(event) => save.mutate(event.target.checked ? null : refs)} />{t(($) => $.references.inherit)}</label>}
      {settings.isPending ? <p className="text-caption text-muted-foreground">{t(($) => $.references.loading)}</p> : (settings.isError || options.isError) ? <div role="alert" className="text-caption">{t(($) => $.references.load_error)} <Button variant="ghost" className="min-h-11" onClick={() => { void settings.refetch(); void options.refetch(); }}>{t(($) => $.references.retry)}</Button></div> : <ul className="space-y-1 text-caption text-muted-foreground">{selected.map((item) => <li key={linkedKey(item)} className="break-words">{item.title}{item.source_name ? ` · ${item.source_name}` : ""}{!item.available && ` · ${t(($) => $.references.unavailable)}`}</li>)}{selected.length === 0 && <li>{t(($) => $.references.none)}</li>}</ul>}
      {!canWrite && <p className="text-caption text-muted-foreground">{t(($) => $.references.read_only)}</p>}
      {save.isError && <p role="alert" className="text-caption text-destructive">{t(($) => $.references.save_error)}</p>}
    </section>
  );
}
