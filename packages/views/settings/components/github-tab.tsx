"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AlertTriangle,
  CheckCircle2,
  ExternalLink,
  GitCommitHorizontal,
  Link2,
  PanelRight,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import {
  deriveGitHubSettings,
  githubCoverageOptions,
  githubInstallationsOptions,
} from "@multica/core/github";
import { api } from "@multica/core/api";
import type { GitHubCoverageResponse, Workspace } from "@multica/core/types";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { SettingsTab } from "./settings-layout";
import { GitHubMark } from "./github-mark";

type SettingsKey =
  | "github_enabled"
  | "github_pr_sidebar_enabled"
  | "co_authored_by_enabled"
  | "github_auto_link_prs_enabled";

export function GitHubTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const navigation = useNavigation();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  // `canView` gates the read-only installation list (every workspace member
  // sees it after MUL-2413); `canManage` gates the Connect / Disconnect
  // actions and comes from the backend response (`can_manage`) so the
  // frontend never claims management rights the server would reject.
  const canView = !!currentMember;

  const { data: installationData } = useQuery({
    ...githubInstallationsOptions(wsId),
    enabled: !!wsId && canView,
  });
  const installations = installationData?.installations ?? [];
  const configured = installationData?.configured ?? false;
  const canManage = installationData?.can_manage === true;
  const connected = installations.length > 0;
  // Owners/admins who can connect, named so a member knows whom to ask
  // (DENE-959) instead of a generic "ask an admin".
  const managerNames = members
    .filter((m) => m.role === "owner" || m.role === "admin")
    .map((m) => m.name)
    .filter(Boolean)
    .join(", ");

  const { data: coverage } = useQuery({
    ...githubCoverageOptions(wsId),
    enabled:
      !!wsId &&
      canManage &&
      connected &&
      installationData?.repository_browse_configured === true,
  });

  const flags = deriveGitHubSettings(workspace);
  const [savingKey, setSavingKey] = useState<SettingsKey | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  async function persistSetting(key: SettingsKey, next: boolean) {
    if (!workspace || savingKey) return;
    setSavingKey(key);
    try {
      const merged = {
        ...((workspace.settings as Record<string, unknown>) ?? {}),
        [key]: next,
      };
      const updated = await api.updateWorkspace(workspace.id, { settings: merged });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.auto_save.toast_saved), {
        id: "settings-auto-save",
      });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_failed));
    } finally {
      setSavingKey(null);
    }
  }

  async function handleConnect() {
    setConnecting(true);
    try {
      const resp = await api.getGitHubConnectURL(wsId);
      if (!resp.configured || !resp.url) {
        toast.error(t(($) => $.github.toast_not_configured));
        return;
      }
      window.open(resp.url, "_blank", "noopener");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_open_failed));
    } finally {
      setConnecting(false);
    }
  }

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteGitHubInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: ["github", wsId] });
      toast.success(t(($) => $.github.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_disconnect_failed));
    } finally {
      setDisconnecting(false);
    }
  }

  if (!workspace) return null;

  const repositoriesHref = `${navigation.pathname}?tab=repositories`;

  return (
    <SettingsTab
      title={t(($) => $.page.tabs.github)}
    >
      <section className="space-y-3">
        <Card>
          <CardContent>
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3">
                <div className="rounded-md border bg-muted/50 p-2 text-muted-foreground">
                  <GitHubMark className="h-4 w-4" />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="github-master" className="text-body font-medium">
                    {t(($) => $.github.section_master)}
                  </Label>
                  <p className="text-body text-muted-foreground">
                    {flags.enabled
                      ? t(($) => $.github.master_description_on)
                      : t(($) => $.github.master_description_off)}
                  </p>
                </div>
              </div>
              <Switch
                id="github-master"
                checked={flags.enabled}
                onCheckedChange={(v) => persistSetting("github_enabled", v)}
                disabled={!canManage || savingKey === "github_enabled"}
              />
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-body font-semibold">{t(($) => $.github.section_connection)}</h2>
        <Card>
          <CardContent className="space-y-4">
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3">
                <GitHubMark className="h-6 w-6 mt-0.5 shrink-0" />
                <div className="space-y-1">
                  <p className="text-body font-medium">{t(($) => $.github.connection_title)}</p>
                  {!connected && !canManage && (
                    <p className="text-caption text-muted-foreground">
                      {managerNames
                        ? t(($) => $.github.contact_named_admin_to_connect, { names: managerNames })
                        : t(($) => $.github.contact_admin_to_connect)}
                    </p>
                  )}
                  {canManage && configured && (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.github.multi_account_hint)}
                    </p>
                  )}
                </div>
              </div>
              {canManage && (
                <Button
                  size="sm"
                  variant={connected ? "outline" : "default"}
                  onClick={handleConnect}
                  disabled={connecting || !configured}
                  title={
                    !configured
                      ? t(($) => $.github.connect_disabled_tooltip)
                      : undefined
                  }
                >
                  {connecting
                    ? t(($) => $.github.connect_opening)
                    : connected
                      ? t(($) => $.github.connect_another)
                      : t(($) => $.github.connect_github)}
                </Button>
              )}
            </div>

            {connected && (
              <ul className="divide-y divide-surface-border rounded-md border">
                {installations.map((inst) => (
                  <li
                    key={inst.id}
                    className="flex items-center justify-between gap-4 px-3 py-2.5"
                  >
                    <div className="space-y-0.5">
                      <p className="text-body">
                        {t(($) => $.github.connected_to, { login: inst.account_login })}
                      </p>
                      {inst.connected_by && (
                        <p className="text-caption text-muted-foreground">
                          {t(($) => $.github.connected_by, { name: inst.connected_by })}
                        </p>
                      )}
                    </div>
                    {canManage && (
                      // Disconnect must stay reachable even when the master switch
                      // is off — disconnect is a separate intent (revoke the App
                      // grant) from hiding the feature.
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => setDisconnectTarget(inst.id)}
                      >
                        {t(($) => $.github.disconnect)}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}

            {canManage && !configured && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.github.not_configured)}{" "}
                <code className="rounded-xs bg-muted px-1 py-0.5 text-micro">GITHUB_APP_SLUG</code>{" "}
                {t(($) => $.github.not_configured_and)}{" "}
                <code className="rounded-xs bg-muted px-1 py-0.5 text-micro">GITHUB_WEBHOOK_SECRET</code>.
              </p>
            )}

            {!canManage && connected && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.github.read_only_hint)}
                {managerNames &&
                  ` ${t(($) => $.github.who_can_connect, { names: managerNames })}`}
              </p>
            )}
          </CardContent>
        </Card>
      </section>

      {coverage?.available && <CoverageSection coverage={coverage} />}

      <section className="space-y-3">
        <h2 className="text-body font-semibold">{t(($) => $.github.section_features)}</h2>
        <Card className="gap-0 py-0">
          <CardContent className="divide-y divide-surface-border px-0">
            <FeatureRow
              id="github-pr-sidebar"
              icon={<PanelRight className="h-4 w-4" />}
              label={t(($) => $.github.feature_pr_sidebar_label)}
              description={
                <p className="text-body text-muted-foreground">
                  {t(($) => $.github.feature_pr_sidebar_description)}
                </p>
              }
              checked={flags.prSidebar}
              disabled={!canManage || !flags.enabled || savingKey === "github_pr_sidebar_enabled"}
              onCheckedChange={(v) => persistSetting("github_pr_sidebar_enabled", v)}
            />

            <FeatureRow
              id="github-coauthor"
              icon={<GitCommitHorizontal className="h-4 w-4" />}
              label={t(($) => $.github.feature_co_author_label)}
              description={
                <p className="text-body text-muted-foreground">
                  {t(($) => $.github.feature_co_author_description_prefix)}{" "}
                  <code className="rounded-xs bg-muted px-1 py-0.5 text-caption">
                    {"Co-authored-by: multica-agent <github@multica.ai>"}
                  </code>{" "}
                  {t(($) => $.github.feature_co_author_description_suffix)}
                </p>
              }
              checked={flags.coAuthor}
              disabled={!canManage || !flags.enabled || savingKey === "co_authored_by_enabled"}
              onCheckedChange={(v) => persistSetting("co_authored_by_enabled", v)}
            />

            <FeatureRow
              id="github-auto-link"
              icon={<Link2 className="h-4 w-4" />}
              label={t(($) => $.github.feature_auto_link_label)}
              description={
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.github.connection_description_prefix)}{" "}
                  <code className="rounded-xs bg-muted px-1 py-0.5 text-micro">
                    {t(($) => $.github.connection_identifier_example)}
                  </code>{" "}
                  {t(($) => $.github.connection_description_suffix)}{" "}
                  <strong>{t(($) => $.github.connection_description_done)}</strong>.
                </p>
              }
              checked={flags.autoLinkPRs}
              disabled={!canManage || !flags.enabled || savingKey === "github_auto_link_prs_enabled"}
              onCheckedChange={(v) => persistSetting("github_auto_link_prs_enabled", v)}
            />
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-body font-semibold">{t(($) => $.github.section_repositories)}</h2>
        <Card>
          <CardContent>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="text-body font-medium">
                {t(($) => $.github.repositories_shortcut_label)}
              </p>
              <Button
                variant="outline"
                size="sm"
                render={<AppLink href={repositoriesHref} />}
                nativeButton={false}
              >
                <ExternalLink className="h-3 w-3" />
                {t(($) => $.github.repositories_shortcut_link)}
              </Button>
            </div>
          </CardContent>
        </Card>
      </section>

      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.github.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.github.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.github.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.github.disconnecting)
                : t(($) => $.github.disconnect_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function FeatureRow({
  id,
  icon,
  label,
  description,
  checked,
  disabled,
  onCheckedChange,
}: {
  id: string;
  icon: React.ReactNode;
  label: string;
  description: React.ReactNode;
  checked: boolean;
  disabled: boolean;
  onCheckedChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-3.5">
      <div className="flex items-start gap-3">
        <div className="rounded-md border bg-muted/50 p-2 text-muted-foreground">{icon}</div>
        <div className="space-y-1">
          <Label htmlFor={id} className="text-body font-medium">
            {label}
          </Label>
          {description}
        </div>
      </div>
      <Switch
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={onCheckedChange}
      />
    </div>
  );
}

// How many installation-covered-but-unregistered repos to list before
// collapsing the rest into a count; a whole-account install can cover dozens.
const UNREGISTERED_PREVIEW = 8;

function CoverageSection({ coverage }: { coverage: GitHubCoverageResponse }) {
  const { t } = useT("settings");
  const uncovered = coverage.registered.filter((r) => r.covered_by.length === 0);
  const extra = coverage.unregistered.length - UNREGISTERED_PREVIEW;

  return (
    <section className="space-y-3">
      <h2 className="text-body font-semibold">{t(($) => $.github.section_coverage)}</h2>
      <Card>
        <CardContent className="space-y-4">
          {uncovered.length > 0 ? (
            <div className="space-y-2">
              <p className="flex items-center gap-2 text-body font-medium text-warning">
                <AlertTriangle className="h-4 w-4 shrink-0" />
                {t(($) => $.github.coverage_uncovered_title, { count: uncovered.length })}
              </p>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.github.coverage_uncovered_hint)}
              </p>
              <ul className="space-y-1">
                {uncovered.map((r) => (
                  <li key={r.full_name}>
                    <code className="rounded-xs bg-muted px-1 py-0.5 text-caption">
                      {r.full_name}
                    </code>
                  </li>
                ))}
              </ul>
            </div>
          ) : coverage.registered.length > 0 ? (
            <p className="flex items-center gap-2 text-body text-muted-foreground">
              <CheckCircle2 className="h-4 w-4 shrink-0 text-success" />
              {t(($) => $.github.coverage_all_covered, { count: coverage.registered.length })}
            </p>
          ) : (
            <p className="text-body text-muted-foreground">
              {t(($) => $.github.coverage_none_registered)}
            </p>
          )}

          {coverage.failed_accounts.length > 0 && (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.github.coverage_failed, {
                accounts: coverage.failed_accounts.join(", "),
              })}
            </p>
          )}

          {coverage.unregistered.length > 0 && (
            <div className="space-y-2">
              <p className="text-caption text-muted-foreground">
                {t(($) => $.github.coverage_unregistered_hint, {
                  count: coverage.unregistered.length,
                })}
              </p>
              <ul className="flex flex-wrap gap-1.5">
                {coverage.unregistered.slice(0, UNREGISTERED_PREVIEW).map((r) => (
                  <li key={r.full_name}>
                    <code className="rounded-xs bg-muted px-1 py-0.5 text-caption">
                      {r.full_name}
                    </code>
                  </li>
                ))}
                {extra > 0 && (
                  <li className="text-caption text-muted-foreground">
                    {t(($) => $.github.coverage_more, { count: extra })}
                  </li>
                )}
              </ul>
            </div>
          )}
        </CardContent>
      </Card>
    </section>
  );
}
