"use client";

// Agent "accounts" tab (design C5: summary bar + drawer).
//
// The rest state answers one question — which account is in effect right now —
// and keeps every management action inside the drawer. Parsing, grouping,
// status mapping, the four page states and the switch plan all come from
// `agent-accounts-model.ts` (DENE-307); this file only renders them and
// performs the single write the plan describes.
//
// Two data sources feed the summary:
// - the account list the daemon reports on the runtime's metadata (read-only,
//   no request of our own);
// - `agent.custom_args` for the agy lever, and the audited
//   `GET /api/agents/{id}/env` for env levers (the agent payload redacts
//   `custom_env`, MUL-2600), which is the only way to tell a bound DSH_HOME
//   from an unbound one.
//
// Invariants from the design doc: no credential value is ever read, rendered
// or logged (only `key_ref` names and `signed_in`); "save and switch" is the
// only control with a side effect; and a view that is loading, empty or in
// error never offers the drawer.

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, FolderTree } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import { providerDisplayName, runtimeKeys } from "@multica/core/runtimes";
import type { Agent, RuntimeDevice } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../../i18n";
import { useNavigation } from "../../../navigation";
import {
  type AgentAccount,
  type AgentAccountBinding,
  type AgentAccountCli,
  accountLeverLabel,
  accountsViewState,
  canManageAccounts,
  cliForProvider,
  groupAccountsByCli,
  parseAccountLever,
  parseAgentAccounts,
  planAccountSwitch,
  resolveCurrentAccount,
} from "./agent-accounts-model";
import { runtimeHomeDir } from "./agy-account-slots";
import {
  AccountActivePill,
  AccountStatusPill,
  AgentAccountDrawer,
  AgentCliBadge,
  CommandBlock,
  accountKey,
  accountLoginCommand,
  agentCliLabel,
} from "./agent-account-drawer";

/**
 * `GET /api/agents/{id}/env` is audited server-side, so its answer is cached
 * for the session instead of being refetched on every mount and focus.
 */
const ENV_STALE_TIME_MS = 5 * 60_000;

function agentEnvQueryKey(wsId: string | null, agentId: string) {
  return ["agent-env", wsId ?? "", agentId] as const;
}

/**
 * The empty state has no account rows to read a lever from, so the copy needs
 * the contract's per-CLI lever once more (DENE-306). Presentation only: no
 * write path consults this table — `planAccountSwitch` decides a switch from
 * the lever the daemon reported on the account itself.
 */
const CLI_LEVER: Record<AgentAccountCli, string> = {
  dsh: "env:DSH_HOME",
  agy: "custom_args:--gemini_dir",
  claude: "env:CLAUDE_CONFIG_DIR",
  codex: "",
  cursor: "",
};

/** Directory a new account of this CLI conventionally lives in (daemon glob). */
const CLI_NEW_ACCOUNT_HOME: Record<AgentAccountCli, string> = {
  dsh: "~/.dsh-account2",
  agy: "~/.gemini-account2",
  claude: "~/.claude-account2",
  codex: "~/.codex",
  cursor: "~/.cursor",
};

type EmptyChoice = "dsh" | "agy" | "manual";

function emptyEntryAccount(cli: AgentAccountCli): AgentAccount {
  return {
    cli,
    account: "account2",
    home: CLI_NEW_ACCOUNT_HOME[cli],
    base_url: "",
    key_ref: "",
    lever: CLI_LEVER[cli],
    signed_in: false,
    quota_reset_at: 0,
  };
}

export interface AgentAccountsTabProps {
  agent: Agent;
  runtimeDevice?: RuntimeDevice;
  onSave: (updates: Partial<Agent>) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}

export function AgentAccountsTab({
  agent,
  runtimeDevice,
  onSave,
  onDirtyChange,
}: AgentAccountsTabProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const slug = useWorkspaceSlug();
  const navigation = useNavigation();
  const queryClient = useQueryClient();

  const parsed = useMemo(
    () => parseAgentAccounts(runtimeDevice),
    [runtimeDevice],
  );
  const groups = useMemo(
    () => groupAccountsByCli(parsed.accounts),
    [parsed.accounts],
  );
  const allAccounts = useMemo(
    () => groups.flatMap((group) => group.accounts),
    [groups],
  );

  // An env lever is only readable through the env endpoint. Waiting for it
  // before rendering the summary keeps a bound DSH_HOME from being described
  // as the CLI default for one frame; an account set with no env lever (agy,
  // codex, cursor) never pays for the request.
  const needsEnvBinding = useMemo(
    () =>
      parsed.accounts.some(
        (account) => parseAccountLever(account.lever).kind === "env",
      ),
    [parsed.accounts],
  );
  const envQuery = useQuery({
    queryKey: agentEnvQueryKey(wsId, agent.id),
    queryFn: () => api.getAgentEnv(agent.id),
    enabled: !!agent.id && needsEnvBinding,
    staleTime: ENV_STALE_TIME_MS,
    refetchOnWindowFocus: false,
  });

  const binding = useMemo<AgentAccountBinding>(
    () => ({
      custom_args: agent.custom_args,
      custom_env: envQuery.data?.custom_env,
      runtime_home: runtimeHomeDir(runtimeDevice),
      provider: runtimeDevice?.provider,
    }),
    [agent.custom_args, envQuery.data, runtimeDevice],
  );

  const viewState = accountsViewState({
    accounts: parsed.accounts,
    error: parsed.error,
    // `runtimeDevice === undefined` means the runtime row has not arrived yet,
    // and the daemon's report travels on it: there is nothing to read.
    loading: runtimeDevice === undefined || (needsEnvBinding && envQuery.isPending),
  });

  const current = useMemo(
    () => resolveCurrentAccount(binding, parsed.accounts),
    [binding, parsed.accounts],
  );
  const currentKey = current ? accountKey(current) : null;
  const others = useMemo(
    () =>
      allAccounts.filter((account) => accountKey(account) !== currentKey),
    [allAccounts, currentKey],
  );

  const [drawerOpen, setDrawerOpen] = useState(false);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [nowMs, setNowMs] = useState(() => Date.now());

  // Re-render when the soonest exhausted quota resets, so the pill flips back
  // to "ready" without a reload.
  const nextResetMs = useMemo(
    () =>
      parsed.accounts.reduce<number | null>((soonest, account) => {
        const at = account.quota_reset_at * 1000;
        if (at <= nowMs) return soonest;
        return soonest === null || at < soonest ? at : soonest;
      }, null),
    [parsed.accounts, nowMs],
  );

  useEffect(() => {
    if (nextResetMs === null) return;
    const delay = Math.min(Math.max(nextResetMs - Date.now(), 0), 2_147_000_000);
    const timer = window.setTimeout(() => setNowMs(Date.now()), delay);
    return () => window.clearTimeout(timer);
  }, [nextResetMs]);

  // A selection in the drawer is unsaved work until "save and switch" commits
  // it, so the surrounding settings layout can guard a tab switch.
  const dirty = drawerOpen && selectedKey !== null && selectedKey !== currentKey;
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const openDrawer = () => {
    setSelectedKey(currentKey);
    setDrawerOpen(true);
  };

  const handleDrawerOpenChange = (open: boolean) => {
    setDrawerOpen(open);
    // Closing drops the selection: nothing inside the drawer took effect.
    if (!open) setSelectedKey(null);
  };

  const handleSaveAndSwitch = async () => {
    const target =
      allAccounts.find((account) => accountKey(account) === selectedKey) ??
      null;
    const plan = planAccountSwitch(binding, target, viewState);

    if (plan.kind === "noop") {
      setDrawerOpen(false);
      setSelectedKey(null);
      return;
    }
    if (plan.kind === "unsupported") {
      toast.error(
        plan.reason === "invalid_home"
          ? t(($) => $.tab_body.accounts.switch_invalid_home_toast)
          : t(($) => $.tab_body.accounts.switch_unsupported_toast),
      );
      return;
    }

    setSaving(true);
    try {
      if (plan.kind === "custom_args") {
        // agy's lever lives in the agent itself; the parent owns that write.
        await onSave({ custom_args: plan.custom_args });
      } else {
        // Env levers cannot ride on `PUT /api/agents/{id}` (custom_env is
        // rejected with 400 there). Re-read the map and change ONLY the target
        // key, so an unrelated variable the user set is written back as-is —
        // values the server masked as "****" are preserved by its guard.
        const envResponse = await api.getAgentEnv(agent.id);
        const nextEnv = {
          ...(envResponse.custom_env ?? {}),
          [plan.key]: plan.value,
        };
        const saved = await api.updateAgentEnv(agent.id, {
          custom_env: nextEnv,
        });
        queryClient.setQueryData(agentEnvQueryKey(wsId, agent.id), saved);
      }
      toast.success(
        t(($) => $.tab_body.accounts.switch_saved_toast, {
          account: target
            ? `${agentCliLabel(target.cli)} · ${target.account}`
            : "",
        }),
      );
      setDrawerOpen(false);
      setSelectedKey(null);
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.tab_body.accounts.switch_failed_toast),
      );
    } finally {
      setSaving(false);
    }
  };

  const handleRetry = () => {
    // The account list rides on the runtime row, so a retry is a runtime read.
    void queryClient.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
  };

  const daemonHref = slug ? paths.workspace(slug).runtimes() : null;

  return (
    <div className="space-y-6">
      <p className="max-w-2xl text-pretty text-body leading-6 text-muted-foreground">
        {t(($) => $.tab_body.accounts.intro)}
      </p>

      {viewState.kind === "loading" ? <AccountsLoading /> : null}

      {viewState.kind === "error" ? (
        <div className="rounded-lg border border-destructive/30 bg-background p-5 text-center">
          <span className="mx-auto flex size-9 items-center justify-center rounded-lg bg-destructive/10 text-destructive">
            <AlertTriangle className="size-4" aria-hidden="true" />
          </span>
          <p className="mt-3 text-body font-medium">
            {t(($) => $.tab_body.accounts.error_title)}
          </p>
          <p className="mx-auto mt-1 max-w-lg text-pretty text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.accounts.error_description)}
          </p>
          <p
            className="mx-auto mt-2 max-w-lg truncate font-mono text-micro text-muted-foreground"
            translate="no"
          >
            {viewState.message}
          </p>
          <div className="mt-4 flex flex-wrap justify-center gap-2">
            <Button type="button" size="sm" onClick={handleRetry}>
              {t(($) => $.tab_body.accounts.error_retry_action)}
            </Button>
            {daemonHref ? (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => navigation.push(daemonHref)}
              >
                {t(($) => $.tab_body.accounts.error_daemon_action)}
              </Button>
            ) : null}
          </div>
        </div>
      ) : null}

      {viewState.kind === "empty" ? (
        <AccountsEmpty provider={runtimeDevice?.provider ?? ""} />
      ) : null}

      {viewState.kind === "ready" ? (
        <>
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-border bg-surface p-3.5">
            {current ? (
              <>
                <AgentCliBadge cli={current.cli} showLabel={false} />
                <div className="min-w-0 flex-1 basis-48">
                  <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="text-caption text-muted-foreground">
                      {t(($) => $.tab_body.accounts.summary_current)}
                    </span>
                    <span className="min-w-0 truncate text-caption font-semibold">
                      {`${agentCliLabel(current.cli)} · ${current.account}`}
                    </span>
                    <AccountActivePill />
                  </div>
                  <p
                    className="mt-0.5 truncate font-mono text-micro text-muted-foreground"
                    translate="no"
                  >
                    {summaryLine(current)}
                  </p>
                </div>
                <div className="ms-auto flex shrink-0 items-center gap-2">
                  <AccountStatusPill account={current} nowMs={nowMs} />
                  {canManageAccounts(viewState) ? (
                    <Button type="button" size="sm" onClick={openDrawer}>
                      {t(($) => $.tab_body.accounts.manage_action)}
                    </Button>
                  ) : null}
                </div>
              </>
            ) : (
              <>
                <div className="min-w-0 flex-1">
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.tab_body.accounts.summary_unknown)}
                  </p>
                </div>
                {canManageAccounts(viewState) ? (
                  <Button type="button" size="sm" onClick={openDrawer}>
                    {t(($) => $.tab_body.accounts.manage_action)}
                  </Button>
                ) : null}
              </>
            )}
          </div>

          {others.length > 0 ? (
            <div className="rounded-lg border border-border bg-surface p-3.5">
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.accounts.others_title)}
              </p>
              <p className="mt-0.5 text-micro text-muted-foreground">
                {t(($) => $.tab_body.accounts.others_hint)}
              </p>
              {/* Read-only by design: a chip never switches the account. */}
              <div className="mt-2.5 flex flex-wrap gap-2">
                {others.map((account) => (
                  <span
                    key={accountKey(account)}
                    className="inline-flex max-w-full items-center gap-1.5 rounded-full border border-border bg-muted px-2 py-0.5 text-micro text-muted-foreground"
                  >
                    <AgentCliBadge cli={account.cli} showLabel={false} />
                    <span className="truncate">
                      {`${agentCliLabel(account.cli)} · ${account.account}`}
                    </span>
                  </span>
                ))}
              </div>
            </div>
          ) : null}

          <AgentAccountDrawer
            open={drawerOpen}
            onOpenChange={handleDrawerOpenChange}
            groups={groups}
            current={current}
            selectedKey={selectedKey}
            onSelect={(account) => setSelectedKey(accountKey(account))}
            dirty={dirty}
            saving={saving}
            onSave={() => void handleSaveAndSwitch()}
            nowMs={nowMs}
          />
        </>
      ) : null}
    </div>
  );
}

/** `LEVER=directory · base_url`, with the lever omitted when the CLI has none. */
function summaryLine(account: AgentAccount): string {
  const lever = accountLeverLabel(account.lever);
  const head = lever ? `${lever}=${account.home}` : account.home;
  return account.base_url ? `${head} · ${account.base_url}` : head;
}

function AccountsLoading() {
  const { t } = useT("agents");
  return (
    <div className="rounded-lg border border-border bg-background p-4">
      <Skeleton className="h-4 w-40" />
      <div className="mt-3 space-y-3">
        {[0, 1, 2].map((index) => (
          <div key={index} className="flex items-center gap-3">
            <Skeleton className="size-5 shrink-0 rounded-md" />
            <div className="min-w-0 flex-1 space-y-1.5">
              <Skeleton className="h-3 w-1/3" />
              <Skeleton className="h-2.5 w-1/2" />
            </div>
            <Skeleton className="h-5 w-16 shrink-0 rounded-full" />
          </div>
        ))}
      </div>
      <p className="mt-3 text-caption text-muted-foreground">
        {t(($) => $.tab_body.accounts.loading_text)}
      </p>
    </div>
  );
}

function AccountsEmpty({ provider }: { provider: string }) {
  const { t } = useT("agents");
  const [picked, setPicked] = useState<EmptyChoice | null>(null);

  const cli = cliForProvider(provider);
  const defaultChoice: EmptyChoice =
    cli === "agy" ? "agy" : cli === "dsh" ? "dsh" : "manual";
  const choice = picked ?? defaultChoice;

  const lever = cli ? CLI_LEVER[cli] : "";
  const leverLabel = accountLeverLabel(lever);

  const choices: { id: EmptyChoice; label: string }[] = [
    {
      id: "dsh",
      label: t(($) => $.tab_body.accounts.empty_action_dsh),
    },
    {
      id: "agy",
      label: t(($) => $.tab_body.accounts.empty_action_agy),
    },
    {
      id: "manual",
      label: t(($) => $.tab_body.accounts.empty_action_manual),
    },
  ];

  return (
    <div className="rounded-lg border border-border bg-background p-6 text-center">
      <span className="mx-auto flex size-10 items-center justify-center rounded-lg bg-muted text-muted-foreground">
        <FolderTree className="size-4" aria-hidden="true" />
      </span>
      <p className="mt-3 text-body font-medium">
        {t(($) => $.tab_body.accounts.empty_title)}
      </p>
      <p className="mx-auto mt-1 max-w-lg text-pretty text-caption leading-5 text-muted-foreground">
        {t(($) => $.tab_body.accounts.empty_description)}
      </p>

      <div className="mt-4 flex flex-wrap justify-center gap-2">
        {choices.map((item) => (
          <Button
            key={item.id}
            type="button"
            size="sm"
            variant={choice === item.id ? "brandSubtle" : "outline"}
            aria-pressed={choice === item.id}
            onClick={() => setPicked(item.id)}
          >
            {item.label}
          </Button>
        ))}
      </div>

      <div className="mt-5 space-y-2 border-t border-border pt-4 text-left">
        {choice === "manual" ? (
          <p className="text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.accounts.empty_hint_manual)}
          </p>
        ) : (
          <>
            <p className="text-caption leading-5 text-muted-foreground">
              {choice === "dsh"
                ? t(($) => $.tab_body.accounts.empty_hint_dsh)
                : t(($) => $.tab_body.accounts.empty_hint_agy)}
            </p>
            <CommandBlock command={accountLoginCommand(emptyEntryAccount(choice))} />
          </>
        )}
        <p className="text-micro text-muted-foreground">
          {leverLabel
            ? t(($) => $.tab_body.accounts.empty_runtime, {
                provider: provider ? providerDisplayName(provider) : "—",
                lever: leverLabel,
              })
            : t(($) => $.tab_body.accounts.empty_runtime_no_lever, {
                provider: provider ? providerDisplayName(provider) : "—",
              })}
        </p>
      </div>
    </div>
  );
}
