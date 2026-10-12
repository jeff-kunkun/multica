"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { routingDispatchOptions, workspaceKeys } from "@multica/core/workspace/queries";
import type { DispatchRule, RoutingDispatch } from "@multica/core/workspace/routing-dispatch";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

/**
 * 分工规则 (DENE-1834 part 4). The owner writes who does what in plain
 * language; the routing judge reads rows out of it; the rows show here with
 * the sentence each came from; nothing takes effect until the person presses
 * 生效. Every confirmed save is a numbered revision listed underneath.
 *
 * Rows are edited locally only. A row touched by hand loses its "model"
 * origin so the server records it as 手改.
 */
export function RoutingDispatchSection({ wsId, canManage }: { wsId: string; canManage: boolean }) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const query = useQuery({ ...routingDispatchOptions(wsId), enabled: !!wsId });
  const data = query.data;
  const editable = canManage && (data?.editable ?? false);

  const [text, setText] = useState("");
  const [rules, setRules] = useState<DispatchRule[]>([]);
  const [dropped, setDropped] = useState<string[]>([]);
  // The rows on screen differ from the server's — a preview or a hand edit
  // that has not been confirmed yet.
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (!data || dirty) return;
    setText(data.policy.text);
    setRules(data.policy.rules);
  }, [data, dirty]);

  const fail = (err: unknown) =>
    toast.error(err instanceof Error ? err.message : t(($) => $.routing.dispatch_failed));

  const settle = (next: RoutingDispatch) => {
    qc.setQueryData(workspaceKeys.routingDispatch(wsId), next);
    void qc.invalidateQueries({ queryKey: workspaceKeys.list() });
    setText(next.policy.text);
    setRules(next.policy.rules);
    setDropped([]);
    setDirty(false);
  };

  const preview = useMutation({
    mutationFn: () => api.previewRoutingDispatch(wsId, text),
    onSuccess: (res) => {
      setRules(res.policy.rules);
      setDropped(res.dropped);
      setDirty(true);
    },
    onError: fail,
  });
  const confirm = useMutation({
    mutationFn: () => api.updateRoutingDispatch(wsId, { text, rules }),
    onSuccess: settle,
    onError: fail,
  });
  const reset = useMutation({
    mutationFn: () => api.updateRoutingDispatch(wsId, { reset: true }),
    onSuccess: settle,
    onError: fail,
  });

  const busy = preview.isPending || confirm.isPending || reset.isPending;
  const textChanged = text !== (data?.policy.text ?? "");
  const canPreview = editable && !busy && text.trim() !== "";
  const canConfirm = editable && !busy && dirty && rules.length > 0;

  const editRule = (index: number, patch: Partial<DispatchRule>) => {
    setRules((prev) =>
      prev.map((r, i) => (i === index ? { ...r, ...patch, origin: "" } : r)),
    );
    setDirty(true);
  };
  const removeRule = (index: number) => {
    setRules((prev) => prev.filter((_, i) => i !== index));
    setDirty(true);
  };

  const reason = !canManage
    ? t(($) => $.routing.dispatch_locked_role)
    : data && !data.editable
      ? t(($) => $.routing.dispatch_locked_agent)
      : null;

  return (
    <SettingsSection
      title={t(($) => $.routing.dispatch_title)}
      description={t(($) => $.routing.dispatch_description)}
      action={
        editable && !data?.is_default ? (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={busy}
            onClick={() => reset.mutate()}
          >
            {t(($) => $.routing.dispatch_reset)}
          </Button>
        ) : null
      }
    >
      <SettingsCard>
        <SettingsRow
          label={t(($) => $.routing.dispatch_text_label)}
          description={reason ?? t(($) => $.routing.dispatch_text_description)}
          size="text"
          align="start"
        >
          <div className="flex flex-col gap-2">
            <Textarea
              value={text}
              rows={4}
              disabled={!editable}
              aria-label={t(($) => $.routing.dispatch_text_label)}
              onChange={(e) => {
                setText(e.target.value);
                setDirty(true);
              }}
            />
            <div className="flex justify-end">
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={!canPreview}
                onClick={() => preview.mutate()}
              >
                {t(($) => $.routing.dispatch_preview)}
              </Button>
            </div>
          </div>
        </SettingsRow>

        {rules.length === 0 ? (
          <p className="px-4 py-3 text-sm text-muted-foreground">
            {t(($) => $.routing.dispatch_no_rules)}
          </p>
        ) : (
          rules.map((rule, index) => (
            <DispatchRuleRow
              key={rule.id || `row-${index}`}
              rule={rule}
              series={data?.series ?? []}
              tiers={data?.tiers ?? []}
              editable={editable}
              onChange={(patch) => editRule(index, patch)}
              onRemove={() => removeRule(index)}
            />
          ))
        )}

        {dropped.length > 0 ? (
          <ul className="px-4 py-3 text-sm text-muted-foreground">
            {dropped.map((line) => (
              <li key={line}>{t(($) => $.routing.dispatch_dropped, { line })}</li>
            ))}
          </ul>
        ) : null}

        {editable ? (
          <div className="flex items-center justify-between gap-2 px-4 py-3">
            <p className="text-sm text-muted-foreground">
              {dirty
                ? textChanged && !preview.isSuccess && rules === data?.policy.rules
                  ? t(($) => $.routing.dispatch_text_pending)
                  : t(($) => $.routing.dispatch_pending)
                : t(($) => $.routing.dispatch_version, { version: data?.policy.version ?? 0 })}
            </p>
            <Button type="button" size="sm" disabled={!canConfirm} onClick={() => confirm.mutate()}>
              {t(($) => $.routing.dispatch_confirm)}
            </Button>
          </div>
        ) : null}
      </SettingsCard>

      {data?.policy.history && data.policy.history.length > 0 ? (
        <SettingsCard className="mt-3">
          {[...data.policy.history].reverse().map((rev) => (
            <SettingsRow
              key={rev.version}
              label={t(($) => $.routing.dispatch_revision, { version: rev.version })}
              description={[rev.by, formatRevisionTime(rev.at)].filter(Boolean).join(" · ")}
              align="start"
            >
              <p className="whitespace-pre-wrap text-sm text-muted-foreground sm:text-right">
                {[rev.text_diff, rev.rules_diff].filter(Boolean).join("\n")}
              </p>
            </SettingsRow>
          ))}
        </SettingsCard>
      ) : null}
    </SettingsSection>
  );
}

function DispatchRuleRow({
  rule,
  series,
  tiers,
  editable,
  onChange,
  onRemove,
}: {
  rule: DispatchRule;
  series: string[];
  tiers: { key: string; label: string }[];
  editable: boolean;
  onChange: (patch: Partial<DispatchRule>) => void;
  onRemove: () => void;
}) {
  const { t } = useT("settings");
  const origin =
    rule.origin === "model"
      ? t(($) => $.routing.dispatch_origin_model)
      : rule.origin === "default"
        ? t(($) => $.routing.dispatch_origin_default)
        : t(($) => $.routing.dispatch_origin_manual);
  const seriesOptions = rule.series && !series.includes(rule.series) ? [...series, rule.series] : series;
  const selectClass =
    "h-8 rounded-md border border-input bg-transparent px-2 text-sm disabled:opacity-50";

  return (
    <div className="flex flex-col gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        {editable ? (
          <Input
            value={rule.category}
            aria-label={t(($) => $.routing.dispatch_col_category)}
            className="h-8 w-full sm:w-36"
            onChange={(e) => onChange({ category: e.target.value })}
          />
        ) : (
          <span className="text-sm font-medium">{rule.category}</span>
        )}
        <select
          value={rule.series ?? ""}
          disabled={!editable}
          aria-label={t(($) => $.routing.dispatch_col_series)}
          className={selectClass}
          onChange={(e) => onChange({ series: e.target.value })}
        >
          <option value="">{t(($) => $.routing.dispatch_any_series)}</option>
          {seriesOptions.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <Input type="number" min={0} max={100} step={0.1} value={rule.weight ?? 1}
          disabled={!editable} className="h-11 w-24" aria-label={t(($) => $.routing.dispatch_weight)}
          onChange={(event) => onChange({ weight: Number(event.target.value) })} />
        <select
          value={rule.max_tier ?? ""}
          disabled={!editable}
          aria-label={t(($) => $.routing.dispatch_col_tier)}
          className={selectClass}
          onChange={(e) => onChange({ max_tier: e.target.value })}
        >
          <option value="">{t(($) => $.routing.dispatch_any_tier)}</option>
          {tiers.map((tier) => (
            <option key={tier.key} value={tier.key}>
              {t(($) => $.routing.dispatch_max_tier, { tier: tier.label })}
            </option>
          ))}
        </select>
        <span className="text-xs text-muted-foreground">{origin}</span>
        {editable ? (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="ml-auto"
            aria-label={t(($) => $.routing.dispatch_remove)}
            onClick={onRemove}
          >
            {t(($) => $.routing.dispatch_remove)}
          </Button>
        ) : null}
      </div>
      {rule.source ? (
        <p className="text-xs text-muted-foreground">
          {t(($) => $.routing.dispatch_source, { source: rule.source })}
        </p>
      ) : null}
    </div>
  );
}

function formatRevisionTime(at: string): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return at;
  return d.toLocaleString();
}
