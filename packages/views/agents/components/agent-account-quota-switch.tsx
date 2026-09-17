"use client";

// One-click recovery for "this agent's account is out of quota" (DENE-466).
//
// The account tab can already switch an account, but only through open drawer →
// pick row → save and switch, and only from inside agent settings. This is the
// other half: the prompt renders wherever the exhaustion is noticed, and the
// single button completes the switch.
//
// Everything it decides comes from `agent-accounts-model.ts` — which account is
// in effect, whether it is exhausted, where the switch goes, and what write that
// switch is. This file renders that and performs the one write.

import { useQuery } from "@tanstack/react-query";
import { ArrowRightLeft } from "lucide-react";
import { toast } from "sonner";
import {
  AGENT_ENV_STALE_TIME_MS,
  agentEnvQueryKey,
  useSwitchAgentAccount,
} from "@multica/core/agents";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import type { Agent, RuntimeDevice } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import {
  type AgentAccountSlotsBinding,
  accountStatus,
  accountSwitchWrite,
  accountsViewState,
  nextAvailableAccount,
  parseAccountLever,
  parseAgentAccounts,
  planAccountSwitch,
  resolveCurrentAccount,
} from "./tabs/agent-accounts-model";
import { agentCliLabel } from "./tabs/agent-account-drawer";
import { runtimeHomeDir } from "./tabs/agy-account-slots";

export interface AgentAccountQuotaSwitchProps {
  agent: Agent;
  runtime: RuntimeDevice | null | undefined;
  /** Injected by tests and by surfaces that already tick a clock. */
  nowMs?: number;
  className?: string;
}

/**
 * Renders nothing unless the agent's current account is actually exhausted:
 * this is an alert with a recovery action attached, not a control that belongs
 * on screen at rest.
 */
export function AgentAccountQuotaSwitch({
  agent,
  runtime,
  nowMs = Date.now(),
  className,
}: AgentAccountQuotaSwitchProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const switchAccount = useSwitchAgentAccount(agent.id);

  const parsed = parseAgentAccounts(runtime);
  // An env lever is only readable through the audited env endpoint, and without
  // it a bound DSH_HOME reads as the CLI default — which would name the wrong
  // account as the exhausted one. Agents with no env lever never pay for it.
  const needsEnvBinding = parsed.accounts.some(
    (account) => parseAccountLever(account.lever).kind === "env",
  );
  const envQuery = useQuery({
    queryKey: agentEnvQueryKey(wsId, agent.id),
    queryFn: () => api.getAgentEnv(agent.id),
    enabled: !!agent.id && needsEnvBinding,
    staleTime: AGENT_ENV_STALE_TIME_MS,
    refetchOnWindowFocus: false,
  });

  const binding: AgentAccountSlotsBinding = {
    custom_args: agent.custom_args,
    custom_env: envQuery.data?.custom_env,
    runtime_config: agent.runtime_config,
    runtime_home: runtimeHomeDir(runtime),
    provider: runtime?.provider,
  };

  const viewState = accountsViewState({
    accounts: parsed.accounts,
    error: parsed.error,
    loading: needsEnvBinding && envQuery.isPending,
  });
  // A list that is loading, empty or in error never produces a write — the same
  // rule the accounts tab follows, for the same reason.
  if (viewState.kind !== "ready") return null;

  const current = resolveCurrentAccount(binding, parsed.accounts);
  if (!current) return null;
  const status = accountStatus(current, nowMs);
  if (status.kind !== "quota_exhausted") return null;

  const target = nextAvailableAccount(binding, parsed.accounts, nowMs);
  const plan = target ? planAccountSwitch(binding, target, viewState) : null;
  const write = target && plan ? accountSwitchWrite(binding, target, plan) : null;

  const currentLabel = `${agentCliLabel(current.cli)} · ${current.account}`;
  const targetLabel = target ? `${agentCliLabel(target.cli)} · ${target.account}` : "";
  const backAt = new Date(status.reset_at_ms).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });

  const handleSwitch = async () => {
    if (!write || !target) return;
    try {
      await switchAccount.mutateAsync(write);
      toast.success(
        t(($) => $.tab_body.accounts.switch_saved_toast, { account: targetLabel }),
      );
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.tab_body.accounts.switch_failed_toast),
      );
    }
  };

  return (
    <section
      className={`rounded-lg border border-destructive/30 bg-destructive/5 p-3.5 ${className ?? ""}`}
      aria-live="polite"
    >
      <p className="text-caption font-medium text-foreground">
        {t(($) => $.tab_body.accounts.quota_switch_title, {
          account: currentLabel,
          time: backAt,
        })}
      </p>
      {write && target ? (
        <>
          <p className="mt-1 text-micro text-muted-foreground">
            {t(($) => $.tab_body.accounts.quota_switch_hint, { account: targetLabel })}
          </p>
          <Button
            type="button"
            size="sm"
            className="mt-2.5"
            disabled={switchAccount.isPending}
            onClick={() => void handleSwitch()}
          >
            <ArrowRightLeft className="size-3.5" aria-hidden="true" />
            {t(($) => $.tab_body.accounts.quota_switch_action, { account: targetLabel })}
          </Button>
        </>
      ) : (
        <p className="mt-1 text-micro text-muted-foreground">
          {t(($) => $.tab_body.accounts.quota_switch_no_target)}
        </p>
      )}
    </section>
  );
}
