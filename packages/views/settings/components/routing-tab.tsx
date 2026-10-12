"use client";

import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { runtimeListOptions } from "@multica/core/runtimes";
import { memberListOptions } from "@multica/core/workspace/queries";
import {
  roleHealth,
  type RoutingHealth,
  type RoutingRoleHealth,
  type RoutingRule,
  type RoutingRuleTable,
} from "@multica/core/workspace/routing-health";
import { DEFAULT_ROUTING_POLICY_PROMPT } from "@multica/core/workspace/routing-policy-prompt";
import {
  routingGatewayIsComplete,
  type RoutingState,
} from "@multica/core/workspace/routing-settings";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";
import { resolveSettingsLocation, settingsHref } from "./settings-navigation";
import { useRoutingForm, type RoutingForm } from "./use-routing-form";
import { RoutingSeatsTable } from "./routing-seats-table";
import { RoutingDomainsSection } from "./routing-domains";
import { RoutingDispatchSection } from "./routing-dispatch";
import { RuntimePicker } from "../../agents/components/runtime-picker";
import { ModelDropdown } from "../../agents/components/model-dropdown";
import { ThinkingSettingField } from "../../agents/components/inspector/thinking-prop-row";

/** The sub-pages, opened with `?tab=routing&integration=<page>`. */
export const ROUTING_PAGES = ["model", "rules", "seats"] as const;
type RoutingPage = (typeof ROUTING_PAGES)[number];

function isRoutingPage(value: string | null): value is RoutingPage {
  return (ROUTING_PAGES as readonly string[]).includes(value ?? "");
}

/**
 * The routing page (DENE-1966): four rows and a short "more" fold.
 *
 * Routing has one path — the rule table sets the tier and the judge may raise
 * it one rung — so the page only offers what still changes that path: the
 * switch, the model that reads tickets, the division rules, and the seats.
 * Knobs that no longer have a control keep their stored values; the form
 * hook carries them through every save.
 *
 * Each of the three detail rows opens a sub-page through the URL, so the
 * browser's back button and the phone back stack both work.
 */
export function RoutingTab() {
  const form = useRoutingForm();
  const navigation = useNavigation();
  const requested = resolveSettingsLocation(navigation.searchParams).integration;
  const href = (page?: RoutingPage) =>
    settingsHref(navigation.pathname, navigation.searchParams, "routing", { integration: page });

  if (isRoutingPage(requested)) {
    return <RoutingSubPage page={requested} form={form} backHref={href()} />;
  }
  return <RoutingOverview form={form} href={href} />;
}

function SaveState({ form }: { form: RoutingForm }) {
  const { t } = useT("settings");
  return (
    <SettingsSaveState
      status={form.autoSave.status}
      savingLabel={t(($) => $.routing.policy_saving)}
      savedLabel={t(($) => $.routing.policy_saved)}
      errorLabel={t(($) => $.routing.policy_save_error)}
    />
  );
}

function RoutingOverview({
  form,
  href,
}: {
  form: RoutingForm;
  href: (page?: RoutingPage) => string;
}) {
  const { t } = useT("settings");
  const { draft, canManage } = form;
  const modelName = draft.analysis.model.trim();
  return (
    <SettingsTab
      title={t(($) => $.routing.title)}
      description={t(($) => $.routing.description)}
      actions={<SaveState form={form} />}
    >
      <SettingsSection>
        <StateLine
          state={form.state}
          health={form.health.data}
          canManage={canManage}
          checking={form.recheck.isPending}
          onRecheck={() => form.recheck.mutate()}
        />
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.routing.enabled_label)}
            description={t(($) => $.routing.enabled_description)}
          >
            <Switch
              checked={draft.enabled}
              disabled={!canManage}
              onCheckedChange={(enabled) => form.set({ enabled })}
              aria-label={t(($) => $.routing.enabled_label)}
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.routing.model_row_label)}
            description={t(($) => $.routing.model_row_description)}
          >
            <RowLink href={href("model")}>
              {modelName || t(($) => $.routing.model_unset)}
            </RowLink>
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.routing.rules_row_label)}
            description={t(($) => $.routing.rules_row_description)}
          >
            <RowLink href={href("rules")}>
              {canManage ? t(($) => $.routing.row_edit) : t(($) => $.routing.row_view)}
            </RowLink>
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.routing.seats_row_label)}
            description={t(($) => $.routing.seats_row_description)}
          >
            <RowLink href={href("seats")}>{t(($) => $.routing.row_view)}</RowLink>
          </SettingsRow>
        </SettingsCard>
        {canManage ? null : <LockedNote />}
      </SettingsSection>

      <details className="group">
        <summary className="flex min-h-11 cursor-pointer items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
          <ChevronRight aria-hidden="true" className="size-4 transition-transform group-open:rotate-90" />
          {t(($) => $.routing.more)}
        </summary>
        <div className="mt-3">
          <MoreSettings form={form} />
        </div>
      </details>
    </SettingsTab>
  );
}

/** A row's way into its sub-page: the current value, then a chevron. */
function RowLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <AppLink
      href={href}
      className="-mr-2 flex min-h-11 min-w-0 max-w-48 items-center gap-1 rounded-md px-2 text-body text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring sm:max-w-64"
    >
      <span className="truncate">{children}</span>
      <ChevronRight aria-hidden="true" className="size-4 shrink-0" />
    </AppLink>
  );
}

function LockedNote() {
  const { t } = useT("settings");
  return (
    <p className="px-0.5 text-caption leading-5 text-muted-foreground">
      {t(($) => $.routing.locked_member)}
    </p>
  );
}

/** The five knobs that stay adjustable outside the four rows. */
function MoreSettings({ form }: { form: RoutingForm }) {
  const { t } = useT("settings");
  const { draft, canManage } = form;
  return (
    <SettingsCard>
      <SettingsRow
        label={t(($) => $.routing.stale_hours_label)}
        description={t(($) => $.routing.stale_hours_description)}
        size="code"
      >
        <Input
          type="number"
          min={1}
          max={720}
          step={1}
          value={form.staleHoursText}
          disabled={!canManage}
          onChange={(e) => form.setStaleHoursText(e.target.value)}
          aria-label={t(($) => $.routing.stale_hours_label)}
        />
      </SettingsRow>
      <SettingsRow
        label={t(($) => $.routing.strongest_gate_label)}
        description={t(($) => $.routing.strongest_gate_description)}
      >
        <Switch
          checked={draft.strongest_gate}
          disabled={!canManage}
          onCheckedChange={(strongest_gate) => form.set({ strongest_gate })}
          aria-label={t(($) => $.routing.strongest_gate_label)}
        />
      </SettingsRow>
      <SettingsRow
        label={t(($) => $.routing.load_label)}
        description={
          draft.prefer_idle
            ? t(($) => $.routing.load_description_on)
            : t(($) => $.routing.load_description_shadow)
        }
      >
        <Switch
          checked={draft.prefer_idle}
          disabled={!canManage}
          onCheckedChange={(prefer_idle) => form.set({ prefer_idle })}
          aria-label={t(($) => $.routing.load_label)}
        />
      </SettingsRow>
      <SettingsRow
        label={t(($) => $.routing.learn_label)}
        description={
          draft.learn_from_outcomes
            ? t(($) => $.routing.learn_description_on)
            : t(($) => $.routing.learn_description_shadow)
        }
      >
        <Switch
          checked={draft.learn_from_outcomes}
          disabled={!canManage}
          onCheckedChange={(learn_from_outcomes) => form.set({ learn_from_outcomes })}
          aria-label={t(($) => $.routing.learn_label)}
        />
      </SettingsRow>
      <SettingsRow
        label={t(($) => $.routing.consult_limit_label)}
        description={t(($) => $.routing.consult_limit_description)}
        size="code"
      >
        <Input
          type="number"
          min={1}
          max={20}
          value={form.consultText}
          disabled={!canManage}
          onChange={(e) => form.setConsultText(e.target.value)}
          aria-label={t(($) => $.routing.consult_limit_label)}
        />
      </SettingsRow>
    </SettingsCard>
  );
}

function RoutingSubPage({
  page,
  form,
  backHref,
}: {
  page: RoutingPage;
  form: RoutingForm;
  backHref: string;
}) {
  const { t } = useT("settings");
  const title = {
    model: t(($) => $.routing.model_row_label),
    rules: t(($) => $.routing.rules_row_label),
    seats: t(($) => $.routing.seats_row_label),
  }[page];
  const description = {
    model: t(($) => $.routing.model_page_description),
    rules: t(($) => $.routing.rules_page_description),
    seats: t(($) => $.routing.seats_page_description),
  }[page];
  return (
    <SettingsTab
      title={title}
      description={description}
      actions={page === "seats" ? null : <SaveState form={form} />}
      breadcrumb={
        <nav
          aria-label={t(($) => $.routing.title)}
          className="flex items-center gap-1.5 text-caption text-muted-foreground"
        >
          <AppLink
            href={backHref}
            className="flex min-h-11 items-center rounded-sm hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
          >
            {t(($) => $.routing.title)}
          </AppLink>
          <ChevronRight aria-hidden="true" className="size-3" />
          <span aria-current="page" className="truncate text-foreground">
            {title}
          </span>
        </nav>
      }
    >
      {page === "model" ? <ModelBlock form={form} /> : null}
      {page === "rules" ? <RulesBlock form={form} /> : null}
      {page === "seats" ? <SeatsBlock form={form} /> : null}
      {form.canManage || page === "seats" ? null : <LockedNote />}
    </SettingsTab>
  );
}

/** The model that reads each ticket and answers the rule table's questions. */
function ModelBlock({ form }: { form: RoutingForm }) {
  const { t } = useT("settings");
  const { draft, canManage, wsId } = form;
  const { data: runtimes = [], isLoading: runtimesLoading } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const analysis = draft.analysis;
  const source = analysis.source ?? "api_gateway";
  const runtimeId = analysis.runtime_id ?? "";
  const runtime = runtimes.find((r) => r.id === runtimeId) ?? null;
  const analysisHealth = roleHealth(form.health.data, "analysis");
  return (
    <SettingsSection>
      <SettingsCard>
        <SettingsRow
          label={t(($) => $.routing.analysis_source_label)}
          description={t(($) => $.routing.analysis_source_description)}
          size="text"
        >
          <select
            className="h-11 w-full rounded-md border border-border bg-background px-3 text-sm"
            value={source}
            disabled={!canManage}
            onChange={(e) => form.setAnalysis({ source: e.target.value as typeof source })}
            aria-label={t(($) => $.routing.analysis_source_label)}
          >
            <option value="api_gateway">{t(($) => $.routing.analysis_source_api_gateway)}</option>
            <option value="runtime_subscription">{t(($) => $.routing.analysis_source_runtime)}</option>
          </select>
        </SettingsRow>
        {source === "api_gateway" ? (
          <>
            <SettingsRow
              label={t(($) => $.routing.analysis_model_label)}
              description={t(($) => $.routing.analysis_model_description)}
              size="text"
            >
              <Input
                value={analysis.model}
                disabled={!canManage}
                placeholder={t(($) => $.routing.model_placeholder)}
                onChange={(e) => form.setAnalysis({ enabled: true, model: e.target.value })}
                aria-label={t(($) => $.routing.analysis_model_label)}
              />
            </SettingsRow>
            <EndpointRows
              urlLabel={t(($) => $.routing.analysis_url_label)}
              keyLabel={t(($) => $.routing.analysis_key_label)}
              baseUrl={analysis.base_url}
              onBaseUrl={(base_url) => form.setAnalysis({ base_url })}
              keyInput={form.analysisKeyInput}
              onKeyInput={form.setAnalysisKeyInput}
              endpoint={analysisHealth}
              health={form.health.data}
              canManage={canManage}
              saving={form.saveKey.isPending}
              onSaveKey={(key) => form.saveKey.mutate({ role: "analysis", key })}
            />
          </>
        ) : (
          <>
            <RuntimePicker
              runtimes={runtimes}
              runtimesLoading={runtimesLoading}
              members={members}
              currentUserId={form.userId}
              selectedRuntimeId={runtimeId}
              onSelect={(runtime_id) => form.setAnalysis({ runtime_id })}
              disabled={!canManage}
            />
            <ModelDropdown
              runtimeId={runtime?.id ?? null}
              runtimeOnline={runtime?.status === "online"}
              value={analysis.model}
              onChange={(model) => form.setAnalysis({ model })}
              disabled={!canManage || !analysis.enabled || !runtime}
            />
            <ThinkingSettingField
              label={t(($) => $.routing.analysis_thinking_label)}
              runtimeId={runtime?.id ?? null}
              runtimeOnline={runtime?.status === "online"}
              provider={runtime?.provider ?? ""}
              model={analysis.model}
              value={analysis.thinking_level ?? "low"}
              canEdit={canManage && analysis.enabled && !!runtime}
              onChange={(thinking_level) => form.setAnalysis({ thinking_level })}
            />
          </>
        )}
      </SettingsCard>
      {source === "api_gateway" ? (
        <>
          <RoleEndpointNote health={form.health.data} role={analysisHealth} />
          <GatewayPairNote
            baseUrl={analysis.base_url}
            keyStored={analysisHealth?.gateway_key_set === true}
          />
        </>
      ) : null}
    </SettingsSection>
  );
}

/** Who does what: the division rules, the prompt, and the tier table. */
function RulesBlock({ form }: { form: RoutingForm }) {
  const { t } = useT("settings");
  const { draft, canManage } = form;
  const defaultPrompt =
    form.health.data?.default_policy_prompt?.trim() || DEFAULT_ROUTING_POLICY_PROMPT;
  const prompt = draft.policy_prompt ?? "";
  const shownPrompt = prompt.trim() === "" ? defaultPrompt : prompt;
  return (
    <>
      <RoutingDispatchSection wsId={form.wsId} canManage={canManage} />
      <SettingsSection
        title={t(($) => $.routing.policy_label)}
        description={t(($) => $.routing.policy_description)}
        action={
          canManage ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => form.set({ policy_prompt: "" })}
            >
              {t(($) => $.routing.policy_restore)}
            </Button>
          ) : null
        }
      >
        <Textarea
          value={shownPrompt}
          disabled={!canManage}
          rows={8}
          aria-label={t(($) => $.routing.policy_label)}
          onChange={(event) => {
            const next = event.target.value;
            form.set({ policy_prompt: next.trim() === defaultPrompt.trim() ? "" : next });
          }}
          className="min-h-36 text-caption leading-5"
        />
        {form.autoSave.status === "error" ? (
          <p className="text-caption text-destructive" role="alert">
            {t(($) => $.routing.policy_save_error)}
          </p>
        ) : null}
      </SettingsSection>
      <RuleTableSection table={form.health.data?.rules} />
    </>
  );
}

/** Which seat takes which tier, and what each one is good at. */
function SeatsBlock({ form }: { form: RoutingForm }) {
  const { t } = useT("settings");
  return (
    <>
      <SettingsSection
        title={t(($) => $.routing.seats_title)}
        description={t(($) => $.routing.filters_placeholder)}
      >
        <SettingsCard>
          <RoutingSeatsTable wsId={form.wsId} canManage={form.canManage} />
        </SettingsCard>
      </SettingsSection>
      <RoutingDomainsSection wsId={form.wsId} canManage={form.canManage} />
    </>
  );
}

/**
 * One role's endpoint pair: URL with the ordinary fields, key behind its own
 * button. Shared by both roles so the two cards cannot drift apart.
 */
function EndpointRows({
  urlLabel,
  urlDescription,
  keyLabel,
  baseUrl,
  onBaseUrl,
  keyInput,
  onKeyInput,
  endpoint,
  health,
  canManage,
  saving,
  onSaveKey,
}: {
  urlLabel: string;
  urlDescription?: string;
  keyLabel: string;
  baseUrl: string;
  onBaseUrl: (v: string) => void;
  keyInput: string;
  onKeyInput: (v: string) => void;
  endpoint?: RoutingRoleHealth;
  health?: RoutingHealth;
  canManage: boolean;
  saving: boolean;
  onSaveKey: (key: string) => void;
}) {
  const { t } = useT("settings");
  const keySet = endpoint?.gateway_key_set === true;
  const unstorable = health?.workspace_key_storable === false;
  return (
    <>
      <SettingsRow
        label={urlLabel}
        description={urlDescription ?? t(($) => $.routing.analysis_url_description)}
        size="text"
      >
        <Input
          value={baseUrl}
          disabled={!canManage}
          placeholder={t(($) => $.routing.gateway_url_placeholder)}
          onChange={(e) => onBaseUrl(e.target.value)}
          aria-label={urlLabel}
        />
      </SettingsRow>

      <SettingsRow
        label={keyLabel}
        description={
          unstorable
            ? t(($) => $.routing.gateway_key_unstorable)
            : t(($) => $.routing.gateway_key_description)
        }
        size="text"
      >
        <div className="flex w-full items-center gap-2">
          <Input
            type="password"
            value={keyInput}
            autoComplete="off"
            disabled={!canManage || unstorable || saving}
            // The placeholder is the whole readback: the stored key never
            // leaves the server, so "a key is saved" is the most this box
            // can honestly say.
            placeholder={
              keySet
                ? t(($) => $.routing.gateway_key_stored)
                : t(($) => $.routing.gateway_key_placeholder)
            }
            onChange={(e) => onKeyInput(e.target.value)}
            aria-label={keyLabel}
          />
          <Button
            type="button"
            size="sm"
            disabled={!canManage || keyInput.trim() === "" || saving}
            onClick={() => onSaveKey(keyInput)}
            className="shrink-0"
          >
            {t(($) => $.routing.gateway_key_save)}
          </Button>
          {keySet && canManage ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={saving}
              onClick={() => onSaveKey("")}
              className="shrink-0"
            >
              {t(($) => $.routing.gateway_key_clear)}
            </Button>
          ) : null}
        </div>
      </SettingsRow>
    </>
  );
}

/** Where the analysis model's calls go. */
function RoleEndpointNote({
  health,
  role,
}: {
  health?: RoutingHealth;
  role?: RoutingRoleHealth;
}) {
  const { t } = useT("settings");
  if (!health || !role?.gateway_host) return null;
  return (
    <p className="px-0.5 text-caption leading-5 text-muted-foreground">
      {role.gateway_scope === "workspace"
        ? t(($) => $.routing.gateway_endpoint_workspace, { host: role.gateway_host })
        : t(($) => $.routing.gateway_endpoint_deployment, { host: role.gateway_host })}
    </p>
  );
}

/**
 * The rule table that sets the tier (DENE-1677), read-only: it ships with the
 * server, so there is nothing here to edit. Hidden on a backend that predates
 * it rather than shown empty.
 */
function RuleTableSection({ table }: { table: RoutingRuleTable | undefined }) {
  const { t } = useT("settings");
  // `?.`: a report that skipped the parser (a mocked client) has no table.
  if (!table?.rules?.length) return null;
  return (
    <SettingsSection
      title={t(($) => $.routing.rules_title)}
      description={t(($) => $.routing.rules_description)}
    >
      <SettingsCard>
        {table.rules.map((rule) => (
          <SettingsRow
            key={rule.id}
            label={rule.label}
            description={ruleCondition(rule, table, {
              anyUnknown: t(($) => $.routing.rules_any_unknown),
              catchAll: t(($) => $.routing.rules_catch_all),
            })}
          >
            <span className="whitespace-nowrap text-body text-muted-foreground">
              {t(($) => $.routing.rules_auto_review, { tier: rule.tier_label })}
            </span>
          </SettingsRow>
        ))}
      </SettingsCard>
    </SettingsSection>
  );
}

/** A row's condition in the questions' own words, in question order. */
export function ruleCondition(
  rule: RoutingRule,
  table: RoutingRuleTable,
  words: { anyUnknown: string; catchAll: string },
): string {
  if (rule.any_unknown) return words.anyUnknown;
  const parts = table.questions.flatMap((q) => {
    const values = rule.when[q.key];
    if (!values?.length) return [];
    const labels = values.map((v) => q.options.find((o) => o.value === v)?.label ?? v);
    return [`${q.label}：${labels.join(" / ")}`];
  });
  return parts.length ? parts.join(" · ") : words.catchAll;
}

/**
 * The half-filled pair.
 *
 * An endpoint with no key, or a key with no endpoint, is not an error — it
 * falls back whole to the deployment gateway, because sending the deployment
 * key to a host the operator never chose, or a workspace key to the
 * deployment's host, are both worse than doing nothing. But a silent fallback
 * is how somebody spends an afternoon wondering why their own model is never
 * called, so it is stated.
 */
function GatewayPairNote({
  baseUrl,
  keyStored,
}: {
  baseUrl: string;
  keyStored: boolean;
}) {
  const { t } = useT("settings");
  const typedSomething = baseUrl.trim() !== "" || keyStored;
  if (!typedSomething || routingGatewayIsComplete(baseUrl, keyStored)) {
    return null;
  }
  return (
    <p className="px-0.5 text-caption leading-5 text-warning-foreground">
      {t(($) => $.routing.gateway_pair_incomplete)}
    </p>
  );
}

/**
 * The four states, each with its own colour and its own sentence.
 *
 * `incomplete` is the one that must never be collapsed into `enabled`:
 * somebody who flipped the switch and walked away will otherwise believe
 * routing is working while the product behaves exactly as it did before.
 */
function StateLine({
  state,
  health,
  canManage,
  checking,
  onRecheck,
}: {
  state: RoutingState;
  health?: RoutingHealth;
  canManage: boolean;
  checking: boolean;
  onRecheck: () => void;
}) {
  const { t } = useT("settings");
  const dot: Record<RoutingState, string> = {
    off: "bg-muted-foreground/40",
    incomplete: "bg-warning",
    enabled: "bg-success",
    ineffective: "bg-destructive",
  };

  const lastCheckedLabel = (unixSeconds: number): string => {
    const { unit, n } = timeAgoBucket(unixSeconds);
    if (unit === "now") return t(($) => $.routing.health_time_just_now);
    if (unit === "minutes") return t(($) => $.routing.health_time_minutes_ago, { n });
    return t(($) => $.routing.health_time_hours_ago, { n });
  };

  let detail: string = t(($) => $.routing.states[state].detail);
  if (state === "ineffective" && health?.reason) {
    detail = `${detail} ${t(($) => $.routing.health_reason_label)}: ${health.reason}`;
  } else if (state === "enabled") {
    detail = health?.last_success_at
      ? t(($) => $.routing.health_connected, { when: lastCheckedLabel(health.last_success_at) })
      : t(($) => $.routing.health_never_checked);
  }
  if (state === "ineffective" && health?.retry_after_seconds) {
    detail = `${detail} ${t(($) => $.routing.health_retry_in, {
      minutes: Math.max(1, Math.round(health.retry_after_seconds / 60)),
    })}`;
  }

  return (
    <div className="flex min-h-11 items-center gap-3 px-0.5">
      <span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full", dot[state])} />
      <p className="min-w-0 flex-1 text-caption leading-5 text-muted-foreground">
        <span className="font-medium text-foreground" data-state={state}>
          {t(($) => $.routing.states[state].chip)}
        </span>
        {" · "}
        {detail}
      </p>
      {/* Offered for both live states: somebody who just typed a model id
          wants to know it works before a ticket finds out for them. */}
      {canManage && (state === "enabled" || state === "ineffective") ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={checking}
          onClick={onRecheck}
          className="shrink-0"
        >
          {checking ? t(($) => $.routing.health_checking) : t(($) => $.routing.health_recheck)}
        </Button>
      ) : null}
    </div>
  );
}

/**
 * How long ago, as a unit plus a count. The caller resolves the copy, because
 * only it holds a properly typed `t`.
 *
 * Deliberately coarse: what this describes is "the last time the routing model
 * answered", and the reader wants to know whether that was recent, not when
 * exactly.
 */
export function timeAgoBucket(
  unixSeconds: number,
  now: number = Date.now(),
): { unit: "now" | "minutes" | "hours"; n: number } {
  const minutes = Math.floor((now / 1000 - unixSeconds) / 60);
  if (minutes < 1) return { unit: "now", n: 0 };
  if (minutes < 60) return { unit: "minutes", n: minutes };
  return { unit: "hours", n: Math.floor(minutes / 60) };
}
