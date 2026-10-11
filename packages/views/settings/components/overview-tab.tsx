"use client";

import { useState } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { workspaceOverviewOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { SettingsSection, SettingsTab } from "./settings-layout";

export function OverviewTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const [project, setProject] = useState("");
  const [agent, setAgent] = useState("");
  const { data: catalog } = useQuery({ ...workspaceOverviewOptions(wsId), staleTime: 60_000 });
  const { data, isPending, isError, isFetching, refetch } = useQuery(workspaceOverviewOptions(wsId, { project: project || undefined, agent: agent || undefined }));
  const projectItems = [{ value: "", label: t(($) => $.overview.all_projects) }, ...(catalog?.projects ?? []).map((row) => ({ value: row.id, label: row.name }))];
  const agentItems = [{ value: "", label: t(($) => $.overview.all_agents) }, ...(catalog?.agents ?? []).map((row) => ({ value: row.id, label: row.name }))];
  const sections = data ? [
    { key: "agents", title: t(($) => $.overview.agents), rows: data.agents },
    { key: "projects", title: t(($) => $.overview.projects), rows: data.projects },
    { key: "chats", title: t(($) => $.overview.chats), rows: data.chats },
    { key: "automations", title: t(($) => $.overview.automations), rows: data.automations },
    { key: "skills", title: t(($) => $.overview.skills), rows: data.skills },
    { key: "squads", title: t(($) => $.overview.squads), rows: data.squads },
    { key: "runtimes", title: t(($) => $.overview.runtimes), rows: data.runtimes },
    { key: "workspace_links", title: t(($) => $.overview.workspace_links), rows: data.workspaceLinks },
    { key: "external_connections", title: t(($) => $.overview.external_connections), rows: data.externalConnections },
  ] : [];
  return (
    <SettingsTab
      title={t(($) => $.page.tabs.overview)}
      scope="workspace"
      actions={<Button variant="outline" className="min-h-11" disabled={isFetching} onClick={() => void refetch()}>{t(($) => $.overview.refresh)}</Button>}
    >
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <Select value={project} onValueChange={(value) => setProject(value ?? "")} items={projectItems}>
          <SelectTrigger aria-label={t(($) => $.overview.projects)} className="min-h-11 w-full min-w-0"><SelectValue /></SelectTrigger>
          <SelectContent>{projectItems.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent>
        </Select>
        <Select value={agent} onValueChange={(value) => setAgent(value ?? "")} items={agentItems}>
          <SelectTrigger aria-label={t(($) => $.overview.agents)} className="min-h-11 w-full min-w-0"><SelectValue /></SelectTrigger>
          <SelectContent>{agentItems.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent>
        </Select>
      </div>
      {isPending && <p role="status" className="text-body text-muted-foreground">{t(($) => $.overview.loading)}</p>}
      {(isError || (!isPending && !data)) && <p role="alert" className="text-body text-destructive">{t(($) => $.overview.load_error)}</p>}
      {sections.map(({ key, title, rows }) => (
        <SettingsSection key={key} title={data?.omittedSections.includes(key) ? title : `${title} · ${rows.length}`}>
          {data?.omittedSections.includes(key) ? (
            <p className="text-body text-muted-foreground">{t(($) => $.overview.restricted)}</p>
          ) : rows.length === 0 ? (
            <p className="text-body text-muted-foreground">{t(($) => $.overview.empty)}</p>
          ) : (
            <ul className="divide-y divide-border">
              {rows.map((row, index) => {
                const projects = row.projectIds.map((id) => data?.projects.find((project) => project.id === id)?.name).filter(Boolean);
                const runtime = data?.runtimes.find((runtime) => runtime.id === row.runtimeId);
                const owner = data?.agents.find((agent) => agent.id === (row.leadId ?? row.assigneeId));
                const details = [row.model, runtime?.name, runtime?.deviceInfo, row.deviceInfo, row.ownerName ?? owner?.name, row.specialization, row.tier, row.availability ?? row.status,
                  row.workEnabled == null ? null : row.workEnabled ? t(($) => $.overview.accepting_work) : t(($) => $.overview.paused),
                  ...projects,
                  ...row.resources,
                  ...row.tickets.map((ticket) => `${ticket.title}${ticket.status ? ` · ${ticket.status}` : ""}`),
                  ...row.schedules.map((schedule) => [schedule.cron ?? schedule.kind, schedule.timezone].filter(Boolean).join(" ")),
                  row.nextRunAt ? new Date(row.nextRunAt).toLocaleString() : null,
                ].filter(Boolean).join(" · ");
                return <li key={row.id || index} className="min-w-0 space-y-1 py-3 first:pt-0 last:pb-0">
                  <p className="break-words text-body font-medium [overflow-wrap:anywhere]">{row.name}</p>
                  {details && <p className="break-words text-caption text-muted-foreground [overflow-wrap:anywhere]">{details}</p>}
                  {row.description && <p className="break-words text-caption text-muted-foreground [overflow-wrap:anywhere]">{row.description}</p>}
                </li>;
              })}
            </ul>
          )}
        </SettingsSection>
      ))}
    </SettingsTab>
  );
}
